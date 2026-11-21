// Package reminders implementa lembretes (!lembrar) persistentes: ficam no
// SQLite e um único timer aponta para o próximo vencimento.
package reminders

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/joaodobread/ravena-go/internal/app"
	"github.com/joaodobread/ravena-go/internal/bot"
)

// Limites para ninguém lotar o banco.
const (
	maxPerUser = 20
	maxAhead   = 366 * 24 * time.Hour
	maxText    = 1000
)

var migrations = []string{
	`CREATE TABLE reminders (
		id      INTEGER PRIMARY KEY,
		chat    TEXT NOT NULL,
		user    TEXT NOT NULL,
		text    TEXT NOT NULL,
		due     INTEGER NOT NULL,
		created INTEGER NOT NULL
	)`,
	`CREATE INDEX reminders_due ON reminders(due)`,
	`CREATE INDEX reminders_chat_user ON reminders(chat, user)`,
}

// Module implementa app.Feature.
type Module struct{}

// Name implementa app.Feature.
func (Module) Name() string { return "reminders" }

// Register implementa app.Feature.
func (Module) Register(ctx context.Context, d *app.Deps) error {
	if err := d.Store.Migrate(ctx, "reminders", migrations); err != nil {
		return err
	}
	s := New(d.Store.DB(), d.Client, d.Log, time.Now)
	if err := d.Router.Register(s.Commands()...); err != nil {
		return err
	}
	go s.Run(ctx)
	return nil
}

// Scheduler guarda e dispara lembretes.
type Scheduler struct {
	db     *sql.DB
	client bot.Client
	log    *slog.Logger
	now    func() time.Time
	wake   chan struct{}
	mu     sync.Mutex // serializa disparos
}

// New cria o Scheduler.
func New(db *sql.DB, client bot.Client, log *slog.Logger, now func() time.Time) *Scheduler {
	return &Scheduler{db: db, client: client, log: log, now: now, wake: make(chan struct{}, 1)}
}

// Commands devolve os comandos.
func (s *Scheduler) Commands() []*bot.Command {
	return []*bot.Command{
		{
			Name: "lembrar", Aliases: []string{"lembrete"}, Category: "utilidades",
			Description: "Cria um lembrete",
			Usage:       "!lembrar amanhã às 9h pagar a conta (ou responda a uma mensagem)",
			Reactions:   bot.Reactions{After: "⏰", Error: "❌"},
			Run:         s.add,
		},
		{Name: "lembretes", Category: "utilidades", Description: "Seus lembretes pendentes neste chat", Run: s.list},
		{Name: "l-cancelar", Category: "utilidades", Description: "Cancela um lembrete", Usage: "!l-cancelar <número>", Run: s.cancel},
	}
}

func (s *Scheduler) add(ctx context.Context, c *bot.Context) error {
	now := s.now()
	due, text, ok := ParseWhen(c.RawArgs(), now)
	if !ok {
		return bot.Userf("Não entendi quando. Exemplos:\n%slembrar em 10 minutos tirar o bolo\n%slembrar amanhã às 9h academia\n%slembrar 25/12 às 10h natal", c.Prefix, c.Prefix, c.Prefix)
	}
	if text == "" && c.Message.Quoted != nil {
		text = c.Message.Quoted.Text
	}
	text = strings.TrimSpace(text)
	switch {
	case text == "":
		return bot.Userf("Lembrar do quê? Escreva depois da data ou responda a uma mensagem.")
	case len(text) > maxText:
		return bot.Userf("Texto longo demais (máx. %d caracteres).", maxText)
	case !due.After(now):
		return bot.Userf("Essa data já passou.")
	case due.Sub(now) > maxAhead:
		return bot.Userf("Só consigo lembrar até um ano à frente.")
	}

	chat, user := c.Message.Chat.String(), c.Message.Sender.String()
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM reminders WHERE chat = ? AND user = ?`, chat, user).Scan(&n); err != nil {
		return err
	}
	if n >= maxPerUser {
		return bot.Userf("Você já tem %d lembretes aqui. Cancele algum com %sl-cancelar.", n, c.Prefix)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO reminders(chat, user, text, due, created) VALUES(?, ?, ?, ?, ?)`,
		chat, user, text, due.Unix(), now.Unix()); err != nil {
		return err
	}
	s.poke()
	return c.ReplyText(ctx, "⏰ Combinado! Te lembro %s.", human(due, now))
}

type item struct {
	id   int64
	text string
	due  time.Time
}

