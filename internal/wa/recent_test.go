package wa

import (
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func imageEvent(id string) *events.Message {
	e := event(&waE2E.Message{ImageMessage: &waE2E.ImageMessage{}})
	e.Info.ID = id
	return e
}

func TestRecentKeepsMessagesAndEvictsOldest(t *testing.T) {
	r := NewRecent(2, time.Hour)
	text := event(&waE2E.Message{Conversation: new("oi")})
	text.Info.ID = "T"
	r.Add(text)
	r.Add(imageEvent("A"))
	r.Add(imageEvent("B"))
	r.Add(imageEvent("C")) // expulsa A

	for _, id := range []types.MessageID{"T", "A"} {
		if _, ok := r.Get(group, id); ok {
			t.Errorf("%s deveria ter sido expulsa (capacidade 2)", id)
		}
	}
	reaction := event(&waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{Text: new("👍")}})
	reaction.Info.ID = "R"
	r.Add(reaction)
	if _, ok := r.Get(group, "R"); ok {
		t.Error("reação não deveria ser guardada")
	}
	for _, id := range []types.MessageID{"B", "C"} {
		if _, ok := r.Get(group, id); !ok {
			t.Errorf("%s deveria estar no cache", id)
		}
	}
	if _, ok := r.Get(sender, "C"); ok {
		t.Error("chave deveria incluir o chat")
	}
}

func TestRecentExpires(t *testing.T) {
	now := time.Unix(0, 0)
	r := NewRecent(4, time.Minute)
	r.now = func() time.Time { return now }
	r.Add(imageEvent("A"))
	now = now.Add(2 * time.Minute)
	if _, ok := r.Get(group, "A"); ok {
		t.Fatal("deveria ter expirado")
	}
}

func TestReactionOf(t *testing.T) {
	react := func(text string) *events.Message {
		return event(&waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{
			Key: &waCommon.MessageKey{ID: new("ALVO")}, Text: new(text),
		}})
	}
	got, ok := ReactionOf(react("🖼"))
	if !ok || got != (Reaction{Chat: group, TargetID: "ALVO", Emoji: "🖼", From: sender}) {
		t.Fatalf("got %+v %v", got, ok)
	}
	if _, ok := ReactionOf(react("")); ok {
		t.Error("remoção de reação deveria ser ignorada")
	}
	if _, ok := ReactionOf(event(&waE2E.Message{Conversation: new("oi")})); ok {
		t.Error("texto não é reação")
	}
}
