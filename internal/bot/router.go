package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"
)

// Handler executa um comando.
type Handler func(ctx context.Context, c *Context) error

// Reactions são os emojis aplicados à mensagem do comando.
type Reactions struct {
	Before string // ao começar
	After  string // ao terminar com sucesso
	Error  string // ao falhar
}

// Command é um comando do bot (ex.: !s).
type Command struct {
	Name        string
	Aliases     []string
	Description string
	Usage       string // ex.: "!dado 2d6"
	Category    string
	Reactions   Reactions
	// Triggers são emojis que, usados como reação a uma mensagem, rodam o
	// comando sobre ela (ex.: reagir 🖼 a uma foto gera a figurinha).
	Triggers []string
	// AdminOnly exige admin do grupo (ou super admin do bot).
	AdminOnly bool
	// SuperAdminOnly exige super admin do bot.
	SuperAdminOnly bool
	// GroupOnly recusa o comando no privado.
	GroupOnly bool
	// IgnorePause roda mesmo com o grupo pausado (ex.: o próprio !g-pausar).
	IgnorePause bool
	// Cooldown entre usos do comando no mesmo chat.
	Cooldown time.Duration
	// Hidden esconde o comando do menu.
	Hidden bool
	Run    Handler
}

// Names devolve o nome e os aliases.
func (c *Command) Names() []string { return append([]string{c.Name}, c.Aliases...) }

// Context é o que um Handler recebe.
type Context struct {
	Message *Message
	Command *Command
	Invoked string // nome/alias usado
	Args    []string
	Client  Client
	// Prefix usado no chat (útil para montar textos de ajuda).
	Prefix string

	roles roles
}

// roles resolve, uma vez por comando e só se alguém perguntar, quem é admin.
type roles struct {
	once    sync.Once
	resolve func(ctx context.Context) (admin, super bool)
	admin   bool
	super   bool
}

func (r *roles) get(ctx context.Context) (bool, bool) {
	r.once.Do(func() {
		if r.resolve != nil {
			r.admin, r.super = r.resolve(ctx)
		}
	})
	return r.admin, r.super
}

// IsAdmin diz se o autor é admin do grupo ou super admin do bot.
func (c *Context) IsAdmin(ctx context.Context) bool {
	admin, super := c.roles.get(ctx)
	return admin || super
}

// IsSuperAdmin diz se o autor é super admin do bot.
func (c *Context) IsSuperAdmin(ctx context.Context) bool {
	_, super := c.roles.get(ctx)
	return super
}

// ArgText são os argumentos juntados por espaço.
func (c *Context) ArgText() string { return strings.Join(c.Args, " ") }

// Reply responde citando a mensagem do comando.
func (c *Context) Reply(ctx context.Context, out Outgoing) error {
	return c.Client.Reply(ctx, c.Message, out)
}

// ReplyText responde com texto.
func (c *Context) ReplyText(ctx context.Context, format string, args ...any) error {
	if len(args) > 0 {
		format = fmt.Sprintf(format, args...)
	}
	return c.Reply(ctx, Text(format))
}

// Observer vê toda mensagem admitida, antes dos comandos (ranking, buffer de
// conversa...). Deve ser rápido e não pode responder: roda no caminho de toda mensagem.
type Observer func(ctx context.Context, msg *Message)

// Listener trata mensagens que não são comandos (figurinha automática,
// interações...). Devolve true quando tratou a mensagem.
type Listener func(ctx context.Context, msg *Message, client Client) (bool, error)

// UserError é um erro cuja mensagem pode ser mostrada a quem chamou o comando.
type UserError struct{ Msg string }

func (e *UserError) Error() string { return e.Msg }

// Userf cria um UserError.
func Userf(format string, args ...any) error {
	if len(args) == 0 {
		return &UserError{Msg: format}
	}
	return &UserError{Msg: fmt.Sprintf(format, args...)}
}

// ErrSilent faz o roteador ignorar a mensagem sem responder nem reagir.
var ErrSilent = errors.New("ignorado")

const genericError = "Ops, algo deu errado. Tente de novo em instantes."

