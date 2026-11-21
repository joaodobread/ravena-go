package wa

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/media"
)

// groupTTL é curto porque a lista de admins decide permissões.
const groupTTL = 2 * time.Minute

// Client implementa bot.Client sobre o whatsmeow.
type Client struct {
	wa *whatsmeow.Client

	mu        sync.Mutex
	groups    map[types.JID]cachedGroup
	joined    []*bot.GroupInfo
	joinedExp time.Time

	recent *Recent // opcional: guarda o que o bot envia, para reações a isso
}

// RecordSent faz as mensagens enviadas pelo bot entrarem em recent.
func (c *Client) RecordSent(recent *Recent) { c.recent = recent }

func (c *Client) send(ctx context.Context, chat types.JID, msg *waE2E.Message) error {
	resp, err := c.wa.SendMessage(ctx, chat, msg)
	if err != nil || c.recent == nil {
		return err
	}
	me := c.Me()
	c.recent.Add(&events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: me, IsFromMe: true, IsGroup: chat.Server == types.GroupServer},
			ID:            resp.ID, Timestamp: resp.Timestamp,
		},
		Message: msg,
	})
	return nil
}

type cachedGroup struct {
	info    *bot.GroupInfo
	expires time.Time
}

var _ bot.Client = (*Client)(nil)

// NewClient cria o adaptador.
func NewClient(wa *whatsmeow.Client) *Client {
	return &Client{wa: wa, groups: map[types.JID]cachedGroup{}}
}

// Reply envia out citando to.
func (c *Client) Reply(ctx context.Context, to *bot.Message, out bot.Outgoing) error {
	msg, err := c.build(ctx, out, quote(to, out.Mentions))
	if err != nil {
		return err
	}
	return c.send(ctx, to.Chat, msg)
}

// Send envia out em chat, sem citar.
func (c *Client) Send(ctx context.Context, chat types.JID, out bot.Outgoing) error {
	var ci *waE2E.ContextInfo
	if len(out.Mentions) > 0 {
		ci = &waE2E.ContextInfo{MentionedJID: jidStrings(out.Mentions)}
	}
	msg, err := c.build(ctx, out, ci)
	if err != nil {
		return err
	}
	return c.send(ctx, chat, msg)
}

// React reage a msg.
func (c *Client) React(ctx context.Context, msg *bot.Message, emoji string) error {
	_, err := c.wa.SendMessage(ctx, msg.Chat, c.wa.BuildReaction(msg.Chat, msg.Sender, msg.ID, emoji))
	return err
}

// Delete apaga msg para todos.
func (c *Client) Delete(ctx context.Context, msg *bot.Message) error {
	_, err := c.wa.SendMessage(ctx, msg.Chat, c.wa.BuildRevoke(msg.Chat, msg.Sender, msg.ID))
	return err
}

// UpdateParticipants adiciona/remove/promove/rebaixa membros.
func (c *Client) UpdateParticipants(ctx context.Context, chat types.JID, users []types.JID, action bot.ParticipantAction) error {
	_, err := c.wa.UpdateGroupParticipants(ctx, chat, users, whatsmeow.ParticipantChange(action))
	c.Invalidate(chat)
	return err
}

// Me é o JID do bot (vazio antes do pareamento).
func (c *Client) Me() types.JID {
	if c.wa.Store.ID == nil {
		return types.EmptyJID
	}
	return c.wa.Store.ID.ToNonAD()
}

// ChatName devolve o nome do grupo.
func (c *Client) ChatName(ctx context.Context, chat types.JID) (string, error) {
	if chat.Server != types.GroupServer {
		return "", nil
	}
	info, err := c.GroupInfo(ctx, chat)
	if err != nil {
		return "", err
	}
	return info.Name, nil
}

// GroupInfo devolve os dados do grupo, com cache curto.
func (c *Client) GroupInfo(ctx context.Context, chat types.JID) (*bot.GroupInfo, error) {
	if chat.Server != types.GroupServer {
		return nil, fmt.Errorf("wa: %s não é grupo", chat)
	}
	now := time.Now()
	c.mu.Lock()
	if e, ok := c.groups[chat]; ok && now.Before(e.expires) {
		c.mu.Unlock()
		return e.info, nil
	}
	c.mu.Unlock()

	raw, err := c.wa.GetGroupInfo(ctx, chat)
	if err != nil {
		return nil, err
	}
	info := convertGroup(raw)
	c.mu.Lock()
	c.groups[chat] = cachedGroup{info: info, expires: now.Add(groupTTL)}
	// Limpeza preguiçosa: o mapa só cresce com grupos ativos e expira sozinho.
	if len(c.groups) > 512 {
		for k, v := range c.groups {
			if now.After(v.expires) {
				delete(c.groups, k)
			}
		}
	}
	c.mu.Unlock()
	return info, nil
}

// Invalidate descarta o cache de um grupo (ex.: ao receber events.GroupInfo).
func (c *Client) Invalidate(chat types.JID) {
	c.mu.Lock()
	delete(c.groups, chat)
	c.mu.Unlock()
}

