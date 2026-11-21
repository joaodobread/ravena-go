// Package download baixa vídeos/áudios de redes sociais com yt-dlp (sob
// demanda, um por vez) e busca letras de música no LRCLib.
package download

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/joaodobread/ravena-go/internal/app"
	"github.com/joaodobread/ravena-go/internal/bot"
)

// File é um arquivo baixado.
type File struct {
	Data  []byte
	Name  string
	Mime  string
	Title string
}

// Fetcher baixa a mídia de target (URL ou "ytsearch1:<busca>").
type Fetcher interface {
	Fetch(ctx context.Context, target string, audio bool) (File, error)
}

// ErrTooBig indica que a mídia passa do limite.
var ErrTooBig = errors.New("arquivo grande demais")

// Module implementa app.Feature.
type Module struct{}

// Name implementa app.Feature.
func (Module) Name() string { return "download" }

// Register implementa app.Feature.
func (Module) Register(_ context.Context, d *app.Deps) error {
	f := &Feature{
		fetch:    NewYTDLP("", "", 50<<20),
		http:     d.HTTP,
		lrclib:   "https://lrclib.net",
		resolver: net.DefaultResolver,
		log:      d.Log,
	}
	return d.Router.Register(f.Commands()...)
}

// Feature são os comandos de download.
type Feature struct {
	fetch    Fetcher
	http     *http.Client
	lrclib   string
	resolver interface {
		LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
	}
	log *slog.Logger
}

// Commands devolve os comandos.
func (f *Feature) Commands() []*bot.Command {
	loading := bot.Reactions{Before: "⌛️", After: "✅", Error: "❌"}
	return []*bot.Command{
		{
			Name: "download", Category: "midia", Reactions: loading, Triggers: []string{"⬇️"},
			Aliases: []string{"baixar", "dl", "yt", "x", "twitter", "tiktok", "tk", "insta", "instagram",
				"fb", "facebook", "pin", "pinterest", "reddit", "vimeo", "twitch", "sc", "soundcloud"},
			Description: "Baixa o vídeo de um link (YouTube, TikTok, Instagram, X…)",
			Usage:       "!download <link> (ou responda a uma mensagem com link)",
			Run:         func(ctx context.Context, c *bot.Context) error { return f.download(ctx, c, false) },
		},
		{
			Name: "download-audio", Category: "midia", Reactions: loading, Triggers: []string{"⏬"},
			Aliases:     []string{"download-musica", "yt-audio", "yt-musica", "tiktok-audio", "insta-audio", "sc-audio", "sc-musica", "soundcloud-audio"},
			Description: "Baixa só o áudio (mp3) de um link",
			Usage:       "!download-audio <link>",
			Run:         func(ctx context.Context, c *bot.Context) error { return f.download(ctx, c, true) },
		},
		{
			Name: "sr", Category: "midia", Reactions: loading, Aliases: []string{"musica", "música"},
			Description: "Procura a música no YouTube e manda o áudio com a letra",
			Usage:       "!sr <artista - música>",
			Run:         f.songRequest,
		},
		{
			Name: "letra", Category: "midia", Aliases: []string{"lyrics"},
			Description: "Letra de música (LRCLib)",
			Usage:       "!letra <artista - música>",
			Run:         f.lyricsCommand,
		},
	}
}

var urlRE = regexp.MustCompile(`https?://[^\s<>"']+`)

// target acha o link nos argumentos, na mensagem citada ou na reagida.
func target(c *bot.Context) string {
	for _, text := range []string{c.ArgText(), quotedText(c), c.Message.Text} {
		if u := urlRE.FindString(text); u != "" {
			return strings.TrimRight(u, ".,;!?)")
		}
	}
	return ""
}

func quotedText(c *bot.Context) string {
	if c.Message.Quoted != nil {
		return c.Message.Quoted.Text
	}
	return ""
}

func (f *Feature) download(ctx context.Context, c *bot.Context, audio bool) error {
	raw := target(c)
	if raw == "" {
		return bot.Userf("Mande o link junto do comando ou responda a uma mensagem com o link.")
	}
	if err := f.checkURL(ctx, raw); err != nil {
		return err
	}
	file, err := f.fetch.Fetch(ctx, raw, audio)
	if err != nil {
		return f.fetchError(err)
	}
	return c.Reply(ctx, outgoing(file, audio))
}

