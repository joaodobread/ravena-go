package fun

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/bot/bottest"
	"github.com/joaodobread/ravena-go/internal/store"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// testPolicy resolve admins pelo FakeClient e libera todo o resto.
type testPolicy struct{ fc *bottest.FakeClient }

func (testPolicy) Prefix(context.Context, *bot.Message) string { return "" }
func (testPolicy) Admit(context.Context, *bot.Message) bool    { return true }
func (testPolicy) Allow(context.Context, *bot.Context) error   { return nil }
func (testPolicy) Done(context.Context, *bot.Context, error)   {}
func (p testPolicy) Roles(ctx context.Context, m *bot.Message) (bool, bool) {
	if !m.IsGroup {
		return false, false
	}
	g, _ := p.fc.GroupInfo(ctx, m.Chat)
	return g.IsAdmin(m.Sender), false
}

var other = types.NewJID("5511777777777", types.DefaultUserServer)

type env struct {
	f   *Feature
	r   *bot.Router
	fc  *bottest.FakeClient
	now time.Time
}

func setup(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	fc := &bottest.FakeClient{
		Names: map[types.JID]string{bottest.Group: "Grupo Teste"},
		Groups: map[types.JID]*bot.GroupInfo{bottest.Group: {
			JID: bottest.Group, Name: "Grupo Teste",
			Participants: []bot.Participant{
				{JID: bottest.User},
				{JID: bottest.Admin, IsAdmin: true},
				{JID: bottest.Bot},
			},
		}},
	}
	f, err := New(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{f: f, fc: fc, now: time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC)}
	f.now = func() time.Time { return e.now }
	f.rand = func(n int) int { return 0 }
	r := bot.NewRouter("!", fc, quiet)
	r.SetPolicy(testPolicy{fc})
	if err := r.Register(f.Commands()...); err != nil {
		t.Fatal(err)
	}
	e.r = r
	return e
}

// fixed faz o sorteio devolver sempre v (limitado a n-1).
func fixed(v int) func(int) int { return func(n int) int { return min(v, n-1) } }

func (e *env) send(m *bot.Message) string {
	before := len(e.fc.Replies)
	e.r.Handle(context.Background(), m)
	if len(e.fc.Replies) == before {
		return ""
	}
	return e.fc.Last().Out.Text
}

func from(sender types.JID, name, text string) *bot.Message {
	m := bottest.GroupMsg(text)
	m.Sender, m.PushName = sender, name
	return m
}

