package ai

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/bot/bottest"
	"github.com/joaodobread/ravena-go/internal/llm"
	"github.com/joaodobread/ravena-go/internal/store"
)

// fakeLLM é um servidor OpenRouter falso que grava as requisições.
type fakeLLM struct {
	mu      sync.Mutex
	reqs    []map[string]any
	replies []string // consumidas em ordem; a última se repete. "!err" = HTTP 500
}

func (f *fakeLLM) requests() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.reqs...)
}

// lastBody é a última requisição em JSON (para procurar textos).
func (f *fakeLLM) lastBody(t *testing.T) string {
	t.Helper()
	reqs := f.requests()
	if len(reqs) == 0 {
		t.Fatal("nenhuma chamada ao LLM")
	}
	b, _ := json.Marshal(reqs[len(reqs)-1])
	return string(b)
}

func newLLM(t *testing.T, replies ...string) (*llm.Client, *fakeLLM) {
	t.Helper()
	f := &fakeLLM{replies: replies}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.reqs = append(f.reqs, body)
		reply := "ok"
		if len(f.replies) > 0 {
			reply = f.replies[0]
			if len(f.replies) > 1 {
				f.replies = f.replies[1:]
			}
		}
		f.mu.Unlock()
		if reply == "!err" {
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"error":{"message":"caiu"}}`))
			return
		}
		b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": reply}}}})
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	c := llm.New("k", "modelo")
	c.BaseURL = srv.URL
	return c, f
}

type fakeFramer struct{ calls int }

var testJPEG = []byte{0xff, 0xd8, 0xff, 0xe0, 'J', 'F', 'I', 'F'}

func (f *fakeFramer) Frame(context.Context, []byte) ([]byte, error) {
	f.calls++
	return testJPEG, nil
}

type fakeCustom struct {
	calls int
	ran   bool
}

func (c *fakeCustom) RunRandom(context.Context, *bot.Message) (bool, error) {
	c.calls++
	return c.ran, nil
}

type env struct {
	f      *Feature
	fc     *bottest.FakeClient
	router *bot.Router
	store  *store.Store
	llm    *fakeLLM
	now    time.Time
	rolls  []int // valores devolvidos por Rand, em ordem; vazio = n-1
}

// newEnv cria a funcionalidade registrada num Router real. replies nil = sem LLM.
func newEnv(t *testing.T, replies []string) *env {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	fc := &bottest.FakeClient{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := bot.NewRouter("!", fc, log)
	e := &env{fc: fc, router: r, store: st, now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	f := &Feature{
		Router:  r,
		Store:   st,
		BotName: func() string { return "Ravena" },
		Log:     log,
		Buffer:  NewBuffer(0, 0),
		Media:   &fakeFramer{},
		Now:     func() time.Time { return e.now },
		Go:      func(fn func()) { fn() },
	}
	f.Rand = func(n int) int {
		if len(e.rolls) == 0 {
			return n - 1 // nunca dispara nada aleatório
		}
		v := e.rolls[0]
		e.rolls = e.rolls[1:]
		return min(v, n-1)
	}
	if replies != nil {
		c, fl := newLLM(t, replies...)
		f.LLM, e.llm = c, fl
	}
	if err := r.Register(f.Commands()...); err != nil {
		t.Fatal(err)
	}
	r.Observe(f.Observe)
	r.Listen(f.MentionListener)
	r.Listen(f.SillyListener)
	r.Listen(f.InteractListener)
	e.f = f
	return e
}

func (e *env) say(text string) {
	m := bottest.GroupMsg(text)
	e.router.Handle(context.Background(), m)
}

func (e *env) disableInteract(t *testing.T) {
	t.Helper()
	if _, err := e.store.UpdateGroup(context.Background(), bottest.Group.String(), func(g *store.Group) { g.Interact.Enabled = false }); err != nil {
		t.Fatal(err)
	}
}

func lastText(t *testing.T, fc *bottest.FakeClient) string {
	t.Helper()
	texts := fc.Texts()
	if len(texts) == 0 {
		t.Fatal("nenhuma resposta de texto")
	}
	return texts[len(texts)-1]
}

// --- buffer ---

func TestBufferBoundsAndLRU(t *testing.T) {
	b := NewBuffer(3, 2)
	for _, s := range []string{"1", "2", "3", "4", "5"} {
		b.Add("a", "Ana", s)
	}
	got := b.Recent("a")
	if len(got) != 3 || got[0].Text != "3" || got[2].Text != "5" {
		t.Fatalf("anel errado: %+v", got)
	}
	b.Add("b", "Bia", "oi")
	b.Add("a", "Ana", "6") // a vira o mais recente
	b.Add("c", "Caio", "oi")
	if b.Len() != 2 || b.Recent("b") != nil || b.Recent("a") == nil {
		t.Fatalf("LRU deveria ter expulsado b: len=%d", b.Len())
	}
	b.Clear("a")
	if b.Recent("a") != nil || b.Len() != 1 {
		t.Fatal("Clear não esqueceu o chat")
	}
	b.Add("c", strings.Repeat("n", 100), strings.Repeat("é", 500))
	last := b.Recent("c")[1]
	if n := len([]rune(last.Text)); n != maxEntryRunes+1 || len([]rune(last.Author)) != maxNameRunes+1 {
		t.Fatalf("truncamento: texto %d runas, autor %q", n, last.Author)
	}
	b.Add("c", "x", "   ")
	if len(b.Recent("c")) != 2 {
		t.Fatal("texto vazio não deveria entrar")
	}
}

func TestFormatEntriesKeepsNewestWithinLimit(t *testing.T) {
	var entries []Entry
	for range 100 {
		entries = append(entries, Entry{Author: "A", Text: strings.Repeat("x", 290)})
	}
	entries = append(entries, Entry{Author: "Z", Text: "última"})
	out := formatEntries(entries)
	if len(out) > maxPromptChars || !strings.HasSuffix(out, "Z: última\n") {
		t.Fatalf("formatação: %d chars, fim %q", len(out), out[len(out)-20:])
	}
}

func TestObserverSkipsCommandsAndPrivate(t *testing.T) {
	e := newEnv(t, []string{"ok"})
	e.disableInteract(t)
	e.say("bom dia")
	e.say("!naoexiste")
	e.router.Handle(context.Background(), bottest.PrivateMsg("no pv"))
	got := e.f.Buffer.Recent(bottest.Group.String())
	if len(got) != 1 || got[0] != (Entry{Author: "Fulano", Text: "bom dia"}) {
		t.Fatalf("buffer: %+v", got)
	}
}

// --- !ai ---

func TestAIText(t *testing.T) {
	e := newEnv(t, []string{"Porto Alegre, claro."})
	m := bottest.GroupMsg("!ai qual a capital do RS?")
	m.Quoted = &bot.Message{Text: "estamos falando de capitais do sul"}
	e.router.Handle(context.Background(), m)
	if got := lastText(t, e.fc); got != "Porto Alegre, claro." {
		t.Fatalf("resposta: %q", got)
	}
	body := e.llm.lastBody(t)
	for _, want := range []string{"qual a capital do RS?", "Contexto da mensagem respondida: estamos falando", "Ravena", "Fulano", `"max_tokens":600`} {
		if !strings.Contains(body, want) {
			t.Errorf("requisição sem %q: %s", want, body)
		}
	}
	if strings.Contains(body, "image_url") {
		t.Error("não deveria mandar imagem")
	}
	if r := e.fc.Reactions; len(r) != 2 || r[1] != "🤖" {
		t.Errorf("reações: %v", r)
	}
}

func TestAIWithImage(t *testing.T) {
	e := newEnv(t, []string{"Um gato."})
	m := bottest.GroupMsg("!ia")
	m.Quoted = &bot.Message{Media: bottest.StaticMedia(bot.MediaVideo, "video/mp4", []byte("vid"))}
	e.router.Handle(context.Background(), m)
	if got := lastText(t, e.fc); got != "Um gato." {
		t.Fatalf("resposta: %q", got)
	}
	body := e.llm.lastBody(t)
	if !strings.Contains(body, "data:image/jpeg;base64,") || !strings.Contains(body, "Analise esta imagem") || !strings.Contains(body, "quadro de um vídeo") {
		t.Fatalf("requisição sem imagem/prompt padrão: %s", body)
	}
	if e.f.Media.(*fakeFramer).calls != 1 {
		t.Fatal("deveria extrair um quadro")
	}
}

func TestAIImageWithoutConverter(t *testing.T) {
	e := newEnv(t, []string{"ok"})
	e.f.Media = nil
	m := bottest.GroupMsg("!ai o que é isso?")
	m.Media = bottest.StaticMedia(bot.MediaImage, "image/jpeg", testJPEG)
	e.router.Handle(context.Background(), m)
	if !strings.Contains(e.llm.lastBody(t), "data:image/jpeg;base64,") {
		t.Fatal("JPEG cru deveria ir direto ao LLM")
	}
}

func TestAIUsageErrors(t *testing.T) {
	e := newEnv(t, []string{"ok"})
	e.say("!ai oi")
	if got := lastText(t, e.fc); got != MsgAIUsage {
		t.Fatalf("pergunta curta: %q", got)
	}
	m := bottest.GroupMsg("!ai transcreve isso aí")
	m.Media = bottest.StaticMedia(bot.MediaAudio, "audio/ogg", []byte("a"))
	e.router.Handle(context.Background(), m)
	if got := lastText(t, e.fc); got != MsgMediaKind {
		t.Fatalf("áudio: %q", got)
	}
	if len(e.llm.requests()) != 0 {
		t.Fatal("não deveria chamar o LLM")
	}
}

func TestAILLMFailure(t *testing.T) {
	e := newEnv(t, []string{"!err"})
	e.say("!ai me explica a vida")
	if got := lastText(t, e.fc); got != MsgAIError {
		t.Fatalf("erro: %q", got)
	}
}

func TestAIReactionTriggerIgnoresQuoted(t *testing.T) {
	e := newEnv(t, []string{"resposta"})
	target := bottest.GroupMsg("quanto é dois mais dois")
	target.Quoted = &bot.Message{Text: "isso não deveria entrar no prompt"}
	e.router.HandleReaction(context.Background(), target, bot.Reaction{Emoji: "🤖", By: bottest.Admin})
	body := e.llm.lastBody(t)
	if !strings.Contains(body, "quanto é dois mais dois") || strings.Contains(body, "não deveria entrar") {
		t.Fatalf("prompt por reação: %s", body)
	}
}

// --- menção ---

func TestMentionListener(t *testing.T) {
	e := newEnv(t, []string{"Sou a Ravena!"})
	m := bottest.GroupMsg("@5562900000000 quem é você?")
	m.MentionsBot = true
	e.router.Handle(context.Background(), m)
	if got := lastText(t, e.fc); got != "Sou a Ravena!" {
		t.Fatalf("resposta: %q", got)
	}
	if body := e.llm.lastBody(t); strings.Contains(body, "@5562900000000") || !strings.Contains(body, "quem é você?") {
		t.Fatalf("menção não removida: %s", body)
	}

	hi := bottest.GroupMsg("@5562900000000")
	hi.MentionsBot = true
	e.router.Handle(context.Background(), hi)
	if got := lastText(t, e.fc); got != MsgMentionHi {
		t.Fatalf("saudação: %q", got)
	}
	if r := e.fc.Reactions; r[len(r)-1] != "🤖" {
		t.Fatalf("reações: %v", r)
	}
}

func TestMentionListenerIgnores(t *testing.T) {
	e := newEnv(t, []string{"x"})
	e.disableInteract(t)
	cases := map[string]*bot.Message{
		"sem menção": bottest.GroupMsg("quem é você?"),
		"comando":    {ID: "m", Chat: bottest.Group, Sender: bottest.User, IsGroup: true, Text: "@5562900000000 !s", MentionsBot: true},
		"privado":    {ID: "p", Chat: bottest.User, Sender: bottest.User, Text: "@5562900000000 oi tudo bem?", MentionsBot: true},
	}
	for name, m := range cases {
		handled, err := e.f.MentionListener(context.Background(), m, e.fc)
		if handled || err != nil {
			t.Errorf("%s: handled=%v err=%v", name, handled, err)
		}
	}
}

// --- !resumo / !interagir ---

func TestResumo(t *testing.T) {
	e := newEnv(t, []string{"- falaram de futebol"})
	e.disableInteract(t)
	e.say("!resumo")
	if got := lastText(t, e.fc); got != MsgNoSummary {
		t.Fatalf("sem histórico: %q", got)
	}
	e.say("vamos ver o jogo hoje?")
	e.say("bora, 20h")
	e.say("!resumo")
	if got := lastText(t, e.fc); got != "📋 *Resumo da conversa:*\n\n- falaram de futebol" {
		t.Fatalf("resumo: %q", got)
	}
	if body := e.llm.lastBody(t); !strings.Contains(body, "Fulano: vamos ver o jogo hoje?\\nFulano: bora, 20h") || strings.Contains(body, "!resumo") {
		t.Fatalf("prompt: %s", body)
	}
	if e.f.Buffer.Recent(bottest.Group.String()) != nil {
		t.Fatal("histórico deveria ser limpo após o resumo")
	}
	e.router.Handle(context.Background(), bottest.PrivateMsg("!resumo"))
	if got := lastText(t, e.fc); !strings.Contains(got, "só funciona em grupos") {
		t.Fatalf("pv: %q", got)
	}
}

func TestInteragirCommand(t *testing.T) {
	e := newEnv(t, []string{"kkk concordo @5511999999999"})
	e.disableInteract(t)
	e.say("!interagir")
	if got := lastText(t, e.fc); got != MsgNoInteract {
		t.Fatalf("sem histórico: %q", got)
	}
	e.say("esse calor tá demais")
	e.say("!interagir")
	last := e.fc.Last()
	if last.To != nil || last.Out.Text != "kkk concordo @5511999999999" || len(last.Out.Mentions) != 1 || last.Out.Mentions[0].User != "5511999999999" {
		t.Fatalf("interação: %+v", last)
	}
}

// --- interação automática ---

func (e *env) interact(t *testing.T) *store.Interact {
	t.Helper()
	g, ok, err := e.store.Group(context.Background(), bottest.Group.String())
	if err != nil || !ok {
		return nil
	}
	return &g.Interact
}

func TestAutoInteractChanceAndCooldown(t *testing.T) {
	e := newEnv(t, []string{"opa, também acho"})
	// padrão: habilitado, chance 100/10000, cooldown 30 min

	e.rolls = []int{100} // sorteio 101 > 100: não dispara
	e.say("primeira mensagem")
	if len(e.llm.requests()) != 0 || e.interact(t) != nil {
		t.Fatal("não deveria disparar nem gravar o grupo")
	}

	e.rolls = []int{99, 0} // sorteio 100 <= 100: dispara
	e.say("segunda mensagem")
	if got := lastText(t, e.fc); got != "opa, também acho" {
		t.Fatalf("interação: %q", got)
	}
	if in := e.interact(t); in == nil || !in.Last.Equal(e.now) {
		t.Fatalf("Last não gravado: %+v", in)
	}
	if e.f.Buffer.Recent(bottest.Group.String()) != nil {
		t.Fatal("histórico deveria ser limpo")
	}

	sent := len(e.fc.Replies)
	e.now = e.now.Add(29 * time.Minute)
	e.rolls = []int{0, 0}
	e.say("em cooldown")
	if len(e.fc.Replies) != sent {
		t.Fatal("cooldown ignorado")
	}

	e.now = e.now.Add(2 * time.Minute)
	e.rolls = []int{0, 0}
	e.say("depois do cooldown")
	if len(e.fc.Replies) != sent+1 {
		t.Fatal("deveria disparar depois do cooldown")
	}
}

func TestAutoInteractDisabledAndChanceCap(t *testing.T) {
	e := newEnv(t, []string{"x"})
	if _, err := e.store.UpdateGroup(context.Background(), bottest.Group.String(), func(g *store.Group) {
		g.Interact.Chance = 9000 // limitado a 500
	}); err != nil {
		t.Fatal(err)
	}
	e.rolls = []int{500} // 501 > 500
	e.say("oi gente")
	if len(e.llm.requests()) != 0 {
		t.Fatal("chance deveria ter teto de 500")
	}
	e.disableInteract(t)
	e.rolls = []int{0, 0}
	e.say("oi de novo")
	if len(e.llm.requests()) != 0 {
		t.Fatal("desabilitado não dispara")
	}
}

func TestAutoInteractProportion(t *testing.T) {
	e := newEnv(t, []string{"msg do llm"})
	custom := &fakeCustom{ran: true}
	e.f.Custom = custom

	e.rolls = []int{0, 70} // dispara; 70 >= proporcao 50 → comando personalizado
	handled, err := e.f.InteractListener(context.Background(), bottest.GroupMsg("a"), e.fc)
	if handled || err != nil || custom.calls != 1 || len(e.llm.requests()) != 0 {
		t.Fatalf("comando: handled=%v err=%v calls=%d llm=%d", handled, err, custom.calls, len(e.llm.requests()))
	}

	e.now = e.now.Add(time.Hour)
	e.f.Buffer.Add(bottest.Group.String(), "Ana", "oi")
	e.rolls = []int{0, 10} // 10 < 50 → LLM
	if handled, _ := e.f.InteractListener(context.Background(), bottest.GroupMsg("b"), e.fc); handled || custom.calls != 1 || len(e.llm.requests()) != 1 {
		t.Fatalf("LLM: calls=%d llm=%d", custom.calls, len(e.llm.requests()))
	}

	// grupo sem comandos elegíveis: cai no LLM
	custom.ran = false
	e.now = e.now.Add(time.Hour)
	e.f.Buffer.Add(bottest.Group.String(), "Ana", "oi")
	e.rolls = []int{0, 90}
	e.f.InteractListener(context.Background(), bottest.GroupMsg("c"), e.fc)
	if custom.calls != 2 || len(e.llm.requests()) != 2 {
		t.Fatalf("fallback: calls=%d llm=%d", custom.calls, len(e.llm.requests()))
	}

	// useCmds desligado: sempre LLM, sem sortear proporção
	if _, err := e.store.UpdateGroup(context.Background(), bottest.Group.String(), func(g *store.Group) { g.Interact.UseCmds = false }); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(time.Hour)
	e.f.Buffer.Add(bottest.Group.String(), "Ana", "oi")
	e.rolls = []int{0, 99}
	e.f.InteractListener(context.Background(), bottest.GroupMsg("d"), e.fc)
	if custom.calls != 2 || len(e.llm.requests()) != 3 {
		t.Fatalf("useCmds=false: calls=%d llm=%d", custom.calls, len(e.llm.requests()))
	}
}

func TestAutoInteractSilentOnRefusal(t *testing.T) {
	e := newEnv(t, []string{"Não foi possível gerar."})
	e.f.Buffer.Add(bottest.Group.String(), "Ana", "oi")
	e.rolls = []int{0}
	e.f.InteractListener(context.Background(), bottest.GroupMsg("x"), e.fc)
	if len(e.fc.Replies) != 0 || e.f.Buffer.Recent(bottest.Group.String()) == nil {
		t.Fatal("recusa do modelo deveria ser descartada em silêncio")
	}
}

// --- sillies ---

func TestClassifySilly(t *testing.T) {
	cases := map[string]string{
		"ravena lixo":              catInsult,
		"Bot muito burro!":         catInsult,
		"odiei o bot":              catInsult,
		"cala a boca ravena":       catInsult,
		"`ravena inútil`":          catInsult,
		"Ravena é muito linda":     catCompliment,
		"obrigada ravena":          catCompliment,
		"te amo bot":               catCompliment,
		"Corvinha é perfeita":      catCompliment, // nome configurado
		"botei o lixo fora":        "",
		"o bot é lixo":             "",
		"ravena":                   "",
		"bom dia":                  "",
		"ravena me ajuda com isso": "",
		"lixo":                     "",
	}
	for text, want := range cases {
		if got := classifySilly(text, "Corvinha"); got != want {
			t.Errorf("%q: got %q, want %q", text, got, want)
		}
	}
}

func TestSillyListener(t *testing.T) {
	e := newEnv(t, []string{"Respeita, viu?"})
	e.disableInteract(t)
	ctx := context.Background()

	e.rolls = []int{99, 0} // 99 >= 25 → figurinha; escolhe a 1ª
	handled, err := e.f.SillyListener(ctx, bottest.GroupMsg("ravena lixo"), e.fc)
	if !handled || err != nil || e.fc.Last().Out.Kind != bot.OutSticker || len(e.fc.Last().Out.Data) == 0 {
		t.Fatalf("figurinha: handled=%v err=%v last=%+v", handled, err, e.fc.Last().Out.Kind)
	}

	// cooldown de 30 s por chat
	if handled, _ := e.f.SillyListener(ctx, bottest.GroupMsg("ravena lixo"), e.fc); handled {
		t.Fatal("cooldown ignorado")
	}

	e.now = e.now.Add(31 * time.Second)
	e.rolls = []int{0} // < 25 → LLM
	if handled, _ := e.f.SillyListener(ctx, bottest.GroupMsg("bot burro"), e.fc); !handled || lastText(t, e.fc) != "Respeita, viu?" {
		t.Fatal("deveria responder pelo LLM")
	}

	// elogio não tem figurinha: sempre LLM
	e.now = e.now.Add(31 * time.Second)
	e.rolls = []int{99}
	if handled, _ := e.f.SillyListener(ctx, bottest.GroupMsg("ravena linda"), e.fc); !handled || !strings.Contains(e.llm.lastBody(t), "te elogiou") {
		t.Fatal("elogio deveria ir ao LLM")
	}

	// texto comum não é tratado
	e.now = e.now.Add(31 * time.Second)
	if handled, _ := e.f.SillyListener(ctx, bottest.GroupMsg("bom dia gente"), e.fc); handled {
		t.Fatal("texto comum tratado")
	}

	// Silly desligado no grupo
	if _, err := e.store.UpdateGroup(ctx, bottest.Group.String(), func(g *store.Group) { g.Silly = false }); err != nil {
		t.Fatal(err)
	}
	if handled, _ := e.f.SillyListener(ctx, bottest.GroupMsg("ravena lixo"), e.fc); handled {
		t.Fatal("Silly=false deveria desligar")
	}
}

func TestSillyStickersEmbedded(t *testing.T) {
	if n := len(sillyStickers()[catInsult]); n != 2 {
		t.Fatalf("figurinhas de xingamento: %d", n)
	}
	if n := len(sillyStickers()[catCompliment]); n != 0 {
		t.Fatalf("figurinhas de elogio: %d", n)
	}
}

func TestSillyLLMFailureFallsBackToSticker(t *testing.T) {
	e := newEnv(t, []string{"!err"})
	e.rolls = []int{0, 0}
	handled, err := e.f.SillyListener(context.Background(), bottest.GroupMsg("ravena lixo"), e.fc)
	if !handled || err != nil || e.fc.Last().Out.Kind != bot.OutSticker {
		t.Fatalf("fallback: handled=%v err=%v", handled, err)
	}
}

// --- !ocr ---

func TestOCR(t *testing.T) {
	e := newEnv(t, []string{"  PROIBIDO ESTACIONAR\nguincho  ", "   "})
	e.say("!ocr")
	if got := lastText(t, e.fc); got != MsgOCRNoImage {
		t.Fatalf("sem imagem: %q", got)
	}
	m := bottest.GroupMsg("!ocr")
	m.Media = bottest.StaticMedia(bot.MediaImage, "image/jpeg", []byte("img"))
	e.router.Handle(context.Background(), m)
	if got := lastText(t, e.fc); got != "📝 *Texto Extraído:*\n\nPROIBIDO ESTACIONAR\nguincho" {
		t.Fatalf("ocr: %q", got)
	}
	if body := e.llm.lastBody(t); !strings.Contains(body, "data:image/jpeg;base64,") || !strings.Contains(body, `"max_tokens":1000`) {
		t.Fatalf("requisição: %s", body)
	}
	e.router.Handle(context.Background(), m)
	if got := lastText(t, e.fc); got != MsgOCREmpty {
		t.Fatalf("vazio: %q", got)
	}
}

// --- !tarot ---

func TestTarot(t *testing.T) {
	reading := "Situação: você está num momento de recomeço. Desafio: paciência. Caminho: confie no processo e invista na carreira."
	e := newEnv(t, []string{reading})
	e.rolls = []int{0, 0, 0, 0, 0} // cartas 0,1,2; frases 0,0
	e.say("!tarot vou mudar de emprego?")
	got := lastText(t, e.fc)
	for _, want := range []string{"*O Louco*, *O Mago*, *A Sacerdotisa*", "> " + reading, "Cartomante _Ravena_"} {
		if !strings.Contains(got, want) {
			t.Errorf("tiragem sem %q:\n%s", want, got)
		}
	}
	if body := e.llm.lastBody(t); !strings.Contains(body, "vou mudar de emprego?") || !strings.Contains(body, `"max_tokens":400`) {
		t.Fatalf("prompt: %s", body)
	}

	e.now = e.now.Add(24 * time.Hour)
	e.say("!tarot")
	if got := lastText(t, e.fc); !strings.Contains(got, "Destino em Pausa") || !strings.Contains(got, "6 dias") {
		t.Fatalf("cooldown: %q", got)
	}
	if len(e.llm.requests()) != 1 {
		t.Fatal("cooldown não deveria chamar o LLM")
	}
}

func TestTarotFallbackKeepsNoCooldown(t *testing.T) {
	e := newEnv(t, []string{"!err"})
	e.say("!tarot")
	got := lastText(t, e.fc)
	if !strings.Contains(got, "Significados Rápidos") || strings.Count(got, "• *") != 3 {
		t.Fatalf("fallback: %q", got)
	}
	var last int64
	if ok, _ := e.store.GetJSON(context.Background(), tarotKey(bottest.User.User), &last); ok {
		t.Fatal("falha do LLM não deveria gravar cooldown")
	}
}

func TestDrawCardsDistinct(t *testing.T) {
	f := &Feature{}
	for range 200 {
		c := f.drawCards()
		if c[0] == c[1] || c[1] == c[2] || c[0] == c[2] {
			t.Fatalf("cartas repetidas: %v", c)
		}
	}
}

// --- sem LLM ---

func TestNilLLM(t *testing.T) {
	e := newEnv(t, nil)
	for _, cmd := range []string{"!ai qual o sentido da vida", "!resumo", "!interagir", "!ocr"} {
		e.say(cmd)
		if got := lastText(t, e.fc); got != MsgUnavailable {
			t.Errorf("%s: %q", cmd, got)
		}
	}
	n := len(e.fc.Replies)
	ctx := context.Background()
	m := bottest.GroupMsg("@5562900000000 oi tudo bem?")
	m.MentionsBot = true
	e.rolls = []int{0, 0, 0}
	for _, l := range []bot.Listener{e.f.MentionListener, e.f.SillyListener, e.f.InteractListener} {
		if handled, err := l(ctx, m, e.fc); handled || err != nil {
			t.Fatalf("listener sem LLM tratou: %v %v", handled, err)
		}
	}
	if handled, _ := e.f.SillyListener(ctx, bottest.GroupMsg("ravena lixo"), e.fc); handled || len(e.fc.Replies) != n {
		t.Fatal("silly sem LLM deveria ficar desligado")
	}
	// tarot ainda funciona, com os significados prontos
	e.say("!tarot")
	if got := lastText(t, e.fc); !strings.Contains(got, "Significados Rápidos") {
		t.Fatalf("tarot sem LLM: %q", got)
	}
}

func TestNilLLMInteractStillRunsCustom(t *testing.T) {
	e := newEnv(t, nil)
	custom := &fakeCustom{ran: true}
	e.f.Custom = custom
	e.rolls = []int{0}
	if handled, _ := e.f.InteractListener(context.Background(), bottest.GroupMsg("x"), e.fc); handled || custom.calls != 1 {
		t.Fatalf("sem LLM deveria usar comando personalizado: calls=%d", custom.calls)
	}
}

func TestModuleRegister(t *testing.T) {
	if (Module{}).Name() != "ai" {
		t.Fatal("nome")
	}
}
