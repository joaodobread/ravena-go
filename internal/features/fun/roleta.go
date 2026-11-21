package fun

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/joaodobread/ravena-go/internal/bot"
)

const (
	roletaDefaultTimeout = 300
	roletaMinTimeout     = 10
	roletaMaxTimeout     = 259200 // 72 horas
)

var roletaEmojis = []string{"🥇", "🥈", "🥉", "🐅", "🐆", "🦌", "🐐", "🐏", "🐓", "🐇"}

var roletaFrases = []string{
	"Todos esses momentos se perderão no tempo, como lágrimas na chuva. Hora de morrer.",
	"O que fazemos na vida ecoa na eternidade.",
	"Valar Morghulis.",
	"Diga ao Sol que ele pode se pôr, pois eu encontrei o verdadeiro brilho.",
	"Até logo, Cowboy do Espaço.",
	"O mundo é apenas um palco, e a maioria de nós está desesperadamente sem ensaio.",
	"Não chore porque acabou, sorria porque aconteceu.",
	"A morte é apenas a próxima grande aventura.",
	"Você não está morrendo, está apenas sendo promovido a fantasma.",
	"Que a Força esteja com você em sua jornada final.",
	"Um homem só morre quando é esquecido.",
	"O meu tesouro? Se quiserem, podem pegá-lo. Procurem-no! Ele contém tudo o que este mundo tem a oferecer!",
	"Eu sou o Homem de Ferro.",
	"Você morreu.",
	"Sayonara, baby.",
	"A vida é uma jornada, mas a morte é o destino final.",
	"Vivi uma vida sem arrependimentos.",
	"Não tema a morte, tema a vida não vivida.",
	"O inverno chegou para você.",
	"Isso é tudo, pessoal!",
	"A morte é o fim de uma canção, mas a melodia continua.",
	"O medo da morte é pior que a própria morte.",
	"O amanhã não é garantido para ninguém.",
	"Sua jornada termina aqui, mas sua lenda apenas começou.",
	"O brilho que queima duas vezes mais forte, queima pela metade do tempo.",
}

func (f *Feature) roletaCommands() []*bot.Command {
	return []*bot.Command{
		{
			Name:        "roletarussa",
			Description: "Joga roleta russa, risco de ser silenciado",
			Category:    "jogos",
			GroupOnly:   true,
			Reactions:   bot.Reactions{Error: "❌"}, // 🔫/💀 aplicados pelo próprio jogo
			Run:         f.roleta,
		},
		{
			Name:        "roleta-ranking",
			Aliases:     []string{"roletaranking"},
			Description: "Mostra ranking da roleta russa",
			Category:    "jogos",
			GroupOnly:   true,
			Cooldown:    10 * time.Second,
			Reactions:   bot.Reactions{After: "🏆", Error: "❌"},
			Run: func(ctx context.Context, c *bot.Context) error {
				text, err := f.roletaRanking(ctx, c.Message.Chat.String())
				if err != nil {
					return err
				}
				return c.Reply(ctx, bot.Text(text))
			},
		},
		{
			Name:        "roleta-reset",
			Description: "Reseta os dados da roleta russa para este grupo",
			Category:    "jogos",
			GroupOnly:   true,
			AdminOnly:   true,
			Cooldown:    time.Minute,
			Reactions:   bot.Reactions{After: "🔄", Error: "❌"},
			Run:         f.roletaReset,
		},
		{
			Name:        "roleta-tempo",
			Description: "Define o tempo de timeout da roleta russa",
			Usage:       "!roleta-tempo 300",
			Category:    "jogos",
			GroupOnly:   true,
			AdminOnly:   true,
			Cooldown:    10 * time.Second,
			Reactions:   bot.Reactions{After: "🕐", Error: "❌"},
			Run:         f.roletaTempo,
		},
	}
}

type roletaPlayer struct {
	key                              string
	current, maxTries, deaths, total int
	deadUntil                        int64 // unix ms
}

func (f *Feature) roletaGroup(ctx context.Context, chat string) (timeout int, last string, err error) {
	err = f.db.QueryRowContext(ctx, `SELECT timeout, last_player FROM fun_roleta_groups WHERE chat = ?`, chat).Scan(&timeout, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return roletaDefaultTimeout, "", nil
	}
	return timeout, last, err
}

