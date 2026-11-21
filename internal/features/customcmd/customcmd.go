// Package customcmd implementa os comandos personalizados por grupo (a família
// !g-addCmd do Ravena): criação pelos admins, respostas em texto ou mídia,
// variáveis ({pessoa}, {mention}...), gatilhos sem prefixo e a página do painel.
package customcmd

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/joaodobread/ravena-go/internal/app"
	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/store"
)

// Category é a categoria dos comandos personalizados: !g-muteCategoria
// personalizados silencia todos eles no grupo.
const Category = "personalizados"

// Limites por grupo (LIMIT_COMMANDS do Ravena e um teto de armazenamento
// bem menor que o original, para caber no volume).
const (
	MaxCommands   = 100
	MaxResponses  = 30
	MaxMediaBytes = 16 << 20  // por arquivo
	MaxGroupBytes = 150 << 20 // por grupo
	MaxTriggerLen = 10        // palavras
	MaxCooldown   = 60000     // segundos
)

// Response é uma resposta de um comando: texto ou mídia (com legenda em Text).
type Response struct {
	// Kind vazio é texto; senão image, video, gif, sticker, audio, voice ou document.
	Kind     string `json:"kind,omitempty"`
	Text     string `json:"text,omitempty"`
	File     string `json:"file,omitempty"` // nome do arquivo na pasta do grupo
	Mime     string `json:"mime,omitempty"`
	Name     string `json:"name,omitempty"` // nome original (documentos)
	Animated bool   `json:"animated,omitempty"`
}

// IsMedia diz se a resposta é uma mídia.
func (r Response) IsMedia() bool { return r.Kind != "" }

// Command é um comando personalizado de um grupo.
type Command struct {
	Trigger   string     `json:"trigger"`
	Responses []Response `json:"responses"`
	Active    bool       `json:"active"`
	AdminOnly bool       `json:"adminOnly,omitempty"`
	// IgnoreInteract tira o comando das interações automáticas.
	IgnoreInteract bool `json:"ignoreInteract,omitempty"`
	// SendAll envia todas as respostas, em ordem; senão uma aleatória.
	SendAll bool `json:"sendAllResponses,omitempty"`
	// IgnorePrefix dispara o comando quando o gatilho aparece em qualquer mensagem.
	IgnorePrefix bool `json:"ignorePrefix,omitempty"`
	// NoQuote envia sem citar a mensagem (o padrão do Ravena é citar).
	NoQuote bool `json:"noQuote,omitempty"`
	// Private responde no privado de quem chamou.
	Private     bool     `json:"replyPrivate,omitempty"`
	Mentions    []string `json:"mentions,omitempty"` // JIDs marcados em toda resposta
	CooldownSec int      `json:"cooldown,omitempty"`
	StartReact  string   `json:"startReact,omitempty"` // reação ao começar
	React       string   `json:"react,omitempty"`      // reação ao terminar
	Count       int      `json:"count"`
	LastUsed    int64    `json:"lastUsed,omitempty"`
	CreatedBy   string   `json:"createdBy,omitempty"`
	CreatedAt   int64    `json:"createdAt,omitempty"`
}

// Policy é o que o módulo usa da policy do bot (pausa, mute, anti-spam,
// cooldown e papéis), para os comandos personalizados seguirem as mesmas regras.
type Policy interface {
	Allow(ctx context.Context, c *bot.Context) error
	Roles(ctx context.Context, msg *bot.Message) (admin, super bool)
	Done(ctx context.Context, c *bot.Context, err error)
}

// Module implementa app.Feature. Use sempre um ponteiro (&customcmd.Module{}):
// o mesmo valor serve para RunRandom e MenuEntries depois do Register.
type Module struct {
	router *bot.Router
	client bot.Client
	store  *store.Store
	db     *sql.DB
	policy Policy // pode ser nil
	dir    string // raiz das mídias
	log    *slog.Logger
	rand   func(n int) int
	now    func() time.Time

	writeMu sync.Mutex // serializa ler-alterar-gravar

	cacheMu sync.Mutex
	cache   map[string]*cacheEntry
}

type cacheEntry struct {
	cmds         []*Command // ordenados por gatilho; somente leitura
	ignorePrefix bool       // comandos do grupo dispensam o prefixo
	at           time.Time
}

const (
	cacheMax = 256
	cacheTTL = 15 * time.Minute
)

var _ app.Feature = (*Module)(nil)

