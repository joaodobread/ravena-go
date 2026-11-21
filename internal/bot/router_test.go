package bot_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"testing"

	"go.mau.fi/whatsmeow/types"

	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/bot/bottest"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func newRouter(t *testing.T) (*bot.Router, *bottest.FakeClient) {
	t.Helper()
	fc := &bottest.FakeClient{}
	return bot.NewRouter("!", fc, quiet), fc
}

func msg(text string) *bot.Message {
	return &bot.Message{ID: "1", Chat: bottest.Group, Sender: bottest.User, IsGroup: true, Text: text}
}

func TestParse(t *testing.T) {
	r, _ := newRouter(t)
	tests := []struct {
		in       string
		wantName string
		wantArgs []string
		wantOK   bool
	}{
		{"!s", "s", []string{}, true},
		{"  !Sticker  Meu   Pacote ", "sticker", []string{"Meu", "Pacote"}, true},
		{"!", "", nil, false},
		{"! s", "s", []string{}, true},
		{"s", "", nil, false},
		{"", "", nil, false},
	}
	for _, tt := range tests {
		name, args, ok := r.Parse(tt.in)
		if ok != tt.wantOK || name != tt.wantName || (ok && !slices.Equal(args, tt.wantArgs)) {
			t.Errorf("Parse(%q) = %q %q %v, want %q %q %v", tt.in, name, args, ok, tt.wantName, tt.wantArgs, tt.wantOK)
		}
	}
}

