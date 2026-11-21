package sequence_test

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/bot/bottest"
	"github.com/joaodobread/ravena-go/internal/features/gamekit"
	"github.com/joaodobread/ravena-go/internal/features/sequence"
	"github.com/joaodobread/ravena-go/internal/store"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type env struct {
	r     *bot.Router
	fc    *bottest.FakeClient
	sched *gamekit.FakeScheduler
	game  *sequence.Game
	now   time.Time
	stop  context.CancelFunc
}

func setup(t *testing.T) *env {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(ctx, "sequence", sequence.Migrations()); err != nil {
		t.Fatal(err)
	}
	e := &env{fc: &bottest.FakeClient{}, sched: &gamekit.FakeScheduler{}, now: time.Unix(1_700_000_000, 0), stop: cancel}
	e.game = sequence.New(ctx, sequence.Config{
		DB: st.DB(), Client: e.fc, Schedule: e.sched.Schedule, Log: quiet,
		Rand: func(int) int { return 0 }, Now: func() time.Time { return e.now },
	})
	e.r = bot.NewRouter("!", e.fc, quiet)
	if err := e.r.Register(e.game.Commands()...); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) as(user types.JID, name, text string) {
	m := bottest.GroupMsg(text)
	m.Sender, m.PushName = user, name
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

func (e *env) lastReaction() string {
	if len(e.fc.Reactions) == 0 {
		return ""
	}
	return e.fc.Reactions[len(e.fc.Reactions)-1]
}

var other = types.NewJID("5511777777777", types.DefaultUserServer)

func TestRound(t *testing.T) {
	e := setup(t)
	e.as(bottest.User, "Fulano", "!sequencia")
	// rand=0: aritmética começando em 0 com razão 2, faltando o 3º termo.
	if got := e.last(t); !strings.Contains(got, "*0, 2, ___, 6, 8, 10, 12, 14*") || !strings.Contains(got, "30 segundos") {
		t.Fatalf("início: %q", got)
	}
	e.as(bottest.User, "Fulano", "!sequencia")
	if !strings.Contains(e.last(t), "Já existe um desafio") {
		t.Fatalf("duplicado: %q", e.last(t))
	}

	e.as(bottest.User, "Fulano", "!seq 5")
	if e.lastReaction() != "❌" {
		t.Fatalf("erro: %q", e.lastReaction())
	}
	e.now = e.now.Add(3 * time.Second) // 10% do tempo: 10 + 0.9*90 = 91
	e.as(bottest.User, "Fulano", "!seq 4")
	if e.lastReaction() != "✅" {
		t.Fatalf("acerto: %q", e.lastReaction())
	}
	e.as(bottest.User, "Fulano", "!seq 4")
	if e.lastReaction() != "👀" {
		t.Fatalf("repetido: %q", e.lastReaction())
	}
	e.now = e.now.Add(12 * time.Second) // 50%: 55
	e.as(other, "Beltrano", "!seq 4")

	if _, ok := e.sched.FireNext(); !ok {
		t.Fatal("sem timer")
	}
	end := e.last(t)
	for _, want := range []string{"resposta aceita era: *4*", "razão 2", "🥇 *Fulano* (+91 pts) em 3.0s", "🥈 *Beltrano* (+55 pts) em 15.0s"} {
		if !strings.Contains(end, want) {
			t.Fatalf("fim sem %q: %q", want, end)
		}
	}
	if e.game.Active() != 0 {
		t.Fatal("rodada não terminou")
	}

	e.as(bottest.User, "Fulano", "!seq 4")
	if !strings.Contains(e.last(t), "Nenhum desafio ativo") {
		t.Fatalf("depois do fim: %q", e.last(t))
	}

	e.as(bottest.User, "Fulano", "!sequencia-ranking")
	rank := e.last(t)
	if !strings.Contains(rank, "🥇 *Fulano*\n   └ 91 pts (1 vitórias)") || !strings.Contains(rank, "🥈 *Beltrano*") {
		t.Fatalf("ranking: %q", rank)
	}
}

func TestNobodyWins(t *testing.T) {
	e := setup(t)
	e.as(bottest.User, "Fulano", "!sequencia")
	e.sched.FireNext()
	if !strings.Contains(e.last(t), "Ninguém acertou") {
		t.Fatalf("got %q", e.last(t))
	}
}

func TestSeqWithoutArgs(t *testing.T) {
	e := setup(t)
	e.as(bottest.User, "Fulano", "!seq")
	if !strings.Contains(e.last(t), "Desafio de Lógica") {
		t.Fatalf("!seq sem jogo deveria iniciar: %q", e.last(t))
	}
	e.as(bottest.User, "Fulano", "!seq")
	if !strings.Contains(e.last(t), "precisa enviar uma resposta") {
		t.Fatalf("!seq sem resposta: %q", e.last(t))
	}
}

func TestEmptyRanking(t *testing.T) {
	e := setup(t)
	e.as(bottest.User, "Fulano", "!sequencia-ranking")
	if !strings.Contains(e.last(t), "Ainda não há ranking") {
		t.Fatalf("got %q", e.last(t))
	}
}

func TestWorksInPrivate(t *testing.T) {
	e := setup(t)
	e.r.Handle(context.Background(), bottest.PrivateMsg("!sequencia"))
	if !strings.Contains(e.last(t), "Desafio de Lógica") {
		t.Fatalf("got %q", e.last(t))
	}
}

func TestContextEndStopsRounds(t *testing.T) {
	e := setup(t)
	e.as(bottest.User, "Fulano", "!sequencia")
	e.stop()
	deadline := time.Now().Add(time.Second)
	for e.game.Active() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if e.game.Active() != 0 || e.sched.Pending() != 0 {
		t.Fatal("ctx encerrado mas a rodada continua")
	}
}

// Todos os geradores produzem desafios coerentes para qualquer sorteio.
func TestGeneratorsAreValid(t *testing.T) {
	seeds := []func(n int) int{
		func(int) int { return 0 },
		func(n int) int { return n - 1 },
		func(n int) int { return n / 2 },
	}
	for gi := range 11 {
		for si, seed := range seeds {
			first := true
			rnd := func(n int) int {
				if first { // escolhe o gerador
					first = false
					return gi
				}
				return seed(n)
			}
			p := sequence.Random(rnd)
			if p.Type == "" || p.Explanation == "" || len(p.Answers) == 0 {
				t.Fatalf("gerador %d/%d incompleto: %+v", gi, si, p)
			}
			if p.Type != "Matemática de Emojis" {
				if strings.Count(p.Sequence, "___") != 1 {
					t.Fatalf("gerador %d/%d sem lacuna única: %q", gi, si, p.Sequence)
				}
				if p.Type != "Lógica Mista" && slices.Contains(strings.Split(p.Sequence, ", "), p.Answers[0]) {
					t.Fatalf("gerador %d/%d mostra a resposta %q: %q", gi, si, p.Answers[0], p.Sequence)
				}
			}
		}
	}
}

func TestWeekdaysPuzzle(t *testing.T) {
	e := setup(t)
	// Gerador 4 (dias da semana), sentido inverso (rnd=0), começa em Sábado,
	// lacuna no 2º termo: Sábado, ___ (Sexta)...
	calls := 0
	g := sequence.Random(func(n int) int {
		calls++
		if calls == 1 {
			return 4
		}
		return 0
	})
	if g.Answers[0] != "Sexta" {
		t.Fatalf("resposta = %q (%q)", g.Answers[0], g.Sequence)
	}
	_ = e
}
