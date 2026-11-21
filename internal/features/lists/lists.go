// Package lists implementa as listas de presença do grupo: !listas/!ll
// (mostrar), !lc (criar), !lct (criar com título), !ld (apagar), !le (entrar),
// !ls (sair), !lt (título) e !lr (admin remove alguém). Reagir com 1️⃣…🔟 à
// mensagem "Listas disponíveis" entra/sai da lista daquele número.
package lists

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/joaodobread/ravena-go/internal/app"
	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/features/gamekit"
)

// Limites.
const (
	MaxLists   = 20
	MaxMembers = 300
	maxName    = 40
	maxTitle   = 100
	header     = "*Listas disponíveis*"
)

var migrations = []string{
	`CREATE TABLE lists (group_id TEXT PRIMARY KEY, json_data TEXT NOT NULL)`,
}

// Migrations são as migrações do escopo "lists".
func Migrations() []string { return migrations }

// Member é alguém numa lista.
type Member struct {
	ID       string `json:"id"` // JID canônico
	Name     string `json:"name"`
	JoinedAt int64  `json:"joinedAt"`
}

// List é uma lista do grupo.
type List struct {
	Name      string   `json:"name"`
	Title     string   `json:"title,omitempty"`
	CreatedAt int64    `json:"createdAt"`
	CreatedBy string   `json:"createdBy"`
	Members   []Member `json:"members"`
}

func (l *List) label() string {
	if l.Title != "" {
		return l.Title
	}
	return l.Name
}

func (l *List) index(user types.JID) int {
	return slices.IndexFunc(l.Members, func(m Member) bool {
		j, err := types.ParseJID(m.ID)
		return err == nil && j.User == user.User
	})
}

// Feature é a funcionalidade.
type Feature struct {
	db  *sql.DB
	now func() time.Time
	mu  sync.Mutex // serializa ler-alterar-gravar das listas
}

// New cria a funcionalidade; now pode ser nil.
func New(db *sql.DB, now func() time.Time) *Feature {
	if now == nil {
		now = time.Now
	}
	return &Feature{db: db, now: now}
}

// NumberEmojis são os emojis 1️⃣…🔟 em todas as grafias que o WhatsApp manda
// (com e sem o seletor de variação / o keycap).
var NumberEmojis = func() []string {
	var out []string
	for d := '1'; d <= '9'; d++ {
		s := string(d)
		out = append(out, s+"️⃣", s+"⃣", s+"️")
	}
	return append(out, "🔟")
}()

// EmojiIndex converte 1️⃣…🔟 em 0…9 (-1 se não for número).
func EmojiIndex(e string) int {
	if e == "🔟" {
		return 9
	}
	e = strings.NewReplacer("️", "", "⃣", "").Replace(e)
	if len(e) == 1 && e[0] >= '1' && e[0] <= '9' {
		return int(e[0] - '1')
	}
	return -1
}

func numberEmoji(i int) string {
	switch {
	case i < 9:
		return string(rune('1'+i)) + "️⃣"
	case i == 9:
		return "🔟"
	}
	return fmt.Sprintf("%d.", i+1)
}

// Commands devolve os comandos.
func (f *Feature) Commands() []*bot.Command {
	cmd := func(name string, aliases []string, desc, usage, after string, run bot.Handler) *bot.Command {
		return &bot.Command{Name: name, Aliases: aliases, Description: desc, Usage: usage, Category: "grupo",
			GroupOnly: true, Reactions: bot.Reactions{After: after}, Run: run}
	}
	lr := cmd("lr", nil, "Remove alguém de uma lista (admins)", "!lr <lista> <número>", "❌", f.remove)
	lr.AdminOnly = true
	return []*bot.Command{
		cmd("listas", []string{"ll"}, "Mostra as listas do grupo", "", "📋", f.show),
		cmd("lc", nil, "Cria uma ou mais listas", "!lc <lista> [lista2...]", "➕", f.create),
		cmd("lct", nil, "Cria uma lista com título", "!lct <lista> <título>", "➕", f.createTitled),
		cmd("ld", nil, "Apaga uma ou mais listas", "!ld <lista> [lista2...]", "🧹", f.delete),
		cmd("le", nil, "Entra em uma ou mais listas", "!le <lista> [lista2...]", "👉", f.join),
		cmd("ls", nil, "Sai de uma ou mais listas", "!ls <lista> [lista2...]", "👈", f.leave),
		cmd("lt", nil, "Define o título de uma lista", "!lt <lista> <título>", "✏️", f.title),
		lr,
		{Name: "listas-reacao", Category: "grupo", GroupOnly: true, Hidden: true,
			Description: "Entra/sai de uma lista reagindo com o número dela",
			Triggers:    NumberEmojis, Run: f.reaction},
	}
}

