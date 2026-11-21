package customcmd

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/joaodobread/ravena-go/internal/bot"
)

const okReact = "✅"

func (m *Module) commands() []*bot.Command {
	mk := func(name, usage, desc string, run bot.Handler) *bot.Command {
		return &bot.Command{
			Name: name, Usage: usage, Description: desc, Category: "grupo",
			AdminOnly: true, GroupOnly: true,
			Reactions: bot.Reactions{After: okReact, Error: "❌"},
			Run:       run,
		}
	}
	toggle := func(name, desc string, flip func(*Command) string) *bot.Command {
		return mk(name, "!"+name+" <comando>", desc, func(ctx context.Context, c *bot.Context) error {
			return m.toggle(ctx, c, flip)
		})
	}
	return []*bot.Command{
		mk("g-addCmd", "!g-addCmd <gatilho> <resposta> (ou respondendo a uma mensagem/mídia)", "Cria um comando personalizado", m.addCmd),
		mk("g-addCmdReply", "!g-addCmdReply <gatilho> <resposta> (ou respondendo)", "Adiciona outra resposta a um comando existente", m.addCmdReply),
		mk("g-delCmd", "!g-delCmd <gatilho>", "Exclui um comando personalizado", m.delCmd),
		mk("g-cmds", "!g-cmds", "Lista os comandos personalizados do grupo", m.listCmds),
		mk("g-variaveis", "!g-variaveis", "Lista as variáveis aceitas nas respostas", m.listVars),
		mk("g-setCustomSemPrefixo", "!g-setCustomSemPrefixo", "Faz com que comandos personalizados não precisem de prefixo", m.toggleGroupPrefix),
		toggle("g-cmd-enable", "Habilita um comando personalizado", func(cmd *Command) string {
			cmd.Active = true
			return "habilitado"
		}),
		toggle("g-cmd-disable", "Desabilita um comando personalizado", func(cmd *Command) string {
			cmd.Active = false
			return "desabilitado"
		}),
		toggle("g-cmd-setPV", "A resposta do comando vai para o PV de quem pediu", func(cmd *Command) string {
			cmd.Private = !cmd.Private
			if cmd.Private {
				return "agora é respondido no PV da pessoa que solicitou"
			}
			return "agora é respondido dentro do grupo (padrão)"
		}),
		toggle("g-cmd-enviarTudo", "Envia todas as respostas do comando (se houver mais de uma)", func(cmd *Command) string {
			cmd.SendAll = !cmd.SendAll
			if cmd.SendAll {
				return "agora envia TODAS as respostas"
			}
			return "agora envia UMA resposta aleatória"
		}),
		toggle("g-cmd-responder", "Alterna se o comando responde citando a mensagem", func(cmd *Command) string {
			cmd.NoQuote = !cmd.NoQuote
			if cmd.NoQuote {
				return "agora apenas envia a mensagem (sem citar)"
			}
			return "agora responde citando a mensagem"
		}),
		toggle("g-cmd-setAdm", "Alterna se apenas admins podem usar o comando", func(cmd *Command) string {
			cmd.AdminOnly = !cmd.AdminOnly
			if cmd.AdminOnly {
				return "agora só pode ser usado por administradores"
			}
			return "agora pode ser usado por todos"
		}),
		toggle("g-cmd-setInteragir", "Alterna se o comando é usado nas interações automáticas", func(cmd *Command) string {
			cmd.IgnoreInteract = !cmd.IgnoreInteract
			if cmd.IgnoreInteract {
				return "não será mais usado nas interações automáticas"
			}
			return "agora pode ser usado nas interações automáticas"
		}),
		toggle("g-cmd-semPrefixo", "Alterna se o comando dispara quando o gatilho aparece em qualquer mensagem", func(cmd *Command) string {
			cmd.IgnorePrefix = !cmd.IgnorePrefix
			if cmd.IgnorePrefix {
				return "agora dispara sempre que o gatilho aparecer numa mensagem, sem prefixo"
			}
			return "agora só dispara com o prefixo"
		}),
		mk("g-cmd-react", "!g-cmd-react <comando> <emoji|off>", "Reação ao terminar o comando", func(ctx context.Context, c *bot.Context) error {
			return m.setReact(ctx, c, false)
		}),
		mk("g-cmd-startReact", "!g-cmd-startReact <comando> <emoji|off>", "Reação ao começar o comando", func(ctx context.Context, c *bot.Context) error {
			return m.setReact(ctx, c, true)
		}),
		mk("g-cmd-cd", "!g-cmd-cd <comando> <segundos>", "Define o cooldown de um comando personalizado", m.setCooldown),
	}
}

