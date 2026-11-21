// Package ai reúne os comandos e comportamentos que usam LLM: !ai, !resumo,
// !interagir, !ocr, !tarot, a resposta a menções ao bot, a interação
// automática nos grupos e as respostas a elogios/xingamentos.
package ai

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"regexp"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/joaodobread/ravena-go/internal/app"
	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/llm"
	"github.com/joaodobread/ravena-go/internal/store"
)

// CustomRunner roda um comando personalizado aleatório do grupo (vem da
// funcionalidade de comandos personalizados). Devolve false se o grupo não
// tem nenhum comando elegível para interação.
type CustomRunner interface {
	RunRandom(ctx context.Context, msg *bot.Message) (bool, error)
}

// Chatter é o que a funcionalidade usa do llm.Client.
type Chatter interface {
	Chat(ctx context.Context, req llm.ChatRequest) (string, error)
}

// Framer extrai um quadro JPEG pequeno de imagem/vídeo (media.Converter).
type Framer interface {
	Frame(ctx context.Context, in []byte) ([]byte, error)
}

// Mensagens mostradas ao usuário.
const (
	MsgUnavailable = "IA indisponível no momento."
	MsgAIUsage     = "Por favor, forneça uma pergunta ou uma imagem com uma pergunta. Exemplo: !ai Qual é a capital da França?"
	MsgAIError     = "Desculpe, encontrei um erro ao processar sua solicitação."
	MsgMentionHi   = "Olá! Como posso te ajudar? Você pode tirar dúvida de quais comandos eu tenho e também como usar eles, com exemplos, é só pedir! Se quiser saber meus comandos, envie !cmd"
	MsgMediaKind   = "Ainda não processo este tipo de arquivo 😟 Consigo apenas analisar imagens e vídeos!"
)

// Loading é a reação enquanto o LLM trabalha.
const Loading = "⌛️"

// Module adapta a funcionalidade ao contrato app.Feature.
type Module struct {
	// Custom, se definido, permite à interação automática rodar comandos
	// personalizados do grupo. nil = a interação usa sempre o LLM.
	Custom CustomRunner
}

// Name implementa app.Feature.
func (Module) Name() string { return "ai" }

// Register implementa app.Feature. Listeners, em ordem: menção ao bot,
// interações bobas e interação automática (esta nunca "trata" a mensagem).
func (m Module) Register(ctx context.Context, d *app.Deps) error {
	f := &Feature{
		Router:  d.Router,
		Store:   d.Store,
		Custom:  m.Custom,
		BotName: d.BotName,
		Log:     d.Log,
		Buffer:  NewBuffer(0, 0),
		Ctx:     ctx,
	}
	if d.LLM != nil { // evita interface não-nil com ponteiro nil
		f.LLM = d.LLM
	}
	if d.Media != nil {
		f.Media = d.Media
	}
	if err := d.Router.Register(f.Commands()...); err != nil {
		return err
	}
	d.Router.Observe(f.Observe)
	d.Router.Listen(f.MentionListener)
	d.Router.Listen(f.SillyListener)
	d.Router.Listen(f.InteractListener)
	return nil
}

// Feature guarda as dependências; os campos opcionais podem ficar zerados.
type Feature struct {
	LLM     Chatter // nil = IA indisponível
	Media   Framer  // nil = imagens vão cruas (só JPEG/PNG)
	Router  *bot.Router
	Store   *store.Store
	Custom  CustomRunner
	BotName func() string
	Log     *slog.Logger
	Buffer  *Buffer
	// Ctx vive enquanto o bot viver (interações automáticas em segundo plano).
	Ctx context.Context

	// Injeção para testes.
	Now  func() time.Time
	Rand func(n int) int // [0, n)
	// Go roda fn em segundo plano (testes trocam por chamada direta).
	Go func(fn func())

	interactMu sync.Mutex
	sillyMu    sync.Mutex
	sillyLast  map[string]time.Time
}

func (f *Feature) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

func (f *Feature) randN(n int) int {
	if f.Rand != nil {
		return f.Rand(n)
	}
	return rand.IntN(n)
}

func (f *Feature) spawn(fn func()) {
	if f.Go != nil {
		f.Go(fn)
		return
	}
	go fn()
}

