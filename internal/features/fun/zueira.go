package fun

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/joaodobread/ravena-go/internal/bot"
)

// phraseCmd é um comando que sorteia uma frase de uma lista.
type phraseCmd struct {
	name    string
	aliases []string
	desc    string
	list    string
	after   string
	// wrap monta o texto final a partir da frase sorteada (nil = a frase).
	wrap func(phrase string) string
}

var phraseCmds = []phraseCmd{
	{name: "biscoito", aliases: []string{"biscoito-da-sorte", "biscoito-sorte"}, desc: "Abre um biscoito da sorte", list: "biscoito-frases", after: "🥠",
		wrap: func(p string) string { return "🥠 " + p }},
	{name: "cantada", desc: "Faz uma cantada para alguém do grupo", list: "cantadas-ruins", after: "💕",
		wrap: func(p string) string { return "💕 *{nomeAutor}* chegou em *{mention}* com: \n" + p }},
	{name: "violencia", aliases: []string{"violência"}, desc: "Pratica um ato de violência", list: "violencia", after: "💢"},
	{name: "morreu", desc: "de gue?", list: "morreu", after: "💀"},
	{name: "boleto", desc: "Escolhe alguém pra pagar", list: "boleto", after: "🔳"},
	{name: "clonarcartao", desc: "Pra pagar o agiota", list: "cartao", after: "💳"},
	{name: "presente", desc: "Os melhores da internet", list: "presente", after: "🎁",
		wrap: func(p string) string { return "*{nomeAutor}* deu _" + p + "_ para *{mention}*! 🎁" }},
	{name: "aniversario", aliases: []string{"aniversário"}, desc: "Parabeniza um membro do grupo!", list: "aniversario", after: "🎂"},
	{name: "pecar", aliases: []string{"meus-pecados"}, desc: "Confessa seus pecados", list: "pecados", after: "⛪️"},
	{name: "genshin", desc: "Faz uma oração no gacha", list: "genshin", after: "☄️"},
}

func (f *Feature) zueiraCommands() []*bot.Command {
	var cmds []*bot.Command
	for _, p := range phraseCmds {
		cmds = append(cmds, &bot.Command{
			Name:        p.name,
			Aliases:     p.aliases,
			Description: p.desc,
			Category:    "zoeira",
			Reactions:   bot.Reactions{After: p.after},
			Run: func(ctx context.Context, c *bot.Context) error {
				phrase := f.pick(phrases()[p.list])
				if phrase == "" {
					return fmt.Errorf("lista %q vazia", p.list)
				}
				if p.wrap != nil {
					phrase = p.wrap(phrase)
				}
				return c.Reply(ctx, f.render(ctx, c, phrase))
			},
		})
	}
	cmds = append(cmds, &bot.Command{
		Name:        "pix",
		Description: "Faz uma transferência pela Ravenabank",
		Usage:       "!pix @pessoa 100",
		Category:    "zoeira",
		Reactions:   bot.Reactions{After: "💸"},
		Run:         f.pix,
	})
	return cmds
}

// parseMoney lê "12,50" ou "12.5"; aceita só números finitos.
func parseMoney(s string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.Replace(strings.TrimSpace(s), ",", ".", 1), 64)
	if err != nil || math.IsInf(v, 0) || math.IsNaN(v) {
		return 0, false
	}
	return v, true
}

func formatMoney(v float64) string {
	return "R$" + strings.Replace(strconv.FormatFloat(v, 'f', 2, 64), ".", ",", 1)
}

func (f *Feature) digits(n int) string {
	var b strings.Builder
	for range n {
		b.WriteByte(byte('0' + f.rand(10)))
	}
	return b.String()
}

func (f *Feature) hex(n int) string {
	const chars = "0123456789abcdef"
	var b strings.Builder
	for range n {
		b.WriteByte(chars[f.rand(len(chars))])
	}
	return b.String()
}

func cpfDigit(s string) int {
	sum := 0
	for i, ch := range s {
		sum += int(ch-'0') * (len(s) + 1 - i)
	}
	if r := sum % 11; r >= 2 {
		return 11 - r
	}
	return 0
}

// cpf gera um CPF com dígitos verificadores válidos (e falso, claro).
func (f *Feature) cpf() string {
	base := f.digits(9)
	d1 := cpfDigit(base)
	d2 := cpfDigit(base + strconv.Itoa(d1))
	return fmt.Sprintf("%s.%s.%s-%d%d", base[:3], base[3:6], base[6:], d1, d2)
}

func (f *Feature) pix(ctx context.Context, c *bot.Context) error {
	value, idx := 50.0, len(c.Args)
	for i, a := range c.Args {
		if v, ok := parseMoney(a); ok {
			if v != 0 {
				value = v
			}
			idx = i
			break
		}
	}

	var out bot.Outgoing
	receiver := "Fulano"
	if len(c.Message.Mentions) > 0 {
		j := c.Message.Mentions[0]
		receiver = bot.MentionText(j)
		out.Mentions = append(out.Mentions, j)
	} else if name := strings.Join(c.Args[:idx], " "); name != "" {
		receiver = name
	}

	now := f.now().In(brt)
	var b strings.Builder
	b.WriteString("🏦 *Comprovante Ravenabank*\n")
	b.WriteString("✅ Pix enviado com sucesso\n\n")
	fmt.Fprintf(&b, "💸 *%s*\n📅 %s\n\n", formatMoney(value), now.Format("02/01/2006 às 15:04"))
	b.WriteString("*Recebedor*\n")
	fmt.Fprintf(&b, "Nome: %s\nCPF: %s\nAgência: %s-%s\nConta: %s-%s\nBanco: Ravenabank\n",
		receiver, f.cpf(), f.digits(4), f.digits(1), f.digits(6), f.digits(1))
	fmt.Fprintf(&b, "Chave PIX: %s-%s-%s-%s-%s\n\n", f.hex(8), f.hex(4), f.hex(4), f.hex(4), f.hex(12))
	b.WriteString("*Pagador*\n")
	fmt.Fprintf(&b, "Nome: %s\nCPF: %s\nAgência: %s-%s\nConta: %s-%s\nBanco: Ravenabank\n\n",
		authorName(c.Message, "Fulano"), f.cpf(), f.digits(4), f.digits(1), f.digits(6), f.digits(1))
	fmt.Fprintf(&b, "ID: %s\nDocumento: 00000%s\nAutenticação: 1.%s.%s.%s\n\n",
		strings.Repeat("0", 14)+f.digits(16), f.digits(5), f.digits(5), f.digits(7), f.digits(6))
	b.WriteString("> Este é o seu comprovante, guarde com cuidado e não compartilhe")

	out.Kind, out.Text = bot.OutText, b.String()
	return c.Reply(ctx, out)
}
