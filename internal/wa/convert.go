// Package wa adapta o whatsmeow para os tipos do pacote bot.
package wa

import (
	"context"
	"strings"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/joaodobread/ravena-go/internal/bot"
)

// Downloader baixa mídias do WhatsApp (implementado por *whatsmeow.Client).
type Downloader interface {
	Download(ctx context.Context, msg whatsmeow.DownloadableMessage) ([]byte, error)
}

// ref é o que o adaptador guarda em bot.Message.Ref para citar a mensagem.
type ref struct{ raw *waE2E.Message }

// Convert transforma um evento do whatsmeow em bot.Message. Nada é baixado aqui.
func Convert(d Downloader, evt *events.Message) *bot.Message {
	m := unwrap(evt.Message)
	msg := &bot.Message{
		ID:        evt.Info.ID,
		Chat:      evt.Info.Chat,
		Sender:    evt.Info.Sender,
		PushName:  evt.Info.PushName,
		IsGroup:   evt.Info.IsGroup,
		FromMe:    evt.Info.IsFromMe,
		Timestamp: evt.Info.Timestamp,
		Text:      textOf(m),
		Media:     mediaOf(d, m),
		Ref:       ref{raw: evt.Message},
	}
	if ci := contextInfo(m); ci != nil && ci.GetQuotedMessage() != nil {
		q := ci.GetQuotedMessage()
		sender, _ := types.ParseJID(ci.GetParticipant())
		qm := unwrap(q)
		msg.Quoted = &bot.Message{
			ID:     ci.GetStanzaID(),
			Chat:   msg.Chat,
			Sender: sender,
			Text:   textOf(qm),
			Media:  mediaOf(d, qm),
			Ref:    ref{raw: q},
		}
	}
	return msg
}

// unwrap tira as camadas que o whatsmeow não remove (view once citado,
// documento com legenda...).
func unwrap(m *waE2E.Message) *waE2E.Message {
	for range 4 { // camadas podem vir aninhadas; 4 é mais que suficiente
		switch {
		case m.GetEphemeralMessage() != nil:
			m = m.GetEphemeralMessage().GetMessage()
		case m.GetViewOnceMessage() != nil:
			m = m.GetViewOnceMessage().GetMessage()
		case m.GetViewOnceMessageV2() != nil:
			m = m.GetViewOnceMessageV2().GetMessage()
		case m.GetViewOnceMessageV2Extension() != nil:
			m = m.GetViewOnceMessageV2Extension().GetMessage()
		case m.GetDocumentWithCaptionMessage() != nil:
			m = m.GetDocumentWithCaptionMessage().GetMessage()
		default:
			return m
		}
	}
	return m
}

func textOf(m *waE2E.Message) string {
	switch {
	case m.GetConversation() != "":
		return m.GetConversation()
	case m.GetExtendedTextMessage() != nil:
		return m.GetExtendedTextMessage().GetText()
	case m.GetImageMessage() != nil:
		return m.GetImageMessage().GetCaption()
	case m.GetVideoMessage() != nil:
		return m.GetVideoMessage().GetCaption()
	case m.GetDocumentMessage() != nil:
		return m.GetDocumentMessage().GetCaption()
	}
	return ""
}

func contextInfo(m *waE2E.Message) *waE2E.ContextInfo {
	switch {
	case m.GetExtendedTextMessage() != nil:
		return m.GetExtendedTextMessage().GetContextInfo()
	case m.GetImageMessage() != nil:
		return m.GetImageMessage().GetContextInfo()
	case m.GetVideoMessage() != nil:
		return m.GetVideoMessage().GetContextInfo()
	case m.GetStickerMessage() != nil:
		return m.GetStickerMessage().GetContextInfo()
	case m.GetDocumentMessage() != nil:
		return m.GetDocumentMessage().GetContextInfo()
	case m.GetAudioMessage() != nil:
		return m.GetAudioMessage().GetContextInfo()
	}
	return nil
}

func mediaOf(d Downloader, m *waE2E.Message) *bot.Media {
	var (
		kind     bot.MediaKind
		mime     string
		animated bool
		dm       whatsmeow.DownloadableMessage
	)
	switch {
	case m.GetImageMessage() != nil:
		im := m.GetImageMessage()
		kind, mime, dm = bot.MediaImage, im.GetMimetype(), im
	case m.GetVideoMessage() != nil:
		vm := m.GetVideoMessage()
		kind, mime, dm = bot.MediaVideo, vm.GetMimetype(), vm
		if vm.GetGifPlayback() {
			kind = bot.MediaGIF
		}
	case m.GetStickerMessage() != nil:
		sm := m.GetStickerMessage()
		kind, mime, animated, dm = bot.MediaSticker, sm.GetMimetype(), sm.GetIsAnimated(), sm
	case m.GetAudioMessage() != nil:
		am := m.GetAudioMessage()
		kind, mime, dm = bot.MediaAudio, am.GetMimetype(), am
	case m.GetDocumentMessage() != nil:
		doc := m.GetDocumentMessage()
		mime, dm = doc.GetMimetype(), doc
		switch {
		case strings.HasPrefix(mime, "image/"):
			kind = bot.MediaImage
		case strings.HasPrefix(mime, "video/"):
			kind = bot.MediaVideo
		default:
			kind = bot.MediaDocument
		}
	default:
		return nil
	}
	return &bot.Media{
		Kind:     kind,
		Mime:     mime,
		Animated: animated,
		Download: func(ctx context.Context) ([]byte, error) { return d.Download(ctx, dm) },
	}
}
