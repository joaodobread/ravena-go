package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/llm"
	"github.com/joaodobread/ravena-go/internal/store"
)

// Mensagens de resumo/interação.
const (
	MsgNoSummary  = "Nenhuma mensagem recente para resumir."
	MsgNoInteract = "Nenhuma mensagem recente para interagir."
	MsgSummaryErr = "Falha ao gerar resumo. Por favor, tente novamente."
	MsgInteractEr = "Falha ao gerar mensagem. Por favor, tente novamente."
)

const (
	summaryPrompt  = "Resuma de forma concisa, em português do Brasil e em tópicos curtos, os principais pontos da conversa de grupo de WhatsApp abaixo.\n\n"
	interactPrompt = "Abaixo está uma conversa recente de um grupo de WhatsApp. Escreva UMA única mensagem curta e natural para participar da conversa com algo relevante, " +
		"no mesmo tom e estilo informal das pessoas. Responda só com a mensagem, em português do Brasil.\n\n"
	// interactTimeout limita a interação automática em segundo plano.
	interactTimeout = 60 * time.Second
)

func (f *Feature) runResumo(ctx context.Context, c *bot.Context) error {
	if f.LLM == nil {
		return bot.Userf(MsgUnavailable)
	}
	chat := c.Message.Chat.String()
	entries := f.Buffer.Recent(chat)
	if len(entries) == 0 {
		return bot.Userf(MsgNoSummary)
	}
	summary, err := f.LLM.Chat(ctx, llm.ChatRequest{
		System:      fmt.Sprintf("Você é %s, um bot de WhatsApp.", f.name()),
		Messages:    []llm.Message{{Role: "user", Text: summaryPrompt + formatEntries(entries)}},
		Temperature: 0.5,
		MaxTokens:   500,
	})
	if err != nil {
		f.log().Error("resumo: LLM falhou", "err", err)
		return bot.Userf(MsgSummaryErr)
	}
	f.Buffer.Clear(chat)
	return c.Reply(ctx, bot.Text("📋 *Resumo da conversa:*\n\n"+strings.TrimSpace(summary)))
}

var errNothing = errors.New("sem mensagens")

// interaction gera a mensagem de interação a partir do histórico de chat.
func (f *Feature) interaction(ctx context.Context, chat string) (bot.Outgoing, error) {
	entries := f.Buffer.Recent(chat)
	if len(entries) == 0 {
		return bot.Outgoing{}, errNothing
	}
	text, err := f.LLM.Chat(ctx, llm.ChatRequest{
		System:      fmt.Sprintf("Você é %s, um bot de WhatsApp que participa do grupo.", f.name()),
		Messages:    []llm.Message{{Role: "user", Text: interactPrompt + formatEntries(entries)}},
		Temperature: 0.9,
		MaxTokens:   200,
	})
	if err != nil {
		return bot.Outgoing{}, err
	}
	text = strings.TrimSpace(text)
	out := bot.Text(text)
	out.Mentions = mentionsIn(text)
	return out, nil
}

func (f *Feature) runInteragir(ctx context.Context, c *bot.Context) error {
	if f.LLM == nil {
		return bot.Userf(MsgUnavailable)
	}
	chat := c.Message.Chat
	out, err := f.interaction(ctx, chat.String())
	if errors.Is(err, errNothing) {
		return bot.Userf(MsgNoInteract)
	}
	if err != nil {
		f.log().Error("interagir: LLM falhou", "err", err)
		return bot.Userf(MsgInteractEr)
	}
	f.Buffer.Clear(chat.String())
	return c.Client.Send(ctx, chat, out)
}

// InteractListener reproduz a interação automática do Ravena: com a chance
// e o cooldown do grupo, manda uma mensagem do LLM ou roda um comando
// personalizado aleatório. Nunca trata a mensagem (devolve false) e, quando
// não dispara, custa uma leitura do grupo (em cache) e um sorteio.
func (f *Feature) InteractListener(ctx context.Context, msg *bot.Message, client bot.Client) (bool, error) {
	if !msg.IsGroup || f.Store == nil || (f.LLM == nil && f.Custom == nil) {
		return false, nil
	}
	useLLM, fire, err := f.rollInteract(ctx, msg)
	if err != nil || !fire {
		return false, err
	}
	f.spawn(func() {
		ctx, cancel := context.WithTimeout(f.background(), interactTimeout)
		defer cancel()
		if err := f.autoInteract(ctx, msg, client, useLLM); err != nil {
			f.log().Warn("interação automática falhou", "err", err, "chat", msg.Chat)
		}
	})
	return false, nil
}

// rollInteract decide se a interação dispara e já grava Interact.Last.
func (f *Feature) rollInteract(ctx context.Context, msg *bot.Message) (useLLM, fire bool, err error) {
	jid := msg.Chat.String()
	f.interactMu.Lock() // evita duas interações simultâneas no mesmo instante
	defer f.interactMu.Unlock()

	g, ok, err := f.Store.Group(ctx, jid)
	if err != nil {
		return false, false, err
	}
	if !ok {
		g = store.NewGroup(jid)
	}
	in := g.Interact
	now := f.now()
	if !in.Enabled || g.Paused || now.Sub(in.Last) < in.EffectiveCooldown() {
		return false, false, nil
	}
	if f.randN(10000)+1 > in.EffectiveChance() {
		return false, false, nil
	}
	if _, err := f.Store.UpdateGroup(ctx, jid, func(g *store.Group) { g.Interact.Last = now }); err != nil {
		return false, false, err
	}
	useLLM = !in.UseCmds || f.Custom == nil || f.randN(100) < in.Proporcao
	if f.LLM == nil {
		useLLM = false // só chega aqui com Custom definido
	}
	if useLLM && g.IsMuted("interagir", "ia") {
		return false, false, nil // como o original: o !interagir silenciado não roda
	}
	return useLLM, true, nil
}

func (f *Feature) autoInteract(ctx context.Context, msg *bot.Message, client bot.Client, useLLM bool) error {
	if !useLLM {
		ran, err := f.Custom.RunRandom(ctx, msg)
		if err != nil || ran || f.LLM == nil {
			return err
		}
		// grupo sem comandos personalizados elegíveis: cai no LLM
	}
	out, err := f.interaction(ctx, msg.Chat.String())
	if errors.Is(err, errNothing) {
		return nil
	}
	if err != nil {
		return err
	}
	if out.Text == "" || strings.Contains(out.Text, "Não foi poss") {
		return nil // o modelo se recusou: no automático, fica quieto
	}
	f.Buffer.Clear(msg.Chat.String())
	return client.Send(ctx, msg.Chat, out)
}
