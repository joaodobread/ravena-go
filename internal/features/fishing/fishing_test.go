package fishing

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/bot/bottest"
	"github.com/joaodobread/ravena-go/internal/store"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// script devolve os valores enfileirados e, depois, 0.99 (nada acontece:
// sem lendário, sem lixo, sem item).
type script struct{ vals []float64 }

func (s *script) next() float64 {
	if len(s.vals) == 0 {
		return 0.99
	}
	v := s.vals[0]
	s.vals = s.vals[1:]
	return v
}

func (s *script) push(v ...float64) { s.vals = append(s.vals, v...) }

func repeat(v float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

// normal enfileira um peixe comum: índice (fração), faixa normal e peso (fração).
// Peso = round2(w*79 + 1).
func (s *script) normal(idx, w float64) {
	s.push(repeat(0.99, len(rareFishes))...)
	s.push(idx, 0.5, w)
}

// upgrade enfileira o item de pesca name (depois de um peixe comum).
func (s *script) upgrade(name string, value ...float64) {
	s.push(0.99) // sem lixo
	i := slices.IndexFunc(upgrades, func(it item) bool { return it.name == name })
	s.push(repeat(0.99, i)...)
	s.push(0)
	s.push(value...)
}

func (s *script) downgrade(name string, value ...float64) {
	s.push(0.99)
	s.push(repeat(0.99, len(upgrades))...)
	i := slices.IndexFunc(downgrades, func(it item) bool { return it.name == name })
	s.push(repeat(0.99, i)...)
	s.push(0)
	s.push(value...)
}

type fakeWallet struct{ got map[string]int }

func (w *fakeWallet) AddCoins(_ context.Context, user string, n int) (int, error) {
	w.got[user] += n
	return w.got[user], nil
}

type fakePolicy struct{ admin bool }

func (fakePolicy) Prefix(context.Context, *bot.Message) string { return "" }
func (fakePolicy) Admit(context.Context, *bot.Message) bool    { return true }
func (fakePolicy) Allow(context.Context, *bot.Context) error   { return nil }
func (p fakePolicy) Roles(context.Context, *bot.Message) (bool, bool) {
	return p.admin, false
}
func (fakePolicy) Done(context.Context, *bot.Context, error) {}

type env struct {
	t      *testing.T
	g      *Game
	r      *bot.Router
	client *bottest.FakeClient
	rnd    *script
	now    time.Time
}

func setup(t *testing.T, wallet Wallet) *env {
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
	e.g = New(st.DB(), wallet, e.rnd.next, func() time.Time { return e.now }, quiet)
	e.r = bot.NewRouter("!", e.client, quiet)
	if err := e.r.Register(e.g.Commands()...); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) send(text string) string {
	e.t.Helper()
	return e.sendMsg(bottest.GroupMsg(text))
}

func (e *env) sendMsg(m *bot.Message) string {
	e.t.Helper()
	before := len(e.client.Replies)
	e.r.Handle(context.Background(), m)
	if len(e.client.Replies) == before {
		return ""
	}
	return e.client.Last().Out.Text
}

func (e *env) wait(d time.Duration) { e.now = e.now.Add(d) }

func (e *env) player(user string) *player {
	e.t.Helper()
	var p *player
	err := e.g.inTx(context.Background(), func(tx *sql.Tx) error {
		var err error
		p, err = e.g.load(context.Background(), tx, user, false)
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

func (e *env) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.g.db.QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func mustContain(t *testing.T, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Fatalf("resposta sem %q:\n%s", w, got)
		}
	}
}

var user = bottest.User.User

func TestCatchFlow(t *testing.T) {
	e := setup(t, nil)
	e.rnd.normal(0, 0.5)
	out := e.send("!pescar")
	first := fishNames()[0]
	mustContain(t, out, "Fulano pescou um *"+first+"* de _40.50 kg_!", "Seu maior peixe: "+first+" (40.50 kg)", "Iscas restantes: 4/5")

	p := e.player(user)
	if len(p.fishes) != 1 || p.catches != 1 || p.baitsUsed != 1 || p.invWeight != 40.5 {
		t.Fatalf("jogador = %+v", p)
	}
	if n := e.count(`SELECT total_catches FROM fishing_group_stats WHERE group_id = ? AND user_id = ?`, bottest.Group.String(), user); n != 1 {
		t.Fatalf("group catches = %d", n)
	}
	if got := e.client.Reactions; !slices.Equal(got, []string{"🎣", "🐟"}) {
		t.Fatalf("reações = %v", got)
	}
}

func TestPrivateCatchDoesNotTouchGroupStats(t *testing.T) {
	e := setup(t, nil)
	e.sendMsg(bottest.PrivateMsg("!pesca"))
	if n := e.count(`SELECT COUNT(*) FROM fishing_group_stats`); n != 0 {
		t.Fatalf("group stats = %d", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM fishing_inventory`); n != 1 {
		t.Fatalf("inventário = %d", n)
	}
}

func TestCooldown(t *testing.T) {
	e := setup(t, nil)
	e.send("!pescar")
	e.wait(4 * time.Second)
	if out := e.send("!pescar"); out != "" {
		t.Fatalf("pescou no cooldown: %q", out)
	}
	if last := e.client.Reactions[len(e.client.Reactions)-1]; last != "😴" {
		t.Fatalf("reação = %q", last)
	}
	if p := e.player(user); p.baits != 4 {
		t.Fatalf("gastou isca no cooldown: %d", p.baits)
	}
	e.wait(time.Second)
	if out := e.send("!pescar"); out == "" {
		t.Fatal("cooldown não liberou")
	}
}

func TestLongerCooldownDebuff(t *testing.T) {
	e := setup(t, nil)
	e.rnd.normal(0, 0.5)
	e.rnd.downgrade("Fiscalização Ambiental")
	mustContain(t, e.send("!pescar"), "Fiscalização Ambiental", "Aumenta o tempo de espera.")
	// O debuff vale a partir da próxima: esta ainda tem 5s.
	e.wait(5 * time.Second)
	if e.send("!pescar") == "" {
		t.Fatal("primeira pescaria após o debuff deveria liberar em 5s")
	}
	e.wait(14 * time.Second)
	if e.send("!pescar") != "" {
		t.Fatal("cooldown triplicado não foi aplicado")
	}
	e.wait(time.Second)
	if e.send("!pescar") == "" {
		t.Fatal("cooldown de 15s não liberou")
	}
	if d := e.player(user).debuffs; len(d) != 1 || d[0].uses != 1 {
		t.Fatalf("debuffs = %+v", d)
	}
}

func TestBaitsRunOutAndRegenerate(t *testing.T) {
	e := setup(t, nil)
	for range MaxBaits {
		if e.send("!pescar") == "" {
			t.Fatal("deveria pescar")
		}
		e.wait(6 * time.Second)
	}
	if out := e.send("!pescar"); out != "" {
		t.Fatalf("pescou sem isca: %q", out)
	}
	if last := e.client.Reactions[len(e.client.Reactions)-1]; last != "🍥" {
		t.Fatalf("reação = %q", last)
	}
	mustContain(t, e.send("!meus-pescados"), "Iscas:* 0/5", "Próxima em")
	e.wait(BaitRegen)
	mustContain(t, e.send("!pescar"), "Iscas restantes: 0/5")
}

func TestTrashConsumesBaitUnlessProtected(t *testing.T) {
	e := setup(t, nil)
	e.rnd.normal(0, 0.5)
	e.rnd.push(0.1, 0) // lixo: Bota velha
	out := e.send("!pescar")
	mustContain(t, out, "jogou a linha e...", "Bota velha. Que pena!", "Iscas restantes: 4/5")
	p := e.player(user)
	if p.junk != 1 || len(p.fishes) != 0 || p.catches != 0 {
		t.Fatalf("jogador = %+v", p)
	}

	// Ganha o Anzol de Titânio (10 usos) e pesca lixo: não perde isca.
	e.wait(6 * time.Second)
	e.rnd.normal(0, 0.5)
	e.rnd.upgrade("Anzol de Titânio")
	e.send("!pescar") // 3 iscas
	e.wait(6 * time.Second)
	e.rnd.normal(0, 0.5)
	e.rnd.push(0.1, 0)
	mustContain(t, e.send("!pescar"), "mas seu Anzol de Titânio te salvou", "Iscas restantes: 3/5")
	if b := e.player(user).buffs; len(b) != 1 || b[0].uses != 9 {
		t.Fatalf("buffs = %+v", b)
	}
}

func TestWeightBuffsApplyNextAndExpire(t *testing.T) {
	e := setup(t, nil)
	e.rnd.normal(0, 0.5)
	e.rnd.upgrade("Balança Adulterada")
	mustContain(t, e.send("!pescar"), "_40.50 kg_", "Balança Adulterada")

	e.wait(6 * time.Second)
	e.rnd.normal(0, 0.5)
	mustContain(t, e.send("!pescar"), "_101.25 kg_", "+150% de peso (40.5kg → 101.25kg)")
	if b := e.player(user).buffs; len(b) != 0 {
		t.Fatalf("buff deveria ter acabado: %+v", b)
	}

	e.wait(6 * time.Second)
	e.rnd.normal(0, 0.5)
	mustContain(t, e.send("!pescar"), "_40.50 kg_")
}

func TestGuaranteedWeightAndBonus(t *testing.T) {
	e := setup(t, nil)
	e.rnd.normal(0, 0.5)
	e.rnd.upgrade("Sonar Portátil")
	e.send("!pescar")

	// Com o sonar, o peso sai direto da faixa 40–90 (sem sorteio de faixa).
	e.wait(6 * time.Second)
	e.rnd.push(repeat(0.99, len(rareFishes))...)
	e.rnd.push(0, 0.5)              // 40 + 0.5*50 = 65
	e.rnd.upgrade("Minhocão", 0.99) // +30kg na próxima
	mustContain(t, e.send("!pescar"), "_65.00 kg_", "Minhocão")
	if b := e.player(user).buffs; len(b) != 1 || b[0].effect != effNextFishBonus || b[0].value != 30 {
		t.Fatalf("buffs = %+v", b)
	}

	e.wait(6 * time.Second)
	e.rnd.normal(0, 0.5)
	mustContain(t, e.send("!pescar"), "_70.50 kg_", "+30kg (40.5kg → 70.5kg)")
	if b := e.player(user).buffs; len(b) != 0 {
		t.Fatalf("buffs = %+v", b)
	}
}

func TestLegendaryAndMonthlyLimit(t *testing.T) {
	e := setup(t, nil)
	e.rnd.push(0, 0.5) // Cthulhu, base 15.5
	out := e.send("!pescar")
	mustContain(t, out, "INCRÍVEL!", "*Cthulhu* de _26680.50 kg_", "UM MONSTRO!")
	if n := e.count(`SELECT COUNT(*) FROM fishing_legendary_history WHERE fish_name = 'Cthulhu' AND year = 2026`); n != 1 {
		t.Fatalf("histórico = %d", n)
	}
	e.wait(6 * time.Second)
	e.rnd.push(0) // sorteou Cthulhu de novo, mas o limite do mês acabou
	if out := e.send("!pescar"); strings.Contains(out, "INCRÍVEL") || e.count(`SELECT COUNT(*) FROM fishing_legendary_history`) != 1 {
		t.Fatalf("passou do limite mensal: %s", out)
	}
	mustContain(t, e.send("!pesca-lendas"), "*Cthulhu* (26680.50 kg)", "Pescador: Fulano")
	mustContain(t, e.send("!pesca-lendas 2020"), "Ainda não foram pescados peixes lendários em 2020")
}

func TestInventoryLimit(t *testing.T) {
	e := setup(t, nil)
	if err := e.g.AddBaits(context.Background(), user, 20); err != nil {
		t.Fatal(err)
	}
	var out string
	for i := range MaxFishPerUser + 1 {
		e.rnd.normal(0, float64(i+1)/20)
		out = e.send("!pescar")
		e.wait(6 * time.Second)
	}
	mustContain(t, out, "Inventário cheio!")
	p := e.player(user)
	if len(p.fishes) != MaxFishPerUser {
		t.Fatalf("inventário = %d", len(p.fishes))
	}
	if !slices.ContainsFunc(p.fishes, func(f fish) bool { return f.weight == round2(11.0/20*79+1) }) {
		t.Fatal("o peixe recém-pescado não pode ser solto")
	}
}

func TestStolenFishIsNotKept(t *testing.T) {
	e := setup(t, nil)
	e.rnd.normal(0, 0.5)
	e.rnd.downgrade("Gato Ladrão")
	mustContain(t, e.send("!pescar"), "jogou a linha e...", "Maldito Gato Ladrão!")
	if p := e.player(user); len(p.fishes) != 0 || p.catches != 0 || p.baits != 4 {
		t.Fatalf("jogador = %+v", p)
	}
}

func TestRemoveBaitsAndClearInventory(t *testing.T) {
	e := setup(t, nil)
	e.send("!pescar")
	e.wait(6 * time.Second)
	e.rnd.normal(0, 0.5)
	e.rnd.downgrade("Mina Aquática")
	mustContain(t, e.send("!pescar"), "inventário de peixes foi destruído")
	if p := e.player(user); len(p.fishes) != 1 { // só o peixe pescado depois da mina
		t.Fatalf("inventário = %d", len(p.fishes))
	}
	e.wait(6 * time.Second)
	e.rnd.normal(0, 0.5)
	e.rnd.downgrade("Balde Furado", 0.99) // 4 iscas
	mustContain(t, e.send("!pescar"), "comeu 3 de suas iscas! (0/5", "Iscas restantes: 0/5")
}

func TestWalletIntegration(t *testing.T) {
	w := &fakeWallet{got: map[string]int{}}
	e := setup(t, w)
	e.rnd.normal(0, 0.5)
	e.rnd.upgrade("Ficha de Cassino", 0.99) // 5 moedinhas
	mustContain(t, e.send("!pescar"), "+5 moedinhas")
	if w.got[user] != 5 {
		t.Fatalf("wallet = %v", w.got)
	}

	// Sem wallet, a Ficha de Cassino não sai.
	e2 := setup(t, nil)
	e2.rnd.normal(0, 0.5)
	e2.rnd.upgrade("Ficha de Cassino", 0.99)
	if out := e2.send("!pescar"); strings.Contains(out, "moedinhas") {
		t.Fatalf("sem wallet não deveria dar moedinhas: %s", out)
	}
}

func TestEquipmentRaisesLimits(t *testing.T) {
	e := setup(t, nil)
	e.rnd.normal(0, 0.5)
	e.rnd.upgrade("Caixa de Iscas")
	mustContain(t, e.send("!pescar"), "Limite de iscas aumentado: +2", "Iscas restantes: 4/7")
	mustContain(t, e.send("!pesca-ficha"), "🧳 *Equipamentos:*", "Caixa de Iscas (+2 iscas)", "Inventário:* 1/10")
}

func TestTackleAPI(t *testing.T) {
	w := &fakeWallet{got: map[string]int{}}
	e := setup(t, w)
	ctx := context.Background()
	if err := e.g.AddBaits(ctx, "123", 3); err != nil {
		t.Fatal(err)
	}
	if p := e.player("123"); p.baits != 8 {
		t.Fatalf("iscas = %d", p.baits)
	}
	e.rnd.push(0) // primeiro item da lista (menor chance)
	label, err := e.g.GiveRandomUpgrade(ctx, "123")
	if err != nil {
		t.Fatal(err)
	}
	if want := upgrades[0].emoji + " " + upgrades[0].name; label != want {
		t.Fatalf("label = %q, want %q", label, want)
	}
	if b := e.player("123").buffs; len(b) != 1 || b[0].name != upgrades[0].name {
		t.Fatalf("buffs = %+v", b)
	}
}

func TestRanking(t *testing.T) {
	e := setup(t, nil)
	e.rnd.normal(0, 0.1)
	e.send("!pescar")
	other := bottest.GroupMsg("!pescar")
	other.Sender, other.PushName = bottest.Admin, "Beltrano"
	e.rnd.normal(0, 0.9)
	e.sendMsg(other)

	out := e.send("!pesca-ranking")
	if strings.Index(out, "Beltrano") > strings.Index(out, "Fulano") {
		t.Fatalf("ordem errada:\n%s", out)
	}
	mustContain(t, out, "(Maior Peixe)", "🥇 Beltrano")
	e.wait(6 * time.Second)
	mustContain(t, e.send("!pescados quantidade"), "(Quantidade Total)", "1 peixes")

	mustContain(t, e.sendMsg(bottest.PrivateMsg("!pesca-ranking")), "só funciona em grupos")
}

func TestResetPermissions(t *testing.T) {
	e := setup(t, nil)
	e.send("!pescar")
	mustContain(t, e.send("!pesca-reset"), "Só admins")
	if n := e.count(`SELECT COUNT(*) FROM fishing_group_stats`); n != 1 {
		t.Fatal("não-admin resetou")
	}
	e.r.SetPolicy(fakePolicy{admin: true})
	mustContain(t, e.send("!pesca-reset"), "1 jogadores")
	if n := e.count(`SELECT COUNT(*) FROM fishing_group_stats`); n != 0 {
		t.Fatal("reset não apagou")
	}
	e.wait(11 * time.Second)
	mustContain(t, e.send("!pesca-reset"), "Não há dados")
	// O inventário pessoal continua.
	if n := e.count(`SELECT COUNT(*) FROM fishing_inventory`); n != 1 {
		t.Fatal("reset apagou o inventário")
	}
}

func TestSheetAndInfoCommands(t *testing.T) {
	e := setup(t, nil)
	mustContain(t, e.send("!meus-pescados"), "use !pescar para começar")
	e.rnd.normal(0, 0.5)
	e.send("!pescar")
	e.wait(6 * time.Second)
	mustContain(t, e.send("!pesca-dados"), "Ficha do Pescador: Fulano", "1. "+fishNames()[0]+": 40.50 kg", "Peso Total:* 40.50 kg")
	mustContain(t, e.send("!pesca-info"), "Iscas Máximas:* `5`", "Cthulhu", "Total de Peixes Pescados:* 1", "Pescador Mais Dedicado:* _Fulano_")
	mustContain(t, e.send("!pesca-peixes"), fishNames()[0]+" (1)", "Peixes Raríssimos")
}

func TestFishNamesEmbedded(t *testing.T) {
	if n := len(fishNames()); n < 400 {
		t.Fatalf("peixes = %d", n)
	}
	for _, n := range fishNames() {
		if strings.TrimSpace(n) != n || n == "" {
			t.Fatalf("nome inválido %q", n)
		}
	}
}