func (f *Feature) roleta(ctx context.Context, c *bot.Context) error {
	chat := c.Message.Chat.String()
	name := authorName(c.Message, "Jogador")
	timeout, last, err := f.roletaGroup(ctx, chat)
	if err != nil {
		return err
	}
	key, found, err := f.existingKey(ctx, "fun_roleta_players", c)
	if err != nil {
		return err
	}
	p := roletaPlayer{key: key}
	if found {
		if err := f.db.QueryRowContext(ctx,
			`SELECT current, max_tries, deaths, total, dead_until FROM fun_roleta_players WHERE chat = ? AND user = ?`,
			chat, key).Scan(&p.current, &p.maxTries, &p.deaths, &p.total, &p.deadUntil); err != nil {
			return err
		}
	}

	now := f.now()
	if p.deadUntil > now.UnixMilli() {
		secs := int((time.UnixMilli(p.deadUntil).Sub(now) + time.Second - 1) / time.Second)
		f.react(ctx, c, "💀")
		return c.ReplyText(ctx, "💀 %s já está morto na roleta russa. Ressuscita em %dm%ds.", name, secs/60, secs%60)
	}
	if last == key {
		return c.ReplyText(ctx, "🔄 %s, espere outra pessoa jogar antes de tentar novamente.", name)
	}

	if _, err := f.db.ExecContext(ctx, `
		INSERT INTO fun_roleta_groups(chat, last_player) VALUES(?, ?)
		ON CONFLICT(chat) DO UPDATE SET last_player = excluded.last_player`, chat, key); err != nil {
		return err
	}

	p.current++
	p.total++
	died := f.rand(6) == 0
	var text string
	if died {
		p.deaths++
		tries, oldMax := p.current, p.maxTries
		if tries > p.maxTries {
			p.maxTries = tries
			text = fmt.Sprintf("Morreu em %d, um novo record! Seu máximo antes disso era %d.\nNeste grupo, você já morreu %d vezes.", tries, oldMax, p.deaths)
		} else {
			text = fmt.Sprintf("Morreu em %d.\nNeste grupo, você já morreu %d vezes.", tries, p.deaths)
		}
		p.current = 0
		p.deadUntil = now.Add(time.Duration(timeout) * time.Second).UnixMilli()
		text = fmt.Sprintf("💥🔫 *BANG* - *F no chat* %s\n\n> _%s_", text, f.pick(roletaFrases))
	} else {
		text = fmt.Sprintf("💨🔫 *click* - Tá *safe*! ```%d```", p.current)
	}

	if _, err := f.db.ExecContext(ctx, `
		INSERT INTO fun_roleta_players(chat, user, name, current, max_tries, deaths, dead_until, total)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(chat, user) DO UPDATE SET name = excluded.name, current = excluded.current,
			max_tries = excluded.max_tries, deaths = excluded.deaths,
			dead_until = excluded.dead_until, total = excluded.total`,
		chat, key, name, p.current, p.maxTries, p.deaths, p.deadUntil, p.total); err != nil {
		return err
	}
	if died {
		f.react(ctx, c, "💀")
	} else {
		f.react(ctx, c, "🔫")
	}
	return c.Reply(ctx, bot.Text(text))
}

// react é melhor esforço: uma reação perdida não estraga a jogada.
func (f *Feature) react(ctx context.Context, c *bot.Context, emoji string) {
	_ = c.Client.React(ctx, c.Message, emoji)
}

