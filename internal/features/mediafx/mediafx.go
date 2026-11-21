// Package mediafx reúne comandos de mídia "leves": efeitos de imagem
// (ImageMagick), Emoji Kitchen, conversões de áudio (ffmpeg) e buscas que
// devolvem mídia (MyInstants, anime via Jikan, APOD da NASA).
package mediafx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/joaodobread/ravena-go/internal/app"
	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/media"
)

// Processor é o que a funcionalidade precisa do media.Converter. Todos os
// processos externos passam por ele (que limita a concorrência).
type Processor interface {
	FFmpeg(ctx context.Context, in []byte, outExt string, args ...string) ([]byte, error)
	Magick(ctx context.Context, in []byte, inExt, outExt string, args ...string) ([]byte, error)
	StaticSticker(ctx context.Context, in []byte, crop media.Crop) ([]byte, error)
}

// Limites de tamanho/duração.
const (
	// MaxInput é o maior arquivo aceito para conversão.
	MaxInput = 16 << 20
	// maxRemote é o maior arquivo baixado de APIs externas.
	maxRemote = 8 << 20
	// maxPage é o maior corpo HTML/JSON lido.
	maxPage = 2 << 20
	// maxAudioSeconds corta áudios/vídeos processados.
	maxAudioSeconds = "600"
	// maxInstantSeconds corta os áudios do MyInstants.
	maxInstantSeconds = "60"
	// maxImageSide reduz imagens grandes antes dos efeitos (CPU/RAM).
	maxImageSide = "1280x1280>"
)

// Mensagens mostradas ao usuário.
const (
	MsgNoImage  = "Envie uma imagem (ou figurinha) com a legenda do comando, ou responda a uma."
	MsgNoAV     = "Responda a um áudio, mensagem de voz ou vídeo (ou envie com a legenda do comando)."
	MsgTooLarge = "Arquivo grande demais (máx. 16 MB)."
)

// Options configura a funcionalidade. Zeros viram os padrões.
type Options struct {
	HTTP          *http.Client
	Publisher     string // autor das figurinhas
	EmojikURL     string // padrão https://emojik.vercel.app
	MyInstantsURL string // padrão https://www.myinstants.com
	JikanURL      string // padrão https://api.jikan.moe
	APODURL       string // padrão https://api.nasa.gov/planetary/apod
	NASAKey       string // padrão DEMO_KEY
	Log           *slog.Logger
}

// Feature agrupa os comandos.
type Feature struct {
	conv Processor
	o    Options
}

// New cria a funcionalidade.
func New(conv Processor, o Options) *Feature {
	if o.HTTP == nil {
		o.HTTP = app.NewHTTPClient()
	}
	if o.Publisher == "" {
		o.Publisher = "ravena"
	}
	o.EmojikURL = def(o.EmojikURL, "https://emojik.vercel.app")
	o.MyInstantsURL = def(o.MyInstantsURL, "https://www.myinstants.com")
	o.JikanURL = def(o.JikanURL, "https://api.jikan.moe")
	o.APODURL = def(o.APODURL, "https://api.nasa.gov/planetary/apod")
	o.NASAKey = def(o.NASAKey, "DEMO_KEY")
	if o.Log == nil {
		o.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Feature{conv: conv, o: o}
}

func def(v, d string) string {
	if v == "" {
		return d
	}
	return strings.TrimRight(v, "/")
}

// Commands devolve todos os comandos do pacote.
func (f *Feature) Commands() []*bot.Command {
	var cmds []*bot.Command
	cmds = append(cmds, f.effectCommands()...)
	cmds = append(cmds, f.emojikCommand())
	cmds = append(cmds, f.audioCommands()...)
	cmds = append(cmds, f.instantsCommand(), f.animeCommand(), f.apodCommand())
	return cmds
}

// download baixa a mídia aplicando o limite de tamanho.
func download(ctx context.Context, m *bot.Media) ([]byte, error) {
	data, err := m.Download(ctx)
	if err != nil {
		return nil, fmt.Errorf("baixando mídia: %w", err)
	}
	if len(data) > MaxInput {
		return nil, bot.Userf(MsgTooLarge)
	}
	return data, nil
}

var errTooBig = errors.New("resposta grande demais")

// fetch faz um GET e lê no máximo limit bytes do corpo.
func (f *Feature) fetch(ctx context.Context, url string, limit int64) ([]byte, string, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", 0, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; ravena-bot)")
	resp, err := f.o.HTTP.Do(req)
	if err != nil {
		return nil, "", 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, "", resp.StatusCode, err
	}
	if int64(len(body)) > limit {
		return nil, "", resp.StatusCode, errTooBig
	}
	return body, resp.Header.Get("Content-Type"), resp.StatusCode, nil
}

// fetchOK é fetch exigindo status 200.
func (f *Feature) fetchOK(ctx context.Context, url string, limit int64) ([]byte, string, error) {
	body, ct, status, err := f.fetch(ctx, url, limit)
	if err != nil {
		return nil, "", err
	}
	if status != http.StatusOK {
		return nil, "", fmt.Errorf("GET %s: status %d", url, status)
	}
	return body, ct, nil
}

// sticker converte uma imagem em figurinha com metadados.
func (f *Feature) sticker(ctx context.Context, img []byte, pack string) (bot.Outgoing, error) {
	webp, err := f.conv.StaticSticker(ctx, img, media.Crop{Mode: media.Contain})
	if err != nil {
		return bot.Outgoing{}, fmt.Errorf("figurinha: %w", err)
	}
	webp, err = media.SetStickerMetadata(webp, media.StickerMetadata{
		PackID: randomID(), PackName: pack, Publisher: f.o.Publisher,
	})
	if err != nil {
		return bot.Outgoing{}, fmt.Errorf("metadados: %w", err)
	}
	return bot.Outgoing{Kind: bot.OutSticker, Data: webp, Mime: "image/webp"}, nil
}

func randomID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Module adapta a funcionalidade ao contrato app.Feature.
type Module struct{}

// Name implementa app.Feature.
func (Module) Name() string { return "mediafx" }

// Register implementa app.Feature.
func (Module) Register(_ context.Context, d *app.Deps) error {
	pub := "ravena"
	if d.Policy != nil {
		pub = d.BotName()
	}
	f := New(d.Media, Options{
		HTTP:      d.HTTP,
		Publisher: pub,
		NASAKey:   os.Getenv("NASA_API"),
		Log:       d.Log,
	})
	return d.Router.Register(f.Commands()...)
}

// mimeOnly tira os parâmetros de um Content-Type ("image/jpeg; x=y" → "image/jpeg").
func mimeOnly(ct string) string {
	m, _, _ := strings.Cut(ct, ";")
	return strings.TrimSpace(m)
}
