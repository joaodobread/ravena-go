package ai

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/llm"
	"github.com/joaodobread/ravena-go/internal/media"
)

// Figurinhas de data/sillies/<categoria>/ (só webp; "elogios" não tem nenhuma).
//
//go:embed sillies
var sillyFS embed.FS

const (
	catInsult     = "xingamentos"
	catCompliment = "elogios"
	sillyCooldown = 30 * time.Second
	sillyMaxChats = 256 // teto do mapa de cooldowns
)

var (
	sillyNames = []string{"bot", "boot", "ravena"}

	// Nome do bot no começo e uma destas no fim (ex.: "ravena lixo").
	insultAfterName = []string{"lixo", "podre", "inutil", "idiota", "bocaberta", "imprestavel", "ruim", "burro", "burra", "merda", "lento", "lerdo", "lerda",
		"chato", "chata", "horroroso", "horrorosa", "tosco", "tosca", "ridiculo", "ridicula", "pessimo", "pessima", "insuportavel"}
	// Uma destas no começo e o nome do bot no fim (ex.: "odiei o bot").
	insultBeforeName = []string{"odiei o", "odiei a", "ninguem gosta do", "ninguem gosta da", "cala boca", "cala a boca", "vai se ferrar", "vai se foder",
		"odeio a", "odeio o", "que bosta de", "que merda de", "lixo de", "burro esse", "burra essa", "chato esse", "chata essa"}
	complimentAfterName = []string{"lindo", "linda", "fofo", "fofa", "maravilhoso", "maravilhosa", "bom", "boa", "gostoso", "gostosa", "perfeito", "perfeita",
		"inteligente", "util", "melhor", "incrivel", "diva", "deusa", "monstro", "brabo", "braba"}
	complimentBeforeName = []string{"amei o", "amei a", "adorei o", "adorei a", "amo a", "amo o", "sou fa da", "sou fa do", "gosto muito da", "gosto muito do",
		"que orgulho da", "que orgulho do", "obrigado", "obrigada", "parabens", "te amo", "te adoro"}

	// Intensificadores e conectivos removidos antes de comparar.
	fillers = regexp.MustCompile(`\b(um pouco|pra caramba|pra krl|muito|super|tao|bem|bastante|extremamente|completamente|totalmente|realmente|meio|demais|e|um|uma)\b`)
	spaces  = regexp.MustCompile(`\s+`)

	accents = strings.NewReplacer(
		"á", "a", "à", "a", "â", "a", "ã", "a", "ä", "a",
		"é", "e", "è", "e", "ê", "e", "ë", "e",
		"í", "i", "ì", "i", "î", "i", "ï", "i",
		"ó", "o", "ò", "o", "ô", "o", "õ", "o", "ö", "o",
		"ú", "u", "ù", "u", "û", "u", "ü", "u",
		"ç", "c", "ñ", "n",
	)
)

func normalize(s string) string { return accents.Replace(strings.ToLower(s)) }

// hasWordPrefix/hasWordSuffix exigem que w seja palavra inteira ("bot" não casa "botei").
func hasWordPrefix(s, w string) bool {
	return strings.HasPrefix(s, w) && (len(s) == len(w) || !isWordByte(s[len(w)]))
}

func hasWordSuffix(s, w string) bool {
	return strings.HasSuffix(s, w) && (len(s) == len(w) || !isWordByte(s[len(s)-len(w)-1]))
}

func isWordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '_' || b >= 0x80
}

// classifySilly devolve "xingamentos", "elogios" ou "" para text.
func classifySilly(text, botName string) string {
	text = strings.TrimSpace(text)
	if len(text) > 200 {
		return "" // frases longas não são "bot lixo"
	}
	text = strings.TrimSpace(strings.Trim(text, "`"))
	t := fillers.ReplaceAllString(normalize(text), "")
	t = strings.TrimSpace(spaces.ReplaceAllString(t, " "))
	t = strings.TrimFunc(t, func(r rune) bool { return strings.ContainsRune(".,!?;:…~*_ ", r) })
	if t == "" {
		return ""
	}
	names := sillyNames
	if n := normalize(strings.TrimSpace(botName)); n != "" {
		names = append(names[:len(names):len(names)], n)
	}
	match := func(afterName, beforeName []string) bool {
		for _, n := range names {
			for _, w := range afterName {
				if hasWordPrefix(t, n) && hasWordSuffix(t, w) && len(t) > len(n) {
					return true
				}
			}
			for _, w := range beforeName {
				if hasWordPrefix(t, w) && hasWordSuffix(t, n) && len(t) > len(n) {
					return true
				}
			}
		}
		return false
	}
	switch {
	case match(insultAfterName, insultBeforeName):
		return catInsult
	case match(complimentAfterName, complimentBeforeName):
		return catCompliment
	}
	return ""
}

