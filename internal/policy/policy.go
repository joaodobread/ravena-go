// Package policy implementa bot.Policy: prefixo por grupo, pausa, comandos
// silenciados, anti-spam, cooldown, papéis (admin/super admin) e estatísticas.
package policy

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/store"
)

// Settings são as configurações globais do bot (editadas no painel).
type Settings struct {
	BotName          string   `json:"botName"`
	DisabledCommands []string `json:"disabledCommands,omitempty"`
	// SuperAdmins são números (só dígitos) com acesso total.
	SuperAdmins []string `json:"superAdmins,omitempty"`
	// IgnorePrivate faz o bot ignorar conversas privadas.
	IgnorePrivate bool `json:"ignorePrivate"`
}

// SettingsKey é a chave das Settings no kv do store.
const SettingsKey = "settings"

// Options configura o anti-spam.
type Options struct {
	// Debounce é o intervalo mínimo entre comandos do mesmo usuário.
	Debounce time.Duration
	// BanAfter é quantas vezes seguidas furar o Debounce leva a um ban temporário.
	BanAfter int
	// BanFor é a duração do ban (e o tempo para os avisos zerarem).
	BanFor time.Duration
}

// DefaultOptions são os valores do Ravena (CMD_DEBOUNCE, DEBOUNCE_BAN, DEBOUNCE_DECAY).
var DefaultOptions = Options{Debounce: 3 * time.Second, BanAfter: 10, BanFor: time.Minute}

// Policy implementa bot.Policy.
type Policy struct {
	store  *store.Store
	client bot.Client
	log    *slog.Logger
	opts   Options
	now    func() time.Time
	envSA  []string

	settingsMu sync.RWMutex
	settings   Settings

	mu        sync.Mutex
	users     map[string]*userState // anti-spam por usuário
	cooldowns map[string]time.Time  // "chat|cmd" → liberado em
	lastSweep time.Time

	usage chan store.Usage
}

type userState struct {
	last    time.Time
	strikes int
	banned  time.Time // banido até
}

var _ bot.Policy = (*Policy)(nil)

// New cria a Policy. envSuperAdmins vêm da variável SUPER_ADMINS.
func New(ctx context.Context, st *store.Store, client bot.Client, envSuperAdmins []string, opts Options, log *slog.Logger) (*Policy, error) {
	p := &Policy{
		store: st, client: client, log: log, opts: opts, now: time.Now,
		envSA:     normalizeNumbers(envSuperAdmins),
		users:     map[string]*userState{},
		cooldowns: map[string]time.Time{},
		usage:     make(chan store.Usage, 256),
	}
	if err := p.ReloadSettings(ctx); err != nil {
		return nil, err
	}
	return p, nil
}

// ReloadSettings relê as Settings do banco (o painel chama após salvar).
func (p *Policy) ReloadSettings(ctx context.Context) error {
	s := Settings{BotName: "ravena"}
	if _, err := p.store.GetJSON(ctx, SettingsKey, &s); err != nil {
		return err
	}
	s.SuperAdmins = normalizeNumbers(s.SuperAdmins)
	p.settingsMu.Lock()
	p.settings = s
	p.settingsMu.Unlock()
	return nil
}

// Settings devolve uma cópia das configurações globais.
func (p *Policy) Settings() Settings {
	p.settingsMu.RLock()
	defer p.settingsMu.RUnlock()
	s := p.settings
	s.DisabledCommands = slices.Clone(s.DisabledCommands)
	s.SuperAdmins = slices.Clone(s.SuperAdmins)
	return s
}

// SaveSettings grava e aplica novas configurações globais.
func (p *Policy) SaveSettings(ctx context.Context, s Settings) error {
	s.SuperAdmins = normalizeNumbers(s.SuperAdmins)
	if err := p.store.SetJSON(ctx, SettingsKey, s); err != nil {
		return err
	}
	p.settingsMu.Lock()
	p.settings = s
	p.settingsMu.Unlock()
	return nil
}

