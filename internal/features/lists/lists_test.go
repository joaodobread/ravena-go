package lists_test

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
	"github.com/joaodobread/ravena-go/internal/features/lists"
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
	r  *bot.Router
	fc *bottest.FakeClient
}

func setup(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(ctx, "lists", lists.Migrations()); err != nil {
		t.Fatal(err)
	}
	e := &env{fc: &bottest.FakeClient{}}
	e.r = bot.NewRouter("!", e.fc, quiet)
	e.r.SetPolicy(adminPolicy{})
	if err := e.r.Register(lists.New(st.DB(), nil).Commands()...); err != nil {
		t.Fatal(err)
	}
	return e
}

// as manda text e devolve os textos enviados em resposta.
func (e *env) as(t *testing.T, from types.JID, name, text string, mentions ...types.JID) []string {
	t.Helper()
	n := len(e.fc.Texts())
	m := bottest.GroupMsg(text)
	m.Sender, m.PushName, m.Mentions = from, name, mentions
	e.r.Handle(context.Background(), m)
	return e.fc.Texts()[n:]
}

func (e *env) say(t *testing.T, text string) []string { return e.as(t, bottest.User, "Fulano", text) }

func expect(t *testing.T, got []string, subs ...string) {
	t.Helper()
	all := strings.Join(got, "\n---\n")
	for _, s := range subs {
		if !strings.Contains(all, s) {
			t.Fatalf("respostas sem %q: %q", s, all)
		}
	}
}

func TestListsFlow(t *testing.T) {
	e := setup(t)
	expect(t, e.say(t, "!listas"), "Não há listas")
	expect(t, e.say(t, "!lc"), "pelo menos um nome")

	out := e.say(t, "!lc futebol churras")
	if len(out) != 2 {
		t.Fatalf("esperava resumo + quadro: %q", out)
	}
	expect(t, out[:1], "Listas criadas: futebol, churras")
	expect(t, out[1:], "*Listas disponíveis*", "1️⃣ *futebol* (0 membros)", "2️⃣ *churras* (0 membros)", "!le <lista>")
	expect(t, e.say(t, "!lc FUTEBOL"), "já existe", "Nenhuma lista foi criada")

	expect(t, e.say(t, "!lct"), "nome da lista e o título")
	expect(t, e.say(t, "!lct praia Praia no domingo"), "Lista criada: praia (Praia no domingo)", "3️⃣ *Praia no domingo*")
	expect(t, e.say(t, "!lct praia outra"), "já existe")

	expect(t, e.say(t, "!le futebol nada"), "Fulano entrou na lista \"futebol\"", "Lista \"nada\" não encontrada", "*futebol* (1 membro)", "Membros: Fulano")
	expect(t, e.say(t, "!le futebol"), "Você já está na lista")
	expect(t, e.as(t, caio, "Caio", "!le futebol churras"), "Caio entrou na lista \"churras\"", "*futebol* (2 membros)", "Membros: Fulano, Caio")

	expect(t, e.say(t, "!ls"), "pelo menos uma lista")
	expect(t, e.say(t, "!ls churras"), "Você não está na lista", "não saiu de nenhuma")
	expect(t, e.say(t, "!ls futebol"), "Fulano saiu das listas: \"futebol\"", "*futebol* (1 membro)")

	expect(t, e.say(t, "!lt"), "nome da lista e o título")
	expect(t, e.say(t, "!lt nada X"), "não encontrada")
	expect(t, e.say(t, "!lt churras Churrasco sábado"), "atualizado para \"Churrasco sábado\"", "2️⃣ *Churrasco sábado*")

	// Remoção por admin.
	expect(t, e.say(t, "!lr churras 7777"), "Só admins")
	expect(t, e.as(t, bottest.Admin, "Chefe", "!lr churras"), "Informe a lista")
	expect(t, e.as(t, bottest.Admin, "Chefe", "!lr churras 1234"), "não encontrado")
	expect(t, e.as(t, bottest.Admin, "Chefe", "!lr praia 1234"), "não tem membros")
	expect(t, e.as(t, bottest.Admin, "Chefe", "!lr churras +55 11 7777-77777"), "Caio foi removido(a) da lista \"Churrasco sábado\"")
	e.as(t, caio, "Caio", "!le churras")
	expect(t, e.as(t, bottest.Admin, "Chefe", "!lr churras @caio", caio), "Caio foi removido(a)")

	expect(t, e.say(t, "!ld"), "pelo menos uma lista")
	expect(t, e.say(t, "!ld nada"), "Nenhuma lista foi apagada")
	expect(t, e.say(t, "!ld praia churras"), "Listas excluídas: praia, churras", "1️⃣ *futebol*")
	out = e.say(t, "!ld futebol")
	if len(out) != 1 {
		t.Fatalf("sem listas, não deveria mandar o quadro: %q", out)
	}
	expect(t, e.say(t, "!ll"), "Não há listas")
}

