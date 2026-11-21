// Package bottest tem um Client falso para testes de funcionalidades.
package bottest

import (
	"context"
	"sync"

	"go.mau.fi/whatsmeow/types"

	"github.com/joaodobread/ravena-go/internal/bot"
)

// Reply é uma resposta registrada pelo FakeClient. To é nil em envios sem citação.
type Reply struct {
	To   *bot.Message
	Chat types.JID
	Out  bot.Outgoing
}

// FakeClient registra tudo o que o bot envia.
type FakeClient struct {
	mu        sync.Mutex
	Replies   []Reply
	Reactions []string
	Names     map[types.JID]string
	Groups    map[types.JID]*bot.GroupInfo
	Deleted   []string
	Updates   []string // "action:user"
	ReplyErr  error
	Self      types.JID
}

func (f *FakeClient) Reply(_ context.Context, to *bot.Message, out bot.Outgoing) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ReplyErr != nil {
		return f.ReplyErr
	}
	f.Replies = append(f.Replies, Reply{To: to, Chat: to.Chat, Out: out})
	return nil
}

func (f *FakeClient) Send(_ context.Context, chat types.JID, out bot.Outgoing) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ReplyErr != nil {
		return f.ReplyErr
	}
	f.Replies = append(f.Replies, Reply{Chat: chat, Out: out})
	return nil
}

func (f *FakeClient) GroupInfo(_ context.Context, chat types.JID) (*bot.GroupInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if g, ok := f.Groups[chat]; ok {
		return g, nil
	}
	return &bot.GroupInfo{JID: chat, Name: f.Names[chat]}, nil
}

func (f *FakeClient) Delete(_ context.Context, msg *bot.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Deleted = append(f.Deleted, msg.ID)
	return nil
}

func (f *FakeClient) UpdateParticipants(_ context.Context, _ types.JID, users []types.JID, action bot.ParticipantAction) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range users {
		f.Updates = append(f.Updates, string(action)+":"+u.User)
	}
	return nil
}

func (f *FakeClient) Me() types.JID {
	if f.Self.IsEmpty() {
		return Bot
	}
	return f.Self
}

// Texts devolve os textos enviados, em ordem.
func (f *FakeClient) Texts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.Replies {
		if r.Out.Kind == bot.OutText {
			out = append(out, r.Out.Text)
		}
	}
	return out
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
	Admin = types.NewJID("5511888888888", types.DefaultUserServer)
	Bot   = types.NewJID("5562900000000", types.DefaultUserServer)
)

// GroupMsg cria uma mensagem de User no Group.
func GroupMsg(text string) *bot.Message {
	return &bot.Message{ID: "m1", Chat: Group, Sender: User, PushName: "Fulano", IsGroup: true, Text: text}
}

// PrivateMsg cria uma mensagem de User no privado.
func PrivateMsg(text string) *bot.Message {
	return &bot.Message{ID: "p1", Chat: User, Sender: User, PushName: "Fulano", Text: text}
}