// RunUsageWriter grava as estatísticas em lote até ctx acabar.
func (p *Policy) RunUsageWriter(ctx context.Context) {
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case u := <-p.usage:
			// Contexto próprio: a gravação não deve ser cortada no desligamento.
			if err := p.store.LogUsage(context.Background(), u); err != nil {
				p.log.Warn("falha ao gravar estatística", "err", err)
			}
		case <-tick.C:
			if _, err := p.store.PruneUsage(ctx, p.now().AddDate(0, 0, -90)); err != nil {
				p.log.Warn("falha ao limpar estatísticas", "err", err)
			}
		}
	}
}

func (p *Policy) group(ctx context.Context, msg *bot.Message) *store.Group {
	if !msg.IsGroup {
		return nil
	}
	g, _, err := p.store.Group(ctx, msg.Chat.String())
	if err != nil {
		p.log.Warn("falha ao ler grupo", "err", err, "chat", msg.Chat)
	}
	return g // nil = grupo sem configuração salva (padrões)
}

// Prefix devolve o prefixo do grupo ("" usa o padrão).
func (p *Policy) Prefix(ctx context.Context, msg *bot.Message) string {
	if g := p.group(ctx, msg); g != nil {
		return g.Prefix
	}
	return ""
}

// Admit descarta usuários ignorados e, se configurado, o privado.
func (p *Policy) Admit(ctx context.Context, msg *bot.Message) bool {
	if !msg.IsGroup {
		return !p.Settings().IgnorePrivate || p.isSuper(msg)
	}
	g := p.group(ctx, msg)
	return g == nil || !(g.IsIgnored(msg.Sender.User) || g.IsIgnored(msg.SenderAlt.User))
}

// Allow aplica as regras de configuração ao comando.
func (p *Policy) Allow(ctx context.Context, c *bot.Context) error {
	cmd, msg := c.Command, c.Message
	super := p.isSuper(msg)
	if !super && slices.Contains(p.Settings().DisabledCommands, cmd.Name) {
		return bot.ErrSilent
	}
	if g := p.group(ctx, msg); g != nil {
		if g.Paused && !cmd.IgnorePause {
			return bot.ErrSilent
		}
		if g.IsMuted(cmd.Name, cmd.Category) && !c.IsAdmin(ctx) {
			return bot.ErrSilent
		}
	}
	if super {
		return nil
	}
	if !p.throttle(msg.Sender.User) {
		return bot.ErrSilent
	}
	if cmd.Cooldown > 0 {
		if wait := p.cooldown(msg.Chat.String()+"|"+cmd.Name, cmd.Cooldown); wait > 0 {
			return bot.Userf("⏳ Calma! Dá para usar %s%s de novo em %s.", c.Prefix, cmd.Name, humanize(wait))
		}
	}
	return nil
}

// Roles resolve admin do grupo e super admin do bot.
func (p *Policy) Roles(ctx context.Context, msg *bot.Message) (admin, super bool) {
	super = p.isSuper(msg)
	if !msg.IsGroup {
		return false, super
	}
	if g := p.group(ctx, msg); g != nil &&
		(slices.Contains(g.AdditionalAdmins, msg.Sender.User) || slices.Contains(g.AdditionalAdmins, msg.SenderAlt.User)) {
		return true, super
	}
	info, err := p.client.GroupInfo(ctx, msg.Chat)
	if err != nil {
		p.log.Warn("falha ao consultar admins", "err", err, "chat", msg.Chat)
		return false, super
	}
	if m, ok := info.Member(msg.Sender); ok {
		return m.IsAdmin, super
	}
	if !msg.SenderAlt.IsEmpty() {
		return info.IsAdmin(msg.SenderAlt), super
	}
	return false, super
}

