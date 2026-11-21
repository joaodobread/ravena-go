// Package anagram implementa o jogo Anagrama por rodadas (!anagrama, !ana,
// !ana-dica, !ana-pular, !anagrama-ranking, !anagrama-reset).
//
// O estado da partida ativa fica em memória (um por grupo, com teto); pontos
// e recordes ficam no SQLite. Cada partida tem um único timer (time.AfterFunc)
// que é trocado a cada rodada e cancelado no fim.
package anagram

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/joaodobread/ravena-go/internal/app"
	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/features/gamekit"
	"github.com/joaodobread/ravena-go/internal/llm"
)

//go:embed words.txt
var wordsFile string

var defaultWords = sync.OnceValue(func() []string { return parseWords(wordsFile) })

func parseWords(s string) []string {
	var out []string
	for _, w := range strings.Split(s, "\n") {
		if w = strings.TrimSpace(w); w != "" {
			out = append(out, w)
		}
	}
	return out
}

// Regras do jogo.
const (
	RoundTime     = 60 * time.Second
	NextDelay     = 2 * time.Second
	HintsPerGame  = 3
	SkipsPerGame  = 3
	MaxGames      = 200 // partidas simultâneas (teto de memória)
	minUnrevealed = 2
)

// Chatter é o que o jogo precisa do LLM (dica).
type Chatter interface {
	Chat(ctx context.Context, req llm.ChatRequest) (string, error)
}

// Config monta o jogo. Campos zerados recebem o padrão.
type Config struct {
	DB        *sql.DB
	Client    bot.Client // usado pelos timers (fora de um comando)
	LLM       Chatter    // nil: a dica só revela uma letra
	Schedule  gamekit.Scheduler
	Rand      func(n int) int
	Words     []string
	RoundTime time.Duration
	NextDelay time.Duration
	Log       *slog.Logger
}

type game struct {
	round      int
	hintsUsed  int
	skipsUsed  int
	maxHints   int
	word       string
	scrambled  string
	revealed   []bool
	roundEnded bool
	seq        int // identifica o timer atual
	stop       func() bool
}

// Game é a funcionalidade.
type Game struct {
	cfg  Config
	base context.Context

	mu    sync.Mutex
	games map[types.JID]*game
}

var migrations = []string{
	`CREATE TABLE anagram_groups (
		group_id        TEXT PRIMARY KEY,
		record_round    INTEGER NOT NULL DEFAULT 0,
		total_tries     INTEGER NOT NULL DEFAULT 0,
		total_tips_used INTEGER NOT NULL DEFAULT 0,
		last_updated    INTEGER
	)`,
	`CREATE TABLE anagram_scores (
		group_id        TEXT NOT NULL,
		user_id         TEXT NOT NULL,
		user_name       TEXT NOT NULL DEFAULT '',
		points          INTEGER NOT NULL DEFAULT 0,
		tries           INTEGER NOT NULL DEFAULT 0,
		correct_guesses INTEGER NOT NULL DEFAULT 0,
		tips_used       INTEGER NOT NULL DEFAULT 0,
		last_updated    INTEGER,
		PRIMARY KEY (group_id, user_id)
	)`,
}

// Migrations são as migrações do escopo "anagram".
func Migrations() []string { return migrations }

// New cria o jogo. base é o ctx de Register: quando termina, todas as
// partidas são canceladas.
func New(base context.Context, cfg Config) *Game {
	if cfg.Schedule == nil {
		cfg.Schedule = gamekit.RealScheduler
	}
	if cfg.Rand == nil {
		cfg.Rand = rand.IntN
	}
	if len(cfg.Words) == 0 {
		cfg.Words = defaultWords()
	}
	if cfg.RoundTime == 0 {
		cfg.RoundTime = RoundTime
	}
	if cfg.NextDelay == 0 {
		cfg.NextDelay = NextDelay
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	g := &Game{cfg: cfg, base: base, games: map[types.JID]*game{}}
	context.AfterFunc(base, g.stopAll)
	return g
}

func (g *Game) stopAll() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for chat, gm := range g.games {
		if gm.stop != nil {
			gm.stop()
		}
		delete(g.games, chat)
	}
}

