package groupcmds

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"

	"go.mau.fi/whatsmeow/types"

	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/bot/bottest"
	"github.com/joaodobread/ravena-go/internal/store"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type testPolicy struct{ c bot.Client }

func (testPolicy) Prefix(context.Context, *bot.Message) string { return "" }
func (testPolicy) Admit(context.Context, *bot.Message) bool    { return true }
func (testPolicy) Allow(context.Context, *bot.Context) error   { return nil }
func (testPolicy) Done(context.Context, *bot.Context, error)   {}
func (p testPolicy) Roles(ctx context.Context, m *bot.Message) (bool, bool) {
	if !m.IsGroup {
		return false, false
	}
	g, _ := p.c.GroupInfo(ctx, m.Chat)
	return g.IsAdmin(m.Sender), false
}

// failDelete simula o bot sem permissão para apagar.
type failDelete struct{ *bottest.FakeClient }

func (failDelete) Delete(context.Context, *bot.Message) error { return errors.New("forbidden") }

var (
	other   = types.NewJID("5511777777777", types.DefaultUserServer)
	userLID = types.NewJID("111222333", types.HiddenUserServer)
	botLID  = types.NewJID("999888777", types.HiddenUserServer)
)

type env struct {
	r  *bot.Router
	fc *bottest.FakeClient
	f  *Feature
}

func setup(t *testing.T, wrap func(*bottest.FakeClient) bot.Client) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	fc := &bottest.FakeClient{Groups: map[types.JID]*bot.GroupInfo{bottest.Group: {
		JID: bottest.Group,
		Participants: []bot.Participant{
			{JID: bottest.User, LID: userLID, Phone: bottest.User},
			{JID: bottest.Admin, IsAdmin: true},
			{JID: other},
			{JID: botLID, LID: botLID, Phone: bottest.Bot},
		},
	}}}
	var client bot.Client = fc
	if wrap != nil {
		client = wrap(fc)
	}
	f, err := New(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	r := bot.NewRouter("!", client, quiet)
	r.SetPolicy(testPolicy{client})
	if err := r.Register(f.Commands()...); err != nil {
		t.Fatal(err)
	}
	return &env{r: r, fc: fc, f: f}
}

func from(sender types.JID, text string) *bot.Message {
	m := bottest.GroupMsg(text)
	m.Sender = sender
	return m
}

func (e *env) send(m *bot.Message) bottest.Reply {
	n := len(e.fc.Replies)
	e.r.Handle(context.Background(), m)
	if len(e.fc.Replies) == n {
		return bottest.Reply{}
	}
	return e.fc.Last()
}

func users(js []types.JID) []string {
	var out []string
	for _, j := range js {
		out = append(out, j.User)
	}
	return out
}

func TestAttention(t *testing.T) {
	e := setup(t, nil)
	if got := e.send(from(bottest.User, "!atencao")).Out.Text; got != MsgNotAdmin {
		t.Fatalf("não admin: %q", got)
	}
	r := e.send(from(bottest.Admin, "!galera bora\njogar"))
	if r.To != nil || r.Out.Text != MsgHeader+"\n\nbora\njogar" {
		t.Fatalf("galera: %+v", r)
	}
	want := []string{bottest.User.User, bottest.Admin.User, other.User}
	if got := users(r.Out.Mentions); !slices.Equal(got, want) {
		t.Fatalf("menções %v, quer %v (sem o bot)", got, want)
	}
	for _, alias := range []string{"!atenção", "!atençao", "!atencao"} {
		if got := e.send(from(bottest.Admin, alias)).Out.Text; got != MsgHeader {
			t.Fatalf("%s: %q", alias, got)
		}
	}
	if got := e.send(bottest.PrivateMsg("!atencao")).Out.Text; got != "Esse comando só funciona em grupos." {
		t.Fatalf("privado: %q", got)
	}
}

func TestAttentionQuotedMedia(t *testing.T) {
	e := setup(t, nil)
	m := from(bottest.Admin, "!atencao")
	m.Quoted = &bot.Message{ID: "q", Sender: other, Text: "olha isso",
		Media: bottest.StaticMedia(bot.MediaImage, "image/jpeg", []byte("jpg"))}
	r := e.send(m)
	if r.Out.Kind != bot.OutImage || string(r.Out.Data) != "jpg" || r.Out.Caption != MsgHeader+"\n\nolha isso" || len(r.Out.Mentions) != 3 {
		t.Fatalf("mídia: %+v", r.Out)
	}
	m.Quoted = &bot.Message{ID: "q", Sender: other, Text: "só texto"}
	if r := e.send(m); r.Out.Kind != bot.OutText || r.Out.Text != MsgHeader+"\n\nsó texto" {
		t.Fatalf("texto citado: %+v", r.Out)
	}
}

func TestAttentionReaction(t *testing.T) {
	e := setup(t, nil)
	target := from(other, "reunião às 20h")
	ctx := context.Background()

	e.r.HandleReaction(ctx, target, bot.Reaction{Emoji: "📢", By: bottest.User})
	if len(e.fc.Replies) != 0 {
		t.Fatalf("não admin reagindo não deveria disparar: %+v", e.fc.Replies)
	}
	e.r.HandleReaction(ctx, target, bot.Reaction{Emoji: "📢", By: bottest.Admin})
	r := e.fc.Last()
	if r.Out.Text != MsgHeader+"\n\nreunião às 20h" || len(r.Out.Mentions) != 3 {
		t.Fatalf("reação: %+v", r)
	}
	// Por texto, o comando oculto exige admin como o normal.
	if got := e.send(from(bottest.User, "!atencao-reacao")).Out.Text; got != MsgNotAdmin {
		t.Fatalf("oculto por texto: %q", got)
	}
}