// Done enfileira a estatística do comando (descarta se a fila estiver cheia).
func (p *Policy) Done(_ context.Context, c *bot.Context, err error) {
	u := store.Usage{
		Command: c.Command.Name, Chat: c.Message.Chat.String(), User: c.Message.Sender.User,
		OK: err == nil, At: p.now(),
	}
	select {
	case p.usage <- u:
	default:
	}
}

func (p *Policy) isSuper(msg *bot.Message) bool {
	p.settingsMu.RLock()
	defer p.settingsMu.RUnlock()
	for _, list := range [][]string{p.envSA, p.settings.SuperAdmins} {
		for _, n := range list {
			if numberMatches(n, msg.Sender) || numberMatches(n, msg.SenderAlt) {
				return true
			}
		}
	}
	return false
}

// throttle aplica o anti-spam; false = ignorar o comando.
func (p *Policy) throttle(user string) bool {
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sweep(now)

	u, ok := p.users[user]
	if !ok {
		p.users[user] = &userState{last: now}
		return true
	}
	if now.Before(u.banned) {
		return false
	}
	if now.Sub(u.last) > p.opts.BanFor {
		u.strikes = 0 // avisos expiram
	}
	tooFast := now.Sub(u.last) < p.opts.Debounce
	u.last = now
	if !tooFast {
		return true
	}
	u.strikes++
	if p.opts.BanAfter > 0 && u.strikes >= p.opts.BanAfter {
		u.banned = now.Add(p.opts.BanFor)
		u.strikes = 0
		p.log.Info("usuário bloqueado temporariamente por spam", "user", user, "for", p.opts.BanFor)
	}
	return false
}

// cooldown devolve quanto falta para liberar key (0 = liberado e marcado).
func (p *Policy) cooldown(key string, d time.Duration) time.Duration {
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	if until, ok := p.cooldowns[key]; ok && now.Before(until) {
		return until.Sub(now)
	}
	p.cooldowns[key] = now.Add(d)
	return 0
}

// sweep limpa estados vencidos a cada minuto para os mapas não crescerem.
func (p *Policy) sweep(now time.Time) {
	if now.Sub(p.lastSweep) < time.Minute {
		return
	}
	p.lastSweep = now
	for k, u := range p.users {
		if now.Sub(u.last) > p.opts.BanFor && now.After(u.banned) {
			delete(p.users, k)
		}
	}
	for k, until := range p.cooldowns {
		if now.After(until) {
			delete(p.cooldowns, k)
		}
	}
}

// normalizeNumbers tira tudo que não é dígito ("556296975194@c.us" → "556296975194").
func normalizeNumbers(in []string) []string {
	var out []string
	for _, s := range in {
		if at := strings.IndexByte(s, '@'); at >= 0 {
			s = s[:at]
		}
		var b strings.Builder
		for _, r := range s {
			if r >= '0' && r <= '9' {
				b.WriteRune(r)
			}
		}
		if b.Len() > 0 && !slices.Contains(out, b.String()) {
			out = append(out, b.String())
		}
	}
	return out
}

// numberMatches compara um número com jid, tolerando o 9º dígito de
// celulares brasileiros (o WhatsApp usa os dois formatos).
func numberMatches(number string, jid types.JID) bool {
	if jid.User == "" || (jid.Server != types.DefaultUserServer && jid.Server != types.HiddenUserServer) {
		return false
	}
	return jid.User == number || brVariant(number) == jid.User || brVariant(jid.User) == number
}

// brVariant alterna o 9º dígito: 55DD9XXXXXXXX ⇄ 55DDXXXXXXXX.
func brVariant(n string) string {
	if !strings.HasPrefix(n, "55") {
		return ""
	}
	switch len(n) {
	case 13:
		if n[4] == '9' {
			return n[:4] + n[5:]
		}
	case 12:
		return n[:4] + "9" + n[4:]
	}
	return ""
}

func humanize(d time.Duration) string {
	if d < time.Minute {
		return (d.Round(time.Second)).String()
	}
	return d.Round(time.Minute).String()
}