func TestDice(t *testing.T) {
	e := setup(t)
	cases := []struct {
		name string
		rand int
		text string
		want string
	}{
		{"crítico", 19, "!d20", "Fulano rolou:\n✨ *20* (Sucesso Crítico!) (d20)"},
		{"falha", 0, "!d20", "Fulano rolou:\n💀 *1* (Falha Crítica!) (d20)"},
		{"simples", 3, "!d6", "Fulano rolou:\n🎲 *4* (d6)"},
		{"vários com mod", 0, "!d6 2 +3", "Fulano rolou:\n🎲 *5* [1 + 1 + 3] (2d6+3)"},
		{"um com mod", 4, "!d8 -2", "Fulano rolou:\n🎲 *3* [5 -2] (d8-2)"},
		{"roll padrão", 9, "!roll", "Fulano rolou:\n🎲 *10* (d20)"},
		{"roll fórmula", 1, "!roll 3d4-1", "Fulano rolou:\n🎲 *5* [2 + 2 + 2 - 1] (3d4-1)"},
		{"roll com espaço", 1, "!roll 1d10 + 2", "Fulano rolou:\n🎲 *4* [2 +2] (d10+2)"},
		{"roll inválido", 0, "!roll banana", MsgDiceFormat},
		{"d100", 99, "!d100", "Fulano rolou:\n🎲 *100* (d100)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e.f.rand = fixed(tc.rand)
			if got := e.send(bottest.GroupMsg(tc.text)); got != tc.want {
				t.Fatalf("got %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestDiceLimits(t *testing.T) {
	e := setup(t)
	got := e.send(bottest.GroupMsg("!roll 50d5000"))
	if !strings.HasPrefix(got, "⚠️ Número máximo de dados é 20.\n⚠️ Número máximo de faces é 1000.") ||
		!strings.Contains(got, "(20d1000)") {
		t.Fatalf("got %q", got)
	}
	got = e.send(bottest.GroupMsg("!roll 99999999999999999999999d6"))
	if !strings.Contains(got, "máximo de dados") || !strings.Contains(got, "(20d6)") {
		t.Fatalf("got %q", got)
	}
	if got := e.send(bottest.GroupMsg("!roll d1")); !strings.Contains(got, "(d2)") {
		t.Fatalf("d1 deveria virar d2: %q", got)
	}
}

func TestPhrasesEmbedded(t *testing.T) {
	for _, p := range phraseCmds {
		if len(phrases()[p.list]) == 0 {
			t.Errorf("lista %q vazia", p.list)
		}
	}
}

func TestPhraseCommands(t *testing.T) {
	e := setup(t)
	for _, p := range phraseCmds {
		for _, name := range append([]string{p.name}, p.aliases...) {
			for _, v := range []int{0, 1, 1000} {
				e.f.rand = fixed(v)
				got := e.send(bottest.GroupMsg("!" + name))
				if got == "" || strings.Contains(got, "{") {
					t.Fatalf("!%s (rand %d): %q", name, v, got)
				}
			}
		}
	}
}

func TestMentionPicksMentionedThenQuotedThenMember(t *testing.T) {
	e := setup(t)
	// Marcado na mensagem.
	m := bottest.GroupMsg("!morreu @5511777777777")
	m.Mentions = []types.JID{other}
	e.send(m)
	last := e.fc.Last().Out
	if !strings.Contains(last.Text, "@5511777777777") || len(last.Mentions) != 1 || last.Mentions[0] != other {
		t.Fatalf("menção: %+v", last)
	}

	// Mensagem citada.
	m = bottest.GroupMsg("!morreu")
	m.Quoted = &bot.Message{ID: "q", Sender: bottest.Admin}
	e.send(m)
	if last = e.fc.Last().Out; !strings.Contains(last.Text, bot.MentionText(bottest.Admin)) {
		t.Fatalf("citada: %q", last.Text)
	}

	// Membro aleatório, nunca o bot.
	for v := range 3 {
		e.f.rand = fixed(v)
		e.send(bottest.GroupMsg("!morreu"))
		last = e.fc.Last().Out
		if strings.Contains(last.Text, bot.MentionText(bottest.Bot)) || len(last.Mentions) != 1 {
			t.Fatalf("aleatório %d: %+v", v, last)
		}
	}
}

func TestMentionDistinctPeopleInSameText(t *testing.T) {
	e := setup(t)
	c := &bot.Context{Message: bottest.GroupMsg("x"), Client: e.fc}
	out := e.f.render(context.Background(), c, "{mention} e {mention} e {mention}")
	if len(out.Mentions) != 2 {
		t.Fatalf("esperava 2 pessoas distintas (sem o bot): %+v", out)
	}
	if out.Mentions[0] == out.Mentions[1] {
		t.Fatalf("repetiu: %+v", out)
	}
}

func TestMentionPrivateFallback(t *testing.T) {
	e := setup(t)
	got := e.send(bottest.PrivateMsg("!boleto"))
	if !strings.Contains(got, "*Usuário*") {
		t.Fatalf("got %q", got)
	}
}

func TestRenderVariables(t *testing.T) {
	e := setup(t)
	e.f.rand = fixed(0)
	c := &bot.Context{Message: bottest.GroupMsg("x"), Client: e.fc}
	out := e.f.render(context.Background(), c, "{nomeAutor} {rndDadoRange-10-99} {genshinWeapon3Emoji} {desconhecida}")
	want := "Fulano 10 " + phrases()["genshinWeapon3Emoji"][0] + " {desconhecida}"
	if out.Text != want {
		t.Fatalf("got %q want %q", out.Text, want)
	}
}

func TestCantadaAndPresente(t *testing.T) {
	e := setup(t)
	m := bottest.GroupMsg("!cantada")
	m.Mentions = []types.JID{other}
	got := e.send(m)
	if !strings.HasPrefix(got, "💕 *Fulano* chegou em *@5511777777777* com: \n") {
		t.Fatalf("cantada: %q", got)
	}
	m = bottest.GroupMsg("!presente")
	m.Mentions = []types.JID{other}
	got = e.send(m)
	if !strings.HasPrefix(got, "*Fulano* deu _") || !strings.HasSuffix(got, "_ para *@5511777777777*! 🎁") {
		t.Fatalf("presente: %q", got)
	}
	if got := e.send(bottest.GroupMsg("!biscoito")); !strings.HasPrefix(got, "🥠 ") {
		t.Fatalf("biscoito: %q", got)
	}
}

func TestPix(t *testing.T) {
	e := setup(t)
	m := bottest.GroupMsg("!pix @5511888888888 100")
	m.Mentions = []types.JID{bottest.Admin}
	got := e.send(m)
	out := e.fc.Last().Out
	if !strings.Contains(got, "R$100,00") || !strings.Contains(got, "Nome: @5511888888888") ||
		len(out.Mentions) != 1 || !strings.Contains(got, "21/11/2026 às 12:00") {
		t.Fatalf("pix com menção: %q %+v", got, out.Mentions)
	}

	got = e.send(bottest.GroupMsg("!pix João da Silva 12,5"))
	if !strings.Contains(got, "R$12,50") || !strings.Contains(got, "Nome: João da Silva") || !strings.Contains(got, "Nome: Fulano") {
		t.Fatalf("pix com nome: %q", got)
	}

	got = e.send(bottest.GroupMsg("!pix"))
	if !strings.Contains(got, "R$50,00") || !strings.Contains(got, "Nome: Fulano") {
		t.Fatalf("pix padrão: %q", got)
	}
	if got := e.send(bottest.GroupMsg("!pix Inf")); !strings.Contains(got, "R$50,00") {
		t.Fatalf("Inf não é valor: %q", got)
	}
}

func TestCPFValid(t *testing.T) {
	e := setup(t)
	seq := 0
	e.f.rand = func(n int) int { seq++; return seq % n }
	cpf := strings.NewReplacer(".", "", "-", "").Replace(e.f.cpf())
	if len(cpf) != 11 {
		t.Fatalf("cpf %q", cpf)
	}
	if d1 := cpfDigit(cpf[:9]); byte('0'+d1) != cpf[9] {
		t.Fatalf("dígito 1 inválido em %q", cpf)
	}
	if d2 := cpfDigit(cpf[:10]); byte('0'+d2) != cpf[10] {
		t.Fatalf("dígito 2 inválido em %q", cpf)
	}
}

func TestPintoScoreAndComment(t *testing.T) {
	if s := pintoScore(maxFlaccid, maxErect, maxGirth, 0); s != 1000 {
		t.Fatalf("score máximo = %d", s)
	}
	if s := pintoScore(minFlaccid, minErect, minGirth, 30); s != 0 {
		t.Fatalf("score mínimo = %d", s)
	}
	if !strings.Contains(pintoComment(1000), "OLIMPO") || !strings.Contains(pintoComment(-5), "MICROSCÓPICO") {
		t.Fatal("comentários errados")
	}
}

func TestPinto(t *testing.T) {
	e := setup(t)
	e.f.rand = func(n int) int { return n - 1 } // tudo no máximo
	got := e.send(bottest.GroupMsg("!pinto"))
	for _, want := range []string{"*Fulano*", "Flácido:* 12.0 cm", "Ereto:* 25.0 cm", "Circunferência:* 18.0 cm",
		"30.0°, torto para direita", "_900 pontos_", "IMPRESSIONANTE", "daqui a 3 dias"} {
		if !strings.Contains(got, want) {
			t.Fatalf("falta %q em %q", want, got)
		}
	}

	// Cooldown de 3 dias, mesmo vindo pela identidade alternativa.
	e.now = e.now.Add(24 * time.Hour)
	m := bottest.GroupMsg("!pinto")
	m.Sender, m.SenderAlt = types.NewJID("123456", types.HiddenUserServer), bottest.User
	got = e.send(m)
	if !strings.HasPrefix(got, "⌛️ Fulano, você já realizou") || !strings.Contains(got, "em 2 dia(s), dia 24/11/2026, 12:00") {
		t.Fatalf("cooldown: %q", got)
	}

	e.now = e.now.Add(2*24*time.Hour + time.Minute)
	e.f.rand = fixed(0)
	if got = e.send(bottest.GroupMsg("!pinto")); !strings.Contains(got, "Flácido:* 3.0 cm") {
		t.Fatalf("após cooldown: %q", got)
	}

	if got := e.send(bottest.PrivateMsg("!pinto")); got != "Esse comando só funciona em grupos." {
		t.Fatalf("privado: %q", got)
	}
}

func TestPintoRankingAndReset(t *testing.T) {
	e := setup(t)
	if got := e.send(bottest.GroupMsg("!pinto-ranking")); !strings.Contains(got, "Ainda não há dados") {
		t.Fatalf("vazio: %q", got)
	}
	ctx := context.Background()
	chat := bottest.Group.String()
	for i := range 12 {
		_, err := e.f.db.ExecContext(ctx, `INSERT INTO fun_pinto VALUES(?, ?, ?, 1, 1, 1, 0, ?, 0)`,
			chat, "u"+string(rune('a'+i)), "P"+string(rune('a'+i)), 900-i)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.f.db.ExecContext(ctx, `INSERT INTO fun_pinto VALUES(?, ?, 'Fulano', 1, 1, 1, 0, 5, 0)`, chat, bottest.User.User); err != nil {
		t.Fatal(err)
	}
	got := e.send(bottest.GroupMsg("!pinto-ranking"))
	if !strings.HasPrefix(got, "🍆 *Ranking do Tamanho - Grupo Teste*\n\n🥇 Pa: 900 pontos\n🥈 Pb: 899 pontos\n🥉 Pc: 898 pontos\n4. Pd") ||
		!strings.HasSuffix(got, "...\n\n13. Você: 5 pontos") {
		t.Fatalf("ranking: %q", got)
	}

	if got := e.send(bottest.GroupMsg("!pinto-reset")); got != "Só admins do grupo podem usar esse comando." {
		t.Fatalf("não admin: %q", got)
	}
	got = e.send(from(bottest.Admin, "Admin", "!pinto-reset"))
	if !strings.Contains(got, "removidos dados de 13 jogadores") {
		t.Fatalf("reset: %q", got)
	}
	texts := e.fc.Texts()
	if !strings.HasPrefix(texts[len(texts)-2], "🍆 *Ranking") {
		t.Fatalf("ranking antes do reset não enviado: %q", texts[len(texts)-2])
	}
	if got := e.send(from(bottest.Admin, "Admin", "!pinto-reset")); got != "⚠️ Não há dados do jogo para este grupo." {
		t.Fatalf("reset vazio: %q", got)
	}
}

func TestRoleta(t *testing.T) {
	e := setup(t)
	e.f.rand = fixed(3) // safe
	if got := e.send(bottest.GroupMsg("!roletarussa")); got != "💨🔫 *click* - Tá *safe*! ```1```" {
		t.Fatalf("1ª: %q", got)
	}
	if e.fc.Reactions[len(e.fc.Reactions)-1] != "🔫" {
		t.Fatalf("reações: %v", e.fc.Reactions)
	}
	if got := e.send(bottest.GroupMsg("!roletarussa")); got != "🔄 Fulano, espere outra pessoa jogar antes de tentar novamente." {
		t.Fatalf("repetido: %q", got)
	}
	e.send(from(other, "Ciclano", "!roletarussa"))
	if got := e.send(bottest.GroupMsg("!roletarussa")); !strings.Contains(got, "```2```") {
		t.Fatalf("2ª: %q", got)
	}
	e.send(from(other, "Ciclano", "!roletarussa"))

	e.f.rand = fixed(0) // morre
	got := e.send(bottest.GroupMsg("!roletarussa"))
	if !strings.HasPrefix(got, "💥🔫 *BANG* - *F no chat* Morreu em 3, um novo record! Seu máximo antes disso era 0.\nNeste grupo, você já morreu 1 vezes.") {
		t.Fatalf("morte: %q", got)
	}
	if e.fc.Reactions[len(e.fc.Reactions)-1] != "💀" {
		t.Fatalf("reações: %v", e.fc.Reactions)
	}
	e.send(from(other, "Ciclano", "!roletarussa"))

	e.now = e.now.Add(4*time.Minute + 30*time.Second)
	if got := e.send(bottest.GroupMsg("!roletarussa")); got != "💀 Fulano já está morto na roleta russa. Ressuscita em 0m30s." {
		t.Fatalf("morto: %q", got)
	}
	e.now = e.now.Add(31 * time.Second)
	e.f.rand = fixed(3)
	if got := e.send(bottest.GroupMsg("!roletarussa")); !strings.Contains(got, "```1```") {
		t.Fatalf("ressuscitado: %q", got)
	}

	got = e.send(bottest.GroupMsg("!roletaranking"))
	want := "🏆 *Rankings Roleta Russa* 🔫\n\n🍀 *Sorte - Máx. Tentativas sem morrer*\n" +
		"\t🥇 1°: 3 - Ciclano\n\t🥈 2°: 3 *(1 atual)* - Fulano\n\n🪦 *Número de Mortes*\n" +
		"\t🥇 1°: 1 - Ciclano\n\t🥈 2°: 1 - Fulano"
	if got != want {
		t.Fatalf("ranking:\n%q\nwant\n%q", got, want)
	}

	if got := e.send(bottest.PrivateMsg("!roletarussa")); got != "Esse comando só funciona em grupos." {
		t.Fatalf("privado: %q", got)
	}
}

func TestRoletaTempoAndReset(t *testing.T) {
	e := setup(t)
	admin := func(text string) string { return e.send(from(bottest.Admin, "Admin", text)) }

	if got := e.send(bottest.GroupMsg("!roleta-tempo 60")); got != "Só admins do grupo podem usar esse comando." {
		t.Fatalf("não admin: %q", got)
	}
	if got := admin("!roleta-tempo"); !strings.HasPrefix(got, "Por favor, forneça um tempo") {
		t.Fatalf("sem arg: %q", got)
	}
	if got := admin("!roleta-tempo abc"); !strings.HasPrefix(got, "Por favor, forneça um tempo") {
		t.Fatalf("arg inválido: %q", got)
	}
	for _, tc := range [][2]string{
		{"5", "10 segundo(s)"},
		{"90", "1 minuto(s) e 30 segundo(s)"},
		{"999999999", "4320 minuto(s)"},
		{"99999999999999999999", "4320 minuto(s)"},
		{"120", "2 minuto(s)"},
	} {
		in, want := tc[0], tc[1]
		if got := admin("!roleta-tempo " + in); got != "🕐 Tempo de \"morte\" na roleta russa definido para "+want+"." {
			t.Fatalf("%s: %q", in, got)
		}
	}

	// Timeout de 120s vale na morte.
	e.f.rand = fixed(0)
	e.send(bottest.GroupMsg("!roletarussa"))
	e.now = e.now.Add(119 * time.Second)
	e.send(from(other, "Ciclano", "!roletarussa"))
	if got := e.send(bottest.GroupMsg("!roletarussa")); got != "💀 Fulano já está morto na roleta russa. Ressuscita em 0m1s." {
		t.Fatalf("timeout: %q", got)
	}

	if got := e.send(bottest.GroupMsg("!roleta-reset")); got != "Só admins do grupo podem usar esse comando." {
		t.Fatalf("reset não admin: %q", got)
	}
	if got := admin("!roleta-reset"); !strings.Contains(got, "removidos dados de 2 jogadores") {
		t.Fatalf("reset: %q", got)
	}
	if got := admin("!roleta-reset"); got != "⚠️ Não há dados da roleta russa para este grupo." {
		t.Fatalf("reset vazio: %q", got)
	}
	if got := e.send(bottest.GroupMsg("!roleta-ranking")); got != "🏆 Ainda não há jogadores na roleta russa deste grupo." {
		t.Fatalf("ranking vazio: %q", got)
	}
	// O último jogador também é esquecido.
	e.f.rand = fixed(3)
	if got := e.send(from(other, "Ciclano", "!roletarussa")); !strings.Contains(got, "```1```") {
		t.Fatalf("após reset: %q", got)
	}
}

func TestModule(t *testing.T) {
	if (Module{}).Name() != "fun" {
		t.Fatal("nome")
	}
}
