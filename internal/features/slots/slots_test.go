package slots

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

type script struct{ vals []float64 }

func (s *script) next() float64 {
	if len(s.vals) == 0 {
		return 0.5
	}
	v := s.vals[0]
	s.vals = s.vals[1:]
	return v
}

func (s *script) push(v ...float64) { s.vals = append(s.vals, v...) }

type fakeTackle struct {
	baits    map[string]int
	upgrades []string
}

func (f *fakeTackle) AddBaits(_ context.Context, user string, n int) error {
	f.baits[user] += n
	return nil
}

func (f *fakeTackle) GiveRandomUpgrade(_ context.Context, user string) (string, error) {
	f.upgrades = append(f.upgrades, user)
	return "📡 Sonar Portátil", nil
}

type env struct {
	t      *testing.T
	g      *Game
	r      *bot.Router
	client *bottest.FakeClient
	rnd    *script
	now    time.Time
}

func setup(t *testing.T, tackle Tackle) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := Migrate(ctx, st); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, rnd: &script{}, now: time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)}
	e.client = &bottest.FakeClient{}
	e.g = New(st.DB(), tackle, e.rnd.next, func() time.Time { return e.now })
	e.r = bot.NewRouter("!", e.client, quiet)
	if err := e.r.Register(e.g.Commands()...); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) send(m *bot.Message) string {
	e.t.Helper()
	before := len(e.client.Replies)
	e.r.Handle(context.Background(), m)
	if len(e.client.Replies) == before {
		return ""
	}
	return e.client.Last().Out.Text
}

func mustContain(t *testing.T, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Fatalf("resposta sem %q:\n%s", w, got)
		}
	}
}

var userID = bottest.User.User

func TestLose(t *testing.T) {
	e := setup(t, nil)
	e.rnd.push(0, 0.15, 0.25, 0)
	out := e.send(bottest.GroupMsg("!slots"))
	mustContain(t, out, "[ 🍎 ] [ 🍋 ] [ 🍒 ]", "❌ "+loseMessages[0], "Moedinhas: 4/5")
	var plays, wins int
	if err := e.g.db.QueryRow(`SELECT total_plays, total_wins FROM slots_group_stats WHERE user_id = ?`, userID).Scan(&plays, &wins); err != nil {
		t.Fatal(err)
	}
	if plays != 1 || wins != 0 {
		t.Fatalf("stats = %d/%d", plays, wins)
	}
}

func TestWinJunkPrize(t *testing.T) {
	e := setup(t, nil)
	e.rnd.push(0.95, 0.95, 0.95, 0.1, 0)
	mustContain(t, e.send(bottest.GroupMsg("!slots")), "[ 💎 ] [ 💎 ] [ 💎 ]", "Você ganhou: *Meia furada*!")
	mustContain(t, e.send(bottest.GroupMsg("!slots-premios")), "🗑️ *Meia furada* - _10/03/2026, 09:00:00_")
}

func TestWinCoinsCappedAtMax(t *testing.T) {
	e := setup(t, nil)
	e.rnd.push(0, 0, 0, 0.5, 0.1, 0.99)
	mustContain(t, e.send(bottest.GroupMsg("!slots")), "*5 Moedinhas*", "Moedinhas: 5/5")
}

func TestWithoutTackleFishingPrizesBecomeCoins(t *testing.T) {
	e := setup(t, nil)
	e.rnd.push(0, 0, 0, 0.5, 0.9, 0)
	mustContain(t, e.send(bottest.GroupMsg("!slots")), "*1 Moedinhas*")
}

func TestWinFishingPrizes(t *testing.T) {
	tk := &fakeTackle{baits: map[string]int{}}
	e := setup(t, tk)
	e.rnd.push(0, 0, 0, 0.5, 0.5, 0.99)
	mustContain(t, e.send(bottest.GroupMsg("!slots")), "*10 Iscas de Pesca*")
	if tk.baits[userID] != 10 {
		t.Fatalf("iscas = %v", tk.baits)
	}
	e.rnd.push(0, 0, 0, 0.5, 0.9)
	mustContain(t, e.send(bottest.GroupMsg("!slots")), "*📡 Sonar Portátil*")
	if len(tk.upgrades) != 1 || tk.upgrades[0] != userID {
		t.Fatalf("upgrades = %v", tk.upgrades)
	}
	e.now = e.now.Add(time.Minute)
	mustContain(t, e.send(bottest.GroupMsg("!slots-premios")), "🌟 *📡 Sonar Portátil*", "🌟 *10 Iscas*")
}

func TestCoinsRunOutAndRegenerate(t *testing.T) {
	e := setup(t, nil)
	for range MaxCoins {
		e.rnd.push(0, 0.15, 0.25)
		e.send(bottest.GroupMsg("!slots"))
	}
	mustContain(t, e.send(bottest.GroupMsg("!slots")), "não tem moedinhas", "Próxima moedinha em: *2m 0s*")
	e.now = e.now.Add(90 * time.Second)
	mustContain(t, e.send(bottest.GroupMsg("!slots")), "*0m 30s*")
	e.now = e.now.Add(30 * time.Second)
	e.rnd.push(0, 0.15, 0.25)
	mustContain(t, e.send(bottest.GroupMsg("!slots")), "Moedinhas: 0/5")
}

func TestWallet(t *testing.T) {
	e := setup(t, nil)
	ctx := context.Background()
	if n, err := e.g.Coins(ctx, "42"); err != nil || n != MaxCoins {
		t.Fatalf("Coins = %d, %v", n, err)
	}
	if n, _ := e.g.AddCoins(ctx, "42", 3); n != MaxCoins {
		t.Fatalf("passou do teto: %d", n)
	}
	for range 4 {
		e.rnd.push(0, 0.15, 0.25)
		m := bottest.GroupMsg("!slots")
		m.Sender.User = "42"
		e.send(m)
	}
	if n, _ := e.g.AddCoins(ctx, "42", 2); n != 3 {
		t.Fatalf("AddCoins = %d, want 3", n)
	}
	var m Module
	if _, err := m.AddCoins(ctx, "42", 1); err == nil {
		t.Fatal("Module sem Register deveria falhar")
	}
}

func TestRanking(t *testing.T) {
	e := setup(t, nil)
	mustContain(t, e.send(bottest.GroupMsg("!slots-ranking")), "Ainda não há dados")
	e.client.Names = nil
	e.rnd.push(0, 0, 0, 0.1, 0)
	e.send(bottest.GroupMsg("!slots"))
	other := bottest.GroupMsg("!slots")
	other.Sender, other.PushName = bottest.Admin, "Beltrano"
	e.rnd.push(0, 0.15, 0.25)
	e.send(other)
	e.rnd.push(0, 0.15, 0.25)
	e.send(other)

	e.now = e.now.Add(11 * time.Second)
	out := e.send(bottest.GroupMsg("!slots-ranking"))
	mustContain(t, out, "Top Vencedores", "🥇 1°: 1 - Fulano", "🥇 2 perdas - Beltrano")
	mustContain(t, e.send(bottest.PrivateMsg("!slots-ranking")), "só funciona em grupos")
}

func TestPrizesEmpty(t *testing.T) {
	e := setup(t, nil)
	mustContain(t, e.send(bottest.PrivateMsg("!slots-premios")), "ainda não ganhou nenhum prêmio")
}

func TestUserKeyPrefersPhone(t *testing.T) {
	m := bottest.GroupMsg("")
	m.Sender.Server, m.Sender.User = "lid", "999"
	m.SenderAlt = bottest.User
	if got := UserKey(m); got != userID {
		t.Fatalf("UserKey = %q", got)
	}
}
