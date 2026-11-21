package stop_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/bot/bottest"
	"github.com/joaodobread/ravena-go/internal/features/gamekit"
	"github.com/joaodobread/ravena-go/internal/features/stop"
	"github.com/joaodobread/ravena-go/internal/llm"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type fakeLLM struct {
	out    string
	err    error
	prompt string
}

func (f *fakeLLM) Chat(_ context.Context, req llm.ChatRequest) (string, error) {
	f.prompt = req.Messages[0].Text
	return f.out, f.err
}

type env struct {
	r     *bot.Router
	fc    *bottest.FakeClient
	sched *gamekit.FakeScheduler
	game  *stop.Game
	now   time.Time
	stop  context.CancelFunc
}

func setup(t *testing.T, ai stop.Chatter) *env {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	e := &env{fc: &bottest.FakeClient{}, sched: &gamekit.FakeScheduler{}, now: time.Unix(1_700_000_000, 0), stop: cancel}
	e.game = stop.New(ctx, stop.Config{
		Client: e.fc, LLM: ai, Schedule: e.sched.Schedule, Log: quiet,
		Rand: func(int) int { return 0 }, Now: func() time.Time { return e.now },
	})
	e.r = bot.NewRouter("!", e.fc, quiet)
	if err := e.r.Register(e.game.Commands()...); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) last(t *testing.T) bottest.Reply {
	t.Helper()
	if len(e.fc.Replies) == 0 {
		t.Fatal("nenhuma resposta")
	}
	return e.fc.Last()
}

var player2 = types.NewJID("5511777777777", types.DefaultUserServer)

func (e *env) private(from types.JID, name, text string) {
	m := bottest.PrivateMsg(text)
	m.Chat, m.Sender, m.PushName = from, from, name
	e.r.Handle(context.Background(), m)
}

func card(answers string) string {
	return "!stop " + bottest.Group.String() + "\n🛑 *STOP!* ✋ - Letra: *A*\n\n" + answers
}

func TestRoundWithoutLLM(t *testing.T) {
	e := setup(t, nil)
	e.r.Handle(context.Background(), bottest.GroupMsg("!stop"))
	texts := e.fc.Texts()
	if len(texts) != 2 || !strings.Contains(texts[0], "Letra: *A*") || !strings.Contains(texts[0], "1m 30s") {
		t.Fatalf("início: %q", texts)
	}
	tmpl := texts[1]
	for _, want := range []string{"!stop " + bottest.Group.String(), "- Alimento:", "- Fruta:", "- Animal:", "- Celebridade:", "- Carro:"} {
		if !strings.Contains(tmpl, want) {
			t.Fatalf("gabarito sem %q: %q", want, tmpl)
		}
	}

	e.r.Handle(context.Background(), bottest.GroupMsg("!adedonha"))
	if !strings.Contains(e.last(t).Out.Text, "Já existe um jogo") || !strings.Contains(e.last(t).Out.Text, "1m 30s") {
		t.Fatalf("duplicado: %q", e.last(t).Out.Text)
	}

	e.now = e.now.Add(30 * time.Second)
	e.private(bottest.User, "Fulano", card("- Alimento: Arroz\n- Fruta: Abacaxi\n- Animal: Baleia\n- Celebridade:\n- *Carro*: Audi"))
	if got := e.last(t).Out.Text; !strings.Contains(got, "✅ Recebi") || !strings.Contains(got, "1m 0s") {
		t.Fatalf("resposta: %q", got)
	}
	e.private(player2, "Beltrano", card("- Alimento: Açaí\n- Fruta: Banana"))
	e.private(player2, "Beltrano", card("- Alimento: Açaí\n- Fruta: Amora"))
	if got := e.last(t).Out.Text; !strings.Contains(got, "🔄 Recebi") {
		t.Fatalf("atualização: %q", got)
	}

	if _, ok := e.sched.FireNext(); !ok {
		t.Fatal("sem timer")
	}
	texts = e.fc.Texts()
	if !strings.Contains(texts[len(texts)-2], "Tempo esgotado") {
		t.Fatalf("aviso de fim: %q", texts[len(texts)-2])
	}
	res := e.last(t)
	for _, want := range []string{
		"🥇 Fulano: 30 pontos", "🥈 Beltrano: 20 pontos",
		"Vencedor:* @" + bottest.User.User,
		"- ✅ Alimento: Arroz", "- ❌ Animal: Baleia", "- ❌ Celebridade: (não respondeu)", "- ✅ Carro: Audi",
		"- ✅ Alimento: Açaí",
	} {
		if !strings.Contains(res.Out.Text, want) {
			t.Fatalf("resultado sem %q: %q", want, res.Out.Text)
		}
	}
	if len(res.Out.Mentions) != 1 || res.Out.Mentions[0] != bottest.User {
		t.Fatalf("menções: %v", res.Out.Mentions)
	}
	if e.game.Active() != 0 {
		t.Fatal("rodada não terminou")
	}

	e.private(bottest.User, "Fulano", card("- Alimento: Arroz"))
	if !strings.Contains(e.last(t).Out.Text, "Não há um jogo ativo") {
		t.Fatalf("depois do fim: %q", e.last(t).Out.Text)
	}
}