func TestReactionToggles(t *testing.T) {
	e := setup(t)
	out := e.say(t, "!lc a b")
	board := &bot.Message{ID: "b1", Chat: bottest.Group, Sender: bottest.Bot, FromMe: true, IsGroup: true, Text: out[1]}

	react := func(target *bot.Message, emoji string, by types.JID) bot.Outgoing {
		n := len(e.fc.Replies)
		e.r.HandleReaction(context.Background(), target, bot.Reaction{Emoji: emoji, By: by})
		if len(e.fc.Replies) == n {
			return bot.Outgoing{}
		}
		return e.fc.Last().Out
	}

	got := react(board, "2️⃣", caio)
	if !strings.Contains(got.Text, "@"+caio.User+" entrou na lista \"b\"") || len(got.Mentions) != 1 {
		t.Fatalf("entrar: %+v", got)
	}
	// Grafia sem o seletor de variação também conta.
	if got := react(board, "2⃣", caio); !strings.Contains(got.Text, "saiu da lista \"b\"") {
		t.Fatalf("sair: %+v", got)
	}
	if got := react(board, "1️", caio); !strings.Contains(got.Text, "entrou na lista \"a\"") {
		t.Fatalf("grafia 3: %+v", got)
	}
	expect(t, e.say(t, "!ll"), "*a* (1 membro)", "Membros: "+caio.User)

	// Número sem lista, mensagem de usuário ou outra mensagem do bot: nada.
	if got := react(board, "9️⃣", caio); got.Text != "" {
		t.Fatalf("lista inexistente: %q", got.Text)
	}
	if got := react(bottest.GroupMsg(out[1]), "1️⃣", caio); got.Text != "" {
		t.Fatalf("mensagem de usuário: %q", got.Text)
	}
	other := *board
	other.Text = "outra coisa"
	if got := react(&other, "1️⃣", caio); got.Text != "" {
		t.Fatalf("outra mensagem: %q", got.Text)
	}
}

func TestEmojiIndex(t *testing.T) {
	cases := map[string]int{"1️⃣": 0, "1⃣": 0, "1️": 0, "5️⃣": 4, "9⃣": 8, "🔟": 9, "👍": -1, "0️⃣": -1}
	for in, want := range cases {
		if got := lists.EmojiIndex(in); got != want {
			t.Errorf("EmojiIndex(%q) = %d, want %d", in, got, want)
		}
	}
	seen := map[string]bool{}
	for _, e := range lists.NumberEmojis {
		if seen[e] {
			t.Fatalf("gatilho duplicado %q", e)
		}
		seen[e] = true
	}
}

func TestGroupOnly(t *testing.T) {
	e := setup(t)
	e.r.Handle(context.Background(), bottest.PrivateMsg("!listas"))
	expect(t, e.fc.Texts(), "só funciona em grupos")
}
