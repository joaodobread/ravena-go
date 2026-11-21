package ranking

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/bot/bottest"
	"github.com/joaodobread/ravena-go/internal/store"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type testPolicy struct{ fc *bottest.FakeClient }

func (testPolicy) Prefix(context.Context, *bot.Message) string { return "" }
func (testPolicy) Admit(context.Context, *bot.Message) bool    { return true }
func (testPolicy) Allow(context.Context, *bot.Context) error   { return nil }
func (testPolicy) Done(context.Context, *bot.Context, error)   {}
func (p testPolicy) Roles(ctx context.Context, m *bot.Message) (bool, bool) {
	if !m.IsGroup {
		return false, false
	}
	g, _ := p.fc.GroupInfo(ctx, m.Chat)
	return g.IsAdmin(m.Sender), false
}

var (
	other   = types.NewJID("5511777777777", types.DefaultUserServer)
	lurker  = types.NewJID("5511666666666", types.DefaultUserServer)
	userLID = types.NewJID("111222333", types.HiddenUserServer)
	gone    = types.NewJID("5511555555555", types.DefaultUserServer)
)

type env struct {
	f  *Feature
	r  *bot.Router
	fc *bottest.FakeClient
	st *store.Store
}

func setup(t *testing.T) *env {
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
			{JID: lurker},
			{JID: bottest.Bot},
		},
	}}}
	f, err := New(ctx, st, quiet)
	if err != nil {
		t.Fatal(err)
	}
	f.now = func() time.Time { return time.Date(2026, 11, 21, 15, 4, 0, 0, time.UTC) }
	r := bot.NewRouter("!", fc, quiet)
	r.SetPolicy(testPolicy{fc})
	if err := r.Register(f.Commands()...); err != nil {
		t.Fatal(err)
	}
	r.Observe(f.Observe)
	return &env{f: f, r: r, fc: fc, st: st}
}

func (e *env) say(sender types.JID, name, text string, n int) {
	for range n {
		m := bottest.GroupMsg(text)
		m.Sender, m.PushName = sender, name
		e.r.Handle(context.Background(), m)
	}
}

func (e *env) cmd(sender types.JID, text string) bot.Outgoing {
	m := bottest.GroupMsg(text)
	m.Sender = sender
	n := len(e.fc.Replies)
	e.r.Handle(context.Background(), m)
	if len(e.fc.Replies) == n {
		return bot.Outgoing{}
	}
	return e.fc.Last().Out
}

func TestFaladores(t *testing.T) {
	e := setup(t)
	if got := e.cmd(bottest.User, "!faladores").Text; !strings.Contains(got, "🙊 *Nunca vi*") {
		t.Fatalf("todos nunca vistos: %q", got)
	}

	e.say(other, "Ciclano", "oi", 5)
	e.say(bottest.User, "Fulano", "oi", 2)
	e.say(userLID, "Fulano", "oi", 2) // mesma pessoa pela LID
	e.say(gone, "Saiu", "tchau", 1)
	// Mensagens no privado não contam.
	p := bottest.PrivateMsg("oi")
	e.r.Handle(context.Background(), p)

	out := e.cmd(bottest.Admin, "!faladores") // comandos também contam (Fulano 4+1, Admin 1)
	want := "*🏆 Ranking de faladores do grupo 🗣*\n\n" +
		"🥇 @5511777777777: 5 mensagens\n" +
		"🥈 @5511999999999: 5 mensagens\n" +
		"🥉 @5511888888888: 1 mensagens\n" +
		"4º *Saiu*: 1 mensagens\n" +
		"\n🙊 *Nunca vi*:\n> Nenhuma mensagem registrada destes membros\n- @5511666666666\n" +
		"\n📊 *Estatísticas:*\nTotal de 12 mensagens enviadas por 4 participantes"
	if out.Text != want {
		t.Fatalf("got\n%q\nwant\n%q", out.Text, want)
	}
	if len(out.Mentions) != 3 {
		t.Fatalf("menções: %v", out.Mentions)
	}

	if got := e.cmd(bottest.User, "!faladores").Text; got == "" {
		t.Fatal("sem resposta")
	}
	priv := bottest.PrivateMsg("!faladores")
	e.r.Handle(context.Background(), priv)
	if got := e.fc.Last().Out.Text; got != "Esse comando só funciona em grupos." {
		t.Fatalf("privado: %q", got)
	}
}

