// Package groupadmin implementa os comandos de gerenciamento de grupo do
// Ravena (!g-pausar, !g-setPrefixo, !g-info, !g-interagir*, !g-mute*,
// !g-ignorar, !g-customAdmin, !g-ban...). Tudo é gravado em store.Group.
package groupadmin

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"go.mau.fi/whatsmeow/types"

	"github.com/joaodobread/ravena-go/internal/app"
	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/store"
)

// CustomCategory é a categoria dos comandos personalizados (customcmd.Category),
// aceita por !g-muteCategoria mesmo sem nenhum comando fixo nela.
const CustomCategory = "personalizados"

// Module implementa app.Feature.
type Module struct {
	router *bot.Router
	store  *store.Store
}

var _ app.Feature = (*Module)(nil)

// Name implementa app.Feature.
func (*Module) Name() string { return "groupadmin" }

// Register implementa app.Feature.
func (m *Module) Register(_ context.Context, d *app.Deps) error {
	m.router, m.store = d.Router, d.Store
	return d.Router.Register(m.commands()...)
}

func (m *Module) commands() []*bot.Command {
	mk := func(name string, aliases []string, usage, desc string, run bot.Handler) *bot.Command {
		return &bot.Command{
			Name: name, Aliases: aliases, Usage: usage, Description: desc, Category: "grupo",
			AdminOnly: true, GroupOnly: true,
			Reactions: bot.Reactions{After: "✅", Error: "❌"},
			Run:       run,
		}
	}
	pause := mk("g-pausar", nil, "!g-pausar", "Pausa/retoma o bot no grupo", m.pause)
	pause.IgnorePause = true
	return []*bot.Command{
		pause,
		mk("g-setPrefixo", nil, "!g-setPrefixo [prefixo]", "Altera o prefixo de comandos do grupo (sem argumento, volta ao padrão)", m.setPrefix),
		mk("g-setNome", nil, "!g-setNome <nome>", "Nome de cadastro do grupo (figurinhas, painel)", m.setName),
		mk("g-info", nil, "!g-info", "Mostra a configuração do grupo", m.info),
		mk("g-interagir", nil, "!g-interagir", "Liga/desliga as interações automáticas", m.toggleInteract),
		mk("g-interagir-cmd", []string{"g-interagir-cmds"}, "!g-interagir-cmd", "Liga/desliga o uso de comandos personalizados nas interações", m.toggleInteractCmds),
		mk("g-interagir-cd", nil, "!g-interagir-cd <minutos>", "Intervalo mínimo entre interações (30–43200 min)", m.interactCooldown),
		mk("g-interagir-chance", nil, "!g-interagir-chance <1-500>", "Chance de interagir por mensagem, em 1/10000", m.interactChance),
		mk("g-interagir-proporcao", []string{"g-interagir-prop"}, "!g-interagir-proporcao <0-100>", "% das interações feitas pela IA (o resto usa comandos)", m.interactProportion),
		mk("g-mute", []string{"g-mutar"}, "!g-mute [comando|emoji]", "Silencia/reativa um comando no grupo (sem argumento, lista)", m.mute(toggleOp)),
		mk("g-desmutar", nil, "!g-desmutar <comando>", "Reativa um comando silenciado", m.mute(removeOp)),
		mk("g-muteCategoria", []string{"g-mutarCategoria"}, "!g-muteCategoria [categoria]", "Silencia/reativa uma categoria de comandos (sem argumento, lista)", m.muteCategory(toggleOp)),
		mk("g-desmutarCategoria", nil, "!g-desmutarCategoria <categoria>", "Reativa uma categoria silenciada", m.muteCategory(removeOp)),
		mk("g-ignorar", nil, "!g-ignorar [número|@pessoa]", "O bot passa a ignorar (ou volta a ouvir) a pessoa no grupo (sem argumento, lista)", m.people(ignoredList, toggleOp)),
		mk("g-designorar", nil, "!g-designorar <número|@pessoa>", "O bot volta a ouvir a pessoa", m.people(ignoredList, removeOp)),
		mk("g-customAdmin", nil, "!g-customAdmin [número|@pessoa]", "Alterna um admin extra do bot no grupo (sem argumento, lista)", m.people(adminList, toggleOp)),
		mk("g-addAdmin", nil, "!g-addAdmin <número|@pessoa>", "Adiciona um admin extra do bot no grupo", m.people(adminList, addOp)),
		mk("g-delAdmin", nil, "!g-delAdmin <número|@pessoa>", "Remove um admin extra do bot no grupo", m.people(adminList, removeOp)),
		mk("g-ban", []string{"g-kick"}, "!g-ban @pessoa (ou respondendo)", "Remove pessoas do grupo", m.ban),
	}
}