// Active diz quantas partidas estão em andamento.
func (g *Game) Active() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.games)
}

// Commands devolve os comandos do jogo.
func (g *Game) Commands() []*bot.Command {
	return []*bot.Command{
		{
			Name: "anagrama", Aliases: []string{"anagram"}, Category: "jogos",
			Description: "Inicia uma partida do jogo Anagrama",
			Cooldown:    20 * time.Second, GroupOnly: true,
			Reactions: bot.Reactions{Before: "🧩", Error: "❌"},
			Run:       g.start,
		},
		{
			Name: "ana", Category: "jogos", Usage: "!ana <palpite>",
			Description: "Envia um palpite para o Anagrama",
			GroupOnly:   true,
			Run:         g.guess,
		},
		{
			Name: "ana-dica", Category: "jogos",
			Description: "Pede uma dica da palavra atual do Anagrama",
			Cooldown:    5 * time.Second, GroupOnly: true,
			Reactions: bot.Reactions{After: "📝", Error: "❌"},
			Run:       g.hint,
		},
		{
			Name: "ana-pular", Category: "jogos",
			Description: "Pula a palavra atual do Anagrama",
			Cooldown:    5 * time.Second, GroupOnly: true,
			Reactions: bot.Reactions{After: "⏭️", Error: "❌"},
			Run:       g.skip,
		},
		{
			Name: "anagrama-ranking", Aliases: []string{"a-rank", "anagramaranking"}, Category: "jogos",
			Description: "Mostra o ranking do Anagrama no grupo",
			Cooldown:    10 * time.Second, GroupOnly: true,
			Reactions: bot.Reactions{After: "🏆", Error: "❌"},
			Run:       g.ranking,
		},
		{
			Name: "anagrama-reset", Category: "jogos",
			Description: "Reseta o ranking e o recorde do Anagrama (admins)",
			Cooldown:    60 * time.Second, GroupOnly: true, AdminOnly: true,
			Reactions: bot.Reactions{Before: "🧹", After: "✅", Error: "❌"},
			Run:       g.reset,
		},
	}
}

// scramble embaralha word garantindo (se possível) um resultado diferente.
func (g *Game) scramble(word string) string {
	r := []rune(word)
	for range 10 {
		for i := len(r) - 1; i > 0; i-- {
			j := g.cfg.Rand(i + 1)
			r[i], r[j] = r[j], r[i]
		}
		if string(r) != word {
			break
		}
	}
	return string(r)
}

// newRound sorteia a palavra e arma o timer. Chamar com g.mu travado.
func (g *Game) newRound(chat types.JID, gm *game) {
	if gm.stop != nil {
		gm.stop()
	}
	word := g.cfg.Words[g.cfg.Rand(len(g.cfg.Words))]
	gm.word = word
	gm.scrambled = g.scramble(word)
	gm.revealed = make([]bool, len([]rune(word)))
	gm.roundEnded = false
	gm.maxHints = HintsPerGame + (gm.round-1)/3
	gm.seq++
	seq := gm.seq
	gm.stop = g.cfg.Schedule(g.cfg.RoundTime, func() { g.timeUp(chat, gm, seq) })
}

func spaced(s string) string {
	letters := make([]string, 0, len(s))
	for _, r := range strings.ToUpper(s) {
		letters = append(letters, string(r))
	}
	return strings.Join(letters, " ")
}

func roundText(round int, scrambled string, record int) string {
	return fmt.Sprintf("🧩 *ANAGRAMA* ⭐ Rodada %d · 🏆 Recorde: %d\n\n🔤 *%s*", round, record, spaced(scrambled))
}

