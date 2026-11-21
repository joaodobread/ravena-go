package groupadmin_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/types"

	"github.com/joaodobread/ravena-go/internal/app"
	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/bot/bottest"
	"github.com/joaodobread/ravena-go/internal/features/groupadmin"
	"github.com/joaodobread/ravena-go/internal/policy"
	"github.com/joaodobread/ravena-go/internal/store"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

var (
	super   = types.NewJID("5511555555555", types.DefaultUserServer)
	waAdmin = bottest.Admin
	lid     = types.NewJID("98765432101234", types.HiddenUserServer)
	other   = types.NewJID("5511777777777", types.DefaultUserServer)
)

type env struct {
	t      *testing.T
	st     *store.Store
	fc     *bottest.FakeClient
	router *bot.Router
	seq    int
}

func setup(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	fc := &bottest.FakeClient{Groups: map[types.JID]*bot.GroupInfo{
		bottest.Group: {JID: bottest.Group, Participants: []bot.Participant{
			{JID: lid, LID: lid, Phone: bottest.User},
			{JID: waAdmin, IsAdmin: true},
			{JID: other},
			{JID: bottest.Bot, IsAdmin: true},
		}},
	}}
	pol, err := policy.New(ctx, st, fc, []string{super.User}, policy.DefaultOptions, quiet)
	if err != nil {
		t.Fatal(err)
	}
	r := bot.NewRouter("!", fc, quiet)
	r.SetPolicy(pol)
	pong := func(ctx context.Context, c *bot.Context) error { return c.ReplyText(ctx, "pong") }
	if err := r.Register(
		&bot.Command{Name: "ping", Category: "geral", Run: pong},
		&bot.Command{Name: "pescar", Category: "jogos", Triggers: []string{"🎣"}, Run: pong},
	); err != nil {
		t.Fatal(err)
	}
	if err := (&groupadmin.Module{}).Register(ctx, &app.Deps{Router: r, Client: fc, Store: st, Policy: pol, Log: quiet}); err != nil {
		t.Fatal(err)
	}
	return &env{t: t, st: st, fc: fc, router: r}
}

func (e *env) from(sender types.JID, text string, mentions ...types.JID) string {
	e.t.Helper()
	e.fc.Replies = nil
	e.router.Handle(context.Background(), &bot.Message{
		ID: "m", Chat: bottest.Group, Sender: sender, IsGroup: true, Text: text, Mentions: mentions,
	})
	return strings.Join(e.fc.Texts(), "\n")
}

// sa envia como super admin (sem anti-spam).
func (e *env) sa(text string, mentions ...types.JID) string {
	e.t.Helper()
	return e.from(super, text, mentions...)
}

// fresh envia como um usuário comum novo (sem anti-spam acumulado).
func (e *env) fresh(text string) string {
	e.seq++
	return e.from(types.NewJID(fmt.Sprintf("55119000%05d", e.seq), types.DefaultUserServer), text)
}

func (e *env) group() *store.Group {
	g, err := e.st.GroupOrDefault(context.Background(), bottest.Group.String())
	if err != nil {
		e.t.Fatal(err)
	}
	return g
}

func want(t *testing.T, got, sub string) {
	t.Helper()
	if !strings.Contains(got, sub) {
		t.Fatalf("resposta %q não contém %q", got, sub)
	}
}

func TestAdminOnly(t *testing.T) {
	e := setup(t)
	want(t, e.from(other, "!g-pausar"), "Só admins")
	// Admin do WhatsApp passa sem ser super admin.
	want(t, e.from(waAdmin, "!g-info"), "Informações do Grupo")
	// Privado é recusado.
	e.fc.Replies = nil
	e.router.Handle(context.Background(), bottest.PrivateMsg("!g-info"))
	want(t, strings.Join(e.fc.Texts(), ""), "só funciona em grupos")
}

func TestPause(t *testing.T) {
	e := setup(t)
	want(t, e.sa("!g-pausar"), "pausado")
	if !e.group().Paused {
		t.Fatal("deveria estar pausado")
	}
	if got := e.fresh("!ping"); got != "" {
		t.Fatalf("pausado respondeu %q", got)
	}
	want(t, e.sa("!g-pausar"), "reativado")
	want(t, e.fresh("!ping"), "pong")
}

func TestPrefix(t *testing.T) {
	e := setup(t)
	want(t, e.sa("!g-setPrefixo #"), "atualizado para: #")
	if got := e.fresh("!ping"); got != "" {
		t.Fatalf("prefixo antigo respondeu %q", got)
	}
	want(t, e.fresh("#ping"), "pong")
	want(t, e.sa("#g-setPrefixo"), "padrão: !")
	want(t, e.fresh("!ping"), "pong")
	want(t, e.sa("!g-setPrefixo abcd"), "no máximo 3")
}

func TestSetName(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if _, err := e.st.UpdateGroup(ctx, "999@g.us", func(g *store.Group) { g.Name = "ocupado" }); err != nil {
		t.Fatal(err)
	}
	want(t, e.sa("!g-setNome Meu Grupo"), "meu-grupo")
	if e.group().Name != "meu-grupo" {
		t.Fatalf("nome = %q", e.group().Name)
	}
	want(t, e.sa("!g-setNome ocupado"), "Já existe")
	want(t, e.sa("!g-setNome nome/ruim"), "inválido")
	want(t, e.sa("!g-info"), "*Nome:* meu-grupo")
}