func (f *Feature) load(ctx context.Context, chat types.JID) ([]*List, error) {
	var raw string
	err := f.db.QueryRowContext(ctx, `SELECT json_data FROM lists WHERE group_id = ?`, chat.String()).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*List
	return out, json.Unmarshal([]byte(raw), &out)
}

func (f *Feature) save(ctx context.Context, chat types.JID, lists []*List) error {
	if len(lists) == 0 {
		_, err := f.db.ExecContext(ctx, `DELETE FROM lists WHERE group_id = ?`, chat.String())
		return err
	}
	raw, err := json.Marshal(lists)
	if err != nil {
		return err
	}
	_, err = f.db.ExecContext(ctx, `INSERT INTO lists(group_id, json_data) VALUES(?, ?)
		ON CONFLICT(group_id) DO UPDATE SET json_data = excluded.json_data`, chat.String(), string(raw))
	return err
}

func find(lists []*List, name string) int {
	return slices.IndexFunc(lists, func(l *List) bool { return strings.EqualFold(l.Name, name) })
}

// update roda fn sobre as listas do grupo e grava se fn devolver changed.
func (f *Feature) update(ctx context.Context, chat types.JID, fn func(lists []*List) ([]*List, bool, error)) ([]*List, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	lists, err := f.load(ctx, chat)
	if err != nil {
		return nil, err
	}
	lists, changed, err := fn(lists)
	if err != nil || !changed {
		return lists, err
	}
	return lists, f.save(ctx, chat, lists)
}

func memberName(m Member) string {
	if m.Name != "" {
		return m.Name
	}
	if j, err := types.ParseJID(m.ID); err == nil {
		return j.User
	}
	return "Membro"
}