func (f *Feature) name() string {
	if f.BotName != nil {
		if n := f.BotName(); n != "" {
			return n
		}
	}
	return "ravena"
}

func (f *Feature) log() *slog.Logger {
	if f.Log != nil {
		return f.Log
	}
	return slog.Default()
}

func (f *Feature) background() context.Context {
	if f.Ctx != nil {
		return f.Ctx
	}
	return context.Background()
}

// Commands devolve os comandos para registrar no Router.
func (f *Feature) Commands() []*bot.Command {
	return []*bot.Command{
		{
			Name:        "ai",
			Aliases:     []string{"ia", "gpt", "gemini"},
			Description: "Pergunte algo à IA",
			Usage:       "!ai <pergunta> (pode responder a uma imagem)",
			Category:    "ia",
			Reactions:   bot.Reactions{Before: Loading, After: "🤖"},
			Triggers:    []string{"🤖"},
			Cooldown:    30 * time.Second,
			Run:         f.runAI,
		},
		{
			Name:        "resumo",
			Description: "Resume conversas recentes do grupo",
			Category:    "ia",
			Reactions:   bot.Reactions{Before: Loading, After: "📋"},
			Triggers:    []string{"🦜"},
			Cooldown:    300 * time.Second,
			GroupOnly:   true,
			Run:         f.runResumo,
		},
		{
			Name:        "interagir",
			Description: "Gera uma mensagem interativa baseada na conversa",
			Category:    "ia",
			Reactions:   bot.Reactions{Before: Loading, After: "💬"},
			Cooldown:    150 * time.Second,
			GroupOnly:   true,
			Run:         f.runInteragir,
		},
		{
			Name:        "ocr",
			Description: "Extrai texto de uma imagem usando IA",
			Category:    "utilidades",
			Reactions:   bot.Reactions{Before: Loading, After: "📝"},
			Triggers:    []string{"🔍"},
			Cooldown:    20 * time.Second,
			Run:         f.runOCR,
		},
		{
			Name:        "tarot",
			Description: "Consulta a cartomante para uma tiragem",
			Usage:       "!tarot [pergunta]",
			Category:    "jogos",
			Reactions:   bot.Reactions{Before: "🕯️", After: "🔮"},
			Run:         f.runTarot,
		},
	}
}

// Observe guarda as mensagens de texto dos grupos para resumo/interação.
func (f *Feature) Observe(ctx context.Context, msg *bot.Message) {
	if !msg.IsGroup || strings.TrimSpace(msg.Text) == "" {
		return
	}
	if f.Router != nil {
		if p := f.Router.PrefixFor(ctx, msg); p != "" && strings.HasPrefix(strings.TrimSpace(msg.Text), p) {
			return // comandos não entram no histórico
		}
	}
	f.Buffer.Add(msg.Chat.String(), authorName(msg), msg.Text)
}

func authorName(msg *bot.Message) string {
	if msg.PushName != "" {
		return msg.PushName
	}
	return msg.Sender.User
}

// systemPrompt é uma versão enxuta de data/textos/llm_context*.txt.
func (f *Feature) systemPrompt(task string) string {
	s := fmt.Sprintf("Responda sempre em português do Brasil. Você é %s, um bot de WhatsApp gratuito e de código aberto (criado por moothz). "+
		"Não se apresente, a não ser que perguntem sobre você. Você ajuda grupos e streamers com jogos, figurinhas, downloads e IA; a lista de comandos sai com !cmd. "+
		"Seja sucinto. Formatação do WhatsApp: *negrito*, _itálico_, ~riscado~, `mono`, > citação, listas com -.", f.name())
	if task != "" {
		s += "\n" + task
	}
	return s
}

