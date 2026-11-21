package mediafx

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/joaodobread/ravena-go/internal/bot"
)

// MsgEmojikUsage é a ajuda do !emojik.
const MsgEmojikUsage = "Por favor, forneça dois emojis para criar um sticker combinado. Exemplo: !emojik 🥹 😗"

func (f *Feature) emojikCommand() *bot.Command {
	return &bot.Command{
		Name:        "emojik",
		Aliases:     []string{"memoji"},
		Description: "Cria um sticker combinando dois emojis",
		Usage:       "!emojik 🥹 😗",
		Category:    "midia",
		Cooldown:    5 * time.Second,
		Reactions:   bot.Reactions{Before: "⌛️", After: "🧪", Error: "❌"},
		Run:         f.emojik,
	}
}

func (f *Feature) emojik(ctx context.Context, c *bot.Context) error {
	emojis := Emojis(c.ArgText(), 2)
	if len(emojis) < 2 && c.Message.Quoted != nil {
		emojis = Emojis(c.Message.Quoted.Text, 2)
	}
	if len(emojis) < 2 {
		return bot.Userf(MsgEmojikUsage)
	}
	a, b := emojis[0], emojis[1]
	u := f.o.EmojikURL + "/s/" + url.PathEscape(a) + "_" + url.PathEscape(b) + "?size=512"
	img, ct, err := f.fetchOK(ctx, u, maxRemote)
	if err != nil || !strings.HasPrefix(ct, "image/") {
		f.o.Log.Info("emojik: combinação indisponível", "url", u, "err", err, "type", ct)
		return bot.Userf("Não foi possível criar o sticker para %s+%s. Esta combinação pode não estar disponível.", a, b)
	}
	st, err := f.sticker(ctx, img, "Emojik: "+a+"+"+b)
	if err != nil {
		return err
	}
	return c.Reply(ctx, st)
}

// Emojis extrai até n emojis de s, agrupando modificadores (tom de pele,
// VS16, ZWJ, bandeiras e keycaps) no emoji ao qual pertencem.
func Emojis(s string, n int) []string {
	rs := []rune(s)
	var out []string
	for i := 0; i < len(rs) && len(out) < n; {
		r := rs[i]
		switch {
		case isRegional(r):
			if i+1 < len(rs) && isRegional(rs[i+1]) {
				out = append(out, string(rs[i:i+2]))
				i += 2
			} else {
				i++
			}
			continue
		case isKeycapBase(r) && i+1 < len(rs) && (rs[i+1] == 0x20E3 || (rs[i+1] == 0xFE0F && i+2 < len(rs) && rs[i+2] == 0x20E3)):
			j := i + 2
			if rs[i+1] == 0xFE0F {
				j = i + 3
			}
			out = append(out, string(rs[i:j]))
			i = j
			continue
		case !isEmojiBase(r):
			i++
			continue
		}
		j := i + 1
		for j < len(rs) {
			if rs[j] == 0xFE0F || isSkinTone(rs[j]) || (rs[j] >= 0xE0020 && rs[j] <= 0xE007F) {
				j++
			} else if rs[j] == 0x200D && j+1 < len(rs) && isEmojiBase(rs[j+1]) {
				j += 2
			} else {
				break
			}
		}
		out = append(out, string(rs[i:j]))
		i = j
	}
	return out
}

func isRegional(r rune) bool   { return r >= 0x1F1E6 && r <= 0x1F1FF }
func isSkinTone(r rune) bool   { return r >= 0x1F3FB && r <= 0x1F3FF }
func isKeycapBase(r rune) bool { return r == '#' || r == '*' || (r >= '0' && r <= '9') }

func isEmojiBase(r rune) bool {
	switch {
	case r >= 0x1F000 && r <= 0x1FAFF && !isSkinTone(r) && !isRegional(r):
		return true
	case r >= 0x2600 && r <= 0x27BF, r >= 0x2300 && r <= 0x23FF, r >= 0x2B00 && r <= 0x2BFF,
		r >= 0x2190 && r <= 0x21FF, r >= 0x25A0 && r <= 0x25FF, r >= 0x2934 && r <= 0x2935:
		return true
	}
	switch r {
	case 0x00A9, 0x00AE, 0x203C, 0x2049, 0x2122, 0x2139, 0x3030, 0x303D, 0x3297, 0x3299:
		return true
	}
	return false
}
