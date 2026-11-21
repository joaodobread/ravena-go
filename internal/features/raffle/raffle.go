// Package raffle implementa os sorteios do grupo: !sorteio (cria ou mostra o
// ativo), !sorteio-entrar, !sorteio-sair, !sortear (realiza o ativo ou sorteia
// um membro na hora) e !sorteios (histórico). Reagir 🎲 à mensagem do sorteio
// entra/sai dele.
package raffle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/joaodobread/ravena-go/internal/app"
	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/features/gamekit"
)

// Limites.
const (
	MaxParticipants = 2000
	maxTitle        = 100
	Dice            = "🎲"
)

var migrations = []string{
	`CREATE TABLE sorteios (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		group_id    TEXT NOT NULL,
		title       TEXT NOT NULL,
		status      TEXT NOT NULL DEFAULT 'active',
		created_at  INTEGER NOT NULL,
		winner_id   TEXT,
		winner_name TEXT,
		creator_id  TEXT
	)`,
	`CREATE INDEX sorteios_group ON sorteios(group_id, status)`,
	`CREATE TABLE sorteio_participants (
		sorteio_id INTEGER NOT NULL REFERENCES sorteios(id) ON DELETE CASCADE,
		user_id    TEXT NOT NULL,
		user_name  TEXT NOT NULL DEFAULT '',
		joined_at  INTEGER NOT NULL,
		PRIMARY KEY (sorteio_id, user_id)
	)`,
}

// Migrations são as migrações do escopo "raffle".
func Migrations() []string { return migrations }

// Feature é a funcionalidade.
type Feature struct {
	db   *sql.DB
	rand func(n int) int
	now  func() time.Time
}

// New cria a funcionalidade; rnd e now podem ser nil.
func New(db *sql.DB, rnd func(int) int, now func() time.Time) *Feature {
	if rnd == nil {
		rnd = rand.IntN
	}
	if now == nil {
		now = time.Now
	}
	return &Feature{db: db, rand: rnd, now: now}
}

// Commands devolve os comandos.
func (f *Feature) Commands() []*bot.Command {
	return []*bot.Command{
		{Name: "sorteio", Category: "grupo", GroupOnly: true, Usage: "!sorteio [título]",
			Description: "Cria um sorteio ou mostra o sorteio ativo",
			Reactions:   bot.Reactions{After: "🎁"}, Run: f.sorteio},
		{Name: "sorteio-entrar", Category: "grupo", GroupOnly: true,
			Description: "Participa do sorteio ativo",
			Reactions:   bot.Reactions{After: "✅"}, Run: f.entrar},
		{Name: "sorteio-sair", Category: "grupo", GroupOnly: true,
			Description: "Sai do sorteio ativo",
			Reactions:   bot.Reactions{After: "👋"}, Run: f.sair},
		{Name: "sortear", Category: "grupo", GroupOnly: true, Usage: "!sortear [título]",
			Description: "Realiza o sorteio ativo ou sorteia um membro do grupo na hora",
			Reactions:   bot.Reactions{After: "🎉"}, Run: f.sortear},
		{Name: "sorteios", Category: "grupo", GroupOnly: true,
			Description: "Histórico de sorteios do grupo",
			Reactions:   bot.Reactions{After: "📋"}, Run: f.historico},
		{Name: "sorteio-reacao", Category: "grupo", GroupOnly: true, Hidden: true,
			Description: "Entra/sai do sorteio reagindo 🎲 à mensagem dele",
			Triggers:    []string{Dice}, Run: f.reacao},
	}
}

type raffle struct {
	id      int64
	title   string
	creator string
}

func (f *Feature) active(ctx context.Context, chat types.JID) (*raffle, error) {
	var (
		r       raffle
		creator sql.NullString
	)
	err := f.db.QueryRowContext(ctx, `SELECT id, title, creator_id FROM sorteios WHERE group_id = ? AND status = 'active' ORDER BY id DESC LIMIT 1`,
		chat.String()).Scan(&r.id, &r.title, &creator)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	r.creator = creator.String
	return &r, err
}