func (g *Game) start(ctx context.Context, c *bot.Context) error {
	chat := c.Message.Chat
	g.mu.Lock()
	if gm, ok := g.games[chat]; ok {
		round, scr := gm.round, gm.scrambled
		g.mu.Unlock()
		return c.ReplyText(ctx, "Já existe uma partida em andamento na *rodada %d*! A palavra é: *%s*", round, spaced(scr))
	}
	if len(g.games) >= MaxGames {
		g.mu.Unlock()
		return bot.Userf("Muitas partidas de anagrama acontecendo agora. Tente daqui a pouco.")
	}
	gm := &game{round: 1}
	g.games[chat] = gm
	g.newRound(chat, gm)
	scr, hints := gm.scrambled, gm.maxHints
	g.mu.Unlock()

	rec, _ := g.groupStats(ctx, chat)
	if err := c.ReplyText(ctx, "%s", roundText(1, scr, rec.record)); err != nil {
		return err
	}
	return c.Client.Send(ctx, chat, bot.Text(fmt.Sprintf(
		"Use *%sana <palpite>* para responder.\nVocê tem %d segundos!\n\n> 💡 Dicas: %d | 🐇 Pulos: %d",
		c.Prefix, int(g.cfg.RoundTime/time.Second), hints, SkipsPerGame)))
}

// nextRound inicia a rodada seguinte (disparado depois do acerto/pulo).
func (g *Game) nextRound(chat types.JID, gm *game, seq int) {
	g.mu.Lock()
	if g.games[chat] != gm || gm.seq != seq {
		g.mu.Unlock()
		return
	}
	g.newRound(chat, gm)
	round, scr := gm.round, gm.scrambled
	g.mu.Unlock()

	ctx, cancel := gamekit.SendCtx(g.base)
	defer cancel()
	rec, _ := g.groupStats(ctx, chat)
	if err := g.cfg.Client.Send(ctx, chat, bot.Text(roundText(round, scr, rec.record))); err != nil {
		g.cfg.Log.Warn("anagrama: falha ao enviar rodada", "err", err, "chat", chat)
	}
}

// timeUp encerra a partida quando o tempo da rodada acaba.
func (g *Game) timeUp(chat types.JID, gm *game, seq int) {
	g.mu.Lock()
	if g.games[chat] != gm || gm.seq != seq || gm.roundEnded {
		g.mu.Unlock()
		return
	}
	delete(g.games, chat)
	round, word := gm.round, gm.word
	g.mu.Unlock()

	ctx, cancel := gamekit.SendCtx(g.base)
	defer cancel()
	rec, err := g.groupStats(ctx, chat)
	if err != nil {
		g.cfg.Log.Warn("anagrama: lendo recorde", "err", err)
	}
	newRecord := round > rec.record
	if newRecord {
		if _, err := g.cfg.DB.ExecContext(ctx, `INSERT INTO anagram_groups(group_id, record_round, last_updated) VALUES(?, ?, ?)
			ON CONFLICT(group_id) DO UPDATE SET record_round = MAX(record_round, excluded.record_round), last_updated = excluded.last_updated`,
			chat.String(), round, time.Now().Unix()); err != nil {
			g.cfg.Log.Warn("anagrama: gravando recorde", "err", err)
		}
	}
	players, err := g.top(ctx, chat, 10)
	if err != nil {
		g.cfg.Log.Warn("anagrama: lendo ranking", "err", err)
	}
	text := fmt.Sprintf("⏰ O tempo acabou! A palavra era *\"%s\"*.\n\nVocês chegaram à *rodada %d*!\n", word, round)
	if len(players) > 0 {
		text += rankingText(players, "\n🏆 *Ranking do Grupo*\n\n")
	} else {
		text += "Ninguém pontuou ainda."
	}
	if newRecord {
		text += "\n\n🏆 *NOVO RECORDE DO GRUPO!*"
	}
	if err := g.cfg.Client.Send(ctx, chat, bot.Text(text)); err != nil {
		g.cfg.Log.Warn("anagrama: falha ao enviar fim", "err", err, "chat", chat)
	}
}

