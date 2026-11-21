// Package ranking conta as mensagens de cada membro por grupo e mostra quem
// mais fala (!faladores).
//
// A contagem é passiva e barata: o Observer só incrementa um mapa limitado
// em memória, que é gravado no SQLite em lote (a cada 30s, quando enche, ao
// consultar o ranking e ao desligar).
package ranking

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/joaodobread/ravena-go/internal/app"
	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/store"
)

// Mensagens mostradas ao usuário.
const (
	MsgEmpty = "Ainda não há estatísticas de mensagens para este grupo."
)

const (
	flushEvery = 30 * time.Second
	// softMax é quando o lote pede para ser gravado; hardMax é o teto
	// absoluto (novas pessoas são descartadas até a gravação esvaziar o mapa).
	softMax = 2000
	hardMax = 4 * softMax
	topN    = 10
)

// Module adapta a funcionalidade ao contrato app.Feature.
type Module struct{}

// Name implementa app.Feature.
func (Module) Name() string { return "ranking" }

// Register implementa app.Feature.
func (Module) Register(ctx context.Context, d *app.Deps) error {
	f, err := New(ctx, d.Store, d.Log)
	if err != nil {
		return err
	}
	if err := d.Router.Register(f.Commands()...); err != nil {
		return err
	}
	d.Router.Observe(f.Observe)
	go f.Run(ctx)
	return nil
}

type key struct{ chat, user string }

type entry struct {
	jid, name string
	n         int
}

// Feature guarda o lote pendente e o acesso ao banco.
type Feature struct {
	db  *sql.DB
	log *slog.Logger
	now func() time.Time

	mu      sync.Mutex
	pending map[key]*entry
	soft    int
	hard    int
	full    chan struct{} // sinaliza que o lote encheu (capacidade 1)
	flushMu sync.Mutex    // serializa gravações
}

var migrations = []string{
	`CREATE TABLE ranking_msgs (
		chat  TEXT NOT NULL,
		user  TEXT NOT NULL,
		jid   TEXT NOT NULL,
		name  TEXT NOT NULL,
		count INTEGER NOT NULL,
		PRIMARY KEY (chat, user)
	)`,
}

// New aplica as migrações e cria a funcionalidade.
func New(ctx context.Context, st *store.Store, log *slog.Logger) (*Feature, error) {
	if err := st.Migrate(ctx, "ranking", migrations); err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	return &Feature{
		db: st.DB(), log: log, now: time.Now,
		pending: map[key]*entry{}, soft: softMax, hard: hardMax,
		full: make(chan struct{}, 1),
	}, nil
}

// Observe é o bot.Observer: conta a mensagem no lote em memória.
func (f *Feature) Observe(_ context.Context, m *bot.Message) {
	if !m.IsGroup || m.Sender.User == "" {
		return
	}
	k := key{m.Chat.String(), m.Sender.User}
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.pending[k]
	if !ok {
		if len(f.pending) >= f.hard {
			return // lote lotado: descarta até a próxima gravação
		}
		e = &entry{jid: m.Sender.String()}
		f.pending[k] = e
		if len(f.pending) >= f.soft {
			select {
			case f.full <- struct{}{}:
			default:
			}
		}
	}
	e.n++
	if m.PushName != "" {
		e.name = m.PushName
	}
}

// Run grava o lote periodicamente até ctx acabar (e grava uma última vez).
func (f *Feature) Run(ctx context.Context) {
	t := time.NewTicker(flushEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := f.Flush(fctx); err != nil {
				f.log.Error("ranking: gravação final falhou", "err", err)
			}
			cancel()
			return
		case <-t.C:
		case <-f.full:
		}
		if err := f.Flush(ctx); err != nil {
			f.log.Error("ranking: gravação falhou", "err", err)
		}
	}
}

