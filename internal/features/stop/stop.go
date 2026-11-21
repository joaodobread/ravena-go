// Package stop implementa o Stop/Adedonha (!adedonha, !stop): no grupo o
// comando sorteia letra e categorias e abre a rodada; cada jogador copia o
// gabarito, preenche e manda no privado do bot. Quando o tempo acaba, o LLM
// corrige as respostas (sem LLM, vale a primeira letra) e o grupo recebe o
// resultado.
package stop

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/joaodobread/ravena-go/internal/app"
	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/features/gamekit"
	"github.com/joaodobread/ravena-go/internal/llm"
)

// Regras do jogo.
const (
	DefaultDuration   = 90 * time.Second
	MaxMinutes        = 15
	DefaultCategories = 5
	MaxCategories     = 10
	PointsPerAnswer   = 10
	MaxGames          = 100 // rodadas simultâneas
	MaxPlayers        = 100 // jogadores por rodada
	maxAnswerLen      = 60
)

// Categories são as categorias sorteáveis.
var Categories = []string{
	"Alimento", "Fruta", "Animal", "Celebridade", "Carro", "CEP", "Cor", "Esporte",
	"Filme", "Série", "Banda", "Marca", "Meios de Transporte", "Partes do Corpo", "Flor", "Objeto",
}

// Letters são as letras sorteáveis.
var Letters = strings.Split("ABCDEFGHIJLMNOPQRSTUV", "")

// Chatter é o que o jogo precisa do LLM (correção).
type Chatter interface {
	Chat(ctx context.Context, req llm.ChatRequest) (string, error)
}

// Config monta o jogo. Campos zerados recebem o padrão.
type Config struct {
	Client   bot.Client // usado pelo timer de fim
	LLM      Chatter    // nil: validação só pela primeira letra
	Schedule gamekit.Scheduler
	Rand     func(n int) int
	Now      func() time.Time
	Log      *slog.Logger
}

type player struct {
	jid     types.JID
	name    string
	answers map[string]string // categoria → resposta
	valid   map[string]bool
	score   int
}

type round struct {
	letter     string
	categories []string
	ends       time.Time
	players    []*player
	stop       func() bool
}

// Game é a funcionalidade.
type Game struct {
	cfg  Config
	base context.Context

	mu     sync.Mutex
	rounds map[types.JID]*round
}

// New cria o jogo; quando base termina, as rodadas são canceladas.
func New(base context.Context, cfg Config) *Game {
	if cfg.Schedule == nil {
		cfg.Schedule = gamekit.RealScheduler
	}
	if cfg.Rand == nil {
		cfg.Rand = rand.IntN
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	g := &Game{cfg: cfg, base: base, rounds: map[types.JID]*round{}}
	context.AfterFunc(base, g.stopAll)
	return g
}

func (g *Game) stopAll() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for chat, r := range g.rounds {
		r.stop()
		delete(g.rounds, chat)
	}
}

// Active diz quantas rodadas estão em andamento.
func (g *Game) Active() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.rounds)
}

// Commands devolve os comandos do jogo.
func (g *Game) Commands() []*bot.Command {
	return []*bot.Command{{
		Name: "adedonha", Aliases: []string{"stop"}, Category: "jogos",
		Usage:       "!stop [minutos] [categorias]",
		Description: "Inicia uma rodada de Stop/Adedonha (respostas no privado)",
		Reactions:   bot.Reactions{Before: "🛑", After: "📝", Error: "❌"},
		Run: func(ctx context.Context, c *bot.Context) error {
			if c.Message.IsGroup {
				return g.start(ctx, c)
			}
			return g.answer(ctx, c)
		},
	}}
}

func remaining(d time.Duration) string {
	s := max(0, int((d+time.Second-1)/time.Second))
	return fmt.Sprintf("%dm %ds", s/60, s%60)
}

