// Package sequence implementa o desafio de sequência lógica (!sequencia,
// !seq <resposta>, !sequencia-ranking): todos podem responder durante a
// rodada; quem acerta mais rápido ganha mais pontos.
package sequence

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/joaodobread/ravena-go/internal/app"
	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/features/gamekit"
)

// Regras do jogo.
const (
	RoundTime  = 30 * time.Second
	MinScore   = 10
	MaxScore   = 100
	MaxGames   = 200 // rodadas simultâneas (teto de memória)
	MaxWinners = 100
)

// Config monta o jogo. Campos zerados recebem o padrão.
type Config struct {
	DB        *sql.DB
	Client    bot.Client // usado pelo timer de fim
	Schedule  gamekit.Scheduler
	Rand      func(n int) int
	Now       func() time.Time
	RoundTime time.Duration
	Log       *slog.Logger
}

type winner struct {
	id    types.JID
	name  string
	score int
	took  time.Duration
}

type round struct {
	puzzle   Puzzle
	started  time.Time
	winners  []winner
	answered map[string]bool
	stop     func() bool
}

// Game é a funcionalidade.
type Game struct {
	cfg  Config
	base context.Context

	mu     sync.Mutex
	rounds map[types.JID]*round
}

var migrations = []string{
	`CREATE TABLE logic_scores (
		group_id     TEXT NOT NULL,
		user_id      TEXT NOT NULL,
		user_name    TEXT NOT NULL DEFAULT '',
		total_score  INTEGER NOT NULL DEFAULT 0,
		games_played INTEGER NOT NULL DEFAULT 0,
		wins         INTEGER NOT NULL DEFAULT 0,
		last_updated INTEGER,
		PRIMARY KEY (group_id, user_id)
	)`,
}

// Migrations são as migrações do escopo "sequence".
func Migrations() []string { return migrations }

// New cria o jogo; quando base termina, as rodadas são canceladas.
func New(base context.Context, cfg Config) *Game {
	if cfg.Schedule == nil {
		cfg.Schedule = gamekit.RealScheduler
	}
	if cfg.Rand == nil {
		cfg.Rand = rand.IntN
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.RoundTime == 0 {
		cfg.RoundTime = RoundTime
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	g := &Game{cfg: cfg, base: base, rounds: map[types.JID]*round{}}
	context.AfterFunc(base, g.stopAll)
	return g
}

func (g *Game) stopAll() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for chat, r := range g.rounds {
		r.stop()
		delete(g.rounds, chat)
	}
}

// Active diz quantas rodadas estão em andamento.
func (g *Game) Active() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.rounds)
}

// Commands devolve os comandos do jogo.
func (g *Game) Commands() []*bot.Command {
	return []*bot.Command{
		{
			Name: "sequencia", Aliases: []string{"sequência"}, Category: "jogos",
			Description: "Inicia um desafio de sequência lógica",
			Cooldown:    10 * time.Second,
			Reactions:   bot.Reactions{Before: "🧠"},
			Run:         g.start,
		},
		{
			Name: "seq", Category: "jogos", Usage: "!seq <resposta>",
			Description: "Responde o desafio de sequência (sem resposta, inicia um)",
			Run:         g.guess,
		},
		{
			Name: "sequencia-ranking", Category: "jogos",
			Description: "Mostra o ranking de lógica",
			Cooldown:    5 * time.Second,
			Reactions:   bot.Reactions{Before: "📊"},
			Run:         g.ranking,
		},
	}
}

func (g *Game) start(ctx context.Context, c *bot.Context) error {
	chat := c.Message.Chat
	g.mu.Lock()
	if _, ok := g.rounds[chat]; ok {
		g.mu.Unlock()
		return bot.Userf("⚠️ Já existe um desafio de sequência ativo!\nResponda com *%sseq <resposta>*", c.Prefix)
	}
	if len(g.rounds) >= MaxGames {
		g.mu.Unlock()
		return bot.Userf("Muitos desafios acontecendo agora. Tente daqui a pouco.")
	}
	r := &round{puzzle: Random(g.cfg.Rand), started: g.cfg.Now(), answered: map[string]bool{}}
	r.stop = g.cfg.Schedule(g.cfg.RoundTime, func() { g.end(chat, r) })
	g.rounds[chat] = r
	g.mu.Unlock()

	return c.ReplyText(ctx, "🧠 *Desafio de Lógica* 🧠\n\nDescubra o resultado ou o termo que falta:\n\n➡️ *%s*\n\n⏳ Tempo: %d segundos\n💡 Use *%sseq <resposta>* para responder!",
		r.puzzle.Sequence, int(g.cfg.RoundTime/time.Second), c.Prefix)
}