func TestFaladoresTopAndCompleto(t *testing.T) {
	e := setup(t)
	for i := range 12 {
		e.say(types.NewJID(fmt.Sprintf("55110000000%02d", i), types.DefaultUserServer), fmt.Sprintf("P%02d", i), "oi", 20-i)
	}
	got := e.cmd(bottest.User, "!faladores").Text
	if !strings.Contains(got, "10º *P09*: 11 mensagens") || strings.Contains(got, "P10") ||
		!strings.Contains(got, "... e mais 3 membros (use '!faladores completo' para ver todos)") {
		t.Fatalf("top 10: %q", got)
	}
	got = e.cmd(bottest.User, "!faladores completo").Text
	if !strings.Contains(got, "12º *P11*: 9 mensagens") || strings.Contains(got, "e mais") {
		t.Fatalf("completo: %q", got)
	}
}

func TestEmpty(t *testing.T) {
	e := setup(t)
	e.fc.Groups[bottest.Group].Participants = []bot.Participant{{JID: bottest.Bot}}
	m := bottest.GroupMsg("!faladores")
	m.Sender = types.JID{} // sem autor: não conta
	e.r.Handle(context.Background(), m)
	if got := e.fc.Last().Out.Text; got != MsgEmpty {
		t.Fatalf("vazio: %q", got)
	}
}

func TestReset(t *testing.T) {
	e := setup(t)
	e.say(other, "Ciclano", "oi", 3)
	if got := e.cmd(bottest.User, "!faladores-reset").Text; got != "Só admins do grupo podem usar esse comando." {
		t.Fatalf("não admin: %q", got)
	}
	got := e.cmd(bottest.Admin, "!faladores-reset").Text
	if !strings.Contains(got, "🥇 @5511777777777: 3 mensagens") ||
		!strings.HasSuffix(got, "\n\n📅 Fechamento: 21/11/2026 12:04\n\n♻️ *Ranking reiniciado com sucesso!*") {
		t.Fatalf("reset: %q", got)
	}
	got = e.cmd(bottest.User, "!faladores").Text
	if strings.Contains(got, "Ciclano") || strings.Contains(got, "3 mensagens") ||
		!strings.Contains(got, "🥇 @5511999999999: 1 mensagens") {
		t.Fatalf("após reset: %q", got)
	}
}

func TestCleanup(t *testing.T) {
	e := setup(t)
	e.say(gone, "Saiu", "oi", 2)
	e.say(other, "Ciclano", "oi", 1)
	if got := e.cmd(bottest.User, "!faladores-limpeza").Text; got != "Só admins do grupo podem usar esse comando." {
		t.Fatalf("não admin: %q", got)
	}
	if got := e.cmd(bottest.Admin, "!faladores-limpeza").Text; got != "Limpeza concluída! 🧹\n1 membros antigos foram removidos do ranking." {
		t.Fatalf("limpeza: %q", got)
	}
	if got := e.cmd(bottest.Admin, "!faladores").Text; strings.Contains(got, "Saiu") {
		t.Fatalf("ex-membro continua: %q", got)
	}
}

func TestBoundedBatch(t *testing.T) {
	e := setup(t)
	e.f.soft, e.f.hard = 2, 3
	e.say(bottest.User, "A", "x", 1)
	e.say(other, "B", "x", 1)
	select {
	case <-e.f.full:
	default:
		t.Fatal("lote cheio deveria sinalizar")
	}
	e.say(lurker, "C", "x", 1)
	e.say(bottest.Admin, "D", "x", 1) // acima do teto: descartado
	e.say(bottest.User, "A", "x", 1)  // pessoa já no lote continua contando
	if n := len(e.f.pending); n != 3 {
		t.Fatalf("lote com %d entradas, teto 3", n)
	}
	if err := e.f.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(e.f.pending); n != 0 {
		t.Fatalf("lote não esvaziou: %d", n)
	}
	var count int
	if err := e.st.DB().QueryRow(`SELECT count FROM ranking_msgs WHERE user = ?`, bottest.User.User).Scan(&count); err != nil || count != 2 {
		t.Fatalf("count %d err %v", count, err)
	}
}

func TestRunFlushesOnSignalAndShutdown(t *testing.T) {
	e := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.f.Run(ctx); close(done) }()
	e.say(other, "Ciclano", "oi", 4)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run não terminou")
	}
	var count int
	if err := e.st.DB().QueryRow(`SELECT count FROM ranking_msgs WHERE user = ?`, other.User).Scan(&count); err != nil || count != 4 {
		t.Fatalf("gravação final: count %d err %v", count, err)
	}
}

func TestFlushFailureKeepsCounts(t *testing.T) {
	e := setup(t)
	e.say(other, "Ciclano", "oi", 2)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.f.Flush(ctx); err == nil {
		t.Fatal("esperava erro com ctx cancelado")
	}
	if e.f.pending[key{bottest.Group.String(), other.User}].n != 2 {
		t.Fatal("contagem perdida após falha")
	}
}
