package mediafx

import (
	"context"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/joaodobread/ravena-go/internal/bot"
)

// MsgInstantsUsage é a ajuda do !audio.
const MsgInstantsUsage = "🔇 Digite o nome do áudio para buscar no site MyInstants\n!audio nome do áudio"

// maxInstants limita a lista de resultados.
const maxInstants = 20

// Instant é um resultado do MyInstants.
type Instant struct {
	Title string
	MP3   string // URL absoluta
}

var (
	reInstantPlay = regexp.MustCompile(`play\(\s*'([^']+)'`)
	reInstantLink = regexp.MustCompile(`(?s)<a\s[^>]*class="[^"]*\binstant-link\b[^"]*"[^>]*>(.*?)</a>`)
	reTags        = regexp.MustCompile(`<[^>]*>`)
)

// ParseInstants extrai os áudios de uma página de busca do MyInstants. Cada
// resultado é um bloco class="instant" com um botão onclick="play('/media/...mp3', ...)"
// e um link class="instant-link" com o título.
func ParseInstants(page, base string) []Instant {
	baseURL, err := url.Parse(base)
	if err != nil {
		return nil
	}
	blocks := strings.Split(page, `class="instant"`)
	var out []Instant
	for _, b := range blocks[1:] {
		play := reInstantPlay.FindStringSubmatch(b)
		link := reInstantLink.FindStringSubmatch(b)
		if play == nil || link == nil {
			continue
		}
		title := strings.TrimSpace(html.UnescapeString(reTags.ReplaceAllString(link[1], "")))
		ref, err := url.Parse(html.UnescapeString(play[1]))
		if err != nil || title == "" {
			continue
		}
		abs := baseURL.ResolveReference(ref)
		if abs.Scheme != "http" && abs.Scheme != "https" {
			continue
		}
		out = append(out, Instant{Title: title, MP3: abs.String()})
		if len(out) == maxInstants {
			break
		}
	}
	return out
}

func (f *Feature) instantsCommand() *bot.Command {
	return &bot.Command{
		Name:        "audio",
		Aliases:     []string{"áudio"},
		Description: "Busca um áudio no site MyInstants (não é música)",
		Usage:       "!audio <nome do áudio> [número]",
		Category:    "midia",
		Reactions:   bot.Reactions{Before: "⌛️", After: "🔊", Error: "❌"},
		Run:         f.instants,
	}
}

func (f *Feature) searchInstants(ctx context.Context, query string) ([]Instant, error) {
	page, _, err := f.fetchOK(ctx, f.o.MyInstantsURL+"/search/?name="+url.QueryEscape(query), maxPage)
	if err != nil {
		return nil, err
	}
	return ParseInstants(string(page), f.o.MyInstantsURL+"/"), nil
}

func (f *Feature) instants(ctx context.Context, c *bot.Context) error {
	if len(c.Args) == 0 {
		return bot.Userf(MsgInstantsUsage)
	}
	query := c.ArgText()
	index := 0
	if len(c.Args) > 1 {
		if n, err := strconv.Atoi(c.Args[len(c.Args)-1]); err == nil {
			index = n
			query = strings.Join(c.Args[:len(c.Args)-1], " ")
		}
	}
	res, err := f.searchInstants(ctx, query)
	if err != nil {
		return fmt.Errorf("buscando no myinstants: %w", err)
	}
	if len(res) == 0 {
		return bot.Userf("🔇 Nenhum áudio encontrado para \"%s\".", query)
	}
	if index == 0 {
		var sb strings.Builder
		fmt.Fprintf(&sb, "🔊 Resultados para \"%s\":\n", query)
		for i, r := range res {
			fmt.Fprintf(&sb, "- %d. %s\n", i+1, r.Title)
		}
		fmt.Fprintf(&sb, "\nUse: %saudio %s número_do_áudio para enviar o áudio desejado.\n\nExemplo: %saudio %s 1",
			c.Prefix, query, c.Prefix, query)
		return c.Reply(ctx, bot.Text(sb.String()))
	}
	if index < 1 || index > len(res) {
		return bot.Userf("❌ Número inválido, para '%s' digite um número entre 1 e %d.\n%saudio %s n",
			query, len(res), c.Prefix, query)
	}
	r := res[index-1]
	mp3, _, err := f.fetchOK(ctx, r.MP3, maxRemote)
	if err != nil {
		return fmt.Errorf("baixando áudio: %w", err)
	}
	// Nota de voz exige ogg/opus; corta em 60s.
	voice, err := f.conv.FFmpeg(ctx, mp3, ".ogg", voiceArgs(maxInstantSeconds)...)
	if err != nil {
		return fmt.Errorf("convertendo áudio: %w", err)
	}
	if err := c.Reply(ctx, bot.Text("▶️ _"+r.Title+"_")); err != nil {
		return err
	}
	return c.Client.Send(ctx, c.Message.Chat, bot.Outgoing{Kind: bot.OutAudio, Data: voice, Mime: "audio/ogg; codecs=opus", PTT: true})
}
