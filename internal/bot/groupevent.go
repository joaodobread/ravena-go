package bot

import (
	"context"

	"go.mau.fi/whatsmeow/types"
)

// GroupEvent é uma mudança de membros de um grupo.
type GroupEvent struct {
	Chat     types.JID
	By       types.JID // quem fez a mudança (vazio se entrou por link)
	Joined   []types.JID
	Left     []types.JID
	Promoted []types.JID
	Demoted  []types.JID
}

// GroupHandler trata eventos de membros (boas-vindas, despedidas...).
type GroupHandler func(ctx context.Context, ev GroupEvent, client Client)

// OnGroupEvent adiciona um GroupHandler.
func (r *Router) OnGroupEvent(h GroupHandler) { r.groupHandlers = append(r.groupHandlers, h) }

// HandleGroupEvent repassa ev para os GroupHandlers. Nunca entra em pânico.
func (r *Router) HandleGroupEvent(ctx context.Context, ev GroupEvent) {
	defer func() {
		if p := recover(); p != nil {
			r.log.Error("pânico ao tratar evento de grupo", "panic", p, "chat", ev.Chat)
		}
	}()
	for _, h := range r.groupHandlers {
		h(ctx, ev, r.client)
	}
}