func (m *Module) update(ctx context.Context, c *bot.Context, fn func(*store.Group)) (*store.Group, error) {
	return m.store.UpdateGroup(ctx, c.Message.Chat.String(), fn)
}

func (m *Module) group(ctx context.Context, c *bot.Context) (*store.Group, error) {
	return m.store.GroupOrDefault(ctx, c.Message.Chat.String())
}

// ---- pausa, prefixo, nome ----

func (m *Module) pause(ctx context.Context, c *bot.Context) error {
	g, err := m.update(ctx, c, func(g *store.Group) { g.Paused = !g.Paused })
	if err != nil {
		return err
	}
	if g.Paused {
		return c.ReplyText(ctx, "⏸️ Bot pausado neste grupo. Somente o comando `%sg-pausar` será processado até que seja reativado.", c.Prefix)
	}
	return c.ReplyText(ctx, "▶️ Bot reativado neste grupo. Todos os comandos estão disponíveis novamente.")
}

func (m *Module) setPrefix(ctx context.Context, c *bot.Context) error {
	p := ""
	if len(c.Args) > 0 {
		p = c.Args[0]
	}
	if utf8.RuneCountInString(p) > 3 {
		return bot.Userf("O prefixo pode ter no máximo 3 caracteres.")
	}
	if _, err := m.update(ctx, c, func(g *store.Group) { g.Prefix = p }); err != nil {
		return err
	}
	if p == "" {
		return c.ReplyText(ctx, "Prefixo de comando restaurado para o padrão: %s", m.router.DefaultPrefix())
	}
	return c.ReplyText(ctx, "Prefixo de comando atualizado para: %s", p)
}

var nameRe = regexp.MustCompile(`^[a-z0-9_.-]{1,30}$`)

func (m *Module) setName(ctx context.Context, c *bot.Context) error {
	name := strings.ToLower(strings.Join(c.Args, "-"))
	if name == "" {
		return bot.Userf("Informe o nome. Exemplo: %sg-setNome meu-grupo", c.Prefix)
	}
	if !nameRe.MatchString(name) {
		return bot.Userf("Nome inválido: use até 30 caracteres entre letras minúsculas, números, '.', '_' e '-'.")
	}
	groups, err := m.store.Groups(ctx)
	if err != nil {
		return err
	}
	for _, g := range groups {
		if g.Name == name && g.JID != c.Message.Chat.String() {
			return bot.Userf("Já existe um grupo cadastrado com o nome '%s'.", name)
		}
	}
	if _, err := m.update(ctx, c, func(g *store.Group) { g.Name = name }); err != nil {
		return err
	}
	return c.ReplyText(ctx, "Nome do grupo atualizado para: %s", name)
}

// ---- info ----

func yesNo(b bool) string {
	if b {
		return "Sim"
	}
	return "Não"
}

func listOr(xs []string, none string) string {
	if len(xs) == 0 {
		return none
	}
	return strings.Join(xs, ", ")
}