// Policy aplica as regras de configuração (por grupo, anti-spam...) sem que o
// roteador precise conhecer o banco.
type Policy interface {
	// Prefix devolve o prefixo de comandos do chat ("" usa o padrão).
	Prefix(ctx context.Context, msg *Message) string
	// Admit diz se a mensagem deve ser processada (usuário ignorado...).
	Admit(ctx context.Context, msg *Message) bool
	// Allow decide se o comando pode rodar: nil, ErrSilent ou UserError.
	Allow(ctx context.Context, c *Context) error
	// Roles diz se o autor é admin do grupo e/ou super admin do bot.
	Roles(ctx context.Context, msg *Message) (admin, super bool)
	// Done é chamado ao fim de cada comando que rodou (estatísticas).
	Done(ctx context.Context, c *Context, err error)
}

// Router decide o que fazer com cada mensagem.
type Router struct {
	prefix    string
	client    Client
	log       *slog.Logger
	policy    Policy
	commands  map[string]*Command
	ordered   []*Command
	triggers  map[string]*Command
	listeners []Listener
	observers []Observer
}

// NewRouter cria um Router com o prefixo padrão de comandos (ex.: "!").
func NewRouter(prefix string, client Client, log *slog.Logger) *Router {
	return &Router{prefix: prefix, client: client, log: log, commands: map[string]*Command{}, triggers: map[string]*Command{}}
}

// SetPolicy define as regras de configuração. Sem Policy, tudo é permitido.
func (r *Router) SetPolicy(p Policy) { r.policy = p }

// DefaultPrefix é o prefixo padrão.
func (r *Router) DefaultPrefix() string { return r.prefix }

// Register adiciona comandos; nomes e aliases são case-insensitive e únicos.
func (r *Router) Register(cmds ...*Command) error {
	for _, cmd := range cmds {
		if cmd.Run == nil {
			return fmt.Errorf("comando %q sem Run", cmd.Name)
		}
		for _, name := range cmd.Names() {
			key := strings.ToLower(name)
			if prev, ok := r.commands[key]; ok {
				return fmt.Errorf("%q já registrado por %q", key, prev.Name)
			}
			r.commands[key] = cmd
		}
		for _, emoji := range cmd.Triggers {
			if prev, ok := r.triggers[emoji]; ok {
				return fmt.Errorf("reação %q já registrada por %q", emoji, prev.Name)
			}
			r.triggers[emoji] = cmd
		}
		r.ordered = append(r.ordered, cmd)
	}
	return nil
}

// Commands lista os comandos registrados, na ordem de registro.
func (r *Router) Commands() []*Command { return slices.Clone(r.ordered) }

// Lookup acha um comando pelo nome ou alias.
func (r *Router) Lookup(name string) (*Command, bool) {
	c, ok := r.commands[strings.ToLower(name)]
	return c, ok
}

// Observe adiciona um Observer.
func (r *Router) Observe(o Observer) { r.observers = append(r.observers, o) }

// Listen adiciona um Listener; eles rodam em ordem até um tratar a mensagem.
func (r *Router) Listen(l Listener) { r.listeners = append(r.listeners, l) }

// Parse separa "!cmd a b" em ("cmd", ["a", "b"]) usando o prefixo padrão.
func (r *Router) Parse(text string) (name string, args []string, ok bool) {
	return Parse(r.prefix, text)
}

// Parse separa "<prefixo>cmd a b" em ("cmd", ["a", "b"]).
func Parse(prefix, text string) (string, []string, bool) {
	text = strings.TrimSpace(text)
	if prefix == "" || !strings.HasPrefix(text, prefix) {
		return "", nil, false
	}
	fields := strings.Fields(text[len(prefix):])
	if len(fields) == 0 {
		return "", nil, false
	}
	return strings.ToLower(fields[0]), fields[1:], true
}

// PrefixFor devolve o prefixo usado em msg.Chat.
func (r *Router) PrefixFor(ctx context.Context, msg *Message) string {
	if r.policy != nil {
		if p := r.policy.Prefix(ctx, msg); p != "" {
			return p
		}
	}
	return r.prefix
}

// Handle processa uma mensagem. Nunca entra em pânico.
func (r *Router) Handle(ctx context.Context, msg *Message) {
	if msg.FromMe {
		return
	}
	defer r.recover(msg)
	if r.policy != nil && !r.policy.Admit(ctx, msg) {
		return
	}
	for _, o := range r.observers {
		o(ctx, msg)
	}

	prefix := r.PrefixFor(ctx, msg)
	if name, args, ok := Parse(prefix, msg.Text); ok {
		if cmd, found := r.commands[name]; found {
			r.run(ctx, r.newContext(msg, cmd, name, args, prefix))
			return
		}
	}
	for _, l := range r.listeners {
		handled, err := l(ctx, msg, r.client)
		if err != nil {
			r.log.Error("listener falhou", "err", err, "msg", msg.ID, "chat", msg.Chat)
		}
		if handled {
			return
		}
	}
}