// Overview é a mensagem "Listas disponíveis" (a que aceita reações).
func Overview(lists []*List, prefix string) string {
	var b strings.Builder
	b.WriteString(header + "\n\n")
	for i, l := range lists {
		n := len(l.Members)
		plural := "s"
		if n == 1 {
			plural = ""
		}
		fmt.Fprintf(&b, "%s *%s* (%d membro%s)\n", numberEmoji(i), l.label(), n, plural)
		if n > 0 {
			names := make([]string, n)
			for j, m := range l.Members {
				names[j] = memberName(m)
			}
			b.WriteString("Membros: " + strings.Join(names, ", ") + "\n")
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "Reaja com o emoji do número para entrar/sair de uma lista.\nComandos: %sle <lista> (entrar), %sls <lista> (sair)", prefix, prefix)
	return b.String()
}

func (f *Feature) sendOverview(ctx context.Context, c *bot.Context, lists []*List) error {
	if len(lists) == 0 {
		return nil
	}
	return c.Client.Send(ctx, c.Message.Chat, bot.Text(Overview(lists, c.Prefix)))
}

func (f *Feature) show(ctx context.Context, c *bot.Context) error {
	lists, err := f.load(ctx, c.Message.Chat)
	if err != nil {
		return err
	}
	if len(lists) == 0 {
		return bot.Userf("Não há listas criadas neste grupo. Use *%slc <nome>* para criar uma.", c.Prefix)
	}
	return c.ReplyText(ctx, "%s", Overview(lists, c.Prefix))
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// actOn responde o resumo e manda o quadro atualizado.
func (f *Feature) actOn(ctx context.Context, c *bot.Context, lists []*List, lines []string) error {
	if err := c.ReplyText(ctx, "%s", strings.Join(lines, "\n")); err != nil {
		return err
	}
	return f.sendOverview(ctx, c, lists)
}

func (f *Feature) author(ctx context.Context, c *bot.Context) types.JID {
	return gamekit.Author(gamekit.Group(ctx, c.Client, c.Message.Chat), c.Message)
}

func (f *Feature) create(ctx context.Context, c *bot.Context) error {
	if len(c.Args) == 0 {
		return bot.Userf("Informe pelo menos um nome de lista. Ex: *%slc lista1 lista2*", c.Prefix)
	}
	by := f.author(ctx, c).String()
	var created, lines []string
	lists, err := f.update(ctx, c.Message.Chat, func(lists []*List) ([]*List, bool, error) {
		for _, name := range c.Args {
			name = clip(name, maxName)
			switch {
			case find(lists, name) >= 0:
				lines = append(lines, fmt.Sprintf("Lista %q já existe.", name))
			case len(lists) >= MaxLists:
				lines = append(lines, fmt.Sprintf("Limite de %d listas atingido; %q não foi criada.", MaxLists, name))
			default:
				lists = append(lists, &List{Name: name, CreatedAt: f.now().UnixMilli(), CreatedBy: by})
				created = append(created, name)
			}
		}
		return lists, len(created) > 0, nil
	})
	if err != nil {
		return err
	}
	if len(created) == 0 {
		return bot.Userf("%s", strings.Join(append(lines, "Nenhuma lista foi criada."), "\n"))
	}
	s := ""
	if len(created) > 1 {
		s = "s"
	}
	lines = append([]string{fmt.Sprintf("Lista%s criada%s: %s", s, s, strings.Join(created, ", "))}, lines...)
	return f.actOn(ctx, c, lists, lines)
}

func (f *Feature) createTitled(ctx context.Context, c *bot.Context) error {
	if len(c.Args) < 2 {
		return bot.Userf("Informe o nome da lista e o título. Ex: *%slct lista1 Título da Lista*", c.Prefix)
	}
	name, title := clip(c.Args[0], maxName), clip(strings.Join(c.Args[1:], " "), maxTitle)
	by := f.author(ctx, c).String()
	lists, err := f.update(ctx, c.Message.Chat, func(lists []*List) ([]*List, bool, error) {
		if find(lists, name) >= 0 {
			return nil, false, bot.Userf("Lista %q já existe. Use *%slt %s %s* para mudar o título.", name, c.Prefix, name, title)
		}
		if len(lists) >= MaxLists {
			return nil, false, bot.Userf("Limite de %d listas atingido.", MaxLists)
		}
		return append(lists, &List{Name: name, Title: title, CreatedAt: f.now().UnixMilli(), CreatedBy: by}), true, nil
	})
	if err != nil {
		return err
	}
	return f.actOn(ctx, c, lists, []string{fmt.Sprintf("Lista criada: %s (%s)", name, title)})
}

func (f *Feature) delete(ctx context.Context, c *bot.Context) error {
	if len(c.Args) == 0 {
		return bot.Userf("Informe pelo menos uma lista para apagar. Ex: *%sld lista1 lista2*", c.Prefix)
	}
	var deleted, lines []string
	lists, err := f.update(ctx, c.Message.Chat, func(lists []*List) ([]*List, bool, error) {
		for _, name := range c.Args {
			i := find(lists, name)
			if i < 0 {
				lines = append(lines, fmt.Sprintf("Lista %q não encontrada.", name))
				continue
			}
			deleted = append(deleted, lists[i].Name)
			lists = slices.Delete(lists, i, i+1)
		}
		return lists, len(deleted) > 0, nil
	})
	if err != nil {
		return err
	}
	if len(deleted) == 0 {
		return bot.Userf("%s", strings.Join(append(lines, "Nenhuma lista foi apagada."), "\n"))
	}
	s := ""
	if len(deleted) > 1 {
		s = "s"
	}
	lines = append([]string{fmt.Sprintf("Lista%s excluída%s: %s", s, s, strings.Join(deleted, ", "))}, lines...)
	return f.actOn(ctx, c, lists, lines)
}

func (f *Feature) join(ctx context.Context, c *bot.Context) error {
	if len(c.Args) == 0 {
		return bot.Userf("Informe a lista para entrar. Ex: *%sle lista1*", c.Prefix)
	}
	user := f.author(ctx, c)
	name := gamekit.Name(c.Message, user.User)
	var lines []string
	changed := false
	lists, err := f.update(ctx, c.Message.Chat, func(lists []*List) ([]*List, bool, error) {
		for _, arg := range c.Args {
			i := find(lists, arg)
			if i < 0 {
				lines = append(lines, fmt.Sprintf("Lista %q não encontrada.", arg))
				continue
			}
			l := lists[i]
			switch {
			case l.index(user) >= 0:
				lines = append(lines, fmt.Sprintf("Você já está na lista %q.", l.label()))
			case len(l.Members) >= MaxMembers:
				lines = append(lines, fmt.Sprintf("A lista %q está cheia.", l.label()))
			default:
				l.Members = append(l.Members, Member{ID: user.String(), Name: name, JoinedAt: f.now().UnixMilli()})
				lines = append(lines, fmt.Sprintf("%s entrou na lista %q.", name, l.label()))
				changed = true
			}
		}
		return lists, changed, nil
	})
	if err != nil {
		return err
	}
	if !changed {
		return bot.Userf("%s", strings.Join(lines, "\n"))
	}
	return f.actOn(ctx, c, lists, lines)
}

func (f *Feature) leave(ctx context.Context, c *bot.Context) error {
	if len(c.Args) == 0 {
		return bot.Userf("Informe pelo menos uma lista para sair. Ex: *%sls lista1 lista2*", c.Prefix)
	}
	user := f.author(ctx, c)
	name := gamekit.Name(c.Message, user.User)
	var left, errs []string
	lists, err := f.update(ctx, c.Message.Chat, func(lists []*List) ([]*List, bool, error) {
		for _, arg := range c.Args {
			i := find(lists, arg)
			if i < 0 {
				errs = append(errs, fmt.Sprintf("• Lista %q não encontrada.", arg))
				continue
			}
			l := lists[i]
			j := l.index(user)
			if j < 0 {
				errs = append(errs, fmt.Sprintf("• Você não está na lista %q.", l.label()))
				continue
			}
			l.Members = slices.Delete(l.Members, j, j+1)
			left = append(left, fmt.Sprintf("%q", l.label()))
		}
		return lists, len(left) > 0, nil
	})
	if err != nil {
		return err
	}
	summary := fmt.Sprintf("%s não saiu de nenhuma lista.", name)
	if len(left) > 0 {
		summary = fmt.Sprintf("%s saiu das listas: %s.", name, strings.Join(left, ", "))
	}
	if len(errs) > 0 {
		summary += "\n\nErros:\n" + strings.Join(errs, "\n")
	}
	if len(left) == 0 {
		return bot.Userf("%s", summary)
	}
	return f.actOn(ctx, c, lists, []string{summary})
}

func (f *Feature) title(ctx context.Context, c *bot.Context) error {
	if len(c.Args) < 2 {
		return bot.Userf("Informe o nome da lista e o título. Ex: *%slt lista1 Novo Título*", c.Prefix)
	}
	name, title := c.Args[0], clip(strings.Join(c.Args[1:], " "), maxTitle)
	lists, err := f.update(ctx, c.Message.Chat, func(lists []*List) ([]*List, bool, error) {
		i := find(lists, name)
		if i < 0 {
			return nil, false, bot.Userf("Lista %q não encontrada.", name)
		}
		lists[i].Title = title
		return lists, true, nil
	})
	if err != nil {
		return err
	}
	return f.actOn(ctx, c, lists, []string{fmt.Sprintf("Título da lista %q atualizado para %q.", name, title)})
}

func digits(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}

func (f *Feature) remove(ctx context.Context, c *bot.Context) error {
	if len(c.Args) < 2 && !(len(c.Args) == 1 && len(c.Message.Mentions) > 0) {
		return bot.Userf("Informe a lista e o número (ou marque a pessoa). Ex: *%slr lista1 5521987654321*", c.Prefix)
	}
	name := c.Args[0]
	who := ""
	if len(c.Message.Mentions) > 0 {
		who = gamekit.Canonical(gamekit.Group(ctx, c.Client, c.Message.Chat), c.Message.Mentions[0]).User
	} else {
		who = digits(strings.Join(c.Args[1:], ""))
	}
	if who == "" {
		return bot.Userf("Informe o número de quem deve sair da lista.")
	}
	var removed, label string
	lists, err := f.update(ctx, c.Message.Chat, func(lists []*List) ([]*List, bool, error) {
		i := find(lists, name)
		if i < 0 {
			return nil, false, bot.Userf("Lista %q não encontrada.", name)
		}
		l := lists[i]
		label = l.label()
		if len(l.Members) == 0 {
			return nil, false, bot.Userf("A lista %q não tem membros.", label)
		}
		j := slices.IndexFunc(l.Members, func(m Member) bool { return strings.Contains(m.ID, who) })
		if j < 0 {
			return nil, false, bot.Userf("Número %q não encontrado na lista.", who)
		}
		removed = memberName(l.Members[j])
		l.Members = slices.Delete(l.Members, j, j+1)
		return lists, true, nil
	})
	if err != nil {
		return err
	}
	return f.actOn(ctx, c, lists, []string{fmt.Sprintf("%s foi removido(a) da lista %q por um administrador.", removed, label)})
}

// reaction trata 1️⃣…🔟 na mensagem "Listas disponíveis" do bot.
func (f *Feature) reaction(ctx context.Context, c *bot.Context) error {
	msg := c.Message
	if c.Reaction == nil || !(msg.FromMe || msg.SenderIs(c.Client.Me())) || !strings.Contains(strings.ToLower(msg.Text), "listas disponíveis") {
		return bot.ErrSilent
	}
	idx := EmojiIndex(c.Reaction.Emoji)
	if idx < 0 {
		return bot.ErrSilent
	}
	user := gamekit.Canonical(gamekit.Group(ctx, c.Client, msg.Chat), gamekit.Prefer(c.Reaction.By, c.Reaction.ByAlt))
	var text string
	_, err := f.update(ctx, msg.Chat, func(lists []*List) ([]*List, bool, error) {
		if idx >= len(lists) {
			return nil, false, bot.ErrSilent
		}
		l := lists[idx]
		if j := l.index(user); j >= 0 {
			l.Members = slices.Delete(l.Members, j, j+1)
			text = fmt.Sprintf("%s saiu da lista %q", bot.MentionText(user), l.label())
			return lists, true, nil
		}
		if len(l.Members) >= MaxMembers {
			return nil, false, bot.Userf("A lista %q está cheia.", l.label())
		}
		l.Members = append(l.Members, Member{ID: user.String(), JoinedAt: f.now().UnixMilli()})
		text = fmt.Sprintf("%s entrou na lista %q", bot.MentionText(user), l.label())
		return lists, true, nil
	})
	if err != nil {
		return err
	}
	return c.Client.Send(ctx, msg.Chat, bot.Outgoing{Kind: bot.OutText, Text: text, Mentions: []types.JID{user}})
}

// Module adapta a funcionalidade ao contrato app.Feature.
type Module struct{}

// Name implementa app.Feature.
func (Module) Name() string { return "lists" }

// Register implementa app.Feature.
func (Module) Register(ctx context.Context, d *app.Deps) error {
	if err := d.Store.Migrate(ctx, "lists", migrations); err != nil {
		return err
	}
	return d.Router.Register(New(d.Store.DB(), nil).Commands()...)
}
