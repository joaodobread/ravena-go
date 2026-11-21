package anagram_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/bot/bottest"
	"github.com/joaodobread/ravena-go/internal/features/anagram"
	"github.com/joaodobread/ravena-go/internal/features/gamekit"
	"github.com/joaodobread/ravena-go/internal/llm"
	"github.com/joaodobread/ravena-go/internal/store"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// adminPolicy trata bottest.Admin como admin do grupo e libera o resto.
type adminPolicy struct{}

func (adminPolicy) Prefix(context.Context, *bot.Message) string { return "" }
func (adminPolicy) Admit(context.Context, *bot.Message) bool    { return true }
func (adminPolicy) Allow(context.Context, *bot.Context) error   { return nil }
func (adminPolicy) Done(context.Context, *bot.Context, error)   {}
func (adminPolicy) Roles(_ context.Context, m *bot.Message) (bool, bool) {
	return m.Sender == bottest.Admin, false
}

type fakeLLM struct {
	out string
	err error
	n   int
}

func (f *fakeLLM) Chat(context.Context, llm.ChatRequest) (string, error) {
	f.n++
	return f.out, f.err
}

type env struct {
	r     *bot.Router
	fc    *bottest.FakeClient
	sched *gamekit.FakeScheduler
	game  *anagram.Game
	ctx   context.Context
	stop  context.CancelFunc
}

func setup(t *testing.T, words []string, chat anagram.Chatter) *env {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(ctx, "anagram", anagram.Migrations()); err != nil {
		t.Fatal(err)
	}
	fc := &bottest.FakeClient{}
	sched := &gamekit.FakeScheduler{}
	g := anagram.New(ctx, anagram.Config{
		DB: st.DB(), Client: fc, LLM: chat, Schedule: sched.Schedule,
		Rand: func(int) int { return 0 }, Words: words, Log: quiet,
	})
	r := bot.NewRouter("!", fc, quiet)
	r.SetPolicy(adminPolicy{})
	if err := r.Register(g.Commands()...); err != nil {
		t.Fatal(err)
	}
	return &env{r: r, fc: fc, sched: sched, game: g, ctx: ctx, stop: cancel}
}

func (e *env) say(text string) { e.r.Handle(context.Background(), bottest.GroupMsg(text)) }

func (e *env) sayAs(user bot.Message, text string) {
	m := bottest.GroupMsg(text)
	m.Sender, m.PushName = user.Sender, user.PushName
	e.r.Handle(context.Background(), m)
}

func (e *env) last(t *testing.T) string {
	t.Helper()
	texts := e.fc.Texts()
	if len(texts) == 0 {
		t.Fatal("nenhuma resposta")
	}
	return texts[len(texts)-1]
}

func (e *env) has(t *testing.T, sub string) {
	t.Helper()
	for _, s := range e.fc.Texts() {
		if strings.Contains(s, sub) {
			return
		}
	}
	t.Fatalf("nenhuma resposta contém %q: %q", sub, e.fc.Texts())
}

func lastReaction(fc *bottest.FakeClient) string {
	if len(fc.Reactions) == 0 {
		return ""
	}
	return fc.Reactions[len(fc.Reactions)-1]
}

