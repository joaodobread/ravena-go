// Package greetings envia boas-vindas e despedidas configuráveis por grupo.
package greetings

import (
	"context"
	"log/slog"
	"strings"

	"go.mau.fi/whatsmeow/types"

	"github.com/joaodobread/ravena-go/internal/app"
	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/store"
)

// Module implementa app.Feature.
type Module struct{}

// Name implementa app.Feature.
func (Module) Name() string { return "greetings" }

// Register implementa app.Feature.
func (Module) Register(_ context.Context, d *app.Deps) error {
	g := &greeter{store: d.Store, log: d.Log}
	d.Router.OnGroupEvent(g.handle)
	return d.Router.Register(g.commands()...)
}

type greeter struct {
	store *store.Store
	log   *slog.Logger
}

func (g *greeter) handle(ctx context.Context, ev bot.GroupEvent, client bot.Client) {
	grp, ok, err := g.store.Group(ctx, ev.Chat.String())
	if err != nil || !ok || grp.Paused {
		return
	}
	me := client.Me()
	send := func(tpl string, people []types.JID) {
		people = without(people, me)
		if tpl == "" || len(people) == 0 {
			return
		}
		name, _ := client.ChatName(ctx, ev.Chat)
		if err := client.Send(ctx, ev.Chat, bot.Outgoing{Kind: bot.OutText, Text: Render(tpl, people, name), Mentions: people}); err != nil {
			g.log.Warn("falha ao enviar boas-vindas/despedida", "err", err, "chat", ev.Chat)
		}
	}
	send(grp.Greeting, ev.Joined)
	send(grp.Farewell, ev.Left)
}

func without(people []types.JID, me types.JID) []types.JID {
	out := people[:0:0]
	for _, p := range people {
		if p.User != me.User {
			out = append(out, p)
		}
	}
	return out
}

// Render substitui as variáveis do Ravena original.
func Render(tpl string, people []types.JID, group string) string {
	mentions := make([]string, len(people))
	for i, p := range people {
		mentions[i] = bot.MentionText(p)
	}
	plural := len(people) > 1
	pick := func(many, one string) string {
		if plural {
			return many
		}
		return one
	}
	return strings.NewReplacer(
		"{pessoa}", strings.Join(mentions, ", "),
		"{nomePessoas}", strings.Join(mentions, ", "),
		"{numeroPessoas}", strings.Join(mentions, ", "),
		"{tituloGrupo}", group,
		"{grupo}", group,
		"{plural_s}", pick("s", ""),
		"{plural_S}", pick("S", ""),
		"{plural_m}", pick("m", ""),
		"{plural_M}", pick("M", ""),
		"{plural_esao}", pick("são", "é"),
	).Replace(tpl)
}

const help = "Variáveis: {pessoa} (menciona quem entrou/saiu), {grupo}, {plural_s}, {plural_esao}."

func (g *greeter) commands() []*bot.Command {
	set := func(name, desc string, field func(*store.Group) *string, label string) *bot.Command {
		return &bot.Command{
			Name: name, Description: desc, Category: "grupo", AdminOnly: true, GroupOnly: true,
			Usage: "!" + name + " Bem-vindo{plural_s}, {pessoa}!",
			Run: func(ctx context.Context, c *bot.Context) error {
				text := c.RawArgs()
				if text == "" {
					grp, err := g.store.GroupOrDefault(ctx, c.Message.Chat.String())
					if err != nil {
						return err
					}
					if cur := *field(grp); cur != "" {
						return c.ReplyText(ctx, "Mensagem de %s atual:\n\n%s\n\n%s", label, cur, help)
					}
					return c.ReplyText(ctx, "Sem mensagem de %s. Use %s%s <texto>.\n%s", label, c.Prefix, name, help)
				}
				if _, err := g.store.UpdateGroup(ctx, c.Message.Chat.String(), func(grp *store.Group) { *field(grp) = text }); err != nil {
					return err
				}
				return c.ReplyText(ctx, "✅ Mensagem de %s definida.", label)
			},
		}
	}
	del := func(name string, field func(*store.Group) *string, label string) *bot.Command {
		return &bot.Command{
			Name: name, Description: "Remove a mensagem de " + label, Category: "grupo", AdminOnly: true, GroupOnly: true,
			Run: func(ctx context.Context, c *bot.Context) error {
				if _, err := g.store.UpdateGroup(ctx, c.Message.Chat.String(), func(grp *store.Group) { *field(grp) = "" }); err != nil {
					return err
				}
				return c.ReplyText(ctx, "🗑️ Mensagem de %s removida.", label)
			},
		}
	}
	greeting := func(grp *store.Group) *string { return &grp.Greeting }
	farewell := func(grp *store.Group) *string { return &grp.Farewell }
	return []*bot.Command{
		set("g-setBoasvindas", "Define a mensagem de boas-vindas", greeting, "boas-vindas"),
		del("g-delBoasvindas", greeting, "boas-vindas"),
		set("g-setDespedida", "Define a mensagem de despedida", farewell, "despedida"),
		del("g-delDespedida", farewell, "despedida"),
	}
}