// cutWord separa a primeira palavra do resto (preservando o resto).
func cutWord(s string) (string, string) {
	if i := strings.IndexFunc(s, unicode.IsSpace); i >= 0 {
		return s[:i], strings.TrimSpace(s[i:])
	}
	return s, ""
}

// normTrigger deixa o gatilho em minúsculas, sem prefixo e com espaços simples.
func normTrigger(s, prefix string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if prefix != "" && strings.HasPrefix(s, prefix) {
		s = s[len(prefix):]
	} else {
		s = strings.TrimPrefix(s, "!")
	}
	return strings.Join(strings.Fields(s), " ")
}

func (m *Module) validTrigger(t string) error {
	switch {
	case t == "":
		return bot.Userf("⚠️ Por favor, forneça um gatilho de comando válido.")
	case len(strings.Fields(t)) > MaxTriggerLen:
		return bot.Userf("O gatilho do comando não pode ter mais de %d palavras.", MaxTriggerLen)
	}
	first, _ := cutWord(t)
	if _, ok := m.router.Lookup(first); ok {
		return bot.Userf("Já existe um comando do bot chamado '%s'. Escolha outro gatilho.", first)
	}
	return nil
}

// parseNew lê gatilho e resposta de !g-addCmd/!g-addCmdReply.
func (m *Module) parseNew(ctx context.Context, c *bot.Context) (string, Response, error) {
	msg, chat := c.Message, c.Message.Chat.String()
	if len(c.Args) == 0 {
		return "", Response{}, bot.Userf("Por favor, forneça um gatilho. Exemplo: %s%s saudação Olá, {pessoa}!", c.Prefix, c.Command.Name)
	}
	if q := msg.Quoted; q != nil {
		trigger := normTrigger(strings.Join(c.Args, " "), c.Prefix)
		if q.Media != nil {
			r, err := m.saveMedia(ctx, chat, q.Media, q.Text)
			return trigger, r, err
		}
		if strings.TrimSpace(q.Text) == "" {
			return "", Response{}, bot.Userf("A mensagem respondida não tem texto nem mídia.")
		}
		return trigger, Response{Text: q.Text}, nil
	}
	word, body := cutWord(c.RawArgs())
	trigger := normTrigger(word, c.Prefix)
	if msg.Media != nil {
		r, err := m.saveMedia(ctx, chat, msg.Media, body)
		return trigger, r, err
	}
	if body == "" {
		return "", Response{}, bot.Userf("Escreva a resposta depois do gatilho ou use o comando respondendo a uma mensagem.\nEx.: %s%s %s Olá, {pessoa}!", c.Prefix, c.Command.Name, word)
	}
	return trigger, Response{Text: body}, nil
}

func (m *Module) addCmd(ctx context.Context, c *bot.Context) error {
	// Valida o gatilho antes de baixar mídia.
	if len(c.Args) > 0 {
		t := normTrigger(c.Args[0], c.Prefix)
		if c.Message.Quoted != nil {
			t = normTrigger(strings.Join(c.Args, " "), c.Prefix)
		}
		if err := m.validTrigger(t); err != nil {
			return err
		}
	}
	chat := c.Message.Chat.String()
	if n, err := m.count(ctx, chat); err != nil {
		return err
	} else if n >= MaxCommands {
		if old, _ := m.get(ctx, chat, normTrigger(c.Args[0], c.Prefix)); old == nil {
			return bot.Userf("O grupo já tem %d comandos personalizados, o máximo. Apague algum com %sg-delCmd.", MaxCommands, c.Prefix)
		}
	}
	trigger, resp, err := m.parseNew(ctx, c)
	if err != nil {
		return err
	}
	if err := m.validTrigger(trigger); err != nil {
		m.removeFiles(chat, []Response{resp})
		return err
	}
	cmd := &Command{
		Trigger: trigger, Responses: []Response{resp}, Active: true,
		CreatedBy: c.Message.Sender.User, CreatedAt: m.now().Unix(),
	}
	for _, j := range c.Message.Mentions {
		cmd.Mentions = append(cmd.Mentions, j.String())
	}
	m.writeMu.Lock()
	old, err := m.get(ctx, chat, trigger)
	if err == nil {
		err = m.save(ctx, chat, cmd)
	}
	m.writeMu.Unlock()
	if err != nil {
		m.removeFiles(chat, []Response{resp})
		return err
	}
	if old != nil {
		m.removeFiles(chat, old.Responses)
		return c.ReplyText(ctx, "Comando personalizado '%s' substituído.", trigger)
	}
	return c.ReplyText(ctx, "Comando personalizado '%s' adicionado com sucesso.", trigger)
}