func TestDispatchByAliasCaseInsensitive(t *testing.T) {
	r, fc := newRouter(t)
	var got *bot.Context
	err := r.Register(&bot.Command{
		Name: "sticker", Aliases: []string{"S", "fig"},
		Reactions: bot.Reactions{Before: "⏳", After: "🖼"},
		Run:       func(_ context.Context, c *bot.Context) error { got = c; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	r.Handle(context.Background(), msg("!s Pacote Legal"))
	if got == nil || got.Invoked != "s" || !slices.Equal(got.Args, []string{"Pacote", "Legal"}) {
		t.Fatalf("contexto inesperado: %+v", got)
	}
	if !slices.Equal(fc.Reactions, []string{"⏳", "🖼"}) {
		t.Fatalf("reações = %q", fc.Reactions)
	}
}

func TestRegisterRejectsDuplicates(t *testing.T) {
	r, _ := newRouter(t)
	noop := func(context.Context, *bot.Context) error { return nil }
	if err := r.Register(&bot.Command{Name: "a", Aliases: []string{"b"}, Run: noop}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(&bot.Command{Name: "B", Run: noop}); err == nil {
		t.Fatal("alias duplicado deveria falhar")
	}
	if err := r.Register(&bot.Command{Name: "c"}); err == nil {
		t.Fatal("comando sem Run deveria falhar")
	}
}

func TestUserErrorIsShownGenericErrorIsNot(t *testing.T) {
	r, fc := newRouter(t)
	_ = r.Register(
		&bot.Command{Name: "user", Reactions: bot.Reactions{Error: "❌"},
			Run: func(context.Context, *bot.Context) error { return bot.Userf("faltou %s", "mídia") }},
		&bot.Command{Name: "boom",
			Run: func(context.Context, *bot.Context) error { return errors.New("segredo interno") }},
	)

	r.Handle(context.Background(), msg("!user"))
	if got := fc.Last().Out.Text; got != "faltou mídia" {
		t.Fatalf("resposta = %q", got)
	}
	if !slices.Equal(fc.Reactions, []string{"❌"}) {
		t.Fatalf("reações = %q", fc.Reactions)
	}

	r.Handle(context.Background(), msg("!boom"))
	if got := fc.Last().Out.Text; got == "segredo interno" || got == "" {
		t.Fatalf("erro interno vazou ou sumiu: %q", got)
	}
}

func TestListenersRunInOrderUntilHandled(t *testing.T) {
	r, _ := newRouter(t)
	var calls []string
	r.Listen(func(context.Context, *bot.Message, bot.Client) (bool, error) {
		calls = append(calls, "a")
		return false, errors.New("falha não interrompe a cadeia")
	})
	r.Listen(func(context.Context, *bot.Message, bot.Client) (bool, error) {
		calls = append(calls, "b")
		return true, nil
	})
	r.Listen(func(context.Context, *bot.Message, bot.Client) (bool, error) {
		calls = append(calls, "c")
		return true, nil
	})

	r.Handle(context.Background(), msg("oi"))
	r.Handle(context.Background(), msg("!naoexiste"))
	if !slices.Equal(calls, []string{"a", "b", "a", "b"}) {
		t.Fatalf("calls = %q", calls)
	}
}

func TestIgnoresOwnMessagesAndRecoversPanics(t *testing.T) {
	r, fc := newRouter(t)
	_ = r.Register(&bot.Command{Name: "panic", Run: func(context.Context, *bot.Context) error { panic("boom") }})

	own := msg("!panic")
	own.FromMe = true
	r.Handle(context.Background(), own)
	r.Handle(context.Background(), msg("!panic")) // não pode derrubar o teste
	if len(fc.Replies) != 0 {
		t.Fatalf("não deveria responder: %+v", fc.Replies)
	}
}

func TestHandleReactionRunsTriggeredCommandOnTarget(t *testing.T) {
	r, fc := newRouter(t)
	var got *bot.Context
	err := r.Register(&bot.Command{
		Name: "sticker", Triggers: []string{"🖼"},
		Reactions: bot.Reactions{Before: "⌛️", After: "🖼"},
		Run:       func(_ context.Context, c *bot.Context) error { got = c; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	target := msg("legenda da foto")
	r.HandleReaction(context.Background(), target, bot.Reaction{Emoji: "👍", By: bottest.Admin})
	if got != nil {
		t.Fatal("emoji sem trigger não deveria rodar nada")
	}
	r.HandleReaction(context.Background(), target, bot.Reaction{Emoji: "🖼", By: bottest.Admin})
	if got == nil || got.Message != target || !slices.Equal(got.Args, []string{"legenda", "da", "foto"}) || got.Reaction == nil || got.Reaction.By != bottest.Admin {
		t.Fatalf("contexto inesperado: %+v", got)
	}
	if !slices.Equal(fc.Reactions, []string{"⌛️", "🖼"}) {
		t.Fatalf("reações = %q", fc.Reactions)
	}
	if !r.HasTrigger("🖼") || r.HasTrigger("👍") {
		t.Fatal("HasTrigger incorreto")
	}
	if err := r.Register(&bot.Command{Name: "outro", Triggers: []string{"🖼"}, Run: got.Command.Run}); err == nil {
		t.Fatal("trigger duplicado deveria falhar")
	}
}

func TestObserversSeeEveryAdmittedMessage(t *testing.T) {
	r, _ := newRouter(t)
	var seen []string
	r.Observe(func(_ context.Context, m *bot.Message) { seen = append(seen, m.Text) })
	_ = r.Register(&bot.Command{Name: "x", Run: func(context.Context, *bot.Context) error { return nil }})
	r.Handle(context.Background(), msg("oi"))
	r.Handle(context.Background(), msg("!x"))
	own := msg("minha")
	own.FromMe = true
	r.Handle(context.Background(), own)
	if !slices.Equal(seen, []string{"oi", "!x"}) {
		t.Fatalf("seen = %q", seen)
	}
}

func TestGroupEventsReachHandlersAndRecover(t *testing.T) {
	r, _ := newRouter(t)
	var got []string
	r.OnGroupEvent(func(_ context.Context, ev bot.GroupEvent, _ bot.Client) { got = append(got, ev.Joined[0].User) })
	r.OnGroupEvent(func(context.Context, bot.GroupEvent, bot.Client) { panic("boom") })
	r.HandleGroupEvent(context.Background(), bot.GroupEvent{Chat: bottest.Group, Joined: []types.JID{bottest.User}})
	if !slices.Equal(got, []string{bottest.User.User}) {
		t.Fatalf("got = %v", got)
	}
}

func TestRawArgsKeepsLineBreaks(t *testing.T) {
	r, _ := newRouter(t)
	var got string
	_ = r.Register(&bot.Command{Name: "eco", Run: func(_ context.Context, c *bot.Context) error { got = c.RawArgs(); return nil }})
	r.Handle(context.Background(), msg("  !ECO   linha 1\nlinha  2  "))
	if got != "linha 1\nlinha  2" {
		t.Fatalf("RawArgs = %q", got)
	}
	r.Handle(context.Background(), msg("!eco"))
	if got != "" {
		t.Fatalf("sem argumentos: %q", got)
	}
}
