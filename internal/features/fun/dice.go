package fun

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/joaodobread/ravena-go/internal/bot"
)

const (
	maxDice  = 20
	maxSides = 1000
)

var (
	diceRe     = regexp.MustCompile(`(?i)^(\d*)d(\d+)([+-]\d+)?$`)
	modifierRe = regexp.MustCompile(`^[+-]\d+$`)
)

// MsgDiceFormat é a resposta para um padrão de dados inválido.
const MsgDiceFormat = "Formato inválido. Use algo como: d20, 2d6, d8+3, 3d10-2"

type roll struct {
	dice, sides, mod int
}

func (f *Feature) diceCommands() []*bot.Command {
	var cmds []*bot.Command
	for _, sides := range []int{4, 6, 8, 10, 12, 20, 100} {
		cmds = append(cmds, &bot.Command{
			Name:        fmt.Sprintf("d%d", sides),
			Description: fmt.Sprintf("Rola um dado de %d faces", sides),
			Usage:       fmt.Sprintf("!d%d [quantidade] [+modificador]", sides),
			Category:    "jogos",
			Reactions:   bot.Reactions{After: "🎲"},
			Run: func(ctx context.Context, c *bot.Context) error {
				r := roll{dice: 1, sides: sides}
				for i, a := range c.Args {
					if modifierRe.MatchString(a) {
						r.mod, _ = strconv.Atoi(a)
					} else if n, err := strconv.Atoi(a); err == nil && i == 0 {
						r.dice = n
					}
				}
				return f.roll(ctx, c, r)
			},
		})
	}
	cmds = append(cmds, &bot.Command{
		Name:        "roll",
		Description: "Rola dados com padrão customizado (ex: 2d6+3)",
		Usage:       "!roll 2d6+3",
		Category:    "jogos",
		Reactions:   bot.Reactions{After: "🎲"},
		Run: func(ctx context.Context, c *bot.Context) error {
			pattern := "d20"
			if len(c.Args) > 0 {
				pattern = strings.Join(c.Args, "")
			}
			r, ok := parseDice(pattern)
			if !ok {
				return bot.Userf(MsgDiceFormat)
			}
			return f.roll(ctx, c, r)
		},
	})
	return cmds
}

func parseDice(s string) (roll, bool) {
	m := diceRe.FindStringSubmatch(s)
	if m == nil {
		return roll{}, false
	}
	r := roll{dice: 1}
	if m[1] != "" {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			n = maxDice + 1 // número gigante: cai no aviso de limite
		}
		r.dice = n
	}
	var err error
	if r.sides, err = strconv.Atoi(m[2]); err != nil {
		return roll{}, false
	}
	if m[3] != "" {
		if r.mod, err = strconv.Atoi(m[3]); err != nil {
			return roll{}, false
		}
	}
	return r, true
}

func (f *Feature) roll(ctx context.Context, c *bot.Context, r roll) error {
	var warn []string
	if r.dice > maxDice {
		warn = append(warn, fmt.Sprintf("⚠️ Número máximo de dados é %d.", maxDice))
		r.dice = maxDice
	}
	if r.sides > maxSides {
		warn = append(warn, fmt.Sprintf("⚠️ Número máximo de faces é %d.", maxSides))
		r.sides = maxSides
	}
	r.dice = max(r.dice, 1)
	r.sides = max(r.sides, 2)

	rolls := make([]string, r.dice)
	total, first := 0, 0
	for i := range r.dice {
		v := f.between(1, r.sides)
		if i == 0 {
			first = v
		}
		rolls[i] = strconv.Itoa(v)
		total += v
	}
	total += r.mod

	emoji, crit := "🎲", ""
	if r.sides == 20 && r.dice == 1 {
		switch first {
		case 20:
			emoji, crit = "✨", " (Sucesso Crítico!)"
		case 1:
			emoji, crit = "💀", " (Falha Crítica!)"
		}
	}
	mod := ""
	if r.mod != 0 {
		mod = fmt.Sprintf("%+d", r.mod)
	}

	var res string
	switch {
	case r.dice == 1 && r.mod == 0:
		res = fmt.Sprintf("%s *%d*%s (d%d)", emoji, first, crit, r.sides)
	case r.dice == 1:
		res = fmt.Sprintf("%s *%d*%s [%d %s] (d%d%s)", emoji, total, crit, first, mod, r.sides, mod)
	default:
		detail := strings.Join(rolls, " + ")
		if r.mod != 0 {
			detail += fmt.Sprintf(" %s %d", mod[:1], abs(r.mod))
		}
		res = fmt.Sprintf("%s *%d*%s [%s] (%dd%d%s)", emoji, total, crit, detail, r.dice, r.sides, mod)
	}

	text := authorName(c.Message, "Jogador") + " rolou:\n" + res
	if len(warn) > 0 {
		text = strings.Join(warn, "\n") + "\n\n" + text
	}
	return c.Reply(ctx, bot.Text(text))
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