func (f *Feature) roletaRanking(ctx context.Context, chat string) (string, error) {
	type row struct {
		name           string
		value, current int
	}
	query := func(q string) ([]row, error) {
		rows, err := f.db.QueryContext(ctx, q, chat)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.name, &r.value, &r.current); err != nil {
				return nil, err
			}
			out = append(out, r)
		}
		return out, rows.Err()
	}
	luck, err := query(`SELECT name, MAX(max_tries, current), current FROM fun_roleta_players
		WHERE chat = ? AND (max_tries > 0 OR current > 0) ORDER BY MAX(max_tries, current) DESC, name LIMIT 10`)
	if err != nil {
		return "", err
	}
	deaths, err := query(`SELECT name, deaths, 0 FROM fun_roleta_players
		WHERE chat = ? AND deaths > 0 ORDER BY deaths DESC, name LIMIT 10`)
	if err != nil {
		return "", err
	}
	if len(luck) == 0 && len(deaths) == 0 {
		return "🏆 Ainda não há jogadores na roleta russa deste grupo.", nil
	}
	nameOf := func(n string) string {
		if n == "" {
			return "Desconhecido"
		}
		return n
	}
	var b strings.Builder
	b.WriteString("🏆 *Rankings Roleta Russa* 🔫\n\n🍀 *Sorte - Máx. Tentativas sem morrer*\n")
	if len(luck) == 0 {
		b.WriteString("\tAinda não há jogadores neste ranking\n")
	}
	for i, r := range luck {
		playing := ""
		if r.current > 0 {
			playing = fmt.Sprintf(" *(%d atual)*", r.current)
		}
		fmt.Fprintf(&b, "\t%s %d°: %d%s - %s\n", roletaEmojis[i], i+1, r.value, playing, nameOf(r.name))
	}
	b.WriteString("\n🪦 *Número de Mortes*\n")
	if len(deaths) == 0 {
		b.WriteString("\tAinda não há jogadores neste ranking\n")
	}
	for i, r := range deaths {
		fmt.Fprintf(&b, "\t%s %d°: %d - %s\n", roletaEmojis[i], i+1, r.value, nameOf(r.name))
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func (f *Feature) roletaReset(ctx context.Context, c *bot.Context) error {
	chat := c.Message.Chat.String()
	var count int
	if err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM fun_roleta_players WHERE chat = ?`, chat).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return c.ReplyText(ctx, "⚠️ Não há dados da roleta russa para este grupo.")
	}
	ranking, err := f.roletaRanking(ctx, chat)
	if err != nil {
		return err
	}
	if _, err := f.db.ExecContext(ctx, `DELETE FROM fun_roleta_players WHERE chat = ?`, chat); err != nil {
		return err
	}
	if _, err := f.db.ExecContext(ctx, `UPDATE fun_roleta_groups SET last_player = '' WHERE chat = ?`, chat); err != nil {
		return err
	}
	if err := c.Client.Send(ctx, c.Message.Chat, bot.Text(ranking)); err != nil {
		return err
	}
	return c.ReplyText(ctx, "🔄 *Dados da Roleta Russa Resetados*\n\nForam removidos dados de %d jogadores deste grupo.\n\nO ranking acima mostra como estava antes do reset.", count)
}

func (f *Feature) roletaTempo(ctx context.Context, c *bot.Context) error {
	if len(c.Args) == 0 {
		return bot.Userf("Por favor, forneça um tempo em segundos (mínimo %d, máximo %d, 72 horas). Exemplo: %sroleta-tempo 300", roletaMinTimeout, roletaMaxTimeout, c.Prefix)
	}
	secs, err := strconv.Atoi(c.Args[0])
	if err != nil {
		if !strings.Contains(c.Args[0], ".") && len(strings.TrimLeft(c.Args[0], "0123456789")) == 0 {
			secs = roletaMaxTimeout // número gigante
		} else {
			return bot.Userf("Por favor, forneça um tempo em segundos (mínimo %d, máximo %d, 72 horas). Exemplo: %sroleta-tempo 300", roletaMinTimeout, roletaMaxTimeout, c.Prefix)
		}
	}
	secs = min(max(secs, roletaMinTimeout), roletaMaxTimeout)
	if _, err := f.db.ExecContext(ctx, `
		INSERT INTO fun_roleta_groups(chat, timeout) VALUES(?, ?)
		ON CONFLICT(chat) DO UPDATE SET timeout = excluded.timeout`, c.Message.Chat.String(), secs); err != nil {
		return err
	}
	var t string
	switch m, s := secs/60, secs%60; {
	case m > 0 && s > 0:
		t = fmt.Sprintf("%d minuto(s) e %d segundo(s)", m, s)
	case m > 0:
		t = fmt.Sprintf("%d minuto(s)", m)
	default:
		t = fmt.Sprintf("%d segundo(s)", secs)
	}
	return c.ReplyText(ctx, "🕐 Tempo de \"morte\" na roleta russa definido para %s.", t)
}