func (g *Game) guess(ctx context.Context, c *bot.Context) error {
	chat := c.Message.Chat
	g.mu.Lock()
	r, ok := g.rounds[chat]
	g.mu.Unlock()
	if len(c.Args) == 0 {
		if !ok {
			return g.start(ctx, c)
		}
		return bot.Userf("⚠️ Você precisa enviar uma resposta! Ex: *%sseq 42*", c.Prefix)
	}
	if !ok {
		return bot.Userf("⚠️ Nenhum desafio ativo no momento. Use *%ssequencia* para iniciar.", c.Prefix)
	}

	user := gamekit.UserID(c.Message)
	guess := gamekit.Fold(c.ArgText())
	emoji := "❌"
	g.mu.Lock()
	switch {
	case g.rounds[chat] != r:
		g.mu.Unlock()
		return bot.ErrSilent // acabou enquanto isso
	case r.answered[user.User]:
		emoji = "👀"
	case slices.ContainsFunc(r.puzzle.Answers, func(a string) bool { return gamekit.Fold(a) == guess }):
		emoji = "✅"
		if len(r.winners) < MaxWinners {
			took := g.cfg.Now().Sub(r.started)
			left := max(0, 1-float64(took)/float64(g.cfg.RoundTime))
			r.winners = append(r.winners, winner{
				id: user, name: gamekit.Name(c.Message, "Jogador"), took: took,
				score: int(MinScore + left*(MaxScore-MinScore)),
			})
		}
		r.answered[user.User] = true
	}
	g.mu.Unlock()
	return c.Client.React(ctx, c.Message, emoji)
}

// end fecha a rodada, mostra os vencedores e soma os pontos.
func (g *Game) end(chat types.JID, r *round) {
	g.mu.Lock()
	if g.rounds[chat] != r {
		g.mu.Unlock()
		return
	}
	delete(g.rounds, chat)
	winners := slices.Clone(r.winners)
	g.mu.Unlock()

	ctx, cancel := gamekit.SendCtx(g.base)
	defer cancel()
	var b strings.Builder
	fmt.Fprintf(&b, "🏁 *Fim de Jogo!* 🏁\n\nA resposta aceita era: *%s*\n📝 _%s_\n\n", r.puzzle.Answers[0], r.puzzle.Explanation)
	if len(winners) == 0 {
		b.WriteString("🐢 Ninguém acertou a tempo! Tente novamente.")
	} else {
		b.WriteString("🏆 *Vencedores:* 🏆\n")
		slices.SortStableFunc(winners, func(a, b winner) int { return b.score - a.score })
		for i, w := range winners {
			medal := "🔹"
			if i < 3 {
				medal = gamekit.Medal(i)
			}
			fmt.Fprintf(&b, "%s *%s* (+%d pts) em %.1fs\n", medal, w.name, w.score, w.took.Seconds())
			if err := g.addScore(ctx, chat, w); err != nil {
				g.cfg.Log.Warn("sequencia: gravando pontos", "err", err)
			}
		}
	}
	if err := g.cfg.Client.Send(ctx, chat, bot.Text(b.String())); err != nil {
		g.cfg.Log.Warn("sequencia: falha ao enviar fim", "err", err, "chat", chat)
	}
}

func (g *Game) addScore(ctx context.Context, chat types.JID, w winner) error {
	_, err := g.cfg.DB.ExecContext(ctx, `INSERT INTO logic_scores(group_id, user_id, user_name, total_score, games_played, wins, last_updated)
		VALUES(?, ?, ?, ?, 1, 1, ?)
		ON CONFLICT(group_id, user_id) DO UPDATE SET
			user_name = excluded.user_name,
			total_score = total_score + excluded.total_score,
			games_played = games_played + 1,
			wins = wins + 1,
			last_updated = excluded.last_updated`,
		chat.String(), w.id.String(), w.name, w.score, g.cfg.Now().Unix())
	return err
}

func (g *Game) ranking(ctx context.Context, c *bot.Context) error {
	rows, err := g.cfg.DB.QueryContext(ctx, `SELECT user_name, total_score, wins FROM logic_scores
		WHERE group_id = ? ORDER BY total_score DESC LIMIT 10`, c.Message.Chat.String())
	if err != nil {
		return err
	}
	defer rows.Close()
	var b strings.Builder
	b.WriteString("📊 *Ranking de Lógica* 📊\n\n")
	n := 0
	for rows.Next() {
		var (
			name        string
			score, wins int
		)
		if err := rows.Scan(&name, &score, &wins); err != nil {
			return err
		}
		fmt.Fprintf(&b, "%s *%s*\n   └ %d pts (%d vitórias)\n", gamekit.Medal(n), name, score, wins)
		n++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if n == 0 {
		return c.ReplyText(ctx, "📉 Ainda não há ranking aqui. Jogue com *%ssequencia*!", c.Prefix)
	}
	return c.ReplyText(ctx, "%s", strings.TrimRight(b.String(), "\n"))
}

// Module adapta o jogo ao contrato app.Feature.
type Module struct{}

// Name implementa app.Feature.
func (Module) Name() string { return "sequence" }

// Register implementa app.Feature.
func (Module) Register(ctx context.Context, d *app.Deps) error {
	if err := d.Store.Migrate(ctx, "sequence", migrations); err != nil {
		return err
	}
	g := New(ctx, Config{DB: d.Store.DB(), Client: d.Client, Log: d.Log})
	return d.Router.Register(g.Commands()...)
}
