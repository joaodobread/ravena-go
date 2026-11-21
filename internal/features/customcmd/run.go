package customcmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/joaodobread/ravena-go/internal/bot"
)

// listen é o bot.Listener: o roteador já tentou os comandos fixos, então um
// texto com prefixo que chegou aqui pode ser um comando personalizado.
func (m *Module) listen(ctx context.Context, msg *bot.Message, _ bot.Client) (bool, error) {
	if !msg.IsGroup || msg.FromMe {
		return false, nil
	}
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return false, nil
	}
	e, err := m.load(ctx, msg.Chat.String())
	if err != nil {
		return false, err
	}
	if len(e.cmds) == 0 {
		return false, nil
	}
	if g := m.groupConfig(ctx, msg.Chat); g != nil && g.Paused {
		return false, nil
	}

	prefix := m.router.PrefixFor(ctx, msg)
	if body, ok := strings.CutPrefix(text, prefix); ok && prefix != "" {
		if cmd, args := match(e.cmds, strings.Fields(body)); cmd != nil {
			return true, m.execute(ctx, msg, cmd, args, false)
		}
		return false, nil
	}
	if e.ignorePrefix {
		// Grupo sem prefixo para personalizados: o texto inteiro é o gatilho
		// (igual ou começando com ele), como no processCustomIgnoresPrefix.
		if cmd := matchWhole(e.cmds, text); cmd != nil {
			return true, m.execute(ctx, msg, cmd, nil, false)
		}
	}
	lower := strings.ToLower(text)
	for _, c := range e.cmds {
		if c.Active && c.IgnorePrefix && strings.Contains(lower, c.Trigger) {
			return true, m.execute(ctx, msg, c, nil, false)
		}
	}
	return false, nil
}

// match procura o gatilho entre os prefixos de até 10 palavras da entrada:
// primeiro igualdade (do maior para o menor), depois "começa com".
func match(cmds []*Command, words []string) (*Command, []string) {
	if len(words) == 0 {
		return nil, nil
	}
	n := min(len(words), MaxTriggerLen)
	cands := make([]string, n)
	for i := range n {
		cands[i] = strings.ToLower(strings.Join(words[:i+1], " "))
	}
	for i := n - 1; i >= 0; i-- {
		if c := findCmd(cmds, cands[i]); c != nil && c.Active {
			return c, words[i+1:]
		}
	}
	for i := n - 1; i >= 0; i-- {
		for _, c := range cmds {
			if c.Active && strings.HasPrefix(cands[i], c.Trigger) {
				return c, words[i+1:]
			}
		}
	}
	return nil, nil
}

func matchWhole(cmds []*Command, text string) *Command {
	t := strings.ToLower(strings.Join(strings.Fields(text), " "))
	if c := findCmd(cmds, t); c != nil && c.Active {
		return c
	}
	for _, c := range cmds {
		if c.Active && strings.HasPrefix(t, c.Trigger) {
			return c
		}
	}
	return nil
}