func (m *Module) addCmdReply(ctx context.Context, c *bot.Context) error {
	chat := c.Message.Chat.String()
	if len(c.Args) > 0 {
		t := normTrigger(c.Args[0], c.Prefix)
		if c.Message.Quoted != nil {
			t = normTrigger(strings.Join(c.Args, " "), c.Prefix)
		}
		if old, err := m.get(ctx, chat, t); err != nil {
			return err
		} else if old == nil {
			return notFound(t)
		}
	}
	trigger, resp, err := m.parseNew(ctx, c)
	if err != nil {
		return err
	}
	_, err = m.update(ctx, chat, trigger, func(cmd *Command) error {
		if len(cmd.Responses) >= MaxResponses {
			return bot.Userf("O comando '%s' já tem %d respostas, o máximo.", trigger, MaxResponses)
		}
		cmd.Responses = append(cmd.Responses, resp)
		return nil
	})
	if err != nil {
		m.removeFiles(chat, []Response{resp})
		return wrapNotFound(err, trigger)
	}
	return c.ReplyText(ctx, "Adicionada nova resposta ao comando personalizado '%s'.", trigger)
}

func (m *Module) delCmd(ctx context.Context, c *bot.Context) error {
	trigger := normTrigger(c.ArgText(), c.Prefix)
	if trigger == "" {
		return bot.Userf("Por favor, forneça o comando personalizado a ser excluído. Exemplo: %sg-delCmd saudação", c.Prefix)
	}
	chat := c.Message.Chat.String()
	m.writeMu.Lock()
	cmd, err := m.get(ctx, chat, trigger)
	if err == nil && cmd != nil {
		err = m.remove(ctx, chat, trigger)
	}
	m.writeMu.Unlock()
	if err != nil {
		return err
	}
	if cmd == nil {
		return notFound(trigger)
	}
	m.removeFiles(chat, cmd.Responses)
	return c.ReplyText(ctx, "Comando personalizado '%s' excluído.", trigger)
}

func (m *Module) toggle(ctx context.Context, c *bot.Context, flip func(*Command) string) error {
	trigger := normTrigger(c.ArgText(), c.Prefix)
	if trigger == "" {
		return bot.Userf("Por favor, forneça o comando personalizado. Exemplo: %s%s saudação", c.Prefix, c.Command.Name)
	}
	var what string
	_, err := m.update(ctx, c.Message.Chat.String(), trigger, func(cmd *Command) error {
		what = flip(cmd)
		return nil
	})
	if err != nil {
		return wrapNotFound(err, trigger)
	}
	return c.ReplyText(ctx, "Comando personalizado '%s' %s.", trigger, what)
}

func (m *Module) setReact(ctx context.Context, c *bot.Context, start bool) error {
	if len(c.Args) < 2 {
		return bot.Userf("Por favor, forneça um comando e um emoji. Exemplo: %s%s saudação 🎯", c.Prefix, c.Command.Name)
	}
	emoji := c.Args[len(c.Args)-1]
	trigger := normTrigger(strings.Join(c.Args[:len(c.Args)-1], " "), c.Prefix)
	if strings.EqualFold(emoji, "off") || emoji == "-" {
		emoji = ""
	}
	_, err := m.update(ctx, c.Message.Chat.String(), trigger, func(cmd *Command) error {
		if start {
			cmd.StartReact = emoji
		} else {
			cmd.React = emoji
		}
		return nil
	})
	if err != nil {
		return wrapNotFound(err, trigger)
	}
	if emoji == "" {
		return c.ReplyText(ctx, "Reação do comando '%s' removida.", trigger)
	}
	return c.ReplyText(ctx, "Reação do comando '%s' definida para %s.", trigger, emoji)
}