func (g *Game) start(ctx context.Context, c *bot.Context) error {
	chat := c.Message.Chat
	duration, ncat := DefaultDuration, DefaultCategories
	if len(c.Args) > 0 {
		if n, err := strconv.Atoi(c.Args[0]); err == nil && n > 0 && n <= MaxMinutes {
			duration = time.Duration(n) * time.Minute
		}
	}
	if len(c.Args) > 1 {
		if n, err := strconv.Atoi(c.Args[1]); err == nil && n > 0 && n <= MaxCategories {
			ncat = n
		}
	}

	g.mu.Lock()
	if r, ok := g.rounds[chat]; ok {
		left := r.ends.Sub(g.cfg.Now())
		g.mu.Unlock()
		return bot.Userf("🛑 Já existe um jogo de Stop/Adedonha em andamento! Me mande as respostas no privado.\n\n- 🕐 Tempo restante: %s.", remaining(left))
	}
	if len(g.rounds) >= MaxGames {
		g.mu.Unlock()
		return bot.Userf("Muitas rodadas de Stop acontecendo agora. Tente daqui a pouco.")
	}
	pool := slices.Clone(Categories)
	cats := make([]string, 0, ncat)
	for range ncat {
		i := g.cfg.Rand(len(pool))
		cats = append(cats, pool[i])
		pool = slices.Delete(pool, i, i+1)
	}
	r := &round{letter: Letters[g.cfg.Rand(len(Letters))], categories: cats, ends: g.cfg.Now().Add(duration)}
	r.stop = g.cfg.Schedule(duration, func() { g.end(chat, r) })
	g.rounds[chat] = r
	g.mu.Unlock()

	mins, secs := int(duration/time.Minute), int(duration%time.Minute/time.Second)
	limit := fmt.Sprintf("%dm", mins)
	if secs > 0 {
		limit += fmt.Sprintf(" %ds", secs)
	}
	if err := c.ReplyText(ctx, "🛑 *STOP!* ✋ - Letra: *%s*\n\nVocês têm %s para responder! 🕐\nTodas as respostas devem começar com a letra *%s*.\nCopie a mensagem a seguir, preencha as respostas e envie no meu privado!",
		r.letter, limit, r.letter); err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%sstop %s\n🛑 *STOP!* ✋ - Letra: *%s*\n\n", c.Prefix, chat.String(), r.letter)
	for _, cat := range cats {
		fmt.Fprintf(&b, "- %s:\n", cat)
	}
	return c.Client.Send(ctx, chat, bot.Text(strings.TrimRight(b.String(), "\n")))
}

var groupRe = regexp.MustCompile(`\b\d+(?:-\d+)?@g\.us\b`)

// parseAnswers lê linhas "Categoria: resposta" do gabarito preenchido.
func parseAnswers(text string, categories []string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		label, answer, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		label = gamekit.Fold(strings.Trim(strings.TrimSpace(label), "-*• "))
		for _, cat := range categories {
			if label == gamekit.Fold(cat) {
				a := strings.TrimSpace(answer)
				if r := []rune(a); len(r) > maxAnswerLen {
					a = string(r[:maxAnswerLen])
				}
				out[cat] = a
			}
		}
	}
	return out
}

func (g *Game) answer(ctx context.Context, c *bot.Context) error {
	m := groupRe.FindString(c.Message.Text)
	if m == "" {
		return bot.Userf("🛑 Para jogar, use *%sstop* num grupo e me mande aqui o gabarito preenchido (copie a mensagem do grupo, com a primeira linha).", c.Prefix)
	}
	chat, err := types.ParseJID(m)
	if err != nil {
		return bot.Userf("🫸 Não reconheci o grupo desse jogo.")
	}
	user := gamekit.UserID(c.Message)

	g.mu.Lock()
	r, ok := g.rounds[chat]
	var cats []string
	if ok {
		cats = r.categories
	}
	g.mu.Unlock()
	if !ok {
		return bot.Userf("🫸 Não há um jogo ativo nesse grupo.")
	}
	if info := gamekit.Group(ctx, c.Client, chat); info != nil && len(info.Participants) > 0 {
		if _, member := info.Member(user); !member {
			if _, alt := info.Member(c.Message.SenderAlt); !alt {
				return bot.Userf("🫸 Você não está nesse grupo.")
			}
		}
		user = gamekit.Author(info, c.Message)
	}
	answers := parseAnswers(c.Message.Text, cats)
	name := gamekit.Name(c.Message, "Jogador")

	g.mu.Lock()
	if g.rounds[chat] != r || !g.cfg.Now().Before(r.ends) {
		g.mu.Unlock()
		return bot.Userf("🫸 O último jogo nesse grupo já acabou!")
	}
	emoji := "✅"
	idx := slices.IndexFunc(r.players, func(p *player) bool { return p.jid.User == user.User })
	switch {
	case idx >= 0:
		r.players[idx].answers, r.players[idx].name = answers, name
		emoji = "🔄"
	case len(r.players) >= MaxPlayers:
		g.mu.Unlock()
		return bot.Userf("🫸 Essa rodada já está cheia.")
	default:
		r.players = append(r.players, &player{jid: user, name: name, answers: answers})
	}
	left := r.ends.Sub(g.cfg.Now())
	g.mu.Unlock()

	_ = c.Client.React(ctx, c.Message, emoji)
	return c.ReplyText(ctx, "%s Recebi suas respostas!\n- 🕐 Tempo restante: %s", emoji, remaining(left))
}

// StartsWith diz se answer começa com letter, ignorando acentos e caixa.
func StartsWith(answer, letter string) bool {
	a := gamekit.Fold(answer)
	return a != "" && strings.HasPrefix(a, gamekit.Fold(letter))
}