func (g *Game) guess(ctx context.Context, c *bot.Context) error {
	chat := c.Message.Chat
	g.mu.Lock()
	gm, ok := g.games[chat]
	g.mu.Unlock()
	if !ok {
		return bot.ErrSilent // palpites sem partida são ignorados, como no original
	}
	if len(c.Args) == 0 {
		return bot.Userf("Você precisa fornecer um palpite. Ex: `%sana palavra`", c.Prefix)
	}
	guess := gamekit.Fold(c.Args[0])
	user := gamekit.UserID(c.Message)
	name := gamekit.Name(c.Message, "Jogador")
	if err := g.addStats(ctx, chat, user, name, stats{tries: 1}); err != nil {
		return err
	}

	g.mu.Lock()
	if g.games[chat] != gm {
		g.mu.Unlock()
		return bot.ErrSilent
	}
	word := gm.word
	if guess != gamekit.Fold(word) {
		g.mu.Unlock()
		_ = c.Client.React(ctx, c.Message, temperature(guess, gamekit.Fold(word)))
		return nil
	}
	if gm.roundEnded {
		g.mu.Unlock()
		return bot.ErrSilent
	}
	gm.roundEnded = true
	gm.round++
	round := gm.round
	g.scheduleNext(chat, gm)
	g.mu.Unlock()

	if err := g.addStats(ctx, chat, user, name, stats{points: 1, correct: 1}); err != nil {
		return err
	}
	_ = c.Client.React(ctx, c.Message, "✅")
	return c.ReplyText(ctx, "🎉 *%s acertou!* A palavra era *%s*.\n\n🔄 Iniciando próxima rodada... 🌟 *Rodada %d*", name, word, round)
}

// scheduleNext troca o timer da rodada pelo atraso até a próxima. Com g.mu travado.
func (g *Game) scheduleNext(chat types.JID, gm *game) {
	if gm.stop != nil {
		gm.stop()
	}
	gm.seq++
	seq := gm.seq
	gm.stop = g.cfg.Schedule(g.cfg.NextDelay, func() { g.nextRound(chat, gm, seq) })
}

// temperature diz o quão perto o palpite chegou: 🔥 quente, 🥵 morno, 🥶 frio.
func temperature(guess, word string) string {
	a, b := []rune(guess), []rune(word)
	sim := 1 - float64(levenshtein(a, b))/float64(max(len(a), len(b), 1))
	switch {
	case sim > 0.8:
		return "🔥"
	case sim > 0.5:
		return "🥵"
	}
	return "🥶"
}

func levenshtein(a, b []rune) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

func (g *Game) hint(ctx context.Context, c *bot.Context) error {
	chat := c.Message.Chat
	g.mu.Lock()
	gm, ok := g.games[chat]
	if !ok {
		g.mu.Unlock()
		return bot.Userf("Não há um jogo de anagrama em andamento.")
	}
	if gm.roundEnded {
		g.mu.Unlock()
		return bot.Userf("> Aguarde a próxima palavra!")
	}
	if gm.hintsUsed >= gm.maxHints {
		g.mu.Unlock()
		return bot.Userf("> As dicas para esta partida já acabaram!")
	}
	var hidden []int
	for i, r := range gm.revealed {
		if !r {
			hidden = append(hidden, i)
		}
	}
	if len(hidden) <= minUnrevealed {
		g.mu.Unlock()
		return bot.Userf("> A palavra já está muito revelada!")
	}
	gm.revealed[hidden[g.cfg.Rand(len(hidden))]] = true
	gm.hintsUsed++
	word := gm.word
	var b strings.Builder
	for i, r := range []rune(strings.ToUpper(word)) {
		if i > 0 {
			b.WriteByte(' ')
		}
		if gm.revealed[i] {
			b.WriteRune(r)
		} else {
			b.WriteString("_")
		}
	}
	left := gm.maxHints - gm.hintsUsed
	g.mu.Unlock()

	if err := g.addStats(ctx, chat, gamekit.UserID(c.Message), gamekit.Name(c.Message, "Jogador"), stats{tips: 1}); err != nil {
		return err
	}
	text := "📝 " + b.String() + "\n"
	if tip := g.aiHint(ctx, word); tip != "" {
		text += "\nℹ️ *Dica:* _" + tip + "_\n"
	}
	text += fmt.Sprintf("\n> 💡 Vocês têm mais %d dica(s) nesta partida.", left)
	return c.ReplyText(ctx, "%s", text)
}

