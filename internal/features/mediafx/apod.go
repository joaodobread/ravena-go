package mediafx

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/joaodobread/ravena-go/internal/bot"
)

const maxExplanation = 800

type apodResp struct {
	Date        string `json:"date"`
	Title       string `json:"title"`
	Explanation string `json:"explanation"`
	MediaType   string `json:"media_type"`
	URL         string `json:"url"`
	HDURL       string `json:"hdurl"`
	Thumbnail   string `json:"thumbnail_url"`
	Copyright   string `json:"copyright"`
	// Erros da API vêm como {"msg": ...} ou {"error": {"message": ...}}.
	Msg   string `json:"msg"`
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (f *Feature) apodCommand() *bot.Command {
	return &bot.Command{
		Name:        "apod",
		Description: "Foto astronômica do dia (NASA)",
		Usage:       "!apod [AAAA-MM-DD | DD/MM/AAAA | ontem]",
		Category:    "utilidades",
		Reactions:   bot.Reactions{Before: "🌌", After: "✨", Error: "❌"},
		Run:         f.apod,
	}
}

// ParseAPODDate entende AAAA-MM-DD, DD/MM/AAAA, "hoje" e "ontem". "" = hoje (padrão da API).
func ParseAPODDate(s string, now time.Time) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "", "hoje":
		return "", true
	case "ontem":
		return now.AddDate(0, 0, -1).Format(time.DateOnly), true
	case "anteontem":
		return now.AddDate(0, 0, -2).Format(time.DateOnly), true
	}
	for _, layout := range []string{time.DateOnly, "02/01/2006", "2/1/2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format(time.DateOnly), true
		}
	}
	return "", false
}

func brDate(iso string) string {
	if t, err := time.Parse(time.DateOnly, iso); err == nil {
		return t.Format("02/01/2006")
	}
	return iso
}

func (f *Feature) apod(ctx context.Context, c *bot.Context) error {
	date, ok := ParseAPODDate(c.ArgText(), time.Now())
	if !ok {
		return bot.Userf("Data inválida. Use AAAA-MM-DD ou DD/MM/AAAA. Exemplo: !apod 2024-01-15")
	}
	q := url.Values{"api_key": {f.o.NASAKey}, "thumbs": {"true"}}
	if date != "" {
		q.Set("date", date)
	}
	body, _, status, err := f.fetch(ctx, f.o.APODURL+"?"+q.Encode(), maxPage)
	if err != nil {
		return fmt.Errorf("apod: %w", err)
	}
	var r apodResp
	if err := json.Unmarshal(body, &r); err != nil {
		return fmt.Errorf("apod: status %d: %w", status, err)
	}
	if status != 200 {
		if msg := r.Msg + r.Error.Message; msg != "" {
			return bot.Userf("❌ Erro da NASA: %s", msg)
		}
		return fmt.Errorf("apod: status %d", status)
	}

	video := r.MediaType == "video"
	var sb strings.Builder
	sb.WriteString("🚀 *Astronomy Picture of the Day*\n")
	fmt.Fprintf(&sb, "📅 *Data:* %s\n", brDate(r.Date))
	fmt.Fprintf(&sb, "✨ *Título:* %s\n\n", r.Title)
	if ex := []rune(strings.TrimSpace(r.Explanation)); len(ex) > 0 {
		if len(ex) > maxExplanation {
			ex = append(ex[:maxExplanation-3], []rune("...")...)
		}
		sb.WriteString(string(ex) + "\n")
	}
	if r.Copyright != "" {
		fmt.Fprintf(&sb, "©️ %s\n", strings.TrimSpace(r.Copyright))
	}
	switch {
	case video:
		fmt.Fprintf(&sb, "\n🎥 *Vídeo original:* %s", r.URL)
	case r.HDURL != "":
		fmt.Fprintf(&sb, "\n🔗 *Alta Resolução:* %s", r.HDURL)
	case r.URL != "":
		fmt.Fprintf(&sb, "\n🔗 *Link:* %s", r.URL)
	}
	caption := sb.String()

	// A versão "url" é bem menor que a "hdurl" (que vai como link).
	img := r.URL
	if video {
		img = r.Thumbnail
	}
	if img != "" && (video || r.MediaType == "image") {
		data, ct, err := f.fetchOK(ctx, img, maxRemote)
		if err == nil && strings.HasPrefix(ct, "image/") {
			return c.Reply(ctx, bot.Outgoing{Kind: bot.OutImage, Data: data, Mime: mimeOnly(ct), Caption: caption})
		}
		f.o.Log.Warn("apod: falha ao baixar imagem", "url", img, "err", err)
	}
	return c.Reply(ctx, bot.Text(caption))
}