var sillyStickers = sync.OnceValue(func() map[string][]string {
	out := map[string][]string{}
	_ = fs.WalkDir(sillyFS, "sillies", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			cat := path.Base(path.Dir(p))
			out[cat] = append(out[cat], p)
		}
		return nil
	})
	return out
})

// SillyListener responde a elogios/xingamentos ao bot nos grupos.
func (f *Feature) SillyListener(ctx context.Context, msg *bot.Message, client bot.Client) (bool, error) {
	if !msg.IsGroup || f.LLM == nil || msg.Text == "" {
		return false, nil
	}
	cat := classifySilly(msg.Text, f.name())
	if cat == "" {
		return false, nil
	}
	if f.Store != nil {
		g, ok, err := f.Store.Group(ctx, msg.Chat.String())
		if err != nil {
			return false, err
		}
		if ok && !g.Silly {
			return false, nil
		}
	}
	if !f.sillyAllow(msg.Chat.String()) {
		return false, nil
	}

	stickers := sillyStickers()[cat]
	if len(stickers) == 0 || f.randN(100) < 25 {
		err := f.sillyLLM(ctx, msg, client, cat)
		if err == nil {
			return true, nil
		}
		f.log().Warn("silly: LLM falhou", "err", err)
		if len(stickers) == 0 {
			return true, nil
		}
	}
	return true, f.sillySticker(ctx, msg, client, stickers)
}

// sillyAllow aplica o cooldown de 30 s por chat e já o marca.
func (f *Feature) sillyAllow(chat string) bool {
	now := f.now()
	f.sillyMu.Lock()
	defer f.sillyMu.Unlock()
	if f.sillyLast == nil {
		f.sillyLast = map[string]time.Time{}
	}
	if last, ok := f.sillyLast[chat]; ok && now.Sub(last) < sillyCooldown {
		return false
	}
	if len(f.sillyLast) >= sillyMaxChats {
		for k, t := range f.sillyLast {
			if now.Sub(t) >= sillyCooldown {
				delete(f.sillyLast, k)
			}
		}
		if len(f.sillyLast) >= sillyMaxChats {
			return false // muitos chats ao mesmo tempo: melhor ficar quieto
		}
	}
	f.sillyLast[chat] = now
	return true
}

func (f *Feature) sillyLLM(ctx context.Context, msg *bot.Message, client bot.Client, cat string) error {
	who := msg.PushName
	if who == "" {
		who = "usuário"
	}
	text := truncate(msg.Text, 200)
	prompt := fmt.Sprintf("Usuário %s te xingou falando '%s'. Responda à altura, de forma esperta, criativa e engraçada (pode mandar ele se respeitar ou ficar quieto). Uma ou duas frases.", who, text)
	if cat == catCompliment {
		prompt = fmt.Sprintf("Usuário %s te elogiou falando '%s'. Responda de forma fofa, agradecendo e sendo gentil. Uma ou duas frases.", who, text)
	}
	answer, err := f.LLM.Chat(ctx, llm.ChatRequest{
		System:      fmt.Sprintf("Você é %s, um bot de WhatsApp. Responda em português do Brasil. Atrevida se for xingada, fofa e gentil se for elogiada. Não use muitos emojis.", f.name()),
		Messages:    []llm.Message{{Role: "user", Text: prompt}},
		Temperature: 0.9,
		MaxTokens:   150,
	})
	if err != nil {
		return err
	}
	return client.Reply(ctx, msg, bot.Text(strings.TrimSpace(answer)))
}

func (f *Feature) sillySticker(ctx context.Context, msg *bot.Message, client bot.Client, stickers []string) error {
	data, err := sillyFS.ReadFile(stickers[f.randN(len(stickers))])
	if err != nil {
		return err
	}
	animated := false
	if info, err := media.InspectWebP(data); err == nil {
		animated = info.Animated
	}
	if tagged, err := media.SetStickerMetadata(data, media.StickerMetadata{PackID: "ravena-silly", PackName: "Silly", Publisher: f.name()}); err == nil {
		data = tagged
	}
	return client.Reply(ctx, msg, bot.Outgoing{Kind: bot.OutSticker, Data: data, Mime: "image/webp", Animated: animated})
}