// aiHint pede ao LLM um sinônimo/frase; qualquer falha (ou uma resposta que
// entregue a palavra) vira "".
func (g *Game) aiHint(ctx context.Context, word string) string {
	if g.cfg.LLM == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := g.cfg.LLM.Chat(ctx, llm.ChatRequest{
		System:      "Você é um robô que controla um jogo de Anagrama. Responda só com a dica, em português, sem dizer a palavra.",
		Messages:    []llm.Message{{Role: "user", Text: "Dê uma dica curta (sinônimo ou frase) para a palavra: " + word}},
		Temperature: 0.7,
		MaxTokens:   80,
	})
	if err != nil {
		g.cfg.Log.Warn("anagrama: dica da IA falhou", "err", err)
		return ""
	}
	out = strings.TrimSpace(out)
	if out == "" || len(out) > 300 || strings.Contains(gamekit.Fold(out), gamekit.Fold(word)) {
		return ""
	}
	return out
}

func (g *Game) skip(ctx context.Context, c *bot.Context) error {
	chat := c.Message.Chat
	g.mu.Lock()
	gm, ok := g.games[chat]
	if !ok {
		g.mu.Unlock()
		return bot.Userf("> Não há um jogo em andamento para pular.")
	}
	if gm.roundEnded {
		g.mu.Unlock()
		return bot.ErrSilent
	}
	if gm.skipsUsed >= SkipsPerGame {
		g.mu.Unlock()
		return bot.Userf("> Os pulos para esta partida já acabaram!")
	}
	gm.roundEnded = true
	gm.skipsUsed++
	word, left, round := gm.word, SkipsPerGame-gm.skipsUsed, gm.round
	g.scheduleNext(chat, gm)
	g.mu.Unlock()
	return c.ReplyText(ctx, "⏭️ A palavra *\"%s\"* foi pulada!\n\n> 🐇 Pulos restantes: %d\n\n🔄 Carregando nova palavra para a *rodada %d*...", word, left, round)
}

type groupRow struct{ record, tries, tips int }

func (g *Game) groupStats(ctx context.Context, chat types.JID) (groupRow, error) {
	var r groupRow
	err := g.cfg.DB.QueryRowContext(ctx, `SELECT record_round, total_tries, total_tips_used FROM anagram_groups WHERE group_id = ?`,
		chat.String()).Scan(&r.record, &r.tries, &r.tips)
	if err == sql.ErrNoRows {
		err = nil
	}
	return r, err
}

type stats struct{ points, tries, correct, tips int }

// addStats soma s no jogador e no grupo.
func (g *Game) addStats(ctx context.Context, chat, user types.JID, name string, s stats) error {
	now := time.Now().Unix()
	if _, err := g.cfg.DB.ExecContext(ctx, `INSERT INTO anagram_scores(group_id, user_id, user_name, points, tries, correct_guesses, tips_used, last_updated)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(group_id, user_id) DO UPDATE SET
			user_name = excluded.user_name,
			points = points + excluded.points,
			tries = tries + excluded.tries,
			correct_guesses = correct_guesses + excluded.correct_guesses,
			tips_used = tips_used + excluded.tips_used,
			last_updated = excluded.last_updated`,
		chat.String(), user.String(), name, s.points, s.tries, s.correct, s.tips, now); err != nil {
		return fmt.Errorf("anagrama: gravando jogador: %w", err)
	}
	if s.tries == 0 && s.tips == 0 {
		return nil
	}
	if _, err := g.cfg.DB.ExecContext(ctx, `INSERT INTO anagram_groups(group_id, total_tries, total_tips_used, last_updated) VALUES(?, ?, ?, ?)
		ON CONFLICT(group_id) DO UPDATE SET
			total_tries = total_tries + excluded.total_tries,
			total_tips_used = total_tips_used + excluded.total_tips_used,
			last_updated = excluded.last_updated`,
		chat.String(), s.tries, s.tips, now); err != nil {
		return fmt.Errorf("anagrama: gravando grupo: %w", err)
	}
	return nil
}