func (m *Module) info(ctx context.Context, c *bot.Context) error {
	g, err := m.group(ctx, c)
	if err != nil {
		return err
	}
	prefix := g.Prefix
	if prefix == "" {
		prefix = m.router.DefaultPrefix()
	}
	var b strings.Builder
	b.WriteString("*📊 Informações do Grupo*\n\n")
	name := g.Name
	if name == "" {
		name = "não definido"
	}
	fmt.Fprintf(&b, "*Nome:* %s\n", name)
	fmt.Fprintf(&b, "*ID:* %s\n", g.JID)
	fmt.Fprintf(&b, "*Prefixo:* \"%s\"\n", prefix)
	if !g.CreatedAt.IsZero() {
		fmt.Fprintf(&b, "*Cadastrado em:* %s\n", g.CreatedAt.Format("02/01/2006 15:04"))
	}
	fmt.Fprintf(&b, "*Pausado:* %s\n\n", yesNo(g.Paused))
	fmt.Fprintf(&b, "*Admins extras:* %s\n", listOr(g.AdditionalAdmins, "nenhum"))
	fmt.Fprintf(&b, "*Ignorados:* %s\n", listOr(g.IgnoredUsers, "ninguém"))
	fmt.Fprintf(&b, "*Comandos silenciados:* %s\n", listOr(g.MutedCommands, "nenhum"))
	fmt.Fprintf(&b, "*Categorias silenciadas:* %s\n\n", listOr(g.MutedCategories, "nenhuma"))
	in := g.Interact
	fmt.Fprintf(&b, "*Interações automáticas:* %s\n", yesNo(in.Enabled))
	fmt.Fprintf(&b, "- Chance: %s%% (%d/10000)\n", pct(in.EffectiveChance()), in.EffectiveChance())
	fmt.Fprintf(&b, "- Cooldown: %d minutos\n", int(in.EffectiveCooldown().Minutes()))
	fmt.Fprintf(&b, "- Proporção: %d%% IA, %d%% comandos\n", in.Proporcao, 100-in.Proporcao)
	fmt.Fprintf(&b, "- Usa comandos personalizados: %s\n", yesNo(in.UseCmds))
	fmt.Fprintf(&b, "\n*Respostas a elogios/xingamentos:* %s", yesNo(g.Silly))
	if g.Greeting != "" {
		fmt.Fprintf(&b, "\n*Boas-vindas:* %s", g.Greeting)
	}
	if g.Farewell != "" {
		fmt.Fprintf(&b, "\n*Despedida:* %s", g.Farewell)
	}
	return c.ReplyText(ctx, "%s", b.String())
}

func pct(chance int) string { return strconv.FormatFloat(float64(chance)/100, 'f', -1, 64) }

// ---- interações ----

func (m *Module) toggleInteract(ctx context.Context, c *bot.Context) error {
	g, err := m.update(ctx, c, func(g *store.Group) { g.Interact.Enabled = !g.Interact.Enabled })
	if err != nil {
		return err
	}
	if !g.Interact.Enabled {
		return c.ReplyText(ctx, "Interações automáticas *desativadas* para este grupo.")
	}
	in := g.Interact
	return c.ReplyText(ctx, "Interações automáticas *ativadas* para este grupo.\n\n"+
		"📊 Chance atual: %s%%\n🕐 Cooldown atual: %d minutos\n⚖️ Proporção atual: %d%% IA, %d%% comandos\n\n"+
		"Use `%sg-interagir-chance`, `%sg-interagir-cd` e `%sg-interagir-proporcao` para ajustar estes valores.",
		pct(in.EffectiveChance()), int(in.EffectiveCooldown().Minutes()), in.Proporcao, 100-in.Proporcao, c.Prefix, c.Prefix, c.Prefix)
}

func (m *Module) toggleInteractCmds(ctx context.Context, c *bot.Context) error {
	g, err := m.update(ctx, c, func(g *store.Group) { g.Interact.UseCmds = !g.Interact.UseCmds })
	if err != nil {
		return err
	}
	if g.Interact.UseCmds {
		return c.ReplyText(ctx, "🛠 Interações automáticas com comandos personalizados *ativadas* para este grupo.")
	}
	return c.ReplyText(ctx, "🛠 Interações automáticas com comandos personalizados *desativadas* para este grupo.")
}

func intArg(c *bot.Context) (int, bool) {
	if len(c.Args) == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(c.Args[0])
	return n, err == nil
}

const maxInteractCooldown = 43200

func (m *Module) interactCooldown(ctx context.Context, c *bot.Context) error {
	n, ok := intArg(c)
	if !ok {
		g, err := m.group(ctx, c)
		if err != nil {
			return err
		}
		return c.ReplyText(ctx, "🕐 Cooldown atual: %d minutos\n\nUse %sg-interagir-cd [minutos] para alterar. Valores entre %d minutos e 30 dias (%d minutos).",
			int(g.Interact.EffectiveCooldown().Minutes()), c.Prefix, store.MinInteractCooldownMin, maxInteractCooldown)
	}
	note := ""
	if n < store.MinInteractCooldownMin {
		n, note = store.MinInteractCooldownMin, " (mínimo possível)"
	}
	n = min(n, maxInteractCooldown)
	if _, err := m.update(ctx, c, func(g *store.Group) { g.Interact.CooldownMin = n }); err != nil {
		return err
	}
	return c.ReplyText(ctx, "🕐 Cooldown de interações definido para %d minutos%s.", n, note)
}

