package policy

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/bot/bottest"
	"github.com/joaodobread/ravena-go/internal/store"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type env struct {
	st     *store.Store
	pol    *Policy
	client *bottest.FakeClient
	router *bot.Router
	runs   *[]string
	now    *time.Time
}

func setup(t *testing.T, envSA ...string) env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	fc := &bottest.FakeClient{Groups: map[types.JID]*bot.GroupInfo{
		bottest.Group: {JID: bottest.Group, Participants: []bot.Participant{
			{JID: bottest.User},
			{JID: types.NewJID("111", types.HiddenUserServer), Phone: bottest.Admin, IsAdmin: true},
		}},
	}}
	pol, err := New(ctx, st, fc, envSA, DefaultOptions, quiet)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	pol.now = func() time.Time { return now }

	r := bot.NewRouter("!", fc, quiet)
	r.SetPolicy(pol)
	runs := &[]string{}
	rec := func(name string) bot.Handler {
		return func(context.Context, *bot.Context) error { *runs = append(*runs, name); return nil }
	}
	must(t, r.Register(
		&bot.Command{Name: "ping", Category: "geral", Run: rec("ping")},
		&bot.Command{Name: "kick", AdminOnly: true, Run: rec("kick")},
		&bot.Command{Name: "pausar", IgnorePause: true, Run: rec("pausar")},
		&bot.Command{Name: "lento", Cooldown: time.Minute, Run: rec("lento")},
		&bot.Command{Name: "sa-x", SuperAdminOnly: true, Run: rec("sa-x")},
	))
	return env{st, pol, fc, r, runs, &now}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (e env) send(text string, from types.JID) {
	m := bottest.GroupMsg(text)
	m.Sender = from
	e.router.Handle(context.Background(), m)
	*e.now = e.now.Add(5 * time.Second) // fora do debounce por padrão
}

func TestGroupPrefixAndPause(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	_, err := e.st.UpdateGroup(ctx, bottest.Group.String(), func(g *store.Group) { g.Prefix = "/"; g.Paused = true })
	must(t, err)

	e.send("!ping", bottest.User)   // prefixo errado
	e.send("/ping", bottest.User)   // grupo pausado
	e.send("/pausar", bottest.User) // IgnorePause
	if !slices.Equal(*e.runs, []string{"pausar"}) {
		t.Fatalf("runs = %v", *e.runs)
	}
}

func TestMutedCommandsAndCategories(t *testing.T) {
	e := setup(t)
	_, err := e.st.UpdateGroup(context.Background(), bottest.Group.String(), func(g *store.Group) {
		g.MutedCategories = []string{"geral"}
	})
	must(t, err)
	e.send("!ping", bottest.User)  // silenciado
	e.send("!ping", bottest.Admin) // admin passa
	if !slices.Equal(*e.runs, []string{"ping"}) {
		t.Fatalf("runs = %v", *e.runs)
	}
}

func TestAdminOnlyUsesGroupAdminsByPhoneOrLID(t *testing.T) {
	e := setup(t)
	e.send("!kick", bottest.User)
	if got := e.client.Last().Out.Text; got == "" {
		t.Fatal("usuário comum deveria receber recusa")
	}
	// O admin aparece no grupo como LID, mas a mensagem chega pelo número.
	e.send("!kick", bottest.Admin)
	if !slices.Equal(*e.runs, []string{"kick"}) {
		t.Fatalf("runs = %v", *e.runs)
	}

	_, err := e.st.UpdateGroup(context.Background(), bottest.Group.String(), func(g *store.Group) {
		g.AdditionalAdmins = []string{bottest.User.User}
	})
	must(t, err)
	e.send("!kick", bottest.User)
	if len(*e.runs) != 2 {
		t.Fatalf("admin adicional deveria passar: %v", *e.runs)
	}
}

func TestSuperAdminWithAndWithoutNinthDigit(t *testing.T) {
	e := setup(t, "556296975194@c.us") // salvo sem o 9
	withNine := types.NewJID("5562996975194", types.DefaultUserServer)
	e.send("!sa-x", bottest.User)
	e.send("!sa-x", withNine)
	if !slices.Equal(*e.runs, []string{"sa-x"}) {
		t.Fatalf("runs = %v", *e.runs)
	}

	// Pelo painel, via LID alternativo.
	must(t, e.pol.SaveSettings(context.Background(), Settings{SuperAdmins: []string{"+55 11 99999-9999"}}))
	m := bottest.GroupMsg("!sa-x")
	m.Sender, m.SenderAlt = types.NewJID("999", types.HiddenUserServer), bottest.User
	e.router.Handle(context.Background(), m)
	if len(*e.runs) != 2 {
		t.Fatalf("super admin do painel via SenderAlt: %v", *e.runs)
	}
}

func TestAntiSpamBansAfterRepeatedFloods(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	for range 12 { // tudo dentro do debounce
		e.router.Handle(ctx, bottest.GroupMsg("!ping"))
		*e.now = e.now.Add(time.Second)
	}
	if len(*e.runs) != 1 {
		t.Fatalf("só o primeiro deveria rodar, rodaram %d", len(*e.runs))
	}
	*e.now = e.now.Add(10 * time.Second) // ainda banido (1 min)
	e.router.Handle(ctx, bottest.GroupMsg("!ping"))
	*e.now = e.now.Add(2 * time.Minute)
	e.router.Handle(ctx, bottest.GroupMsg("!ping"))
	if len(*e.runs) != 2 {
		t.Fatalf("deveria liberar após o ban: %d", len(*e.runs))
	}
}

func TestCooldownPerChat(t *testing.T) {
	e := setup(t)
	e.send("!lento", bottest.User)
	e.send("!lento", bottest.Admin)
	if len(*e.runs) != 1 || e.client.Last().Out.Text == "" {
		t.Fatalf("cooldown não aplicado: %v %q", *e.runs, e.client.Last().Out.Text)
	}
	*e.now = e.now.Add(time.Minute)
	e.send("!lento", bottest.Admin)
	if len(*e.runs) != 2 {
		t.Fatalf("cooldown não expirou: %v", *e.runs)
	}
}

func TestDisabledCommandsAndIgnoredUsers(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	must(t, e.pol.SaveSettings(ctx, Settings{DisabledCommands: []string{"ping"}}))
	e.send("!ping", bottest.User)
	must(t, e.pol.SaveSettings(ctx, Settings{}))
	_, err := e.st.UpdateGroup(ctx, bottest.Group.String(), func(g *store.Group) { g.IgnoredUsers = []string{bottest.User.User} })
	must(t, err)
	e.send("!ping", bottest.User)
	e.send("!ping", bottest.Admin)
	if !slices.Equal(*e.runs, []string{"ping"}) || len(e.client.Replies) != 0 {
		t.Fatalf("runs=%v replies=%v", *e.runs, e.client.Replies)
	}
}

func TestUsageIsRecorded(t *testing.T) {
	e := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.pol.RunUsageWriter(ctx); close(done) }()
	e.send("!ping", bottest.User)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		top, err := e.st.TopCommands(context.Background(), time.Unix(0, 0), "", 5)
		must(t, err)
		if len(top) == 1 && top[0].Command == "ping" {
			cancel()
			<-done
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	t.Fatal("uso não foi gravado")
}

func TestBrVariant(t *testing.T) {
	cases := map[string]string{"5562996975194": "556296975194", "556296975194": "5562996975194", "14155550000": "", "5562": ""}
	for in, want := range cases {
		if got := brVariant(in); got != want {
			t.Errorf("brVariant(%s) = %q, want %q", in, got, want)
		}
	}
}
