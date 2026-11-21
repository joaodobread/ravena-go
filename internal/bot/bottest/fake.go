// Package bottest tem um Client falso para testes de funcionalidades.
package bottest

import (
	"context"
	"sync"

	"go.mau.fi/whatsmeow/types"

	"github.com/joaodobread/ravena-go/internal/bot"
)

// Reply é uma resposta registrada pelo FakeClient.
type Reply struct {
	To  *bot.Message
	Out bot.Outgoing
}

// FakeClient registra tudo o que o bot envia.
type FakeClient struct {
	mu        sync.Mutex
	Replies   []Reply
	Reactions []string
	Names     map[types.JID]string
	ReplyErr  error
}

func (f *FakeClient) Reply(_ context.Context, to *bot.Message, out bot.Outgoing) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ReplyErr != nil {
		return f.ReplyErr
	}
	f.Replies = append(f.Replies, Reply{To: to, Out: out})
	return nil
}

func (f *FakeClient) React(_ context.Context, _ *bot.Message, emoji string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Reactions = append(f.Reactions, emoji)
	return nil
}

func (f *FakeClient) ChatName(_ context.Context, chat types.JID) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Names[chat], nil
}

// Last devolve a última resposta (ou zero).
func (f *FakeClient) Last() Reply {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.Replies) == 0 {
		return Reply{}
	}
	return f.Replies[len(f.Replies)-1]
}

// StaticMedia cria uma bot.Media cujo Download devolve data.
func StaticMedia(kind bot.MediaKind, mime string, data []byte) *bot.Media {
	return &bot.Media{Kind: kind, Mime: mime, Download: func(context.Context) ([]byte, error) { return data, nil }}
}

// Group e User são JIDs de teste.
var (
	Group = types.NewJID("120363000000000000", types.GroupServer)
	User  = types.NewJID("5511999999999", types.DefaultUserServer)
)