// Flush grava o lote pendente numa transação. Se falhar, as contagens voltam
// para o lote (até o teto) para a próxima tentativa.
func (f *Feature) Flush(ctx context.Context) error {
	f.flushMu.Lock()
	defer f.flushMu.Unlock()
	f.mu.Lock()
	batch := f.pending
	f.pending = make(map[key]*entry, min(len(batch), f.soft))
	f.mu.Unlock()
	if len(batch) == 0 {
		return nil
	}
	err := f.write(ctx, batch)
	if err != nil {
		f.mu.Lock()
		for k, e := range batch {
			if cur, ok := f.pending[k]; ok {
				cur.n += e.n
				if cur.name == "" {
					cur.name = e.name
				}
			} else if len(f.pending) < f.hard {
				f.pending[k] = e
			}
		}
		f.mu.Unlock()
	}
	return err
}

func (f *Feature) write(ctx context.Context, batch map[key]*entry) error {
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO ranking_msgs(chat, user, jid, name, count) VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(chat, user) DO UPDATE SET
			count = count + excluded.count,
			jid = excluded.jid,
			name = CASE WHEN excluded.name <> '' THEN excluded.name ELSE name END`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for k, e := range batch {
		if _, err := stmt.ExecContext(ctx, k.chat, k.user, e.jid, e.name, e.n); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Commands devolve os comandos para registrar no Router.
func (f *Feature) Commands() []*bot.Command {
	return []*bot.Command{
		{
			Name:        "faladores",
			Description: "Mostra o ranking de quem mais fala no grupo",
			Usage:       "!faladores [completo]",
			Category:    "grupo",
			GroupOnly:   true,
			Reactions:   bot.Reactions{After: "🗣", Error: "❌"},
			Run: func(ctx context.Context, c *bot.Context) error {
				full := len(c.Args) > 0 && strings.EqualFold(c.Args[0], "completo")
				out, err := f.report(ctx, c, full)
				if err != nil {
					return err
				}
				return c.Reply(ctx, out)
			},
		},
		{
			Name:        "faladores-limpeza",
			Description: "Remove do ranking membros que saíram do grupo",
			Category:    "grupo",
			GroupOnly:   true,
			AdminOnly:   true,
			Reactions:   bot.Reactions{After: "🧹", Error: "❌"},
			Run:         f.cleanup,
		},
		{
			Name:        "faladores-reset",
			Description: "Mostra o ranking final e reinicia a contagem",
			Category:    "grupo",
			GroupOnly:   true,
			AdminOnly:   true,
			Reactions:   bot.Reactions{After: "♻️", Error: "❌"},
			Run:         f.reset,
		},
	}
}

type row struct {
	user, jid, name string
	count           int
}

func (f *Feature) rows(ctx context.Context, chat types.JID) ([]row, error) {
	if err := f.Flush(ctx); err != nil {
		return nil, err
	}
	rs, err := f.db.QueryContext(ctx, `SELECT user, jid, name, count FROM ranking_msgs WHERE chat = ?`, chat.String())
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []row
	for rs.Next() {
		var r row
		if err := rs.Scan(&r.user, &r.jid, &r.name, &r.count); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rs.Err()
}

// ranked é uma pessoa no ranking, já juntando número e LID do mesmo membro.
type ranked struct {
	name   string
	member bool
	jid    types.JID
	count  int
}

var medals = []string{"🥇", "🥈", "🥉"}

func (f *Feature) report(ctx context.Context, c *bot.Context, full bool) (bot.Outgoing, error) {
	chat := c.Message.Chat
	rows, err := f.rows(ctx, chat)
	if err != nil {
		return bot.Outgoing{}, err
	}
	info, err := c.Client.GroupInfo(ctx, chat)
	if err != nil {
		return bot.Outgoing{}, fmt.Errorf("consultando grupo: %w", err)
	}
	me := c.Client.Me()

	byPerson := map[string]*ranked{}
	var list []*ranked
	seen := make([]bool, len(info.Participants))
	total := 0
	for _, r := range rows {
		total += r.count
		jid, _ := types.ParseJID(r.jid)
		id, member := "u:"+r.user, false
		for i, p := range info.Participants {
			if p.Is(jid) {
				id, member, jid, seen[i] = fmt.Sprintf("m:%d", i), true, p.JID, true
				break
			}
		}
		if p := byPerson[id]; p != nil {
			p.count += r.count
			continue
		}
		p := &ranked{name: r.name, member: member, jid: jid, count: r.count}
		byPerson[id] = p
		list = append(list, p)
	}

	var never []types.JID
	for i, p := range info.Participants {
		if !seen[i] && !p.Is(me) {
			never = append(never, p.JID)
		}
	}
	if len(list) == 0 && len(never) == 0 {
		return bot.Outgoing{}, bot.Userf(MsgEmpty)
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].count != list[j].count {
			return list[i].count > list[j].count
		}
		return list[i].name < list[j].name
	})

	var b strings.Builder
	var mentions []types.JID
	b.WriteString("*🏆 Ranking de faladores do grupo 🗣*\n\n")
	shown := list
	if !full && len(shown) > topN {
		shown = shown[:topN]
	}
	for i, p := range shown {
		pos := fmt.Sprintf("%dº", i+1)
		if i < len(medals) {
			pos = medals[i]
		}
		who := "*" + p.name + "*"
		if p.member {
			who = bot.MentionText(p.jid)
			mentions = append(mentions, p.jid)
		} else if p.name == "" {
			who = "*Alguém*"
		}
		fmt.Fprintf(&b, "%s %s: %d mensagens\n", pos, who, p.count)
	}
	if !full && len(list) > topN {
		fmt.Fprintf(&b, "... e mais %d membros (use '%sfaladores completo' para ver todos)\n", len(list)-topN, c.Prefix)
	}

	if len(never) > 0 {
		b.WriteString("\n🙊 *Nunca vi*:\n> Nenhuma mensagem registrada destes membros\n")
		// Sem notificar: o @ aparece no texto, mas não entra em Mentions.
		listed := never
		if !full && len(listed) > topN {
			listed = listed[:topN]
		}
		for _, j := range listed {
			fmt.Fprintf(&b, "- %s\n", bot.MentionText(j))
		}
		if len(listed) < len(never) {
			fmt.Fprintf(&b, "... e mais %d membros\n", len(never)-len(listed))
		}
	}
	fmt.Fprintf(&b, "\n📊 *Estatísticas:*\nTotal de %d mensagens enviadas por %d participantes", total, len(list))

	out := bot.Text(b.String())
	out.Mentions = mentions
	return out, nil
}

func (f *Feature) cleanup(ctx context.Context, c *bot.Context) error {
	chat := c.Message.Chat
	rows, err := f.rows(ctx, chat)
	if err != nil {
		return err
	}
	info, err := c.Client.GroupInfo(ctx, chat)
	if err != nil {
		return fmt.Errorf("consultando grupo: %w", err)
	}
	if len(info.Participants) == 0 {
		return bot.Userf("Não foi possível obter a lista de participantes do grupo para verificar a limpeza.")
	}
	removed := 0
	for _, r := range rows {
		jid, _ := types.ParseJID(r.jid)
		if _, ok := info.Member(jid); ok {
			continue
		}
		if _, err := f.db.ExecContext(ctx, `DELETE FROM ranking_msgs WHERE chat = ? AND user = ?`, chat.String(), r.user); err != nil {
			return err
		}
		removed++
	}
	return c.ReplyText(ctx, "Limpeza concluída! 🧹\n%d membros antigos foram removidos do ranking.", removed)
}

var brt = time.FixedZone("BRT", -3*3600)

func (f *Feature) reset(ctx context.Context, c *bot.Context) error {
	out, err := f.report(ctx, c, true)
	if err != nil {
		return err
	}
	// O lote acabou de ser gravado por report; o que chegar depois é da nova contagem.
	if _, err := f.db.ExecContext(ctx, `DELETE FROM ranking_msgs WHERE chat = ?`, c.Message.Chat.String()); err != nil {
		return err
	}
	out.Text += "\n\n📅 Fechamento: " + f.now().In(brt).Format("02/01/2006 15:04") + "\n\n♻️ *Ranking reiniciado com sucesso!*"
	return c.Reply(ctx, out)
}