func TestInteract(t *testing.T) {
	e := setup(t)
	want(t, e.sa("!g-interagir"), "*desativadas*") // padrão é ligado
	want(t, e.sa("!g-interagir"), "Chance atual: 1%")
	want(t, e.sa("!g-interagir-cd 5"), "30 minutos (mínimo possível)")
	want(t, e.sa("!g-interagir-cd 99999"), "43200 minutos")
	want(t, e.sa("!g-interagir-cd"), "Cooldown atual: 43200")
	want(t, e.sa("!g-interagir-chance 900"), "5% (máximo possível)")
	want(t, e.sa("!g-interagir-chance 25"), "0.25%")
	want(t, e.sa("!g-interagir-chance"), "(25/10000)")
	want(t, e.sa("!g-interagir-prop 150"), "100% IA e 0% comandos")
	want(t, e.sa("!g-interagir-proporcao 30"), "70% de chance de usar algum comando")
	want(t, e.sa("!g-interagir-cmds"), "*desativadas*")
	in := e.group().Interact
	if !in.Enabled || in.CooldownMin != 43200 || in.Chance != 25 || in.Proporcao != 30 || in.UseCmds {
		t.Fatalf("interact = %+v", in)
	}
}

func TestMute(t *testing.T) {
	e := setup(t)
	want(t, e.sa("!g-mute"), "Nenhum comando")
	want(t, e.sa("!g-mute nada"), "não encontrado")
	want(t, e.sa("!g-mute !ping"), "silenciado")
	if got := e.fresh("!ping"); got != "" {
		t.Fatalf("mutado respondeu %q", got)
	}
	want(t, e.sa("!g-mute"), "- ping")
	want(t, e.sa("!g-desmutar ping"), "reativado")
	want(t, e.fresh("!ping"), "pong")
	want(t, e.sa("!g-mutar 🎣"), "'pescar' silenciado")
	want(t, e.sa("!g-mute pescar"), "reativado")

	want(t, e.sa("!g-muteCategoria"), "personalizados")
	want(t, e.sa("!g-muteCategoria xyz"), "não existe")
	want(t, e.sa("!g-mutarCategoria GERAL"), "'geral' silenciada")
	if got := e.fresh("!ping"); got != "" {
		t.Fatalf("categoria mutada respondeu %q", got)
	}
	want(t, e.sa("!g-desmutarCategoria geral"), "reativada")
	want(t, e.fresh("!ping"), "pong")
	want(t, e.sa("!g-muteCategoria personalizados"), "silenciada")
}

func TestIgnore(t *testing.T) {
	e := setup(t)
	want(t, e.sa("!g-ignorar"), "Nenhum número")
	want(t, e.sa("!g-ignorar 123"), "8 dígitos")
	// Número digitado sem o 9º dígito acha o membro e grava número + LID.
	want(t, e.sa("!g-ignorar +55 11 9999-9999"), "será ignorado")
	ig := e.group().IgnoredUsers
	if !slices.Contains(ig, lid.User) || !slices.Contains(ig, bottest.User.User) {
		t.Fatalf("ignorados = %q", ig)
	}
	if got := e.from(lid, "!ping"); got != "" {
		t.Fatalf("ignorado respondeu %q", got)
	}
	want(t, e.sa("!g-designorar", lid), "não será mais ignorado")
	if len(e.group().IgnoredUsers) != 0 {
		t.Fatalf("deveria limpar todas as identidades: %q", e.group().IgnoredUsers)
	}
	want(t, e.sa("!g-designorar"), "Informe")
}

func TestAdditionalAdmins(t *testing.T) {
	e := setup(t)
	want(t, e.sa("!g-addAdmin @x", other), "agora é admin")
	want(t, e.from(other, "!g-info"), "Admins extras:* "+other.User)
	want(t, e.sa("!g-customAdmin"), "- "+other.User)
	want(t, e.sa("!g-customAdmin", other), "não é mais admin")
	if len(e.group().AdditionalAdmins) != 0 {
		t.Fatalf("admins = %q", e.group().AdditionalAdmins)
	}
	want(t, e.sa("!g-delAdmin 5511777777777"), "não é mais admin")
}

func TestBan(t *testing.T) {
	e := setup(t)
	want(t, e.sa("!g-ban"), "mencione")
	want(t, e.sa("!g-kick @x", other), "Removido(s) do grupo: @"+other.User)
	if !slices.Equal(e.fc.Updates, []string{"remove:" + other.User}) {
		t.Fatalf("updates = %q", e.fc.Updates)
	}
	if ms := e.fc.Last().Out.Mentions; len(ms) != 1 || ms[0] != other {
		t.Fatalf("menções = %v", ms)
	}
	// Pelo autor da mensagem respondida.
	e.fc.Replies = nil
	e.router.Handle(context.Background(), &bot.Message{ID: "q", Chat: bottest.Group, Sender: super, IsGroup: true, Text: "!g-ban",
		Quoted: &bot.Message{ID: "x", Sender: bottest.User}})
	if last := e.fc.Updates[len(e.fc.Updates)-1]; last != "remove:"+lid.User {
		t.Fatalf("deveria remover pelo JID do membro: %q", e.fc.Updates)
	}
	// Bot sem admin.
	e.fc.Groups[bottest.Group].Participants[3].IsAdmin = false
	want(t, e.sa("!g-ban @x", other), "precisa ser administrador")
}
