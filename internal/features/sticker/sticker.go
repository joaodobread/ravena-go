// Package sticker implementa os comandos de figurinha (!s, !sq, !sqi...) e a
// figurinha automática no privado.
package sticker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"

	"github.com/joaodobread/ravena-go/internal/app"
	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/media"
)

// Converter é o que a funcionalidade precisa do media.Converter.
type Converter interface {
	StaticSticker(ctx context.Context, in []byte, crop media.Crop) ([]byte, error)
	AnimatedSticker(ctx context.Context, in []byte, crop media.Crop) ([]byte, error)
	Frame(ctx context.Context, in []byte) ([]byte, error)
	WebPToPNG(ctx context.Context, in []byte) ([]byte, error)
	AnimatedWebPToMP4(ctx context.Context, in []byte) ([]byte, error)
}

// FocusFinder sugere o centro (0–100) do corte a partir de um JPEG.
type FocusFinder interface {
	Focus(ctx context.Context, jpeg []byte) (float64, error)
}

// Mensagens mostradas ao usuário.
const (
	MsgNoMedia     = "Envie uma imagem/vídeo com a legenda do comando, ou responda a uma mídia."
	MsgUnsupported = "Este tipo de mídia não pode ser convertido em figurinha. Apenas imagens, vídeos e figurinhas são suportados."
)

// Feature agrupa os comandos de figurinha.
type Feature struct {
	conv      Converter
	focus     FocusFinder // nil: !sqi cai no corte central
	publisher string
	log       *slog.Logger
}

// New cria a funcionalidade. focus pode ser nil.
func New(conv Converter, focus FocusFinder, publisher string, log *slog.Logger) *Feature {
	return &Feature{conv: conv, focus: focus, publisher: publisher, log: log}
}

type variant struct {
	name     string
	aliases  []string
	desc     string
	crop     media.Crop
	ai       bool
	triggers []string
}

// Loading é a reação enquanto a figurinha é gerada (LOADING_EMOJI no Ravena).
const Loading = "⌛️"

var variants = []variant{
	{"sticker", []string{"s", "fig", "figurinha"}, "Converte mídia em figurinha", media.Crop{Mode: media.Contain}, false, []string{"🖼", "🖼️"}},
	{"sq", []string{"stickerq"}, "Figurinha quadrada cortada no meio", media.Crop{Mode: media.Center}, false, nil},
	{"sqc", []string{"stickerqc"}, "Figurinha quadrada cortada preservando o topo", media.Crop{Mode: media.Top}, false, nil},
	{"sqb", []string{"stickerqb"}, "Figurinha quadrada cortada preservando a base", media.Crop{Mode: media.Bottom}, false, nil},
	{"sqe", []string{"stickerqe"}, "Figurinha quadrada esticada, sem cortar", media.Crop{Mode: media.Stretch}, false, nil},
	{"sqi", []string{"stickerqi"}, "Figurinha quadrada com corte inteligente via IA", media.Crop{Mode: media.Focus}, true, nil},
}

// Commands devolve os comandos para registrar no Router.
func (f *Feature) Commands() []*bot.Command {
	cmds := make([]*bot.Command, 0, len(variants))
	for _, v := range variants {
		cmds = append(cmds, &bot.Command{
			Name:        v.name,
			Aliases:     v.aliases,
			Description: v.desc,
			Category:    "midia",
			Reactions:   bot.Reactions{Before: Loading, After: "🖼", Error: "❌"},
			Triggers:    v.triggers,
			Run: func(ctx context.Context, c *bot.Context) error {
				return f.handle(ctx, c, v)
			},
		})
	}
	return cmds
}

func (f *Feature) handle(ctx context.Context, c *bot.Context, v variant) error {
	src := c.Message.AnyMedia()
	if src == nil {
		return bot.Userf(MsgNoMedia)
	}
	data, err := src.Download(ctx)
	if err != nil {
		return fmt.Errorf("baixando mídia: %w", err)
	}
	if src.Kind == bot.MediaSticker {
		return f.extract(ctx, c, data)
	}

	crop := v.crop
	if v.ai {
		crop = f.aiCrop(ctx, data)
	}
	out, err := f.make(ctx, src, data, crop, f.packName(ctx, c))
	if err != nil {
		return err
	}
	return c.Reply(ctx, out)
}