func TestIgnore(t *testing.T) {
	e := setup(t, nil)
	m := from(userLID, "!ignorar")
	m.SenderAlt = bottest.User
	if got := e.send(m).Out.Text; got != MsgIgnored {
		t.Fatalf("ignorar: %q", got)
	}
	r := e.send(from(bottest.Admin, "!atencao"))
	if got := users(r.Out.Mentions); !slices.Equal(got, []string{bottest.Admin.User, other.User}) {
		t.Fatalf("ignorado ainda mencionado: %v", got)
	}

	// Desfaz vindo só pelo número.
	if got := e.send(from(bottest.User, "!ignorar")).Out.Text; got != MsgIncluded {
		t.Fatalf("incluir: %q", got)
	}
	r = e.send(from(bottest.Admin, "!atencao"))
	if len(r.Out.Mentions) != 3 {
		t.Fatalf("deveria voltar: %v", users(r.Out.Mentions))
	}

	// Todos ignorados.
	for _, j := range []types.JID{bottest.User, bottest.Admin, other} {
		e.send(from(j, "!ignorar"))
	}
	if got := e.send(from(bottest.Admin, "!atencao")).Out.Text; got != MsgNoMembers {
		t.Fatalf("ninguém: %q", got)
	}
	if got := e.send(bottest.PrivateMsg("!ignorar")).Out.Text; got != "Esse comando só funciona em grupos." {
		t.Fatalf("privado: %q", got)
	}
}

func TestApagarReply(t *testing.T) {
	e := setup(t, nil)
	botMsg := &bot.Message{ID: "b1", Sender: botLID, Text: "resposta do bot"}

	// Sem citar nada.
	if got := e.send(from(bottest.User, "!apagar")).Out.Text; got != MsgNoTarget {
		t.Fatalf("sem alvo: %q", got)
	}
	// Membro comum não apaga msg do bot que não o menciona.
	m := from(other, "!apagar")
	m.Quoted = botMsg
	if got := e.send(m).Out.Text; got != MsgNoPerm {
		t.Fatalf("sem permissão: %q", got)
	}
	// Admin apaga.
	m = from(bottest.Admin, "!apagar")
	m.Quoted = botMsg
	e.send(m)
	if !slices.Equal(e.fc.Deleted, []string{"b1"}) || e.fc.Reactions[len(e.fc.Reactions)-1] != "✅" {
		t.Fatalf("admin: %v %v", e.fc.Deleted, e.fc.Reactions)
	}
	// Dono da mensagem apaga a própria (número x LID).
	m = from(bottest.User, "!apagar")
	m.Quoted = &bot.Message{ID: "u1", Sender: userLID, Text: "oops"}
	e.send(m)
	if e.fc.Deleted[len(e.fc.Deleted)-1] != "u1" {
		t.Fatalf("dono: %v", e.fc.Deleted)
	}
	// No privado, qualquer um apaga a mensagem do bot.
	p := bottest.PrivateMsg("!apagar")
	p.Quoted = &bot.Message{ID: "b2", Sender: bottest.Bot}
	e.send(p)
	if e.fc.Deleted[len(e.fc.Deleted)-1] != "b2" {
		t.Fatalf("privado: %v", e.fc.Deleted)
	}
	// No privado, msg que não é do bot nem da pessoa: recusado.
	p.Quoted = &bot.Message{ID: "x", Sender: other}
	if got := e.send(p).Out.Text; got != MsgNoPerm {
		t.Fatalf("privado alheio: %q", got)
	}
}

func TestApagarReaction(t *testing.T) {
	e := setup(t, nil)
	ctx := context.Background()
	target := &bot.Message{ID: "b1", Chat: bottest.Group, Sender: bottest.Bot, FromMe: true, IsGroup: true,
		Text: "oi @5511777777777", Mentions: []types.JID{other}}

	// Quem reagiu não é admin nem foi mencionado.
	e.r.HandleReaction(ctx, target, bot.Reaction{Emoji: "🗑️", By: bottest.User})
	if len(e.fc.Deleted) != 0 || e.fc.Last().Out.Text != MsgNoPerm {
		t.Fatalf("sem permissão: %v %+v", e.fc.Deleted, e.fc.Last())
	}
	// Mencionado pelo bot pode apagar.
	e.r.HandleReaction(ctx, target, bot.Reaction{Emoji: "🗑", By: other})
	if !slices.Equal(e.fc.Deleted, []string{"b1"}) {
		t.Fatalf("mencionado: %v", e.fc.Deleted)
	}
	// Admin reagindo (pela identidade alternativa) apaga mensagem alheia.
	alien := from(other, "spam")
	alien.ID = "s1"
	e.r.HandleReaction(ctx, alien, bot.Reaction{Emoji: "🗑️", By: types.NewJID("424242", types.HiddenUserServer), ByAlt: bottest.Admin})
	if e.fc.Deleted[len(e.fc.Deleted)-1] != "s1" {
		t.Fatalf("admin: %v", e.fc.Deleted)
	}
}

func TestApagarDeleteFails(t *testing.T) {
	e := setup(t, func(fc *bottest.FakeClient) bot.Client { return failDelete{fc} })
	m := from(bottest.Admin, "!apagar")
	m.Quoted = &bot.Message{ID: "s1", Sender: other}
	if got := e.send(m).Out.Text; got != MsgDeleteAdmin {
		t.Fatalf("sem admin: %q", got)
	}
	m.Quoted = &bot.Message{ID: "b1", Sender: bottest.Bot}
	if got := e.send(m).Out.Text; got != "Ops, algo deu errado. Tente de novo em instantes." {
		t.Fatalf("erro genérico: %q", got)
	}
}
