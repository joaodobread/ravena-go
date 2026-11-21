package greetings

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/types"

	"github.com/joaodobread/ravena-go/internal/app"
	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/bot/bottest"
	"github.com/joaodobread/ravena-go/internal/policy"
	"github.com/joaodobread/ravena-go/internal/store"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func setup(t *testing.T) (*bot.Router, *bottest.FakeClient, *store.Store) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	fc := &bottest.FakeClient{
		Names: map[types.JID]string{bottest.Group: "Família"},
		Groups: map[types.JID]*bot.GroupInfo{bottest.Group: {JID: bottest.Group, Name: "Família", Participants: []bot.Participant{
			{JID: bottest.Admin, IsAdmin: true}, {JID: bottest.User},
		}}},
	}
	// Admin como super admin: passa pelo anti-spam (comandos seguidos no teste).
	pol, err := policy.New(ctx, st, fc, []string{bottest.Admin.User}, policy.DefaultOptions, quiet)
	if err != nil {
		t.Fatal(err)
	}
	r := bot.NewRouter("!", fc, quiet)
	r.SetPolicy(pol)
	if err := (Module{}).Register(ctx, &app.Deps{Router: r, Store: st, Log: quiet}); err != nil {
		t.Fatal(err)
	}
	return r, fc, st
}

func adminMsg(text string) *bot.Message {
	m := bottest.GroupMsg(text)
	m.Sender = bottest.Admin
	return m
}

func TestRenderVariables(t *testing.T) {
	a, b := types.NewJID("1", types.DefaultUserServer), types.NewJID("2", types.DefaultUserServer)
	if got := Render("Oi {pessoa}, bem-vindo{plural_s} ao {grupo}! Vocês {plural_esao} legais", []types.JID{a, b}, "G"); got != "Oi @1, @2, bem-vindos ao G! Vocês são legais" {
		t.Fatalf("plural: %q", got)
	}
	if got := Render("{pessoa} {plural_esao} bem-vindo{plural_s}", []types.JID{a}, "G"); got != "@1 é bem-vindo" {
		t.Fatalf("singular: %q", got)
	}
}

func TestSetGreetingAndWelcomeOnJoin(t *testing.T) {
	r, fc, _ := setup(t)
	ctx := context.Background()
	r.Handle(ctx, bottest.GroupMsg("!g-setBoasvindas oi"))
	if !strings.Contains(fc.Last().Out.Text, "admins") {
		t.Fatalf("membro comum não deveria configurar: %q", fc.Last().Out.Text)
	}
	r.Handle(ctx, adminMsg("!g-setBoasvindas Bem-vindo{plural_s} ao {grupo}, {pessoa}!"))
	r.Handle(ctx, adminMsg("!g-setDespedida Tchau {pessoa}"))

	newbie := types.NewJID("5511777777777", types.DefaultUserServer)
	r.HandleGroupEvent(ctx, bot.GroupEvent{Chat: bottest.Group, Joined: []types.JID{newbie, bottest.Bot}})
	out := fc.Last()
	if out.Out.Text != "Bem-vindo ao Família, @5511777777777!" || len(out.Out.Mentions) != 1 || out.Chat != bottest.Group {
		t.Fatalf("boas-vindas: %+v", out)
	}
	r.HandleGroupEvent(ctx, bot.GroupEvent{Chat: bottest.Group, Left: []types.JID{newbie}})
	if fc.Last().Out.Text != "Tchau @5511777777777" {
		t.Fatalf("despedida: %q", fc.Last().Out.Text)
	}

	before := len(fc.Replies)
	r.HandleGroupEvent(ctx, bot.GroupEvent{Chat: bottest.Group, Joined: []types.JID{bottest.Bot}})
	if len(fc.Replies) != before {
		t.Fatal("o próprio bot entrando não deveria gerar boas-vindas")
	}

	r.Handle(ctx, adminMsg("!g-delBoasvindas"))
	before = len(fc.Replies)
	r.HandleGroupEvent(ctx, bot.GroupEvent{Chat: bottest.Group, Joined: []types.JID{newbie}})
	if len(fc.Replies) != before {
		t.Fatal("boas-vindas removidas ainda foram enviadas")
	}
}

func TestShowCurrentGreeting(t *testing.T) {
	r, fc, st := setup(t)
	ctx := context.Background()
	if _, err := st.UpdateGroup(ctx, bottest.Group.String(), func(g *store.Group) { g.Greeting = "Olá {pessoa}" }); err != nil {
		t.Fatal(err)
	}
	r.Handle(ctx, adminMsg("!g-setBoasvindas"))
	if !strings.Contains(fc.Last().Out.Text, "Olá {pessoa}") {
		t.Fatalf("deveria mostrar a atual: %q", fc.Last().Out.Text)
	}
}

func TestPausedGroupStaysQuiet(t *testing.T) {
	r, fc, st := setup(t)
	ctx := context.Background()
	if _, err := st.UpdateGroup(ctx, bottest.Group.String(), func(g *store.Group) { g.Greeting = "oi"; g.Paused = true }); err != nil {
		t.Fatal(err)
	}
	r.HandleGroupEvent(ctx, bot.GroupEvent{Chat: bottest.Group, Joined: []types.JID{bottest.User}})
	if len(fc.Replies) != 0 {
		t.Fatal("grupo pausado não deveria dar boas-vindas")
	}
}
