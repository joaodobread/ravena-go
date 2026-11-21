package reminders

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/bot/bottest"
	"github.com/joaodobread/ravena-go/internal/store"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type env struct {
	s   *Scheduler
	r   *bot.Router
	fc  *bottest.FakeClient
	now *time.Time
}

func setup(t *testing.T) env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(ctx, "reminders", migrations); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 21, 14, 0, 0, 0, time.UTC)
	fc := &bottest.FakeClient{}
	s := New(st.DB(), fc, quiet, func() time.Time { return now })
	r := bot.NewRouter("!", fc, quiet)
	if err := r.Register(s.Commands()...); err != nil {
		t.Fatal(err)
	}
	return env{s, r, fc, &now}
}

func (e env) say(text string) string {
	e.r.Handle(context.Background(), bottest.GroupMsg(text))
	return e.fc.Last().Out.Text
}

func TestAddListFireAndCancel(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if got := e.say("!lembrar em 10 minutos tirar o bolo"); !strings.Contains(got, "em 10 min") {
		t.Fatalf("confirmação: %q", got)
	}
	e.say("!lembrar amanhã às 9h academia")
	if got := e.say("!lembretes"); !strings.Contains(got, "1. 21/11 14:10 — tirar o bolo") || !strings.Contains(got, "2. 22/11 09:00 — academia") {
		t.Fatalf("lista: %q", got)
	}

	if next := e.s.Fire(ctx); !next.Equal(e.now.Add(10 * time.Minute)) {
		t.Fatalf("próximo = %v", next)
	}
	sent := len(e.fc.Replies)
	*e.now = e.now.Add(11 * time.Minute)
	e.s.Fire(ctx)
	if len(e.fc.Replies) != sent+1 {
		t.Fatal("deveria ter disparado um lembrete")
	}
	fired := e.fc.Last()
	if fired.To != nil || fired.Chat != bottest.Group || !strings.Contains(fired.Out.Text, "tirar o bolo") ||
		len(fired.Out.Mentions) != 1 || fired.Out.Mentions[0] != bottest.User {
		t.Fatalf("lembrete enviado: %+v", fired)
	}
	e.s.Fire(ctx)
	if len(e.fc.Replies) != sent+1 {
		t.Fatal("lembrete disparado duas vezes")
	}

	if got := e.say("!l-cancelar 1"); !strings.Contains(got, "academia") {
		t.Fatalf("cancelar: %q", got)
	}
	if got := e.say("!lembretes"); !strings.Contains(got, "não tem lembretes") {
		t.Fatalf("lista vazia: %q", got)
	}
}

func TestReminderFromQuotedMessage(t *testing.T) {
	e := setup(t)
	m := bottest.GroupMsg("!lembrar às 18h")
	m.Quoted = &bot.Message{Text: "comprar pão"}
	e.r.Handle(context.Background(), m)
	if got := e.say("!lembretes"); !strings.Contains(got, "comprar pão") {
		t.Fatalf("lista: %q", got)
	}
}

func TestMisuse(t *testing.T) {
	e := setup(t)
	cases := map[string]string{
		"!lembrar comprar pão":     "Não entendi quando",
		"!lembrar em 10 minutos":   "Lembrar do quê",
		"!lembrar 01/01/2030 9h x": "um ano",
		"!l-cancelar":              "Use !l-cancelar",
		"!l-cancelar 3":            "Não existe",
		"!lembrar em 5 min " + strings.Repeat("a", maxText+1): "longo demais",
	}
	for in, want := range cases {
		if got := e.say(in); !strings.Contains(got, want) {
			t.Errorf("%q → %q (esperava %q)", in, got, want)
		}
	}
}

func TestPerUserLimit(t *testing.T) {
	e := setup(t)
	for range maxPerUser {
		e.say("!lembrar em 1 hora algo")
	}
	if got := e.say("!lembrar em 1 hora mais um"); !strings.Contains(got, "Cancele algum") {
		t.Fatalf("limite: %q", got)
	}
}

func TestRunWakesUpOnNewReminder(t *testing.T) {
	e := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.s.Run(ctx)
	// Lembrete já vencido (now fixo): o Run precisa acordar e disparar.
	if _, err := e.s.db.Exec(`INSERT INTO reminders(chat, user, text, due, created) VALUES(?, ?, 'agora', ?, ?)`,
		bottest.Group.String(), bottest.User.String(), e.now.Unix()-1, e.now.Unix()); err != nil {
		t.Fatal(err)
	}
	e.s.poke()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, txt := range e.fc.Texts() {
			if strings.Contains(txt, "agora") {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("Run não disparou o lembrete")
}
