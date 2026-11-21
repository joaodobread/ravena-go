package translate_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"go.mau.fi/whatsmeow/types"

	"github.com/joaodobread/ravena-go/internal/app"
	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/bot/bottest"
	"github.com/joaodobread/ravena-go/internal/features/translate"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type fakeLLM struct {
	system, prompt string
	out            string
	err            error
}

func (f *fakeLLM) Ask(_ context.Context, system, prompt string) (string, error) {
	f.system, f.prompt = system, prompt
	return f.out, f.err
}

type env struct {
	router *bot.Router
	client *bottest.FakeClient

	mu     sync.Mutex
	fail   bool
	last   url.Values
	detect string
}

func setup(t *testing.T, llm translate.Asker) *env {
	t.Helper()
	e := &env{detect: "pt"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.last = r.URL.Query()
		if e.fail || r.URL.Path != "/translate_a/single" {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		// Divide em dois trechos, como o Google faz com frases.
		q := r.URL.Query().Get("q")
		half := len(q) / 2
		resp := []any{
			[]any{
				[]any{"[" + r.URL.Query().Get("tl") + "]" + q[:half], q[:half], nil, nil, 10},
				[]any{q[half:], q[half:], nil, nil, 10},
			},
			nil, e.detect,
		}
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	e.client = &bottest.FakeClient{Names: map[types.JID]string{}}
	e.router = bot.NewRouter("!", e.client, quiet)
	d := &app.Deps{Router: e.router, Client: e.client, HTTP: srv.Client(), Log: quiet}
	if err := (translate.Module{BaseURL: srv.URL, LLM: llm}).Register(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) say(t *testing.T, msg *bot.Message, want ...string) string {
	t.Helper()
	e.router.Handle(context.Background(), msg)
	out := e.client.Last().Out.Text
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Fatalf("%q: faltou %q em:\n%s", msg.Text, w, out)
		}
	}
	return out
}

func (e *env) query() url.Values {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.last
}

func TestTranslateTargetOnly(t *testing.T) {
	e := setup(t, nil)
	e.say(t, bottest.GroupMsg("!traduzir en Olá, mundo!\nTudo bem?"), "🌐 *Tradução de Português para Inglês*", "[en]Olá, mundo!\nTudo bem?")
	q := e.query()
	if q.Get("sl") != "auto" || q.Get("tl") != "en" || q.Get("client") != "gtx" || q.Get("dt") != "t" {
		t.Fatalf("query = %v", q)
	}
	if q.Get("q") != "Olá, mundo!\nTudo bem?" {
		t.Fatalf("texto perdeu a quebra de linha: %q", q.Get("q"))
	}
	if got := e.client.Reactions; len(got) != 2 || got[0] != "⌛️" || got[1] != "🌐" {
		t.Fatalf("reações = %q", got)
	}
}

func TestTranslateByName(t *testing.T) {
	e := setup(t, nil)
	e.say(t, bottest.GroupMsg("!traduzir japonês bom dia"), "Japonês")
	if e.query().Get("tl") != "ja" {
		t.Fatal("japonês → ja")
	}
	e.say(t, bottest.GroupMsg("!traduzir chinês oi"), "Chinês (Simplificado)")
	if e.query().Get("tl") != "zh-CN" {
		t.Fatalf("tl = %q", e.query().Get("tl"))
	}
}

func TestTranslateSourceAndTarget(t *testing.T) {
	e := setup(t, nil)
	e.say(t, bottest.GroupMsg("!traduzir pt en Olá mundo"), "Tradução de Português para Inglês", "[en]Olá mundo")
	if q := e.query(); q.Get("sl") != "pt" || q.Get("tl") != "en" || q.Get("q") != "Olá mundo" {
		t.Fatalf("query = %v", q)
	}
	// "eu" é código (basco), mas aqui é o começo do texto.
	e.say(t, bottest.GroupMsg("!traduzir en eu te amo"))
	if q := e.query(); q.Get("sl") != "auto" || q.Get("tl") != "en" || q.Get("q") != "eu te amo" {
		t.Fatalf("query = %v", q)
	}
}

func TestTranslateQuoted(t *testing.T) {
	e := setup(t, nil)
	msg := bottest.GroupMsg("!traduzir es")
	msg.Quoted = &bot.Message{ID: "q", Text: "bom dia"}
	e.say(t, msg, "[es]bom dia")

	msg = bottest.GroupMsg("!traduzir en pt")
	msg.Quoted = &bot.Message{ID: "q", Text: "good morning"}
	e.detect = "en"
	e.say(t, msg, "Tradução de Inglês para Português", "[pt]good morning")
	if e.query().Get("sl") != "en" {
		t.Fatal("origem explícita ignorada")
	}
}

func TestTranslateMisuse(t *testing.T) {
	e := setup(t, nil)
	e.say(t, bottest.GroupMsg("!traduzir"), "forneça o idioma")
	e.say(t, bottest.GroupMsg("!traduzir klingon oi"), "Idioma não reconhecido")
	e.say(t, bottest.GroupMsg("!traduzir en"), "responda a uma mensagem")
	if e.query() != nil {
		t.Fatal("não deveria chamar a API")
	}
}

func TestTranslateUpstreamError(t *testing.T) {
	e := setup(t, nil)
	e.fail = true
	e.say(t, bottest.GroupMsg("!traduzir en oi"), "Erro ao traduzir")
	if r := e.client.Reactions; r[len(r)-1] != "❌" {
		t.Fatalf("reações = %q", r)
	}
}

func TestTranslateFallsBackToLLM(t *testing.T) {
	llm := &fakeLLM{out: " hello "}
	e := setup(t, llm)
	e.fail = true
	e.say(t, bottest.GroupMsg("!traduzir en oi"), "Tradução para Inglês", "\n\nhello")
	if llm.prompt != "oi" || !strings.Contains(llm.system, "Inglês") {
		t.Fatalf("llm chamado com %q / %q", llm.system, llm.prompt)
	}

	llm.err, llm.out = errors.New("fora do ar"), ""
	e.say(t, bottest.GroupMsg("!traduzir en oi"), "Erro ao traduzir")
}

func reacted(text string) *bot.Message {
	return &bot.Message{ID: "alvo", Chat: bottest.Group, Sender: bottest.Admin, IsGroup: true, Text: text}
}

func TestReactionTranslates(t *testing.T) {
	e := setup(t, nil)
	e.router.HandleReaction(context.Background(), reacted("bom dia, pessoal"), bot.Reaction{Emoji: "🇯🇵", By: bottest.User})
	last := e.client.Last()
	if last.To == nil || last.To.ID != "alvo" {
		t.Fatalf("deveria responder à mensagem reagida: %+v", last)
	}
	if !strings.Contains(last.Out.Text, "Tradução para Japonês (🇯🇵)") || !strings.Contains(last.Out.Text, "[ja]bom dia") {
		t.Fatalf("texto = %q", last.Out.Text)
	}
	if q := e.query(); q.Get("sl") != "auto" || q.Get("tl") != "ja" {
		t.Fatalf("query = %v", q)
	}

	for flag, lang := range map[string]string{"🇺🇸": "en", "🇬🇧": "en", "🇧🇷": "pt", "🇵🇹": "pt", "🇪🇸": "es", "🇫🇷": "fr", "🇩🇪": "de", "🇮🇹": "it", "🇷🇺": "ru", "🇨🇳": "zh-CN", "🇰🇷": "ko"} {
		if !e.router.HasTrigger(flag) {
			t.Fatalf("%s sem gatilho", flag)
		}
		e.router.HandleReaction(context.Background(), reacted("oi"), bot.Reaction{Emoji: flag, By: bottest.User})
		if got := e.query().Get("tl"); got != lang {
			t.Errorf("%s → %s, want %s", flag, got, lang)
		}
	}
}

func TestReactionIgnoresEmptyAndTypedUse(t *testing.T) {
	e := setup(t, nil)
	e.router.HandleReaction(context.Background(), reacted(""), bot.Reaction{Emoji: "🇺🇸", By: bottest.User})
	e.router.Handle(context.Background(), bottest.GroupMsg("!traduzir-bandeira oi"))
	if len(e.client.Replies) != 0 || e.query() != nil {
		t.Fatalf("não deveria responder: %+v", e.client.Replies)
	}
}

func TestReactionUpstreamError(t *testing.T) {
	e := setup(t, nil)
	e.fail = true
	e.router.HandleReaction(context.Background(), reacted("oi"), bot.Reaction{Emoji: "🇺🇸", By: bottest.User})
	if !strings.Contains(e.client.Last().Out.Text, "Erro ao traduzir") {
		t.Fatalf("resposta = %q", e.client.Last().Out.Text)
	}
}

func TestMenuHidesReactionHelper(t *testing.T) {
	e := setup(t, nil)
	for _, c := range e.router.Commands() {
		if c.Name == "traduzir-bandeira" && !c.Hidden {
			t.Fatal("helper de reação deveria ser Hidden")
		}
	}
}