// HandleReaction roda o comando ligado a emoji sobre target, a mensagem que
// recebeu a reação. O texto/legenda dela vira os argumentos.
func (r *Router) HandleReaction(ctx context.Context, target *Message, emoji string) {
	cmd, ok := r.triggers[emoji]
	if !ok {
		return
	}
	defer r.recover(target)
	if r.policy != nil && !r.policy.Admit(ctx, target) {
		return
	}
	r.run(ctx, r.newContext(target, cmd, emoji, strings.Fields(target.Text), r.PrefixFor(ctx, target)))
}

// Run executa cmd como se msg o tivesse chamado (usado por interações automáticas).
func (r *Router) Run(ctx context.Context, msg *Message, cmd *Command, args []string) {
	defer r.recover(msg)
	r.run(ctx, r.newContext(msg, cmd, cmd.Name, args, r.PrefixFor(ctx, msg)))
}

// HasTrigger diz se emoji dispara algum comando.
func (r *Router) HasTrigger(emoji string) bool {
	_, ok := r.triggers[emoji]
	return ok
}

func (r *Router) newContext(msg *Message, cmd *Command, invoked string, args []string, prefix string) *Context {
	c := &Context{Message: msg, Command: cmd, Invoked: invoked, Args: args, Client: r.client, Prefix: prefix}
	if r.policy != nil {
		c.roles.resolve = func(ctx context.Context) (bool, bool) { return r.policy.Roles(ctx, msg) }
	}
	return c
}

func (r *Router) recover(msg *Message) {
	if p := recover(); p != nil {
		r.log.Error("pânico ao tratar mensagem", "panic", p, "msg", msg.ID, "stack", string(debug.Stack()))
	}
}

// check aplica as restrições declaradas no próprio comando.
func check(ctx context.Context, c *Context) error {
	cmd := c.Command
	switch {
	case cmd.GroupOnly && !c.Message.IsGroup:
		return Userf("Esse comando só funciona em grupos.")
	case cmd.SuperAdminOnly && !c.IsSuperAdmin(ctx):
		return ErrSilent
	case cmd.AdminOnly && c.Message.IsGroup && !c.IsAdmin(ctx):
		return Userf("Só admins do grupo podem usar esse comando.")
	}
	return nil
}

func (r *Router) run(ctx context.Context, c *Context) {
	log := r.log.With("cmd", c.Command.Name, "chat", c.Message.Chat, "msg", c.Message.ID)
	react := func(emoji string) {
		if emoji == "" {
			return
		}
		if err := r.client.React(ctx, c.Message, emoji); err != nil {
			log.Warn("falha ao reagir", "err", err)
		}
	}

	if r.policy != nil {
		if err := r.policy.Allow(ctx, c); err != nil {
			r.refuse(ctx, c, log, err)
			return
		}
	}
	if err := check(ctx, c); err != nil {
		r.refuse(ctx, c, log, err)
		return
	}

	react(c.Command.Reactions.Before)
	err := c.Command.Run(ctx, c)
	if r.policy != nil {
		r.policy.Done(ctx, c, err)
	}
	switch {
	case err == nil:
		react(c.Command.Reactions.After)
	case errors.Is(err, ErrSilent):
		react("") // nada a fazer
	default:
		react(c.Command.Reactions.Error)
		r.refuse(ctx, c, log, err)
	}
}

// refuse responde ao usuário um erro (exceto ErrSilent).
func (r *Router) refuse(ctx context.Context, c *Context, log *slog.Logger, err error) {
	if errors.Is(err, ErrSilent) {
		return
	}
	text := genericError
	if ue, ok := errors.AsType[*UserError](err); ok {
		text = ue.Msg
		log.Info("comando recusado", "reason", ue.Msg)
	} else {
		log.Error("comando falhou", "err", err)
	}
	if err := c.Reply(ctx, Text(text)); err != nil {
		log.Warn("falha ao responder erro", "err", err)
	}
}