// Name implementa app.Feature.
func (*Module) Name() string { return "customcmd" }

var migrations = []string{
	`CREATE TABLE customcmd_commands (
		chat       TEXT NOT NULL,
		trigger    TEXT NOT NULL,
		data       TEXT NOT NULL,
		updated_at INTEGER NOT NULL,
		PRIMARY KEY (chat, trigger)
	)`,
	`CREATE TABLE customcmd_groups (
		chat          TEXT PRIMARY KEY,
		ignore_prefix INTEGER NOT NULL DEFAULT 0
	)`,
}

// Register implementa app.Feature.
func (m *Module) Register(ctx context.Context, d *app.Deps) error {
	if err := d.Store.Migrate(ctx, "customcmd", migrations); err != nil {
		return err
	}
	m.router, m.client, m.store, m.db = d.Router, d.Client, d.Store, d.Store.DB()
	if d.Policy != nil {
		m.policy = d.Policy
	}
	m.dir = filepath.Join(d.DataDir, "customcmd")
	m.log = d.Log
	if m.log == nil {
		m.log = slog.Default()
	}
	if m.rand == nil {
		m.rand = rand.IntN
	}
	if m.now == nil {
		m.now = time.Now
	}
	m.cache = map[string]*cacheEntry{}
	if err := d.Router.Register(m.commands()...); err != nil {
		return err
	}
	d.Router.Listen(m.listen)
	if d.Web != nil {
		m.mountPanel(d.Web)
	}
	return nil
}

// ---- persistência ----

// load devolve os comandos do chat (do cache, se fresco). Não altere o resultado.
func (m *Module) load(ctx context.Context, chat string) (*cacheEntry, error) {
	now := m.now()
	m.cacheMu.Lock()
	if e, ok := m.cache[chat]; ok && now.Sub(e.at) < cacheTTL {
		m.cacheMu.Unlock()
		return e, nil
	}
	m.cacheMu.Unlock()

	cmds, err := m.list(ctx, chat)
	if err != nil {
		return nil, err
	}
	var ip int
	err = m.db.QueryRowContext(ctx, `SELECT ignore_prefix FROM customcmd_groups WHERE chat = ?`, chat).Scan(&ip)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	e := &cacheEntry{cmds: cmds, ignorePrefix: ip != 0, at: now}

	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()
	if len(m.cache) >= cacheMax {
		var oldest string
		for k, v := range m.cache {
			if oldest == "" || v.at.Before(m.cache[oldest].at) {
				oldest = k
			}
		}
		delete(m.cache, oldest)
	}
	m.cache[chat] = e
	return e, nil
}

func (m *Module) invalidate(chat string) {
	m.cacheMu.Lock()
	delete(m.cache, chat)
	m.cacheMu.Unlock()
}

// list lê do banco todos os comandos do chat, ordenados pelo gatilho.
func (m *Module) list(ctx context.Context, chat string) ([]*Command, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT data FROM customcmd_commands WHERE chat = ? ORDER BY trigger`, chat)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Command
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		c := &Command{}
		if err := json.Unmarshal([]byte(raw), c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (m *Module) get(ctx context.Context, chat, trigger string) (*Command, error) {
	var raw string
	err := m.db.QueryRowContext(ctx, `SELECT data FROM customcmd_commands WHERE chat = ? AND trigger = ?`, chat, trigger).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c := &Command{}
	return c, json.Unmarshal([]byte(raw), c)
}

func (m *Module) save(ctx context.Context, chat string, c *Command) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = m.db.ExecContext(ctx,
		`INSERT INTO customcmd_commands(chat, trigger, data, updated_at) VALUES(?, ?, ?, ?)
		 ON CONFLICT(chat, trigger) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
		chat, c.Trigger, string(raw), m.now().Unix())
	m.invalidate(chat)
	return err
}

func (m *Module) remove(ctx context.Context, chat, trigger string) error {
	_, err := m.db.ExecContext(ctx, `DELETE FROM customcmd_commands WHERE chat = ? AND trigger = ?`, chat, trigger)
	m.invalidate(chat)
	return err
}

// errNotFound é devolvido por update quando o comando não existe.
var errNotFound = errors.New("comando não encontrado")