func TestLLMJudges(t *testing.T) {
	ai := &fakeLLM{out: "Claro!\n```json\n{\"0\": {\"Alimento\": true, \"Fruta\": false, \"Animal\": true, \"Celebridade\": true, \"Carro\": true}}\n```"}
	e := setup(t, ai)
	e.r.Handle(context.Background(), bottest.GroupMsg("!stop 2 3"))
	if !strings.Contains(e.fc.Texts()[0], "2m para responder") {
		t.Fatalf("duração: %q", e.fc.Texts()[0])
	}
	if strings.Contains(e.fc.Texts()[1], "Celebridade") {
		t.Fatalf("deveria ter só 3 categorias: %q", e.fc.Texts()[1])
	}
	// Animal começa com B: mesmo com o LLM aceitando, a letra é exigida.
	e.private(bottest.User, "Fulano", card("Alimento: Arroz\nFruta: Abacaxi\nAnimal: Baleia"))
	e.sched.FireNext()
	res := e.last(t).Out.Text
	if !strings.Contains(res, "Fulano: 10 pontos") || !strings.Contains(res, "❌ Fruta: Abacaxi") || !strings.Contains(res, "❌ Animal: Baleia") {
		t.Fatalf("resultado: %q", res)
	}
	if !strings.Contains(ai.prompt, `"letra":"A"`) || !strings.Contains(ai.prompt, "Abacaxi") {
		t.Fatalf("prompt: %q", ai.prompt)
	}
}

func TestLLMFailureFallsBack(t *testing.T) {
	e := setup(t, &fakeLLM{err: errors.New("fora")})
	e.r.Handle(context.Background(), bottest.GroupMsg("!stop"))
	e.private(bottest.User, "Fulano", card("Alimento: Arroz\nFruta: Abacaxi"))
	e.sched.FireNext()
	if !strings.Contains(e.last(t).Out.Text, "Fulano: 20 pontos") {
		t.Fatalf("fallback: %q", e.last(t).Out.Text)
	}
}

func TestNoAnswersCancels(t *testing.T) {
	e := setup(t, nil)
	e.r.Handle(context.Background(), bottest.GroupMsg("!stop"))
	e.sched.FireNext()
	if !strings.Contains(e.last(t).Out.Text, "cancelado") {
		t.Fatalf("got %q", e.last(t).Out.Text)
	}
}

func TestPrivateErrors(t *testing.T) {
	e := setup(t, nil)
	e.private(bottest.User, "Fulano", "!stop")
	if !strings.Contains(e.last(t).Out.Text, "num grupo") {
		t.Fatalf("sem grupo: %q", e.last(t).Out.Text)
	}
	e.private(bottest.User, "Fulano", card("Alimento: Arroz"))
	if !strings.Contains(e.last(t).Out.Text, "Não há um jogo ativo") {
		t.Fatalf("sem jogo: %q", e.last(t).Out.Text)
	}

	e.r.Handle(context.Background(), bottest.GroupMsg("!stop"))
	e.now = e.now.Add(stop.DefaultDuration)
	e.private(bottest.User, "Fulano", card("Alimento: Arroz"))
	if !strings.Contains(e.last(t).Out.Text, "já acabou") {
		t.Fatalf("expirado: %q", e.last(t).Out.Text)
	}
}

func TestNonMemberRejected(t *testing.T) {
	e := setup(t, nil)
	e.fc.Groups = map[types.JID]*bot.GroupInfo{bottest.Group: {JID: bottest.Group, Participants: []bot.Participant{{JID: bottest.User}}}}
	e.r.Handle(context.Background(), bottest.GroupMsg("!stop"))
	e.private(player2, "Intruso", card("Alimento: Arroz"))
	if !strings.Contains(e.last(t).Out.Text, "não está nesse grupo") {
		t.Fatalf("got %q", e.last(t).Out.Text)
	}
	e.private(bottest.User, "Fulano", card("Alimento: Arroz"))
	if !strings.Contains(e.last(t).Out.Text, "Recebi") {
		t.Fatalf("membro: %q", e.last(t).Out.Text)
	}
}

func TestStartsWith(t *testing.T) {
	cases := map[[2]string]bool{
		{"Ágata", "A"}: true, {"  arroz", "A"}: true, {"Baleia", "A"}: false, {"", "A"}: false, {"Éclair", "E"}: true,
	}
	for in, want := range cases {
		if got := stop.StartsWith(in[0], in[1]); got != want {
			t.Errorf("StartsWith(%q, %q) = %v", in[0], in[1], got)
		}
	}
}

func TestContextEndStopsRounds(t *testing.T) {
	e := setup(t, nil)
	e.r.Handle(context.Background(), bottest.GroupMsg("!stop"))
	e.stop()
	deadline := time.Now().Add(time.Second)
	for e.game.Active() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if e.game.Active() != 0 || e.sched.Pending() != 0 {
		t.Fatal("ctx encerrado mas a rodada continua")
	}
}