// image devolve um JPEG pequeno da mídia (ou nil se não houver mídia).
// Áudios e documentos dão UserError.
func (f *Feature) image(ctx context.Context, m *bot.Media) ([]byte, error) {
	if m == nil {
		return nil, nil
	}
	switch m.Kind {
	case bot.MediaImage, bot.MediaVideo, bot.MediaGIF, bot.MediaSticker:
	default:
		return nil, bot.Userf(MsgMediaKind)
	}
	data, err := m.Download(ctx)
	if err != nil {
		return nil, bot.Userf("⚠️ Não foi possível recuperar a mídia da mensagem marcada. Ela pode ter saído do cache ou o download falhou.")
	}
	if f.Media != nil {
		frame, err := f.Media.Frame(ctx, data)
		if err == nil {
			return frame, nil
		}
		f.log().Warn("falha ao extrair quadro", "err", err)
	}
	if m.Kind == bot.MediaImage && (m.Mime == "image/jpeg" || m.Mime == "image/png") {
		return data, nil
	}
	return nil, fmt.Errorf("sem conversor para %s", m.Mime)
}

func (f *Feature) runAI(ctx context.Context, c *bot.Context) error {
	if f.LLM == nil {
		return bot.Userf(MsgUnavailable)
	}
	msg := c.Message
	question := strings.TrimSpace(c.RawArgs())
	if msg.Quoted != nil && c.Reaction == nil {
		if q := strings.TrimSpace(msg.Quoted.Text); len(q) > 10 {
			question += "\n\nContexto da mensagem respondida: " + truncate(q, 1000)
		}
	}
	src := msg.AnyMedia()
	img, err := f.image(ctx, src)
	if err != nil {
		return err
	}
	if img == nil && len([]rune(question)) < 5 {
		return bot.Userf(MsgAIUsage)
	}

	req := llm.ChatRequest{Temperature: 0.7, MaxTokens: 600}
	user := llm.Message{Role: "user", Text: question}
	if img != nil {
		task := "Seu objetivo agora é analisar a imagem recebida, atendendo ao pedido do usuário (se houver)."
		if src.Kind == bot.MediaVideo || src.Kind == bot.MediaGIF {
			task = "Você recebeu o primeiro quadro de um vídeo; analise-o atendendo ao pedido do usuário (se houver)."
		}
		req.System = f.systemPrompt(task)
		if len([]rune(question)) < 4 {
			user.Text = "Analise esta imagem e entregue um resumo detalhado"
		}
		user.Images = [][]byte{img}
	} else {
		req.System = f.systemPrompt("Responda de forma útil e direta.")
	}
	if name := msg.PushName; name != "" {
		req.System = "Nome de quem enviou o prompt: " + name + "\n" + req.System
	}
	req.Messages = []llm.Message{user}

	answer, err := f.LLM.Chat(ctx, req)
	if err != nil {
		f.log().Error("ai: LLM falhou", "err", err)
		return bot.Userf(MsgAIError)
	}
	return c.Reply(ctx, bot.Text(strings.TrimSpace(answer)))
}

// leadingMentions casa "@123..." no começo do texto (a menção ao bot).
var leadingMentions = regexp.MustCompile(`^(\s*@\d+\s*)+`)

// MentionListener responde como o !ai quando o bot é marcado num grupo.
func (f *Feature) MentionListener(ctx context.Context, msg *bot.Message, client bot.Client) (bool, error) {
	if !msg.IsGroup || !msg.MentionsBot || f.LLM == nil {
		return false, nil
	}
	text := strings.TrimSpace(leadingMentions.ReplaceAllString(msg.Text, ""))
	if f.Router != nil {
		if p := f.Router.PrefixFor(ctx, msg); p != "" && strings.HasPrefix(text, p) {
			return false, nil // parece comando: não é conversa com a IA
		}
	}
	if text == "" && msg.AnyMedia() == nil {
		if err := client.Reply(ctx, msg, bot.Text(MsgMentionHi)); err != nil {
			return true, err
		}
		return true, client.React(ctx, msg, "🤖")
	}
	cmd, ok := f.Router.Lookup("ai")
	if !ok {
		return false, nil
	}
	// Router.Run aplica mute/cooldown/pausa e as reações do !ai.
	f.Router.Run(ctx, msg, cmd, strings.Fields(text))
	return true, nil
}

// mentionsIn transforma "@5511..." da resposta do LLM em menções.
var mentionNumber = regexp.MustCompile(`@(\d{8,})`)

func mentionsIn(text string) []types.JID {
	var out []types.JID
	for _, m := range mentionNumber.FindAllStringSubmatch(text, 10) {
		out = append(out, types.NewJID(m[1], types.DefaultUserServer))
	}
	return out
}