func (s *Scheduler) pending(ctx context.Context, chat, user string) ([]item, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, text, due FROM reminders WHERE chat = ? AND user = ? ORDER BY due`, chat, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []item
	for rows.Next() {
		var it item
		var due int64
		if err := rows.Scan(&it.id, &it.text, &due); err != nil {
			return nil, err
		}
		it.due = time.Unix(due, 0).In(s.now().Location())
		out = append(out, it)
	}
	return out, rows.Err()
}

func (s *Scheduler) list(ctx context.Context, c *bot.Context) error {
	items, err := s.pending(ctx, c.Message.Chat.String(), c.Message.Sender.String())
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return c.ReplyText(ctx, "Você não tem lembretes pendentes aqui.")
	}
	var b strings.Builder
	b.WriteString("⏰ *Seus lembretes*\n")
	for i, it := range items {
		fmt.Fprintf(&b, "\n%d. %s — %s", i+1, it.due.Format("02/01 15:04"), truncate(it.text, 80))
	}
	fmt.Fprintf(&b, "\n\nPara cancelar: %sl-cancelar <número>", c.Prefix)
	return c.ReplyText(ctx, "%s", b.String())
}

func (s *Scheduler) cancel(ctx context.Context, c *bot.Context) error {
	n, err := strconv.Atoi(c.ArgText())
	if err != nil || n < 1 {
		return bot.Userf("Use %sl-cancelar <número> (veja os números em %slembretes).", c.Prefix, c.Prefix)
	}
	items, err := s.pending(ctx, c.Message.Chat.String(), c.Message.Sender.String())
	if err != nil {
		return err
	}
	if n > len(items) {
		return bot.Userf("Não existe o lembrete %d.", n)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM reminders WHERE id = ?`, items[n-1].id); err != nil {
		return err
	}
	s.poke()
	return c.ReplyText(ctx, "🗑️ Lembrete cancelado: %s", truncate(items[n-1].text, 80))
}

func (s *Scheduler) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run dispara os lembretes até ctx acabar. Um único timer, sempre apontando
// para o próximo vencimento (ou 1h, para reavaliar).
func (s *Scheduler) Run(ctx context.Context) {
	for {
		next := s.Fire(ctx)
		wait := time.Hour
		if !next.IsZero() {
			wait = min(max(next.Sub(s.now()), 0), time.Hour)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-s.wake:
		case <-timer.C:
		}
		timer.Stop()
	}
}

// Fire envia os lembretes vencidos e devolve o próximo vencimento (zero se nenhum).
func (s *Scheduler) Fire(ctx context.Context) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	rows, err := s.db.QueryContext(ctx, `SELECT id, chat, user, text FROM reminders WHERE due <= ? ORDER BY due LIMIT 50`, now.Unix())
	if err != nil {
		s.log.Error("lendo lembretes", "err", err)
		return now.Add(time.Minute)
	}
	type due struct {
		id               int64
		chat, user, text string
	}
	var batch []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.chat, &d.user, &d.text); err == nil {
			batch = append(batch, d)
		}
	}
	rows.Close()

	for _, d := range batch {
		chat, err1 := types.ParseJID(d.chat)
		user, err2 := types.ParseJID(d.user)
		if err1 == nil && err2 == nil {
			out := bot.Outgoing{Kind: bot.OutText, Text: "⏰ " + bot.MentionText(user) + ", você pediu para lembrar:\n\n" + d.text, Mentions: []types.JID{user}}
			if err := s.client.Send(ctx, chat, out); err != nil {
				s.log.Warn("falha ao enviar lembrete; tento de novo depois", "err", err, "id", d.id)
				return now.Add(time.Minute)
			}
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM reminders WHERE id = ?`, d.id); err != nil {
			s.log.Error("apagando lembrete", "err", err)
		}
	}

	var next sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MIN(due) FROM reminders`).Scan(&next); err != nil || !next.Valid {
		return time.Time{}
	}
	return time.Unix(next.Int64, 0)
}

func human(due, now time.Time) string {
	d := due.Sub(now)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("em %d min", int(d.Round(time.Minute).Minutes()))
	case due.YearDay() == now.YearDay() && due.Year() == now.Year():
		return "hoje às " + due.Format("15:04")
	case due.YearDay() == now.AddDate(0, 0, 1).YearDay():
		return "amanhã às " + due.Format("15:04")
	}
	return "em " + due.Format("02/01/2006 às 15:04")
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