func convertGroup(g *types.GroupInfo) *bot.GroupInfo {
	info := &bot.GroupInfo{
		JID:          g.JID,
		Name:         g.Name,
		Topic:        g.Topic,
		Announce:     g.IsAnnounce,
		Participants: make([]bot.Participant, 0, len(g.Participants)),
	}
	for _, p := range g.Participants {
		info.Participants = append(info.Participants, bot.Participant{
			JID: p.JID, LID: p.LID, Phone: p.PhoneNumber, IsAdmin: p.IsAdmin || p.IsSuperAdmin,
		})
	}
	return info
}

func jidStrings(jids []types.JID) []string {
	out := make([]string, len(jids))
	for i, j := range jids {
		out[i] = j.ToNonAD().String()
	}
	return out
}

func quote(to *bot.Message, mentions []types.JID) *waE2E.ContextInfo {
	ci := &waE2E.ContextInfo{
		StanzaID:    new(to.ID),
		Participant: new(to.Sender.ToNonAD().String()),
	}
	if r, ok := to.Ref.(ref); ok {
		ci.QuotedMessage = r.raw
	}
	if len(mentions) > 0 {
		ci.MentionedJID = jidStrings(mentions)
	}
	return ci
}

var uploadType = map[bot.OutKind]whatsmeow.MediaType{
	bot.OutSticker:  whatsmeow.MediaImage, // figurinhas sobem como imagem
	bot.OutImage:    whatsmeow.MediaImage,
	bot.OutVideo:    whatsmeow.MediaVideo,
	bot.OutDocument: whatsmeow.MediaDocument,
	bot.OutAudio:    whatsmeow.MediaAudio,
}

func (c *Client) build(ctx context.Context, out bot.Outgoing, ci *waE2E.ContextInfo) (*waE2E.Message, error) {
	if out.Kind == bot.OutText {
		return &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: new(out.Text), ContextInfo: ci,
		}}, nil
	}

	mediaType, ok := uploadType[out.Kind]
	if !ok {
		return nil, fmt.Errorf("wa: tipo de saída desconhecido %d", out.Kind)
	}
	up, err := c.wa.Upload(ctx, out.Data, mediaType)
	if err != nil {
		return nil, fmt.Errorf("wa: upload: %w", err)
	}

	switch out.Kind {
	case bot.OutSticker:
		return &waE2E.Message{StickerMessage: &waE2E.StickerMessage{
			URL: new(up.URL), DirectPath: new(up.DirectPath),
			MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256,
			FileLength: new(up.FileLength), Mimetype: new("image/webp"),
			IsAnimated: new(out.Animated),
			Width:      new(uint32(media.StickerSize)), Height: new(uint32(media.StickerSize)),
			ContextInfo: ci,
		}}, nil
	case bot.OutImage:
		return &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
			URL: new(up.URL), DirectPath: new(up.DirectPath),
			MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256,
			FileLength: new(up.FileLength), Mimetype: new(out.Mime),
			Caption: new(out.Caption), ContextInfo: ci,
		}}, nil
	case bot.OutVideo:
		return &waE2E.Message{VideoMessage: &waE2E.VideoMessage{
			URL: new(up.URL), DirectPath: new(up.DirectPath),
			MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256,
			FileLength: new(up.FileLength), Mimetype: new(out.Mime),
			Caption: new(out.Caption), ContextInfo: ci,
		}}, nil
	case bot.OutAudio:
		mime := out.Mime
		if out.PTT {
			mime = "audio/ogg; codecs=opus" // nota de voz exige opus
		}
		return &waE2E.Message{AudioMessage: &waE2E.AudioMessage{
			URL: new(up.URL), DirectPath: new(up.DirectPath),
			MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256,
			FileLength: new(up.FileLength), Mimetype: new(mime),
			PTT: new(out.PTT), ContextInfo: ci,
		}}, nil
	default: // bot.OutDocument
		return &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{
			URL: new(up.URL), DirectPath: new(up.DirectPath),
			MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256,
			FileLength: new(up.FileLength), Mimetype: new(out.Mime),
			FileName: new(out.FileName), Title: new(out.FileName),
			Caption: new(out.Caption), ContextInfo: ci,
		}}, nil
	}
}

// JoinedGroups lista os grupos em que o bot está (usado pelo painel; cache de 1 min).
func (c *Client) JoinedGroups(ctx context.Context) ([]*bot.GroupInfo, error) {
	c.mu.Lock()
	if c.joined != nil && time.Now().Before(c.joinedExp) {
		out := c.joined
		c.mu.Unlock()
		return out, nil
	}
	c.mu.Unlock()

	raw, err := c.wa.GetJoinedGroups(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*bot.GroupInfo, len(raw))
	for i, g := range raw {
		out[i] = convertGroup(g)
	}
	c.mu.Lock()
	c.joined, c.joinedExp = out, time.Now().Add(time.Minute)
	c.mu.Unlock()
	return out, nil
}
