package menu_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/types"

	"github.com/joaodobread/ravena-go/internal/app"
	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/bot/bottest"
	"github.com/joaodobread/ravena-go/internal/features/menu"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func noop(context.Context, *bot.Context) error { return nil }

type extra struct{ calls int }

func (e *extra) MenuEntries(_ context.Context, chat types.JID) []string {
	e.calls++
	if chat != bottest.Group {
		return nil
	}
	return []string{"*!oi*: diz oi"}
}

func setup(t *testing.T, ex menu.ExtraLister) (*bot.Router, *bottest.FakeClient) {
	t.Helper()
	fc := &bottest.FakeClient{Names: map[types.JID]string{bottest.Group: "Grupo Teste"}}
	r := bot.NewRouter("!", fc, quiet)
	d := &app.Deps{Router: r, Client: fc, HTTP: http.DefaultClient, Log: quiet}
	if err := (menu.Module{Extra: ex}).Register(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	// Comandos registrados depois do menu precisam aparecer.
	err := r.Register(
		&bot.Command{Name: "sticker", Aliases: []string{"s", "fig"}, Category: "midia", Description: "Faz figurinha", Triggers: []string{"🖼"}, Run: noop},
		&bot.Command{Name: "pescar", Category: "jogos", Description: "Pesca", Run: noop},
		&bot.Command{Name: "zzz", Category: "jogos", Description: "Último", Run: noop},
		&bot.Command{Name: "lol-build", Category: "jogos", Run: noop},
		&bot.Command{Name: "secreto", Category: "jogos", Hidden: true, Run: noop},
		&bot.Command{Name: "reboot", Category: "geral", SuperAdminOnly: true, Run: noop},
		&bot.Command{Name: "semcat", Run: noop},
		&bot.Command{Name: "x", Category: "nova", Run: noop},
	)
	if err != nil {
		t.Fatal(err)
	}
	return r, fc
}

func TestMenuListsByCategoryInOrder(t *testing.T) {
	r, fc := setup(t, nil)
	r.Handle(context.Background(), bottest.GroupMsg("!menu"))
	out := fc.Last().Out.Text

	for _, want := range []string{"Comandos Ravena", "Grupo Teste", "Veja melhor a lista", "• *!sticker* (!s, !fig) 🖼: Faz figurinha", "• *!menu* (!cmd, !comandos, !info)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("faltou %q em:\n%s", want, out)
		}
	}
	for _, bad := range []string{"secreto", "reboot"} {
		if strings.Contains(out, bad) {
			t.Fatalf("%q não deveria aparecer:\n%s", bad, out)
		}
	}
	order := []string{"*Geral:*", "*Midia:*", "*Jogos:*", "*Nova:*", "*Resto:*"}
	last := -1
	for _, o := range order {
		i := strings.Index(out, o)
		if i < 0 || i < last {
			t.Fatalf("ordem de categorias errada (%s):\n%s", o, out)
		}
		last = i
	}
	// MenuOrder: pescar, lol-build antes do resto (alfabético).
	if !(strings.Index(out, "!pescar") < strings.Index(out, "!lol-build") && strings.Index(out, "!lol-build") < strings.Index(out, "!zzz")) {
		t.Fatalf("ordem de comandos errada:\n%s", out)
	}
}

func TestMenuInvalidatesCacheWhenCommandsChange(t *testing.T) {
	r, fc := setup(t, nil)
	r.Handle(context.Background(), bottest.GroupMsg("!cmd"))
	if strings.Contains(fc.Last().Out.Text, "!novo") {
		t.Fatal("novo comando antes de registrar")
	}
	if err := r.Register(&bot.Command{Name: "novo", Category: "geral", Run: noop}); err != nil {
		t.Fatal(err)
	}
	r.Handle(context.Background(), bottest.GroupMsg("!cmd"))
	if !strings.Contains(fc.Last().Out.Text, "!novo") {
		t.Fatalf("cache não invalidado:\n%s", fc.Last().Out.Text)
	}
}

type prefixPolicy struct{}

func (prefixPolicy) Prefix(context.Context, *bot.Message) string      { return "#" }
func (prefixPolicy) Admit(context.Context, *bot.Message) bool         { return true }
func (prefixPolicy) Allow(context.Context, *bot.Context) error        { return nil }
func (prefixPolicy) Roles(context.Context, *bot.Message) (bool, bool) { return false, false }
func (prefixPolicy) Done(context.Context, *bot.Context, error)        {}

func TestMenuUsesChatPrefix(t *testing.T) {
	r, fc := setup(t, nil)
	r.Handle(context.Background(), bottest.GroupMsg("!menu"))
	r.SetPolicy(prefixPolicy{})
	r.Handle(context.Background(), bottest.GroupMsg("#menu"))
	out := fc.Last().Out.Text
	if !strings.Contains(out, "• *#sticker* (#s, #fig)") || strings.Contains(out, "*!sticker*") {
		t.Fatalf("prefixo do chat não usado:\n%s", out)
	}
}

func TestMenuCategory(t *testing.T) {
	r, fc := setup(t, nil)
	r.Handle(context.Background(), bottest.GroupMsg("!menu Mídia"))
	out := fc.Last().Out.Text
	if !strings.Contains(out, "!sticker") || strings.Contains(out, "!pescar") {
		t.Fatalf("categoria errada:\n%s", out)
	}
}

func TestMenuUnknownCategory(t *testing.T) {
	r, fc := setup(t, nil)
	r.Handle(context.Background(), bottest.GroupMsg("!menu nada"))
	out := fc.Last().Out.Text
	if !strings.Contains(out, "não encontrada") || !strings.Contains(out, "jogos") {
		t.Fatalf("resposta = %q", out)
	}
	r.Handle(context.Background(), bottest.GroupMsg("!menu secreto"))
	if !strings.Contains(fc.Last().Out.Text, "não encontrada") {
		t.Fatal("comando escondido não é categoria")
	}
}

func TestMenuExtraEntries(t *testing.T) {
	ex := &extra{}
	r, fc := setup(t, ex)
	r.Handle(context.Background(), bottest.GroupMsg("!menu"))
	if out := fc.Last().Out.Text; !strings.Contains(out, "Comandos personalizados") || !strings.Contains(out, "• *!oi*: diz oi") {
		t.Fatalf("sem extras:\n%s", out)
	}
	r.Handle(context.Background(), bottest.PrivateMsg("!menu"))
	if out := fc.Last().Out.Text; strings.Contains(out, "personalizados") || strings.Contains(out, "Grupo Teste") {
		t.Fatalf("extras/grupo no privado:\n%s", out)
	}
	if ex.calls != 2 {
		t.Fatalf("calls = %d", ex.calls)
	}
}