func TestFullGame(t *testing.T) {
	e := setup(t, []string{"gato"}, nil)
	e.say("!anagrama")
	e.has(t, "Rodada 1")
	e.has(t, "*A T O G*") // embaralhamento determinístico com rand=0
	if !strings.Contains(e.last(t), "!ana <palpite>") || !strings.Contains(e.last(t), "60 segundos") {
		t.Fatalf("mensagem inicial: %q", e.last(t))
	}

	e.say("!anagrama")
	if !strings.Contains(e.last(t), "Já existe uma partida") {
		t.Fatalf("partida duplicada: %q", e.last(t))
	}

	e.say("!ana xyzw")
	if got := lastReaction(e.fc); got != "🥶" {
		t.Fatalf("reação fria = %q", got)
	}
	e.say("!ana gata")
	if got := lastReaction(e.fc); got != "🥵" {
		t.Fatalf("reação morna = %q", got)
	}

	e.say("!ana GATO")
	if !strings.Contains(e.last(t), "Fulano acertou") || !strings.Contains(e.last(t), "Rodada 2") {
		t.Fatalf("acerto: %q", e.last(t))
	}
	// Um segundo acerto na mesma palavra é ignorado.
	n := len(e.fc.Texts())
	e.say("!ana gato")
	if len(e.fc.Texts()) != n {
		t.Fatalf("acerto repetido respondeu: %q", e.last(t))
	}

	if d, ok := e.sched.FireNext(); !ok || d != anagram.NextDelay {
		t.Fatalf("esperava o atraso da próxima rodada, veio %v %v", d, ok)
	}
	if !strings.Contains(e.last(t), "Rodada 2") {
		t.Fatalf("nova rodada: %q", e.last(t))
	}
	if d, ok := e.sched.FireNext(); !ok || d != anagram.RoundTime {
		t.Fatalf("esperava o timer da rodada, veio %v %v", d, ok)
	}
	end := e.last(t)
	for _, want := range []string{"tempo acabou", "rodada 2", "Fulano - Pontuação: *1*", "NOVO RECORDE"} {
		if !strings.Contains(end, want) {
			t.Fatalf("fim sem %q: %q", want, end)
		}
	}
	if e.game.Active() != 0 || e.sched.Pending() != 0 {
		t.Fatalf("partida não terminou: active=%d pending=%d", e.game.Active(), e.sched.Pending())
	}

	e.say("!a-rank")
	rank := e.last(t)
	if !strings.Contains(rank, "Fulano - Pontuação: *1*") || !strings.Contains(rank, "Recorde do Grupo:* 2") || !strings.Contains(rank, "Tentativas do Grupo:* 4") {
		t.Fatalf("ranking: %q", rank)
	}
}

func TestGuessWithoutGameIsIgnored(t *testing.T) {
	e := setup(t, []string{"gato"}, nil)
	e.say("!ana gato")
	if len(e.fc.Replies) != 0 {
		t.Fatalf("respondeu sem partida: %q", e.fc.Texts())
	}
}

func TestGuessNeedsArgs(t *testing.T) {
	e := setup(t, []string{"gato"}, nil)
	e.say("!anagrama")
	e.say("!ana")
	if !strings.Contains(e.last(t), "precisa fornecer um palpite") {
		t.Fatalf("got %q", e.last(t))
	}
}

func TestGroupOnly(t *testing.T) {
	e := setup(t, []string{"gato"}, nil)
	e.r.Handle(context.Background(), bottest.PrivateMsg("!anagrama"))
	if !strings.Contains(e.last(t), "só funciona em grupos") {
		t.Fatalf("got %q", e.last(t))
	}
}

func TestHintWithoutLLMRevealsLetters(t *testing.T) {
	e := setup(t, []string{"abacaxi"}, nil)
	e.say("!ana-dica")
	if !strings.Contains(e.last(t), "Não há um jogo") {
		t.Fatalf("sem jogo: %q", e.last(t))
	}
	e.say("!anagrama")
	e.say("!ana-dica")
	if got := e.last(t); !strings.HasPrefix(got, "📝 A _ _ _ _ _ _") || strings.Contains(got, "Dica:") || !strings.Contains(got, "mais 2 dica") {
		t.Fatalf("dica: %q", got)
	}
	e.say("!ana-dica")
	e.say("!ana-dica")
	if !strings.Contains(e.last(t), "mais 0 dica") {
		t.Fatalf("terceira dica: %q", e.last(t))
	}
	e.say("!ana-dica")
	if !strings.Contains(e.last(t), "dicas para esta partida já acabaram") {
		t.Fatalf("dicas esgotadas: %q", e.last(t))
	}
}

func TestHintTooRevealed(t *testing.T) {
	e := setup(t, []string{"gato"}, nil)
	e.say("!anagrama")
	e.say("!ana-dica")
	e.say("!ana-dica")
	e.say("!ana-dica")
	if !strings.Contains(e.last(t), "muito revelada") {
		t.Fatalf("got %q", e.last(t))
	}
}