// AutoSticker é um bot.Listener: no privado, toda imagem/vídeo vira figurinha.
func (f *Feature) AutoSticker(ctx context.Context, m *bot.Message, client bot.Client) (bool, error) {
	if m.IsGroup || m.Media == nil {
		return false, nil
	}
	switch m.Media.Kind {
	case bot.MediaImage, bot.MediaVideo, bot.MediaGIF:
	default:
		return false, nil
	}
	data, err := m.Media.Download(ctx)
	if err != nil {
		return true, fmt.Errorf("baixando mídia: %w", err)
	}
	name := m.PushName
	if name == "" {
		name = "sticker"
	}
	out, err := f.make(ctx, m.Media, data, media.Crop{Mode: media.Contain}, name)
	if err != nil {
		return true, err
	}
	return true, client.Reply(ctx, m, out)
}

func (f *Feature) make(ctx context.Context, src *bot.Media, data []byte, crop media.Crop, pack string) (bot.Outgoing, error) {
	var (
		webp     []byte
		err      error
		animated bool
	)
	switch src.Kind {
	case bot.MediaImage:
		webp, err = f.conv.StaticSticker(ctx, data, crop)
	case bot.MediaVideo, bot.MediaGIF:
		animated = true
		webp, err = f.conv.AnimatedSticker(ctx, data, crop)
	default:
		return bot.Outgoing{}, bot.Userf(MsgUnsupported)
	}
	if err != nil {
		return bot.Outgoing{}, fmt.Errorf("convertendo: %w", err)
	}
	webp, err = media.SetStickerMetadata(webp, media.StickerMetadata{
		PackID:    randomID(),
		PackName:  pack,
		Publisher: f.publisher,
	})
	if err != nil {
		return bot.Outgoing{}, fmt.Errorf("gravando metadados: %w", err)
	}
	return bot.Outgoing{Kind: bot.OutSticker, Data: webp, Mime: "image/webp", Animated: animated}, nil
}

// extract devolve a mídia original de uma figurinha respondida.
func (f *Feature) extract(ctx context.Context, c *bot.Context, data []byte) error {
	info, err := media.InspectWebP(data)
	if err != nil {
		return bot.Userf("Não consegui ler essa figurinha.")
	}
	if info.Animated {
		mp4, err := f.conv.AnimatedWebPToMP4(ctx, data)
		if err != nil {
			return fmt.Errorf("figurinha animada → mp4: %w", err)
		}
		return c.Reply(ctx, bot.Outgoing{Kind: bot.OutVideo, Data: mp4, Mime: "video/mp4", Caption: "Figurinha animada"})
	}
	png, err := f.conv.WebPToPNG(ctx, data)
	if err != nil {
		return fmt.Errorf("figurinha → png: %w", err)
	}
	if info.Alpha {
		// Como documento, para o WhatsApp não achatar a transparência.
		return c.Reply(ctx, bot.Outgoing{Kind: bot.OutDocument, Data: png, Mime: "image/png", FileName: "figurinha.png", Caption: "Figurinha transparente"})
	}
	return c.Reply(ctx, bot.Outgoing{Kind: bot.OutImage, Data: png, Mime: "image/png", Caption: "Figurinha"})
}

// aiCrop pergunta ao LLM onde cortar; qualquer falha vira corte central.
func (f *Feature) aiCrop(ctx context.Context, data []byte) media.Crop {
	fallback := media.Crop{Mode: media.Center}
	if f.focus == nil {
		return fallback
	}
	frame, err := f.conv.Frame(ctx, data)
	if err != nil {
		f.log.Warn("sqi: falha ao extrair quadro", "err", err)
		return fallback
	}
	p, err := f.focus.Focus(ctx, frame)
	if err != nil {
		f.log.Warn("sqi: LLM falhou, usando centro", "err", err)
		return fallback
	}
	return media.Crop{Mode: media.Focus, Focus: p}
}

// packName: argumentos do comando > nome do grupo > "sticker".
func (f *Feature) packName(ctx context.Context, c *bot.Context) string {
	if len(c.Args) > 0 {
		return strings.Join(c.Args, " ")
	}
	if c.Message.IsGroup {
		if name, err := c.Client.ChatName(ctx, c.Message.Chat); err == nil && name != "" {
			return name
		}
	}
	return "sticker"
}

func randomID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Module adapta a funcionalidade ao contrato app.Feature.
type Module struct{}

// Name implementa app.Feature.
func (Module) Name() string { return "sticker" }

// Register implementa app.Feature.
func (Module) Register(_ context.Context, d *app.Deps) error {
	var focus FocusFinder
	if d.LLM != nil {
		focus = d.LLM
	}
	f := New(d.Media, focus, "ravena", d.Log)
	if err := d.Router.Register(f.Commands()...); err != nil {
		return err
	}
	d.Router.Listen(f.AutoSticker)
	return nil
}