// end fecha a rodada, corrige e publica o resultado.
func (g *Game) end(chat types.JID, r *round) {
	g.mu.Lock()
	if g.rounds[chat] != r {
		g.mu.Unlock()
		return
	}
	delete(g.rounds, chat)
	g.mu.Unlock()
	// Daqui em diante r não é mais visto por ninguém: dá para usar sem trava.

	ctx, cancel := gamekit.SendCtx(g.base)
	defer cancel()
	send := func(out bot.Outgoing) {
		if err := g.cfg.Client.Send(ctx, chat, out); err != nil {
			g.cfg.Log.Warn("stop: falha ao enviar", "err", err, "chat", chat)
		}
	}
	if len(r.players) == 0 {
		send(bot.Text("📝 O jogo de Stop/Adedonha foi cancelado: ninguém mandou respostas."))
		return
	}
	send(bot.Text("😴 Tempo esgotado para o Stop/Adedonha!\n\nAguarde, analisando respostas..."))
	g.score(ctx, r)
	send(results(r))
}

// score corrige as respostas: o LLM julga e a primeira letra é sempre exigida.
func (g *Game) score(ctx context.Context, r *round) {
	verdict := g.judge(ctx, r)
	for i, p := range r.players {
		p.valid = map[string]bool{}
		for _, cat := range r.categories {
			ok := StartsWith(p.answers[cat], r.letter)
			if verdict != nil {
				ok = ok && verdict[strconv.Itoa(i)][cat]
			}
			p.valid[cat] = ok
			if ok {
				p.score += PointsPerAnswer
			}
		}
	}
}

const judgeSystem = `Você é o juiz de um jogo de "Stop"/"Adedonha". Recebe um JSON com a letra da rodada e, para cada jogador (chave numérica), as respostas por categoria. Uma resposta é válida se começa com a letra e realmente pertence à categoria. Seja rigoroso, mas justo. Responda SOMENTE com um JSON no formato {"<jogador>": {"<categoria>": true|false}}.`

// judge pergunta ao LLM; nil quando indisponível ou a resposta não parseia.
func (g *Game) judge(ctx context.Context, r *round) map[string]map[string]bool {
	if g.cfg.LLM == nil {
		return nil
	}
	in := map[string]any{"letra": r.letter}
	players := map[string]map[string]string{}
	for i, p := range r.players {
		answers := map[string]string{}
		for _, cat := range r.categories {
			answers[cat] = p.answers[cat]
		}
		players[strconv.Itoa(i)] = answers
	}
	in["respostas"] = players
	prompt, _ := json.Marshal(in)

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := g.cfg.LLM.Chat(ctx, llm.ChatRequest{
		System:    judgeSystem,
		Messages:  []llm.Message{{Role: "user", Text: string(prompt)}},
		MaxTokens: 200 + 40*len(r.players)*len(r.categories),
	})
	if err != nil {
		g.cfg.Log.Warn("stop: correção pelo LLM falhou", "err", err)
		return nil
	}
	start, end := strings.Index(out, "{"), strings.LastIndex(out, "}")
	if start < 0 || end <= start {
		g.cfg.Log.Warn("stop: resposta do LLM sem JSON")
		return nil
	}
	var verdict map[string]map[string]bool
	if err := json.Unmarshal([]byte(out[start:end+1]), &verdict); err != nil {
		g.cfg.Log.Warn("stop: JSON do LLM inválido", "err", err)
		return nil
	}
	return verdict
}

func results(r *round) bot.Outgoing {
	players := slices.Clone(r.players)
	slices.SortStableFunc(players, func(a, b *player) int { return b.score - a.score })
	var b strings.Builder
	fmt.Fprintf(&b, "🏁 *Resultados do Stop/Adedonha - Letra: %s*\n\n*Classificação:*\n", r.letter)
	for i, p := range players {
		fmt.Fprintf(&b, "%s %s: %d pontos\n", gamekit.Medal(i), p.name, p.score)
	}
	win := players[0]
	fmt.Fprintf(&b, "\n🏆 *Vencedor:* %s\n\n*Respostas enviadas:*\n", bot.MentionText(win.jid))
	for i, p := range players {
		fmt.Fprintf(&b, "%s *%s*\n", gamekit.Medal(i), p.name)
		for _, cat := range r.categories {
			status := "❌"
			if p.valid[cat] {
				status = "✅"
			}
			a := p.answers[cat]
			if a == "" {
				a = "(não respondeu)"
			}
			fmt.Fprintf(&b, "- %s %s: %s\n", status, cat, a)
		}
		b.WriteString("\n")
	}
	return bot.Outgoing{Kind: bot.OutText, Text: strings.TrimRight(b.String(), "\n"), Mentions: []types.JID{win.jid}}
}

// Module adapta o jogo ao contrato app.Feature.
type Module struct{}

// Name implementa app.Feature.
func (Module) Name() string { return "stop" }

// Register implementa app.Feature.
func (Module) Register(ctx context.Context, d *app.Deps) error {
	cfg := Config{Client: d.Client, Log: d.Log}
	if d.LLM != nil {
		cfg.LLM = d.LLM
	}
	return d.Router.Register(New(ctx, cfg).Commands()...)
}