type player struct {
	name   string
	points int
}

func (g *Game) top(ctx context.Context, chat types.JID, limit int) ([]player, error) {
	rows, err := g.cfg.DB.QueryContext(ctx, `SELECT user_name, points FROM anagram_scores
		WHERE group_id = ? AND points > 0 ORDER BY points DESC, correct_guesses DESC LIMIT ?`, chat.String(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []player
	for rows.Next() {
		var p player
		if err := rows.Scan(&p.name, &p.points); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

var rankEmojis = []string{"🥇", "🥈", "🥉", "🏅", "🎖"}

func rankingText(players []player, header string) string {
	var b strings.Builder
	b.WriteString(header)
	for i, p := range players {
		emoji := "🔸"
		if i < len(rankEmojis) {
			emoji = rankEmojis[i]
		}
		name := strings.TrimSpace(p.name)
		if name == "" {
			name = "Jogador"
		}
		fmt.Fprintf(&b, "%s %s - Pontuação: *%d*\n", emoji, name, p.points)
	}
	return b.String()
}

func (g *Game) ranking(ctx context.Context, c *bot.Context) error {
	chat := c.Message.Chat
	rec, err := g.groupStats(ctx, chat)
	if err != nil {
		return err
	}
	players, err := g.top(ctx, chat, 15)
	if err != nil {
		return err
	}
	if len(players) == 0 && rec.record == 0 {
		return c.ReplyText(ctx, "🏆 Ainda não há dados de Anagrama para este grupo. Comece a jogar com `%sanagrama`!", c.Prefix)
	}
	text := "📊 *Ainda não há jogadores no ranking.*\n"
	if len(players) > 0 {
		text = rankingText(players, "🏆 *Ranking do Grupo*\n\n")
	}
	text += fmt.Sprintf("\n📈 *Recorde do Grupo:* %d rodadas", rec.record)
	if rec.tries > 0 {
		text += fmt.Sprintf("\n🎯 *Total de Tentativas do Grupo:* %d", rec.tries)
	}
	return c.ReplyText(ctx, "%s", text)
}

func (g *Game) reset(ctx context.Context, c *bot.Context) error {
	chat := c.Message.Chat
	g.mu.Lock()
	gm, active := g.games[chat]
	if active {
		if gm.stop != nil {
			gm.stop()
		}
		delete(g.games, chat)
	}
	g.mu.Unlock()
	if active {
		if err := c.Client.Send(ctx, chat, bot.Text("⚙️ O jogo de anagrama foi resetado por um administrador.")); err != nil {
			return err
		}
	}
	for _, q := range []string{`DELETE FROM anagram_groups WHERE group_id = ?`, `DELETE FROM anagram_scores WHERE group_id = ?`} {
		if _, err := g.cfg.DB.ExecContext(ctx, q, chat.String()); err != nil {
			return fmt.Errorf("anagrama: reset: %w", err)
		}
	}
	return c.ReplyText(ctx, "✅ O ranking e o recorde do Anagrama deste grupo foram resetados!")
}

// Module adapta o jogo ao contrato app.Feature.
type Module struct{}

// Name implementa app.Feature.
func (Module) Name() string { return "anagram" }

// Register implementa app.Feature.
func (Module) Register(ctx context.Context, d *app.Deps) error {
	if err := d.Store.Migrate(ctx, "anagram", migrations); err != nil {
		return err
	}
	cfg := Config{DB: d.Store.DB(), Client: d.Client, Log: d.Log}
	if d.LLM != nil {
		cfg.LLM = d.LLM
	}
	return d.Router.Register(New(ctx, cfg).Commands()...)
}
