package wa

import (
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// Recent guarda as últimas mensagens com mídia para que reações a elas
// (ex.: 🖼 → figurinha) possam ser tratadas. O whatsmeow não guarda
// histórico; aqui ficam só os metadados (a mídia é baixada sob demanda).
type Recent struct {
	mu   sync.Mutex
	ttl  time.Duration
	now  func() time.Time
	byID map[recentKey]recentEntry
	ring []recentKey // ordem de chegada; o mais antigo é sobrescrito
	next int
}

type recentKey struct {
	chat types.JID
	id   types.MessageID
}

type recentEntry struct {
	evt  *events.Message
	seen time.Time
}

// NewRecent cria um cache com até size mensagens, válidas por ttl.
func NewRecent(size int, ttl time.Duration) *Recent {
	return &Recent{ttl: ttl, now: time.Now, byID: make(map[recentKey]recentEntry, size), ring: make([]recentKey, size)}
}

// Add guarda evt se ele tiver mídia.
func (r *Recent) Add(evt *events.Message) {
	if mediaOf(nil, unwrap(evt.Message)) == nil {
		return
	}
	k := recentKey{chat: evt.Info.Chat, id: evt.Info.ID}
	r.mu.Lock()
	defer r.mu.Unlock()
	if old := r.ring[r.next]; old.id != "" {
		delete(r.byID, old)
	}
	r.ring[r.next] = k
	r.next = (r.next + 1) % len(r.ring)
	r.byID[k] = recentEntry{evt: evt, seen: r.now()}
}

// Get devolve a mensagem chat/id, se ainda estiver no cache.
func (r *Recent) Get(chat types.JID, id types.MessageID) (*events.Message, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.byID[recentKey{chat: chat, id: id}]
	if !ok || r.now().Sub(e.seen) > r.ttl {
		return nil, false
	}
	return e.evt, true
}

// Reaction é uma reação recebida.
type Reaction struct {
	Chat     types.JID
	TargetID types.MessageID
	Emoji    string
}

// ReactionOf extrai a reação de evt (ok=false se não for reação ou se for remoção).
func ReactionOf(evt *events.Message) (Reaction, bool) {
	rm := evt.Message.GetReactionMessage()
	if rm == nil || rm.GetText() == "" {
		return Reaction{}, false
	}
	return Reaction{Chat: evt.Info.Chat, TargetID: rm.GetKey().GetID(), Emoji: rm.GetText()}, true
}