// execute roda o comando. silent (interação automática) pula reações,
// checagens de cooldown/anti-spam e mensagens de recusa.
func (m *Module) execute(ctx context.Context, msg *bot.Message, cmd *Command, args []string, silent bool) error {
	if len(cmd.Responses) == 0 {
		return nil
	}
	react := func(emoji string) {
		if emoji != "" && !silent {
			if err := m.client.React(ctx, msg, emoji); err != nil {
				m.log.Warn("falha ao reagir", "err", err)
			}
		}
	}
	if cmd.AdminOnly && !m.isAdmin(ctx, msg) {
		if silent {
			return nil
		}
		react("⛔️")
		return m.client.Reply(ctx, msg, bot.Text(fmt.Sprintf("O comando *%s* só pode ser usado por _administradores_.", cmd.Trigger)))
	}

	// Pausa, mute da categoria, anti-spam e cooldown vêm da policy do bot.
	bc := &bot.Context{
		Message: msg, Invoked: cmd.Trigger, Args: args, Client: m.client, Prefix: m.router.PrefixFor(ctx, msg),
		Command: &bot.Command{Name: cmd.Trigger, Category: Category, Cooldown: time.Duration(cmd.CooldownSec) * time.Second},
	}
	// Respostas só com {cmd-} passam pela policy nos próprios comandos
	// embutidos; checar aqui também faria o anti-spam barrá-los.
	if m.policy != nil && !silent && !onlyEmbedded(cmd) {
		if err := m.policy.Allow(ctx, bc); err != nil {
			if ue, ok := errors.AsType[*bot.UserError](err); ok {
				react("😴")
				return m.client.Reply(ctx, msg, bot.Text(ue.Msg))
			}
			return nil // ErrSilent
		}
	}

	react(cmd.StartReact)
	chat := msg.Chat.String()
	if err := m.markUsed(ctx, chat, cmd.Trigger); err != nil {
		m.log.Warn("falha ao contar uso", "err", err, "cmd", cmd.Trigger)
	}

	responses := cmd.Responses
	if !cmd.SendAll {
		responses = []Response{responses[m.rand(len(responses))]}
	}
	var err error
	for _, r := range responses {
		if err = m.sendResponse(ctx, msg, cmd, r, args, bc.Prefix); err != nil {
			break
		}
	}
	if m.policy != nil && !silent {
		m.policy.Done(ctx, bc, err)
	}
	if err != nil {
		react("❌")
		return err
	}
	react(cmd.React)
	return nil
}

func onlyEmbedded(cmd *Command) bool {
	for _, r := range cmd.Responses {
		if r.IsMedia() || !cmdOnlyRe.MatchString(r.Text) {
			return false
		}
	}
	return true
}

func (m *Module) isAdmin(ctx context.Context, msg *bot.Message) bool {
	if m.policy == nil {
		return false
	}
	admin, super := m.policy.Roles(ctx, msg)
	return admin || super
}

func (m *Module) sendResponse(ctx context.Context, msg *bot.Message, cmd *Command, r Response, args []string, prefix string) error {
	if !r.IsMedia() && cmdOnlyRe.MatchString(r.Text) {
		return m.runEmbedded(ctx, msg, r.Text, args, prefix)
	}
	ex := m.newExpander(ctx, msg, cmd.Count+1)
	text := cmdRe.ReplaceAllString(r.Text, "[Comando embutido: $1]")
	text = ex.expand(text)

	var out bot.Outgoing
	if r.IsMedia() {
		var err error
		if out, err = m.mediaOut(msg.Chat.String(), r, text); err != nil {
			return fmt.Errorf("mídia do comando %q: %w", cmd.Trigger, err)
		}
	} else {
		if strings.TrimSpace(text) == "" {
			return nil
		}
		out = bot.Text(text)
	}

	for _, s := range cmd.Mentions {
		if j, err := types.ParseJID(s); err == nil {
			ex.mention(j)
		}
	}
	ex.mention(msg.Sender) // o Ravena marca o autor em toda resposta no grupo
	out.Mentions = ex.mentions

	switch {
	case cmd.Private:
		return m.client.Send(ctx, msg.Sender, out)
	case cmd.NoQuote:
		return m.client.Send(ctx, msg.Chat, out)
	}
	return m.client.Reply(ctx, msg, out)
}

// runEmbedded roda os {cmd-!comando args} de uma resposta como comandos do
// bot, com os argumentos do personalizado no fim.
func (m *Module) runEmbedded(ctx context.Context, msg *bot.Message, text string, args []string, prefix string) error {
	for i, sm := range cmdRe.FindAllStringSubmatch(text, -1) {
		if i >= 10 {
			break
		}
		line := strings.TrimSpace(sm[1])
		line = strings.TrimPrefix(line, prefix)
		line = strings.TrimPrefix(line, "!")
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		c, ok := m.router.Lookup(fields[0])
		if !ok {
			continue
		}
		m.router.Run(ctx, msg, c, append(fields[1:], args...))
	}
	return nil
}