func (m *Module) interactChance(ctx context.Context, c *bot.Context) error {
	n, ok := intArg(c)
	if !ok {
		g, err := m.group(ctx, c)
		if err != nil {
			return err
		}
		ch := g.Interact.EffectiveChance()
		return c.ReplyText(ctx, "📊 Chance atual: %s%% (%d/10000)\n\nUse %sg-interagir-chance [1-%d] para alterar. Valores entre 0.01%% e 5%%.",
			pct(ch), ch, c.Prefix, store.MaxInteractChance)
	}
	note := ""
	n = max(n, 1)
	if n >= store.MaxInteractChance {
		n, note = store.MaxInteractChance, " (máximo possível)"
	}
	if _, err := m.update(ctx, c, func(g *store.Group) { g.Interact.Chance = n }); err != nil {
		return err
	}
	return c.ReplyText(ctx, "📊 Chance de interações definida para %s%%%s.", pct(n), note)
}

func (m *Module) interactProportion(ctx context.Context, c *bot.Context) error {
	help := fmt.Sprintf("Para usar apenas *IA* na interação, envie:\n%sg-interagir-proporcao 100\n\nPara usar apenas *comandos* na interação, envie:\n%sg-interagir-proporcao 0", c.Prefix, c.Prefix)
	n, ok := intArg(c)
	if !ok {
		g, err := m.group(ctx, c)
		if err != nil {
			return err
		}
		p := g.Interact.Proporcao
		return c.ReplyText(ctx, "📊 Proporção de interação atual: %d%% para IA e %d%% para comandos.\n\nUse %sg-interagir-proporcao [0-100] para alterar.\n\n%s", p, 100-p, c.Prefix, help)
	}
	n = min(max(n, 0), 100)
	if _, err := m.update(ctx, c, func(g *store.Group) { g.Interact.Proporcao = n }); err != nil {
		return err
	}
	var simple string
	switch n {
	case 100:
		simple = "Usando apenas *IA* para interagir"
	case 0:
		simple = "Usando apenas *comandos* para interagir"
	default:
		simple = fmt.Sprintf("%d%% de chance de usar algum comando, %d%% de chance de usar IA para interagir", 100-n, n)
	}
	return c.ReplyText(ctx, "📊 Proporção de interação definida para %d%% IA e %d%% comandos.\n\n_%s_\n\n%s", n, 100-n, simple, help)
}

// ---- mute ----

type op int

const (
	toggleOp op = iota
	addOp
	removeOp
)

// apply aplica op a list com os valores vals (tratados como um só item);
// devolve a lista nova e se o item ficou presente.
func apply(list, vals []string, o op) ([]string, bool) {
	present := slices.ContainsFunc(vals, func(v string) bool { return slices.Contains(list, v) })
	add := o == addOp || (o == toggleOp && !present)
	if add {
		for _, v := range vals {
			if !slices.Contains(list, v) {
				list = append(list, v)
			}
		}
		return list, true
	}
	return slices.DeleteFunc(list, func(x string) bool { return slices.Contains(vals, x) }), false
}

// findCommand acha um comando fixo pelo nome, alias ou emoji de reação.
func (m *Module) findCommand(name string) (*bot.Command, bool) {
	if cmd, ok := m.router.Lookup(name); ok {
		return cmd, true
	}
	for _, cmd := range m.router.Commands() {
		if slices.Contains(cmd.Triggers, name) {
			return cmd, true
		}
	}
	return nil, false
}

func stripPrefix(s, prefix string) string {
	if prefix != "" && strings.HasPrefix(s, prefix) {
		return s[len(prefix):]
	}
	return strings.TrimPrefix(s, "!")
}

