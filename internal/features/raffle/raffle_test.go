package raffle_test

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/types"

	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/bot/bottest"
	"github.com/joaodobread/ravena-go/internal/features/raffle"
	"github.com/joaodobread/ravena-go/internal/store"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type adminPolicy struct{}

func (adminPolicy) Prefix(context.Context, *bot.Message) string { return "" }
func (adminPolicy) Admit(context.Context, *bot.Message) bool    { return true }
func (adminPolicy) Allow(context.Context, *bot.Context) error   { return nil }
func (adminPolicy) Done(context.Context, *bot.Context, error)   {}
func (adminPolicy) Roles(_ context.Context, m *bot.Message) (bool, bool) {
	return m.Sender == bottest.Admin, false
}

var caio = types.NewJID("5511777777777", types.DefaultUserServer)

type env struct {
	r    *bot.Router
	fc   *bottest.FakeClient
	pick int
}

func setup(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(ctx, "raffle", raffle.Migrations()); err != nil {
		t.Fatal(err)
	}
	e := &env{fc: &bottest.FakeClient{Groups: map[types.JID]*bot.GroupInfo{bottest.Group: {
		JID:          bottest.Group,
		Participants: []bot.Participant{{JID: bottest.Bot}, {JID: bottest.User}, {JID: bottest.Admin, IsAdmin: true}, {JID: caio}},
	}}}}
	f := raffle.New(st.DB(), func(n int) int { return min(e.pick, n-1) }, nil)
	e.r = bot.NewRouter("!", e.fc, quiet)
	e.r.SetPolicy(adminPolicy{})
	if err := e.r.Register(f.Commands()...); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) as(t *testing.T, from types.JID, name, text string) bot.Outgoing {
	t.Helper()
	m := bottest.GroupMsg(text)
	m.Sender, m.PushName = from, name
	e.r.Handle(context.Background(), m)
	if len(e.fc.Replies) == 0 {
		t.Fatal("nenhuma resposta")
	}
	return e.fc.Last().Out
}

func (e *env) react(t *testing.T, target *bot.Message, by types.JID) bot.Outgoing {
	t.Helper()
	n := len(e.fc.Replies)
	e.r.HandleReaction(context.Background(), target, bot.Reaction{Emoji: "🎲", By: by})
	if len(e.fc.Replies) == n {
		return bot.Outgoing{}
	}
	return e.fc.Last().Out
}

func expect(t *testing.T, out bot.Outgoing, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(out.Text, s) {
			t.Fatalf("resposta sem %q: %q", s, out.Text)
		}
	}
}

func botMsg(text string) *bot.Message {
	return &bot.Message{ID: "b1", Chat: bottest.Group, Sender: bottest.Bot, FromMe: true, IsGroup: true, Text: text}
}

func TestRaffleFlow(t *testing.T) {
	e := setup(t)
	expect(t, e.as(t, bottest.User, "Fulano", "!sorteio-entrar"), "Não há nenhum sorteio ativo")
	expect(t, e.as(t, bottest.User, "Fulano", "!sorteio-sair"), "Não há nenhum sorteio ativo")

	created := e.as(t, bottest.User, "Fulano", "!sorteio Pizza grátis")
	expect(t, created, "NOVO SORTEIO CRIADO", "Sorteio: *Pizza grátis*", "🎲")

	expect(t, e.as(t, bottest.User, "Fulano", "!sorteio-entrar"), "*Fulano* entrou")
	expect(t, e.as(t, bottest.User, "Fulano", "!sorteio-entrar"), "já está participando")

	// Reação 🎲 na mensagem do sorteio: entra, e reagir de novo sai.
	target := botMsg(created.Text)
	out := e.react(t, target, caio)
	expect(t, out, "@"+caio.User+" entrou")
	if len(out.Mentions) != 1 || out.Mentions[0] != caio {
		t.Fatalf("menções: %v", out.Mentions)
	}
	expect(t, e.react(t, target, caio), "@"+caio.User+" saiu")
	e.react(t, target, caio)

	// Reação em mensagem que não é do bot, ou que não é do sorteio, é ignorada.
	if out := e.react(t, bottest.GroupMsg("Sorteio: *Pizza grátis*"), bottest.Admin); out.Text != "" {
		t.Fatalf("reagiu a mensagem de usuário: %q", out.Text)
	}
	if out := e.react(t, botMsg("outra coisa"), bottest.Admin); out.Text != "" {
		t.Fatalf("reagiu a outra mensagem: %q", out.Text)
	}

	status := e.as(t, bottest.Admin, "Chefe", "!sorteio")
	expect(t, status, "Sorteio Ativo:* *Pizza grátis*", "Participantes (2)", "1. Fulano (@"+bottest.User.User+")", "2. @"+caio.User)
	if len(status.Mentions) != 2 {
		t.Fatalf("menções: %v", status.Mentions)
	}
	// A mensagem de status também aceita 🎲.
	expect(t, e.react(t, botMsg(status.Text), bottest.Admin), "entrou")

	expect(t, e.as(t, caio, "Caio", "!sortear"), "Só admins do grupo ou quem criou")

	e.pick = 1
	win := e.as(t, bottest.User, "Fulano", "!sortear") // o criador pode sortear
	expect(t, win, "SORTEIO REALIZADO", "*Pizza grátis*", "@"+caio.User)
	if len(win.Mentions) != 1 || win.Mentions[0] != caio {
		t.Fatalf("menções: %v", win.Mentions)
	}
	expect(t, e.as(t, bottest.User, "Fulano", "!sorteio-entrar"), "Não há nenhum sorteio ativo")

	expect(t, e.as(t, bottest.User, "Fulano", "!sorteios"), "Histórico de Sorteios", "1. *Pizza grátis* - Vencedor(a): @"+caio.User)
}

func TestLeave(t *testing.T) {
	e := setup(t)
	e.as(t, bottest.User, "Fulano", "!sorteio")
	expect(t, e.as(t, bottest.User, "Fulano", "!sorteio-sair"), "não está participando")
	e.as(t, bottest.User, "Fulano", "!sorteio-entrar")
	expect(t, e.as(t, bottest.User, "Fulano", "!sorteio-sair"), "*Fulano* saiu")
	expect(t, e.as(t, bottest.Admin, "Chefe", "!sortear"), "Não há nenhum participante")
}

func TestInstantRaffle(t *testing.T) {
	e := setup(t)
	expect(t, e.as(t, bottest.User, "Fulano", "!sortear"), "Só admins")
	e.pick = 99 // último membro; o bot fica de fora
	out := e.as(t, bottest.Admin, "Chefe", "!sortear Quem paga o café")
	expect(t, out, "SORTEIO INSTANTÂNEO", "*Quem paga o café*", "@"+caio.User)
	e.pick = 0
	out = e.as(t, bottest.Admin, "Chefe", "!sortear")
	if strings.Contains(out.Text, "@"+bottest.Bot.User) {
		t.Fatalf("sorteou o bot: %q", out.Text)
	}
	expect(t, out, "Sorteio Rápido")
	expect(t, e.as(t, bottest.User, "Fulano", "!sorteios"), "1. *Sorteio Rápido*", "2. *Quem paga o café*")
}

func TestEmptyHistoryAndGroupOnly(t *testing.T) {
	e := setup(t)
	expect(t, e.as(t, bottest.User, "Fulano", "!sorteios"), "Nenhum sorteio anterior")
	e.r.Handle(context.Background(), bottest.PrivateMsg("!sorteio"))
	expect(t, e.fc.Last().Out, "só funciona em grupos")
}