func TestHintWithLLM(t *testing.T) {
	ai := &fakeLLM{out: "Felino doméstico"}
	e := setup(t, []string{"gato"}, ai)
	e.say("!anagrama")
	e.say("!ana-dica")
	if !strings.Contains(e.last(t), "*Dica:* _Felino doméstico_") {
		t.Fatalf("dica IA: %q", e.last(t))
	}

	// Dica que entrega a palavra, ou erro do LLM, não aparece.
	ai.out = "É um GATO"
	e.say("!ana-dica")
	if strings.Contains(e.last(t), "Dica:") {
		t.Fatalf("dica entregou a palavra: %q", e.last(t))
	}
	ai.err = errors.New("fora do ar")
	e.say("!ana pulo") // não atrapalha
	if ai.n != 2 {
		t.Fatalf("chamadas ao LLM = %d", ai.n)
	}
}

func TestSkip(t *testing.T) {
	e := setup(t, []string{"gato"}, nil)
	e.say("!ana-pular")
	if !strings.Contains(e.last(t), "Não há um jogo") {
		t.Fatalf("sem jogo: %q", e.last(t))
	}
	e.say("!anagrama")
	for i := 2; i >= 0; i-- {
		e.say("!ana-pular")
		if got := e.last(t); !strings.Contains(got, "foi pulada") || !strings.Contains(got, "Pulos restantes: "+string(rune('0'+i))) {
			t.Fatalf("pulo: %q", got)
		}
		// Pular de novo antes da nova palavra é ignorado.
		n := len(e.fc.Texts())
		e.say("!ana-pular")
		if len(e.fc.Texts()) != n {
			t.Fatalf("pulo duplo respondeu: %q", e.last(t))
		}
		e.sched.FireNext()
		if !strings.Contains(e.last(t), "Rodada 1") {
			t.Fatalf("pular não mantém a rodada: %q", e.last(t))
		}
	}
	e.say("!ana-pular")
	if !strings.Contains(e.last(t), "pulos para esta partida já acabaram") {
		t.Fatalf("pulos esgotados: %q", e.last(t))
	}
}

func TestRankingEmptyAndReset(t *testing.T) {
	e := setup(t, []string{"gato"}, nil)
	e.say("!anagrama-ranking")
	if !strings.Contains(e.last(t), "Ainda não há dados") {
		t.Fatalf("ranking vazio: %q", e.last(t))
	}
	e.say("!anagrama")
	e.say("!ana gato")

	e.say("!anagrama-reset")
	if !strings.Contains(e.last(t), "Só admins") {
		t.Fatalf("reset por não-admin: %q", e.last(t))
	}
	e.sayAs(bot.Message{Sender: bottest.Admin, PushName: "Chefe"}, "!anagrama-reset")
	e.has(t, "resetado por um administrador")
	if !strings.Contains(e.last(t), "foram resetados") {
		t.Fatalf("reset: %q", e.last(t))
	}
	if e.game.Active() != 0 || e.sched.Pending() != 0 {
		t.Fatal("reset não encerrou a partida")
	}
	e.say("!anagrama-ranking")
	if !strings.Contains(e.last(t), "Ainda não há dados") {
		t.Fatalf("ranking após reset: %q", e.last(t))
	}
}

func TestContextEndStopsGames(t *testing.T) {
	e := setup(t, []string{"gato"}, nil)
	e.say("!anagrama")
	if e.game.Active() != 1 {
		t.Fatal("partida não começou")
	}
	e.stop()
	deadline := time.Now().Add(time.Second)
	for e.game.Active() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if e.game.Active() != 0 || e.sched.Pending() != 0 {
		t.Fatalf("ctx encerrado mas active=%d pending=%d", e.game.Active(), e.sched.Pending())
	}
}

func TestRealSchedulerEndsRound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx, "anagram", anagram.Migrations()); err != nil {
		t.Fatal(err)
	}
	fc := &bottest.FakeClient{}
	g := anagram.New(ctx, anagram.Config{DB: st.DB(), Client: fc, Words: []string{"gato"}, RoundTime: 20 * time.Millisecond, Log: quiet})
	r := bot.NewRouter("!", fc, quiet)
	if err := r.Register(g.Commands()...); err != nil {
		t.Fatal(err)
	}
	r.Handle(ctx, bottest.GroupMsg("!anagrama"))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range fc.Texts() {
			if strings.Contains(s, "tempo acabou") {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timer real não encerrou: %q", fc.Texts())
}