func (m *Module) mute(o op) bot.Handler {
	return func(ctx context.Context, c *bot.Context) error {
		if len(c.Args) == 0 {
			if o != toggleOp {
				return bot.Userf("Informe o comando. Exemplo: %s%s figurinha", c.Prefix, c.Command.Name)
			}
			g, err := m.group(ctx, c)
			if err != nil {
				return err
			}
			if len(g.MutedCommands) == 0 {
				return c.ReplyText(ctx, "Nenhum comando está silenciado neste grupo.")
			}
			names := slices.Sorted(slices.Values(g.MutedCommands))
			return c.ReplyText(ctx, "*Comandos silenciados:*\n- %s", strings.Join(names, "\n- "))
		}
		name := stripPrefix(c.Args[0], c.Prefix)
		cmd, ok := m.findCommand(name)
		if !ok {
			return bot.Userf("❌ Comando '%s' não encontrado entre os comandos fixos do bot.\nPara ver todos os comandos, use %scmd.", name, c.Prefix)
		}
		var muted bool
		if _, err := m.update(ctx, c, func(g *store.Group) {
			g.MutedCommands, muted = apply(g.MutedCommands, []string{cmd.Name}, o)
		}); err != nil {
			return err
		}
		if muted {
			return c.ReplyText(ctx, "🔇 Comando '%s' silenciado neste grupo. Admins ainda podem usá-lo.", cmd.Name)
		}
		return c.ReplyText(ctx, "✅ Comando '%s' reativado neste grupo.", cmd.Name)
	}
}