func (m *Module) setCooldown(ctx context.Context, c *bot.Context) error {
	if len(c.Args) < 2 {
		return bot.Userf("Por favor, forneça um comando e o cooldown em segundos. Exemplo: %sg-cmd-cd saudação 30", c.Prefix)
	}
	secs, err := strconv.Atoi(c.Args[len(c.Args)-1])
	if err != nil {
		return bot.Userf("O cooldown deve ser um número (em segundos). Exemplo: %sg-cmd-cd saudação 30", c.Prefix)
	}
	note := ""
	switch {
	case secs <= 0:
		secs, note = 0, " (cooldown removido)"
	case secs > MaxCooldown:
		secs, note = MaxCooldown, fmt.Sprintf(" (máximo: %ds)", MaxCooldown)
	}
	trigger := normTrigger(strings.Join(c.Args[:len(c.Args)-1], " "), c.Prefix)
	if _, err := m.update(ctx, c.Message.Chat.String(), trigger, func(cmd *Command) error {
		cmd.CooldownSec = secs
		return nil
	}); err != nil {
		return wrapNotFound(err, trigger)
	}
	if secs == 0 {
		return c.ReplyText(ctx, "Cooldown do comando '%s' removido.", trigger)
	}
	return c.ReplyText(ctx, "Cooldown do comando '%s' definido para %d segundo(s)%s.", trigger, secs, note)
}

func (m *Module) toggleGroupPrefix(ctx context.Context, c *bot.Context) error {
	chat := c.Message.Chat.String()
	e, err := m.load(ctx, chat)
	if err != nil {
		return err
	}
	on := !e.ignorePrefix
	if err := m.setIgnorePrefix(ctx, chat, on); err != nil {
		return err
	}
	if on {
		return c.ReplyText(ctx, "Os comandos personalizados do grupo agora *não precisam* mais do prefixo pra serem ativados.")
	}
	return c.ReplyText(ctx, "Os comandos personalizados do grupo agora *precisam* do prefixo para serem ativados _(funcionamento normal)_.")
}

func (m *Module) listCmds(ctx context.Context, c *bot.Context) error {
	e, err := m.load(ctx, c.Message.Chat.String())
	if err != nil {
		return err
	}
	if len(e.cmds) == 0 {
		return c.ReplyText(ctx, "Este grupo ainda não tem comandos personalizados. Crie um com %sg-addCmd.", c.Prefix)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "*📋 Comandos personalizados (%d)*\n", len(e.cmds))
	if e.ignorePrefix {
		b.WriteString("_Funcionam sem prefixo._\n")
	}
	for _, cmd := range e.cmds {
		fmt.Fprintf(&b, "\n- %s%s (%d resp.)", c.Prefix, cmd.Trigger, len(cmd.Responses))
		var flags []string
		if !cmd.Active {
			flags = append(flags, "desativado")
		}
		if cmd.AdminOnly {
			flags = append(flags, "só admin")
		}
		if cmd.IgnorePrefix {
			flags = append(flags, "sem prefixo")
		}
		if cmd.CooldownSec > 0 {
			flags = append(flags, fmt.Sprintf("cd %ds", cmd.CooldownSec))
		}
		if len(flags) > 0 {
			fmt.Fprintf(&b, " _[%s]_", strings.Join(flags, ", "))
		}
	}
	return c.ReplyText(ctx, "%s", b.String())
}

func (m *Module) listVars(ctx context.Context, c *bot.Context) error {
	var b strings.Builder
	b.WriteString("*🔣 Variáveis para comandos personalizados*\n")
	for _, v := range Variables {
		fmt.Fprintf(&b, "\n%s — %s", v[0], v[1])
	}
	b.WriteString("\n\n_Números escritos como @5511999999999 também viram menções._")
	return c.ReplyText(ctx, "%s", b.String())
}
