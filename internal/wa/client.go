package wa

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/media"
)

const groupNameTTL = 10 * time.Minute

// Client implementa bot.Client sobre o whatsmeow.
type Client struct {
	wa *whatsmeow.Client

	mu    sync.Mutex
	names map[types.JID]cachedName
}

type cachedName struct {
	name    string
	expires time.Time
}

var _ bot.Client = (*Client)(nil)

// NewClient cria o adaptador.
func NewClient(wa *whatsmeow.Client) *Client {
	return &Client{wa: wa, names: map[types.JID]cachedName{}}
}

// Reply envia out citando to.
func (c *Client) Reply(ctx context.Context, to *bot.Message, out bot.Outgoing) error {
	msg, err := c.build(ctx, out, quote(to))
	if err != nil {
		return err
	}
	_, err = c.wa.SendMessage(ctx, to.Chat, msg)
	return err
}

// React reage a msg.
func (c *Client) React(ctx context.Context, msg *bot.Message, emoji string) error {
	_, err := c.wa.SendMessage(ctx, msg.Chat, c.wa.BuildReaction(msg.Chat, msg.Sender, msg.ID, emoji))
	return err
}

// ChatName devolve o nome do grupo, com cache para não consultar a cada figurinha.
func (c *Client) ChatName(ctx context.Context, chat types.JID) (string, error) {
	if chat.Server != types.GroupServer {
		return "", nil
	}
	now := time.Now()
	c.mu.Lock()
	if e, ok := c.names[chat]; ok && now.Before(e.expires) {
		c.mu.Unlock()
		return e.name, nil
	}
	c.mu.Unlock()

	info, err := c.wa.GetGroupInfo(ctx, chat)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	c.names[chat] = cachedName{name: info.Name, expires: now.Add(groupNameTTL)}
	c.mu.Unlock()
	return info.Name, nil
}

func quote(to *bot.Message) *waE2E.ContextInfo {
	ci := &waE2E.ContextInfo{
		StanzaID:    new(to.ID),
		Participant: new(to.Sender.ToNonAD().String()),
	}
	if r, ok := to.Ref.(ref); ok {
		ci.QuotedMessage = r.raw
	}
	return ci
}

func (c *Client) build(ctx context.Context, out bot.Outgoing, ci *waE2E.ContextInfo) (*waE2E.Message, error) {
	if out.Kind == bot.OutText {
		return &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: new(out.Text), ContextInfo: ci,
		}}, nil
	}

	mediaType := map[bot.OutKind]whatsmeow.MediaType{
		bot.OutSticker:  whatsmeow.MediaImage, // figurinhas sobem como imagem
		bot.OutImage:    whatsmeow.MediaImage,
		bot.OutVideo:    whatsmeow.MediaVideo,
		bot.OutDocument: whatsmeow.MediaDocument,
	}[out.Kind]
	if mediaType == "" {
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
			Width:      proto.Uint32(media.StickerSize), Height: proto.Uint32(media.StickerSize),
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
