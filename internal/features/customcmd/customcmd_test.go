package customcmd

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/joaodobread/ravena-go/internal/app"
	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/bot/bottest"
	"github.com/joaodobread/ravena-go/internal/policy"
	"github.com/joaodobread/ravena-go/internal/store"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type env struct {
	t      *testing.T
	m      *Module
	st     *store.Store
	fc     *bottest.FakeClient
	router *bot.Router
	dir    string
	seq    int
	picks  []int // próximas escolhas de rand (vazio = 0)
}

var member = types.NewJID("5511777777777", types.DefaultUserServer)

func setup(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	fc := &bottest.FakeClient{Groups: map[types.JID]*bot.GroupInfo{
		bottest.Group: {JID: bottest.Group, Name: "Família", Participants: []bot.Participant{
			{JID: bottest.User}, {JID: bottest.Admin, IsAdmin: true}, {JID: member}, {JID: bottest.Bot},
		}},
	}}
	// Admin é super admin: escapa do anti-spam nos testes.
	pol, err := policy.New(ctx, st, fc, []string{bottest.Admin.User}, policy.DefaultOptions, quiet)
	if err != nil {
		t.Fatal(err)
	}
	r := bot.NewRouter("!", fc, quiet)
	r.SetPolicy(pol)
	if err := r.Register(&bot.Command{Name: "ping", Category: "geral", Run: func(ctx context.Context, c *bot.Context) error {
		return c.ReplyText(ctx, "pong %s", c.ArgText())
	}}); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, st: st, fc: fc, router: r, dir: dir}
	e.m = &Module{now: func() time.Time { return time.Date(2026, 11, 21, 15, 4, 5, 0, brt) }}
	e.m.rand = func(n int) int {
		if len(e.picks) == 0 {
			return 0
		}
		p := e.picks[0]
		e.picks = e.picks[1:]
		return p % n
	}
	if err := e.m.Register(ctx, &app.Deps{Router: r, Client: fc, Store: st, Policy: pol, Log: quiet, DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	return e
}

// admin envia como admin do grupo.
func (e *env) admin(text string) *bot.Message {
	msg := &bot.Message{ID: "a", Chat: bottest.Group, Sender: bottest.Admin, PushName: "Chefe", IsGroup: true, Text: text}
	e.router.Handle(context.Background(), msg)
	return msg
}

// user envia como um usuário comum diferente a cada vez (sem anti-spam).
func (e *env) user(text string) *bot.Message {
	e.seq++
	msg := &bot.Message{
		ID: fmt.Sprint("u", e.seq), Chat: bottest.Group, PushName: "Fulano", IsGroup: true, Text: text,
		Sender: types.NewJID(fmt.Sprintf("55119000%05d", e.seq), types.DefaultUserServer),
	}
	e.router.Handle(context.Background(), msg)
	return msg
}

func (e *env) send(msg *bot.Message) { e.router.Handle(context.Background(), msg) }

func (e *env) reset() { e.fc.Replies, e.fc.Reactions = nil, nil }

func (e *env) last() string {
	e.t.Helper()
	l := e.fc.Last()
	if l.Out.Kind == bot.OutText {
		return l.Out.Text
	}
	return l.Out.Caption
}

func (e *env) expectLast(want string) {
	e.t.Helper()
	if got := e.last(); !strings.Contains(got, want) {
		e.t.Fatalf("última resposta = %q, queria conter %q (todas: %q)", got, want, e.fc.Texts())
	}
}

func TestAddAndRun(t *testing.T) {
	e := setup(t)
	e.admin("!g-addCmd oi Olá, {pessoa}!\nBem-vindo ao {nomeGrupo}.")
	e.expectLast("adicionado com sucesso")

	e.reset()
	e.user("!oi")
	e.expectLast("Olá, Fulano!\nBem-vindo ao Família.")
	if l := e.fc.Last(); l.To == nil {
		t.Fatal("deveria citar a mensagem")
	}

	// Gatilho com prefixo e caixa diferentes.
	e.reset()
	e.user("!OI")
	e.expectLast("Olá, Fulano!")
}

func TestAddRequiresAdminAndArgs(t *testing.T) {
	e := setup(t)
	e.user("!g-addCmd oi olá")
	e.expectLast("Só admins")
	e.admin("!g-addCmd")
	e.expectLast("forneça um gatilho")
	e.admin("!g-addCmd oi")
	e.expectLast("Escreva a resposta")
	e.admin("!g-addCmd ping oi")
	e.expectLast("Já existe um comando do bot")
	e.admin("!g-addCmd !!x ok")
	e.reset()
	e.user("!!x")
	e.expectLast("ok")
}

func TestQuotedTextAndMultiWordTrigger(t *testing.T) {
	e := setup(t)
	msg := &bot.Message{ID: "a", Chat: bottest.Group, Sender: bottest.Admin, IsGroup: true, Text: "!g-addCmd Bom Dia",
		Quoted: &bot.Message{ID: "q", Sender: bottest.User, Text: "Bom dia, grupo! ☀️"}}
	e.send(msg)
	e.expectLast("'bom dia' adicionado")
	e.admin("!g-addCmd bom só bom")

	e.reset()
	e.user("!bom dia pessoal")
	e.expectLast("Bom dia, grupo!")
	e.user("!bom")
	e.expectLast("só bom")
	// Match parcial: "bomzinho" começa com "bom".
	e.user("!bomzinho")
	e.expectLast("só bom")
}

func TestQuotedMediaSavedAndSent(t *testing.T) {
	e := setup(t)
	img := []byte("\xff\xd8jpeg")
	e.send(&bot.Message{ID: "a", Chat: bottest.Group, Sender: bottest.Admin, IsGroup: true, Text: "!g-addCmd foto",
		Quoted: &bot.Message{ID: "q", Text: "legenda de {pessoa}", Media: bottest.StaticMedia(bot.MediaImage, "image/jpeg", img)}})
	e.expectLast("adicionado")
	files, _ := os.ReadDir(filepath.Join(e.dir, "customcmd", bottest.Group.User))
	if len(files) != 1 || !strings.HasSuffix(files[0].Name(), ".jpg") {
		t.Fatalf("mídia não salva: %v", files)
	}

	e.reset()
	e.user("!foto")
	out := e.fc.Last().Out
	if out.Kind != bot.OutImage || string(out.Data) != string(img) || out.Caption != "legenda de Fulano" {
		t.Fatalf("mídia enviada errada: kind=%v caption=%q", out.Kind, out.Caption)
	}

	// Figurinha e áudio de voz também.
	e.send(&bot.Message{ID: "b", Chat: bottest.Group, Sender: bottest.Admin, IsGroup: true, Text: "!g-addCmdReply foto",
		Quoted: &bot.Message{ID: "q2", Media: bottest.StaticMedia(bot.MediaAudio, "audio/ogg; codecs=opus", []byte("ogg"))}})
	e.expectLast("nova resposta")
	e.reset()
	e.picks = []int{1}
	e.user("!foto")
	if out := e.fc.Last().Out; out.Kind != bot.OutAudio || !out.PTT {
		t.Fatalf("áudio errado: %+v", out.Kind)
	}

	e.admin("!g-delCmd foto")
	e.expectLast("excluído")
	files, _ = os.ReadDir(filepath.Join(e.dir, "customcmd", bottest.Group.User))
	if len(files) != 0 {
		t.Fatalf("mídias deveriam ser apagadas: %v", files)
	}
	e.reset()
	e.user("!foto")
	if len(e.fc.Replies) != 0 {
		t.Fatal("comando apagado não deveria responder")
	}
}

func TestMediaTooBig(t *testing.T) {
	e := setup(t)
	big := make([]byte, MaxMediaBytes+1)
	e.send(&bot.Message{ID: "a", Chat: bottest.Group, Sender: bottest.Admin, IsGroup: true, Text: "!g-addCmd x",
		Quoted: &bot.Message{ID: "q", Media: bottest.StaticMedia(bot.MediaVideo, "video/mp4", big)}})
	e.expectLast("grande demais")
}

func TestRepliesSendAllAndRandom(t *testing.T) {
	e := setup(t)
	e.admin("!g-addCmd dado um")
	e.admin("!g-addCmdReply dado dois")
	e.admin("!g-addCmdReply nada x")
	e.expectLast("não encontrado")

	e.reset()
	e.picks = []int{1}
	e.user("!dado")
	if got := e.fc.Texts(); !slices.Equal(got, []string{"dois"}) {
		t.Fatalf("aleatória: %q", got)
	}

	e.admin("!g-cmd-enviarTudo dado")
	e.expectLast("TODAS")
	e.reset()
	e.user("!dado")
	if got := e.fc.Texts(); !slices.Equal(got, []string{"um", "dois"}) {
		t.Fatalf("todas: %q", got)
	}
}

func TestEnableDisableAndAdminOnly(t *testing.T) {
	e := setup(t)
	e.admin("!g-addCmd oi olá")
	e.admin("!g-cmd-disable oi")
	e.expectLast("desabilitado")
	e.reset()
	e.user("!oi")
	if len(e.fc.Replies) != 0 {
		t.Fatal("desativado não deveria responder")
	}
	e.admin("!g-cmd-enable oi")
	e.admin("!g-cmd-setAdm oi")
	e.expectLast("só pode ser usado por administradores")
	e.reset()
	e.user("!oi")
	e.expectLast("só pode ser usado por _administradores_")
	if !slices.Contains(e.fc.Reactions, "⛔️") {
		t.Fatalf("faltou ⛔️: %q", e.fc.Reactions)
	}
	e.reset()
	e.admin("!oi")
	e.expectLast("olá")
	e.admin("!g-cmd-enable nada")
	e.expectLast("não encontrado")
}

func TestCooldownReactAndNoQuote(t *testing.T) {
	e := setup(t)
	e.admin("!g-addCmd oi olá")
	e.admin("!g-cmd-cd oi 60")
	e.expectLast("60 segundo")
	e.admin("!g-cmd-cd oi x")
	e.expectLast("deve ser um número")
	e.admin("!g-cmd-react oi 🎯")
	e.admin("!g-cmd-startReact oi ⌛")
	e.admin("!g-cmd-responder oi")
	e.expectLast("sem citar")

	e.reset()
	e.user("!oi")
	e.expectLast("olá")
	if e.fc.Last().To != nil {
		t.Fatal("não deveria citar")
	}
	if !slices.Equal(e.fc.Reactions, []string{"⌛", "🎯"}) {
		t.Fatalf("reações: %q", e.fc.Reactions)
	}
	e.user("!oi")
	e.expectLast("Calma")
}

func TestPrivateReply(t *testing.T) {
	e := setup(t)
	e.admin("!g-addCmd segredo psiu")
	e.admin("!g-cmd-setPV segredo")
	e.reset()
	msg := e.user("!segredo")
	if l := e.fc.Last(); l.Chat != msg.Sender || l.Out.Text != "psiu" {
		t.Fatalf("deveria ir ao PV: %+v", l)
	}
}

func TestIgnorePrefixTriggers(t *testing.T) {
	e := setup(t)
	e.admin("!g-addCmd bolo 🎂")
	e.reset()
	e.user("eu quero bolo")
	if len(e.fc.Replies) != 0 {
		t.Fatal("sem ignorePrefix não deveria disparar")
	}
	e.admin("!g-cmd-semPrefixo bolo")
	e.reset()
	e.user("eu quero BOLO agora")
	e.expectLast("🎂")

	// Grupo inteiro sem prefixo: o texto todo precisa começar com o gatilho.
	e.admin("!g-addCmd tchau até mais")
	e.reset()
	e.user("tchau gente")
	if len(e.fc.Replies) != 0 {
		t.Fatal("sem g-setCustomSemPrefixo não deveria disparar")
	}
	e.admin("!g-setCustomSemPrefixo")
	e.expectLast("*não precisam*")
	e.reset()
	e.user("tchau gente")
	e.expectLast("até mais")
	e.reset()
	e.user("eu disse tchau")
	if len(e.fc.Replies) != 0 {
		t.Fatal("no modo sem prefixo o gatilho precisa estar no começo")
	}
}

func TestPausedAndMutedCategory(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.admin("!g-addCmd oi olá")
	if _, err := e.st.UpdateGroup(ctx, bottest.Group.String(), func(g *store.Group) { g.Paused = true }); err != nil {
		t.Fatal(err)
	}
	e.reset()
	e.user("!oi")
	if len(e.fc.Replies) != 0 {
		t.Fatal("grupo pausado não deveria responder")
	}
	if _, err := e.st.UpdateGroup(ctx, bottest.Group.String(), func(g *store.Group) {
		g.Paused = false
		g.MutedCategories = []string{Category}
	}); err != nil {
		t.Fatal(err)
	}
	e.user("!oi")
	if len(e.fc.Replies) != 0 {
		t.Fatal("categoria silenciada não deveria responder")
	}
	e.admin("!oi") // como nos comandos do bot, admin passa pelo mute
	e.expectLast("olá")
}

func TestVariables(t *testing.T) {
	e := setup(t)
	e.admin("!g-addCmd v {mention} e {mention} | {singleMention}{singleMention} | {rndDado-6}+{rndDadoRange-10-12}={somaRandoms} | {contador} | {dia} {data} {hora} {data-ano} | {desconhecida} | @5511777777777")
	e.reset()
	e.picks = []int{0, 0, 0, 2, 1} // resposta, embaralha as menções (troca), membro aleatório → User, dado → 3, range → 11
	msg := &bot.Message{ID: "x", Chat: bottest.Group, Sender: bottest.User, PushName: "Fulano", IsGroup: true, Text: "!v",
		Mentions: []types.JID{bottest.Admin, member}}
	e.send(msg)
	got := e.last()
	want := "@5511777777777 e @5511888888888 | @5511999999999@5511999999999 | 3+11=14 | 1 | Sábado 21/11/2026 15:04:05 2026 | {desconhecida} | @5511777777777"
	if got != want {
		t.Fatalf("variáveis:\n got %q\nwant %q", got, want)
	}
	ms := e.fc.Last().Out.Mentions
	for _, j := range []types.JID{bottest.Admin, member, bottest.User} {
		if !slices.ContainsFunc(ms, func(x types.JID) bool { return x.User == j.User }) {
			t.Errorf("faltou menção a %s em %v", j, ms)
		}
	}

	// {contador} cresce.
	e.reset()
	e.picks = nil
	e.user("!v")
	if !strings.Contains(e.last(), "| 2 |") {
		t.Fatalf("contador: %q", e.last())
	}
}

func TestEmbeddedCommand(t *testing.T) {
	e := setup(t)
	e.admin("!g-addCmd p {cmd-!ping a}")
	e.reset()
	e.user("!p b")
	e.expectLast("pong a b")
}

func TestListVarsAndRandomMenu(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if ok, err := e.m.RunRandom(ctx, bottest.GroupMsg("oi")); ok || err != nil {
		t.Fatalf("sem comandos: %v %v", ok, err)
	}
	e.admin("!g-addCmd a resposta a")
	e.admin("!g-addCmd b resposta b")
	e.admin("!g-cmd-setInteragir a")
	e.admin("!g-addCmd c resposta c")
	e.admin("!g-cmd-disable c")

	e.admin("!g-cmds")
	e.expectLast("!a (1 resp.)")
	e.expectLast("desativado")
	e.admin("!g-variaveis")
	e.expectLast("{pessoa}")

	if got := e.m.MenuEntries(ctx, bottest.Group); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("MenuEntries = %q", got)
	}
	e.reset()
	ok, err := e.m.RunRandom(ctx, bottest.GroupMsg("conversa"))
	if !ok || err != nil || e.last() != "resposta b" || len(e.fc.Reactions) != 0 {
		t.Fatalf("RunRandom: %v %v %q %q", ok, err, e.fc.Texts(), e.fc.Reactions)
	}
}

func TestMatch(t *testing.T) {
	cmds := []*Command{{Trigger: "a b", Active: true}, {Trigger: "oi", Active: true}, {Trigger: "x", Active: false}}
	tests := []struct {
		in   string
		want string
		args int
	}{
		{"a b c", "a b", 1},
		{"oi tudo bem", "oi", 2},
		{"oizinho", "oi", 0},
		{"x", "", 0},
		{"z", "", 0},
	}
	for _, tt := range tests {
		c, args := match(cmds, strings.Fields(tt.in))
		got := ""
		if c != nil {
			got = c.Trigger
		}
		if got != tt.want || len(args) != tt.args {
			t.Errorf("match(%q) = %q %v", tt.in, got, args)
		}
	}
}
