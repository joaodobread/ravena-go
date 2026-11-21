package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
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
	Category    string
	Reactions   Reactions
	Run         Handler
}

// Context é o que um Handler recebe.
type Context struct {
	Message *Message
	Command *Command
	Invoked string // nome/alias usado
	Args    []string
	Client  Client
}

// Reply responde citando a mensagem do comando.
func (c *Context) Reply(ctx context.Context, out Outgoing) error {
	return c.Client.Reply(ctx, c.Message, out)
}

// Listener trata mensagens que não são comandos (figurinha automática,
// respostas aleatórias...). Devolve true quando tratou a mensagem.
type Listener func(ctx context.Context, msg *Message, client Client) (bool, error)

// UserError é um erro cuja mensagem pode ser mostrada a quem chamou o comando.
type UserError struct{ Msg string }

func (e *UserError) Error() string { return e.Msg }

// Userf cria um UserError.
func Userf(format string, args ...any) error {
	return &UserError{Msg: fmt.Sprintf(format, args...)}
}

const genericError = "Ops, algo deu errado. Tente de novo em instantes."

// Router decide o que fazer com cada mensagem.
type Router struct {
	prefix    string
	client    Client
	log       *slog.Logger
	commands  map[string]*Command
	listeners []Listener
}

// NewRouter cria um Router com o prefixo de comandos (ex.: "!").
func NewRouter(prefix string, client Client, log *slog.Logger) *Router {
	return &Router{prefix: prefix, client: client, log: log, commands: map[string]*Command{}}
}

// Register adiciona comandos; nomes e aliases são case-insensitive e únicos.
func (r *Router) Register(cmds ...*Command) error {
	for _, cmd := range cmds {
		if cmd.Run == nil {
			return fmt.Errorf("comando %q sem Run", cmd.Name)
		}
		for _, name := range append([]string{cmd.Name}, cmd.Aliases...) {
			key := strings.ToLower(name)
			if prev, ok := r.commands[key]; ok {
				return fmt.Errorf("%q já registrado por %q", key, prev.Name)
			}
			r.commands[key] = cmd
		}
	}
	return nil
}

// Listen adiciona um Listener; eles rodam em ordem até um tratar a mensagem.
func (r *Router) Listen(l Listener) { r.listeners = append(r.listeners, l) }

// Parse separa "!cmd a b" em ("cmd", ["a", "b"]).
func (r *Router) Parse(text string) (name string, args []string, ok bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, r.prefix) {
		return "", nil, false
	}
	fields := strings.Fields(text[len(r.prefix):])
	if len(fields) == 0 {
		return "", nil, false
	}
	return strings.ToLower(fields[0]), fields[1:], true
}

// Handle processa uma mensagem. Nunca entra em pânico.
func (r *Router) Handle(ctx context.Context, msg *Message) {
	if msg.FromMe {
		return
	}
	defer func() {
		if p := recover(); p != nil {
			r.log.Error("pânico ao tratar mensagem", "panic", p, "msg", msg.ID, "stack", string(debug.Stack()))
		}
	}()

	if name, args, ok := r.Parse(msg.Text); ok {
		if cmd, found := r.commands[name]; found {
			r.run(ctx, &Context{Message: msg, Command: cmd, Invoked: name, Args: args, Client: r.client})
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

	react(c.Command.Reactions.Before)
	err := c.Command.Run(ctx, c)
	if err == nil {
		react(c.Command.Reactions.After)
		return
	}

	react(c.Command.Reactions.Error)
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