type member struct {
	jid  types.JID
	name string
}

func (m member) label() string {
	if m.name == "" {
		return bot.MentionText(m.jid)
	}
	return fmt.Sprintf("%s (%s)", m.name, bot.MentionText(m.jid))
}

func (f *Feature) participants(ctx context.Context, id int64) ([]member, error) {
	rows, err := f.db.QueryContext(ctx, `SELECT user_id, user_name FROM sorteio_participants WHERE sorteio_id = ? ORDER BY joined_at, rowid`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []member
	for rows.Next() {
		var (
			id string
			m  member
		)
		if err := rows.Scan(&id, &m.name); err != nil {
			return nil, err
		}
		m.jid, _ = types.ParseJID(id)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (f *Feature) sorteio(ctx context.Context, c *bot.Context) error {
	chat := c.Message.Chat
	r, err := f.active(ctx, chat)
	if err != nil {
		return err
	}
	if r == nil {
		title := strings.TrimSpace(c.ArgText())
		if title == "" {
			title = "Sorteio"
		}
		if t := []rune(title); len(t) > maxTitle {
			title = string(t[:maxTitle])
		}
		user := gamekit.Author(gamekit.Group(ctx, c.Client, chat), c.Message)
		if _, err := f.db.ExecContext(ctx, `INSERT INTO sorteios(group_id, title, status, created_at, creator_id) VALUES(?, ?, 'active', ?, ?)`,
			chat.String(), title, f.now().UnixMilli(), user.String()); err != nil {
			return err
		}
		return c.Client.Send(ctx, chat, bot.Text(fmt.Sprintf(
			"🎁 *NOVO SORTEIO CRIADO!* 🎁\n\nSorteio: *%s*\n\n• Para participar: digite *%ssorteio-entrar* ou reaja a esta mensagem com %s\n• Para sair: digite *%ssorteio-sair*\n\nParticipe e boa sorte! 📦",
			title, c.Prefix, Dice, c.Prefix)))
	}

	ps, err := f.participants(ctx, r.id)
	if err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "🎁 *Sorteio Ativo:* *%s*\n\n", r.title)
	var mentions []types.JID
	if len(ps) == 0 {
		b.WriteString("_Nenhum participante inscrito ainda._\n")
	} else {
		fmt.Fprintf(&b, "*Participantes (%d):*\n", len(ps))
		for i, p := range ps {
			fmt.Fprintf(&b, "%d. %s\n", i+1, p.label())
			mentions = append(mentions, p.jid)
		}
	}
	fmt.Fprintf(&b, "\nPara entrar, digite *%ssorteio-entrar* ou reaja com %s nesta mensagem.\nPara sair, digite *%ssorteio-sair*.\nPara realizar o sorteio, use *%ssortear*.",
		c.Prefix, Dice, c.Prefix, c.Prefix)
	return c.Client.Send(ctx, chat, bot.Outgoing{Kind: bot.OutText, Text: b.String(), Mentions: mentions})
}

func (f *Feature) isIn(ctx context.Context, id int64, user types.JID) (bool, error) {
	var n int
	err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sorteio_participants WHERE sorteio_id = ? AND user_id = ?`, id, user.String()).Scan(&n)
	return n > 0, err
}

func (f *Feature) join(ctx context.Context, r *raffle, user types.JID, name string) error {
	var n int
	if err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sorteio_participants WHERE sorteio_id = ?`, r.id).Scan(&n); err != nil {
		return err
	}
	if n >= MaxParticipants {
		return bot.Userf("O sorteio %q já está lotado.", r.title)
	}
	_, err := f.db.ExecContext(ctx, `INSERT OR IGNORE INTO sorteio_participants(sorteio_id, user_id, user_name, joined_at) VALUES(?, ?, ?, ?)`,
		r.id, user.String(), name, f.now().UnixMilli())
	return err
}

func (f *Feature) leave(ctx context.Context, r *raffle, user types.JID) error {
	_, err := f.db.ExecContext(ctx, `DELETE FROM sorteio_participants WHERE sorteio_id = ? AND user_id = ?`, r.id, user.String())
	return err
}

func (f *Feature) entrar(ctx context.Context, c *bot.Context) error {
	r, err := f.active(ctx, c.Message.Chat)
	if err != nil {
		return err
	}
	if r == nil {
		return bot.Userf("Não há nenhum sorteio ativo neste grupo. Crie um com *%ssorteio [nome]*.", c.Prefix)
	}
	user := gamekit.Author(gamekit.Group(ctx, c.Client, c.Message.Chat), c.Message)
	name := gamekit.Name(c.Message, "Participante")
	in, err := f.isIn(ctx, r.id, user)
	if err != nil {
		return err
	}
	if in {
		return bot.Userf("%s, você já está participando do sorteio %q!", name, r.title)
	}
	if err := f.join(ctx, r, user, strings.TrimSpace(c.Message.PushName)); err != nil {
		return err
	}
	return c.ReplyText(ctx, "✅ *%s* entrou no sorteio %q!", name, r.title)
}

func (f *Feature) sair(ctx context.Context, c *bot.Context) error {
	r, err := f.active(ctx, c.Message.Chat)
	if err != nil {
		return err
	}
	if r == nil {
		return bot.Userf("Não há nenhum sorteio ativo neste grupo no momento.")
	}
	user := gamekit.Author(gamekit.Group(ctx, c.Client, c.Message.Chat), c.Message)
	name := gamekit.Name(c.Message, "Participante")
	in, err := f.isIn(ctx, r.id, user)
	if err != nil {
		return err
	}
	if !in {
		return bot.Userf("%s, você não está participando do sorteio %q.", name, r.title)
	}
	if err := f.leave(ctx, r, user); err != nil {
		return err
	}
	return c.ReplyText(ctx, "👋 *%s* saiu do sorteio %q.", name, r.title)
}

// reacao trata 🎲 numa mensagem do bot sobre o sorteio ativo: entra ou sai.
func (f *Feature) reacao(ctx context.Context, c *bot.Context) error {
	msg := c.Message
	if c.Reaction == nil || !(msg.FromMe || msg.SenderIs(c.Client.Me())) {
		return bot.ErrSilent
	}
	r, err := f.active(ctx, msg.Chat)
	if err != nil {
		return err
	}
	if r == nil || !strings.Contains(msg.Text, "orteio") || !strings.Contains(msg.Text, "*"+r.title+"*") {
		return bot.ErrSilent // reação em outra mensagem (ou sorteio antigo)
	}
	info := gamekit.Group(ctx, c.Client, msg.Chat)
	user := gamekit.Canonical(info, gamekit.Prefer(c.Reaction.By, c.Reaction.ByAlt))
	in, err := f.isIn(ctx, r.id, user)
	if err != nil {
		return err
	}
	out := bot.Outgoing{Kind: bot.OutText, Mentions: []types.JID{user}}
	if in {
		if err := f.leave(ctx, r, user); err != nil {
			return err
		}
		out.Text = fmt.Sprintf("👋 %s saiu do sorteio %q!", bot.MentionText(user), r.title)
	} else {
		if err := f.join(ctx, r, user, ""); err != nil {
			return err
		}
		out.Text = fmt.Sprintf("✅ %s entrou no sorteio %q!", bot.MentionText(user), r.title)
	}
	return c.Client.Send(ctx, msg.Chat, out)
}

func (f *Feature) sortear(ctx context.Context, c *bot.Context) error {
	chat := c.Message.Chat
	info := gamekit.Group(ctx, c.Client, chat)
	r, err := f.active(ctx, chat)
	if err != nil {
		return err
	}
	author := gamekit.Author(info, c.Message)
	creator := false
	if r != nil {
		if j, err := types.ParseJID(r.creator); err == nil && j.User == author.User {
			creator = true
		}
	}
	if !creator && !c.IsAdmin(ctx) {
		return bot.Userf("⚠️ Só admins do grupo ou quem criou o sorteio podem usar *%ssortear*.", c.Prefix)
	}

	if r != nil {
		ps, err := f.participants(ctx, r.id)
		if err != nil {
			return err
		}
		if len(ps) == 0 {
			return bot.Userf("⚠️ Não há nenhum participante no sorteio %q. Ninguém pôde ser sorteado.", r.title)
		}
		w := ps[f.rand(len(ps))]
		if _, err := f.db.ExecContext(ctx, `UPDATE sorteios SET status = 'finished', winner_id = ?, winner_name = ? WHERE id = ?`,
			w.jid.String(), w.name, r.id); err != nil {
			return err
		}
		if _, err := f.db.ExecContext(ctx, `DELETE FROM sorteio_participants WHERE sorteio_id = ?`, r.id); err != nil {
			return err
		}
		return c.Reply(ctx, bot.Outgoing{Kind: bot.OutText, Mentions: []types.JID{w.jid}, Text: fmt.Sprintf(
			"🎉 *SORTEIO REALIZADO!* 🎉\n\nSorteio: *%s*\nGanhador(a): %s! 🎁\n\nParabéns! 📦", r.title, w.label())})
	}

	title := strings.TrimSpace(c.ArgText())
	if title == "" {
		title = "Sorteio Rápido"
	}
	var cands []types.JID
	if info != nil {
		me := c.Client.Me()
		for _, p := range info.Participants {
			if !p.Is(me) {
				cands = append(cands, gamekit.Canonical(info, p.JID))
			}
		}
	}
	if len(cands) == 0 {
		return bot.Userf("Nenhum membro elegível encontrado no grupo para o sorteio.")
	}
	w := cands[f.rand(len(cands))]
	if _, err := f.db.ExecContext(ctx, `INSERT INTO sorteios(group_id, title, status, created_at, winner_id, winner_name, creator_id) VALUES(?, ?, 'finished', ?, ?, '', ?)`,
		chat.String(), title, f.now().UnixMilli(), w.String(), author.String()); err != nil {
		return err
	}
	return c.Reply(ctx, bot.Outgoing{Kind: bot.OutText, Mentions: []types.JID{w}, Text: fmt.Sprintf(
		"⚡ *SORTEIO INSTANTÂNEO REALIZADO!* ⚡\n\nSorteio: *%s*\nGanhador(a): %s! 🎁\n\nParabéns! 📦", title, bot.MentionText(w))})
}

func (f *Feature) historico(ctx context.Context, c *bot.Context) error {
	rows, err := f.db.QueryContext(ctx, `SELECT title, winner_id, COALESCE(winner_name, ''), created_at FROM sorteios
		WHERE group_id = ? AND status = 'finished' ORDER BY created_at DESC, id DESC LIMIT 15`, c.Message.Chat.String())
	if err != nil {
		return err
	}
	defer rows.Close()
	var (
		b        strings.Builder
		mentions []types.JID
		n        int
	)
	b.WriteString("📋 *Histórico de Sorteios* 📋\n\n")
	for rows.Next() {
		var (
			title, wid string
			m          member
			at         int64
		)
		if err := rows.Scan(&title, &wid, &m.name, &at); err != nil {
			return err
		}
		m.jid, _ = types.ParseJID(wid)
		n++
		fmt.Fprintf(&b, "%d. *%s* - Vencedor(a): %s em %s\n", n, title, m.label(), time.UnixMilli(at).Format("02/01/2006"))
		mentions = append(mentions, m.jid)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if n == 0 {
		return bot.Userf("Nenhum sorteio anterior registrado neste grupo.")
	}
	return c.Reply(ctx, bot.Outgoing{Kind: bot.OutText, Text: strings.TrimRight(b.String(), "\n"), Mentions: mentions})
}

// Module adapta a funcionalidade ao contrato app.Feature.
type Module struct{}

// Name implementa app.Feature.
func (Module) Name() string { return "raffle" }

// Register implementa app.Feature.
func (Module) Register(ctx context.Context, d *app.Deps) error {
	if err := d.Store.Migrate(ctx, "raffle", migrations); err != nil {
		return err
	}
	return d.Router.Register(New(d.Store.DB(), nil, nil).Commands()...)
}