func (m *Module) categories() []string {
	set := map[string]bool{CustomCategory: true}
	for _, cmd := range m.router.Commands() {
		if cmd.Category != "" {
			set[strings.ToLower(cmd.Category)] = true
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (m *Module) muteCategory(o op) bot.Handler {
	return func(ctx context.Context, c *bot.Context) error {
		cats := m.categories()
		if len(c.Args) == 0 {
			if o != toggleOp {
				return bot.Userf("Informe a categoria: %s", strings.Join(cats, ", "))
			}
			g, err := m.group(ctx, c)
			if err != nil {
				return err
			}
			return c.ReplyText(ctx, "*Categorias silenciadas:* %s\n\nCategorias disponíveis: %s", listOr(g.MutedCategories, "nenhuma"), strings.Join(cats, ", "))
		}
		cat := strings.ToLower(c.Args[0])
		if !slices.Contains(cats, cat) {
			return bot.Userf("Categoria '%s' não existe. Disponíveis: %s", cat, strings.Join(cats, ", "))
		}
		var muted bool
		if _, err := m.update(ctx, c, func(g *store.Group) {
			g.MutedCategories, muted = apply(g.MutedCategories, []string{cat}, o)
		}); err != nil {
			return err
		}
		if muted {
			return c.ReplyText(ctx, "🔇 Categoria '%s' silenciada neste grupo.", cat)
		}
		return c.ReplyText(ctx, "✅ Categoria '%s' reativada neste grupo.", cat)
	}
}

// ---- pessoas (ignorados e admins extras) ----

type personList struct {
	field          func(*store.Group) *[]string
	title, empty   string
	added, removed string
}

var (
	ignoredList = personList{
		field: func(g *store.Group) *[]string { return &g.IgnoredUsers },
		title: "*Números ignorados:*", empty: "Nenhum número está sendo ignorado neste grupo.",
		added: "O número %s será ignorado pelo bot neste grupo.", removed: "O número %s não será mais ignorado.",
	}
	adminList = personList{
		field: func(g *store.Group) *[]string { return &g.AdditionalAdmins },
		title: "*Admins extras do bot:*", empty: "Nenhum admin extra neste grupo.",
		added: "%s agora é admin do bot neste grupo.", removed: "%s não é mais admin do bot neste grupo.",
	}
)

func digits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// brVariant alterna o 9º dígito de celulares brasileiros.
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

// target é uma pessoa alvo: label para mostrar e todas as identidades
// (número e LID) a gravar, já que a policy compara o autor com qualquer uma.
type target struct {
	label string
	ids   []string
}

func identities(info *bot.GroupInfo, user string) []string {
	ids := []string{user}
	if info != nil {
		for _, p := range info.Participants {
			for _, u := range []string{p.JID.User, p.LID.User, p.Phone.User} {
				if u != "" && (u == user || u == brVariant(user)) {
					ids = ids[:0] // grava as identidades reais do membro
					for _, x := range []string{p.JID.User, p.LID.User, p.Phone.User} {
						if x != "" && !slices.Contains(ids, x) {
							ids = append(ids, x)
						}
					}
					return ids
				}
			}
		}
	}
	return ids
}

// targets lê as pessoas de menções, da mensagem respondida ou dos argumentos.
func targets(ctx context.Context, c *bot.Context) ([]target, error) {
	var users []types.JID
	switch {
	case len(c.Message.Mentions) > 0:
		users = c.Message.Mentions
	case c.Message.Quoted != nil && !c.Message.Quoted.Sender.IsEmpty() && len(c.Args) == 0:
		users = []types.JID{c.Message.Quoted.Sender}
	default:
		if len(c.Args) > 0 { // "+55 11 9999-9999" vira um número só
			d := digits(c.ArgText())
			if len(d) < 8 || len(d) > 25 {
				return nil, bot.Userf("O número deve ter pelo menos 8 dígitos.")
			}
			users = []types.JID{types.NewJID(d, types.DefaultUserServer)}
		}
	}
	if len(users) == 0 {
		return nil, nil
	}
	info, _ := c.Client.GroupInfo(ctx, c.Message.Chat)
	out := make([]target, 0, len(users))
	for _, u := range users {
		out = append(out, target{label: u.User, ids: identities(info, u.User)})
	}
	return out, nil
}

func (m *Module) people(pl personList, o op) bot.Handler {
	return func(ctx context.Context, c *bot.Context) error {
		ts, err := targets(ctx, c)
		if err != nil {
			return err
		}
		if len(ts) == 0 {
			if o != toggleOp {
				return bot.Userf("Informe o número ou marque a pessoa. Exemplo: %s%s @pessoa", c.Prefix, c.Command.Name)
			}
			g, err := m.group(ctx, c)
			if err != nil {
				return err
			}
			list := *pl.field(g)
			if len(list) == 0 {
				return c.ReplyText(ctx, "%s", pl.empty)
			}
			return c.ReplyText(ctx, "%s\n- %s", pl.title, strings.Join(list, "\n- "))
		}
		var lines []string
		if _, err := m.update(ctx, c, func(g *store.Group) {
			field := pl.field(g)
			for _, t := range ts {
				var on bool
				*field, on = apply(*field, t.ids, o)
				if on {
					lines = append(lines, fmt.Sprintf(pl.added, t.label))
				} else {
					lines = append(lines, fmt.Sprintf(pl.removed, t.label))
				}
			}
		}); err != nil {
			return err
		}
		return c.ReplyText(ctx, "%s", strings.Join(lines, "\n"))
	}
}

// ---- ban ----

func (m *Module) ban(ctx context.Context, c *bot.Context) error {
	var users []types.JID
	if len(c.Message.Mentions) > 0 {
		users = c.Message.Mentions
	} else if q := c.Message.Quoted; q != nil && !q.Sender.IsEmpty() {
		users = []types.JID{q.Sender}
	}
	if len(users) == 0 {
		return bot.Userf("⚠️ Por favor, mencione pelo menos uma pessoa para remover.")
	}
	info, err := c.Client.GroupInfo(ctx, c.Message.Chat)
	if err != nil {
		return err
	}
	me := c.Client.Me()
	if !info.IsAdmin(me) {
		return bot.Userf("⚠️ O bot precisa ser administrador do grupo para remover membros.")
	}
	var removed, failed, out []string
	var mentions []types.JID
	for _, u := range users {
		if u.User == me.User {
			continue
		}
		jid := u
		if p, ok := info.Member(u); ok && !p.JID.IsEmpty() {
			jid = p.JID
		}
		if err := c.Client.UpdateParticipants(ctx, c.Message.Chat, []types.JID{jid}, bot.ActionRemove); err != nil {
			failed = append(failed, bot.MentionText(u))
		} else {
			removed = append(removed, bot.MentionText(u))
		}
		mentions = append(mentions, u)
	}
	if len(removed) > 0 {
		out = append(out, "✅ Removido(s) do grupo: "+strings.Join(removed, ", "))
	}
	if len(failed) > 0 {
		out = append(out, "❌ Falha ao remover: "+strings.Join(failed, ", "))
	}
	if len(out) == 0 {
		return bot.Userf("Não vou remover a mim mesma. 🙃")
	}
	text := bot.Text(strings.Join(out, "\n"))
	text.Mentions = mentions
	return c.Reply(ctx, text)
}