// update lê, aplica fn e grava o comando, sob o mutex de escrita.
// fn pode devolver um erro para abortar sem gravar.
func (m *Module) update(ctx context.Context, chat, trigger string, fn func(*Command) error) (*Command, error) {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	c, err := m.get(ctx, chat, trigger)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, errNotFound
	}
	if err := fn(c); err != nil {
		return nil, err
	}
	return c, m.save(ctx, chat, c)
}

// markUsed incrementa o contador sem regravar o comando (não atropela edições).
func (m *Module) markUsed(ctx context.Context, chat, trigger string) error {
	_, err := m.db.ExecContext(ctx,
		`UPDATE customcmd_commands
		 SET data = json_set(data, '$.count', COALESCE(json_extract(data, '$.count'), 0) + 1, '$.lastUsed', ?)
		 WHERE chat = ? AND trigger = ?`, m.now().Unix(), chat, trigger)
	m.invalidate(chat)
	return err
}

func (m *Module) count(ctx context.Context, chat string) (int, error) {
	var n int
	err := m.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM customcmd_commands WHERE chat = ?`, chat).Scan(&n)
	return n, err
}

func (m *Module) setIgnorePrefix(ctx context.Context, chat string, on bool) error {
	v := 0
	if on {
		v = 1
	}
	_, err := m.db.ExecContext(ctx,
		`INSERT INTO customcmd_groups(chat, ignore_prefix) VALUES(?, ?)
		 ON CONFLICT(chat) DO UPDATE SET ignore_prefix = excluded.ignore_prefix`, chat, v)
	m.invalidate(chat)
	return err
}

// chatSummary é uma linha da lista de grupos do painel.
type chatSummary struct {
	Chat  string
	Name  string
	Count int
}

func (m *Module) chats(ctx context.Context) ([]chatSummary, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT chat, COUNT(*) FROM customcmd_commands GROUP BY chat ORDER BY chat`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []chatSummary
	for rows.Next() {
		var s chatSummary
		if err := rows.Scan(&s.Chat, &s.Count); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ---- API para outras funcionalidades ----

// MenuEntries lista os gatilhos ativos do chat (para o menu de comandos).
func (m *Module) MenuEntries(ctx context.Context, chat types.JID) []string {
	if m.db == nil {
		return nil
	}
	e, err := m.load(ctx, chat.String())
	if err != nil {
		m.log.Warn("falha ao ler comandos personalizados", "err", err, "chat", chat)
		return nil
	}
	var out []string
	for _, c := range e.cmds {
		if c.Active && len(c.Responses) > 0 {
			out = append(out, c.Trigger)
		}
	}
	return out
}

// RunRandom roda um comando personalizado ativo e aleatório de msg.Chat que
// participe das interações (sem ignoreInteract nem adminOnly), sem argumentos
// e sem reações. Devolve false se o grupo não tiver nenhum.
func (m *Module) RunRandom(ctx context.Context, msg *bot.Message) (bool, error) {
	if m.db == nil || !msg.IsGroup {
		return false, nil
	}
	e, err := m.load(ctx, msg.Chat.String())
	if err != nil {
		return false, err
	}
	var pool []*Command
	for _, c := range e.cmds {
		if c.Active && !c.IgnoreInteract && !c.AdminOnly && len(c.Responses) > 0 {
			pool = append(pool, c)
		}
	}
	if len(pool) == 0 {
		return false, nil
	}
	cmd := pool[m.rand(len(pool))]
	return true, m.execute(ctx, msg, cmd, nil, true)
}

// ---- utilidades ----

func (m *Module) groupConfig(ctx context.Context, chat types.JID) *store.Group {
	g, _, err := m.store.Group(ctx, chat.String())
	if err != nil {
		m.log.Warn("falha ao ler grupo", "err", err, "chat", chat)
	}
	return g
}

func findCmd(cmds []*Command, trigger string) *Command {
	i, ok := slices.BinarySearchFunc(cmds, trigger, func(c *Command, t string) int {
		switch {
		case c.Trigger < t:
			return -1
		case c.Trigger > t:
			return 1
		}
		return 0
	})
	if !ok {
		return nil
	}
	return cmds[i]
}

func notFound(trigger string) error {
	return bot.Userf("Comando personalizado '%s' não encontrado.", trigger)
}

func wrapNotFound(err error, trigger string) error {
	if errors.Is(err, errNotFound) {
		return notFound(trigger)
	}
	if _, ok := errors.AsType[*bot.UserError](err); ok || err == nil {
		return err
	}
	return fmt.Errorf("comando %q: %w", trigger, err)
}