func (f *Feature) fetchError(err error) error {
	if errors.Is(err, ErrTooBig) {
		return bot.Userf("Esse arquivo passa do limite de 50 MB.")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return bot.Userf("O download demorou demais. Tente um vídeo menor.")
	}
	f.log.Warn("download falhou", "err", err)
	return bot.Userf("Não consegui baixar isso. O link é público e de um site suportado?")
}

func outgoing(file File, audio bool) bot.Outgoing {
	switch {
	case audio:
		return bot.Outgoing{Kind: bot.OutAudio, Data: file.Data, Mime: "audio/mpeg"}
	case strings.HasPrefix(file.Mime, "video/") && len(file.Data) <= 50<<20:
		return bot.Outgoing{Kind: bot.OutVideo, Data: file.Data, Mime: file.Mime, Caption: file.Title}
	case strings.HasPrefix(file.Mime, "image/"):
		return bot.Outgoing{Kind: bot.OutImage, Data: file.Data, Mime: file.Mime, Caption: file.Title}
	default:
		return bot.Outgoing{Kind: bot.OutDocument, Data: file.Data, Mime: file.Mime, FileName: file.Name, Caption: file.Title}
	}
}

// checkURL só aceita http(s) para hosts públicos: o yt-dlp segue qualquer
// URL, e o bot roda numa rede privada (Railway) que não deve ser exposta.
func (f *Feature) checkURL(ctx context.Context, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return bot.Userf("Link inválido.")
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".local") {
		return bot.Userf("Link inválido.")
	}
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		addrs, err := f.resolver.LookupIPAddr(ctx, host)
		if err != nil {
			return bot.Userf("Não encontrei esse site.")
		}
		for _, a := range addrs {
			ips = append(ips, a.IP)
		}
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsLinkLocalMulticast() {
			return bot.Userf("Link inválido.")
		}
	}
	return nil
}

func (f *Feature) songRequest(ctx context.Context, c *bot.Context) error {
	query := c.ArgText()
	if query == "" {
		return bot.Userf("Diga a música: %ssr artista - música", c.Prefix)
	}
	file, err := f.fetch.Fetch(ctx, "ytsearch1:"+query, true)
	if err != nil {
		return f.fetchError(err)
	}
	if err := c.Reply(ctx, outgoing(file, true)); err != nil {
		return err
	}
	if lyr, err := f.lyrics(ctx, query); err == nil && lyr != "" {
		return c.ReplyText(ctx, "🎵 *%s*\n\n%s", file.Title, lyr)
	}
	return nil
}

func (f *Feature) lyricsCommand(ctx context.Context, c *bot.Context) error {
	query := c.ArgText()
	if query == "" {
		return bot.Userf("Diga a música: %sletra artista - música", c.Prefix)
	}
	lyr, err := f.lyrics(ctx, query)
	if err != nil {
		return err
	}
	if lyr == "" {
		return bot.Userf("Não achei a letra de “%s”.", query)
	}
	return c.ReplyText(ctx, "🎵 *%s*\n\n%s", query, lyr)
}

// lyrics busca no LRCLib; "" se não achar.
func (f *Feature) lyrics(ctx context.Context, query string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.lrclib+"/api/search?q="+url.QueryEscape(query), nil)
	if err != nil {
		return "", err
	}
	resp, err := f.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("lrclib: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("lrclib: HTTP %d", resp.StatusCode)
	}
	var results []struct {
		PlainLyrics string `json:"plainLyrics"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&results); err != nil {
		return "", fmt.Errorf("lrclib: %w", err)
	}
	for _, r := range results {
		if strings.TrimSpace(r.PlainLyrics) != "" {
			lyr := strings.TrimSpace(r.PlainLyrics)
			if len(lyr) > 4000 { // limite prático de uma mensagem
				lyr = lyr[:4000] + "\n…"
			}
			return lyr, nil
		}
	}
	return "", nil
}
