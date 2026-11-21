package wa

import (
	"context"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/joaodobread/ravena-go/internal/bot"
)

type fakeDownloader struct{ got whatsmeow.DownloadableMessage }

func (f *fakeDownloader) Download(_ context.Context, m whatsmeow.DownloadableMessage) ([]byte, error) {
	f.got = m
	return []byte("dados"), nil
}

var (
	group  = types.NewJID("120363000000000000", types.GroupServer)
	sender = types.NewJID("5511999999999", types.DefaultUserServer)
)

func event(m *waE2E.Message) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: group, Sender: sender, IsGroup: true},
			ID:            "ABC", PushName: "João", Timestamp: time.Unix(1700000000, 0),
		},
		Message: m,
	}
}

func TestConvertTextMessage(t *testing.T) {
	m := Convert(&fakeDownloader{}, event(&waE2E.Message{Conversation: new("!s oi")}))
	if m.Text != "!s oi" || m.ID != "ABC" || m.Chat != group || m.Sender != sender || !m.IsGroup || m.PushName != "João" {
		t.Fatalf("mensagem inesperada: %+v", m)
	}
	if m.Media != nil || m.Quoted != nil {
		t.Fatalf("não deveria ter mídia/citação: %+v", m)
	}
}

func TestConvertImageWithCaptionDownloadsLazily(t *testing.T) {
	d := &fakeDownloader{}
	img := &waE2E.ImageMessage{Caption: new("!sq"), Mimetype: new("image/jpeg")}
	m := Convert(d, event(&waE2E.Message{ImageMessage: img}))
	if m.Text != "!sq" || m.Media == nil || m.Media.Kind != bot.MediaImage || m.Media.Mime != "image/jpeg" {
		t.Fatalf("mensagem inesperada: %+v", m)
	}
	if d.got != nil {
		t.Fatal("não deveria baixar antes de pedirem")
	}
	if b, _ := m.Media.Download(context.Background()); string(b) != "dados" || d.got != img {
		t.Fatal("Download deveria usar a ImageMessage original")
	}
}

func TestConvertQuotedAnimatedSticker(t *testing.T) {
	quoted := &waE2E.Message{StickerMessage: &waE2E.StickerMessage{IsAnimated: new(true), Mimetype: new("image/webp")}}
	m := Convert(&fakeDownloader{}, event(&waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
		Text: new("!s"),
		ContextInfo: &waE2E.ContextInfo{
			StanzaID: new("Q1"), Participant: new(sender.String()), QuotedMessage: quoted,
		},
	}}))
	q := m.Quoted
	if q == nil || q.ID != "Q1" || q.Sender != sender || q.Media == nil || q.Media.Kind != bot.MediaSticker || !q.Media.Animated {
		t.Fatalf("citação inesperada: %+v", q)
	}
	if m.AnyMedia() != q.Media {
		t.Fatal("AnyMedia deveria cair na mídia citada")
	}
}

func TestConvertMediaKinds(t *testing.T) {
	cases := []struct {
		name string
		msg  *waE2E.Message
		want bot.MediaKind
	}{
		{"gif", &waE2E.Message{VideoMessage: &waE2E.VideoMessage{GifPlayback: new(true)}}, bot.MediaGIF},
		{"vídeo", &waE2E.Message{VideoMessage: &waE2E.VideoMessage{}}, bot.MediaVideo},
		{"áudio", &waE2E.Message{AudioMessage: &waE2E.AudioMessage{}}, bot.MediaAudio},
		{"voz", &waE2E.Message{AudioMessage: &waE2E.AudioMessage{PTT: new(true), Seconds: new(uint32(7)), FileLength: new(uint64(900))}}, bot.MediaAudio},
		{"documento de imagem", &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{Mimetype: new("image/png")}}, bot.MediaImage},
		{"documento pdf", &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{Mimetype: new("application/pdf")}}, bot.MediaDocument},
		{"documento com legenda", &waE2E.Message{DocumentWithCaptionMessage: &waE2E.FutureProofMessage{
			Message: &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{Mimetype: new("video/mp4")}},
		}}, bot.MediaVideo},
		{"view once", &waE2E.Message{ViewOnceMessageV2: &waE2E.FutureProofMessage{
			Message: &waE2E.Message{ImageMessage: &waE2E.ImageMessage{}},
		}}, bot.MediaImage},
	}
	for _, tt := range cases {
		m := Convert(&fakeDownloader{}, event(tt.msg))
		if m.Media == nil || m.Media.Kind != tt.want {
			t.Errorf("%s: mídia %+v, want kind %d", tt.name, m.Media, tt.want)
		}
	}
}

func TestQuoteUsesRawMessage(t *testing.T) {
	raw := &waE2E.Message{Conversation: new("oi")}
	ci := quote(&bot.Message{ID: "X", Sender: sender, Ref: ref{raw: raw}}, nil)
	if ci.GetStanzaID() != "X" || ci.GetParticipant() != sender.String() || ci.GetQuotedMessage() != raw {
		t.Fatalf("ContextInfo inesperado: %+v", ci)
	}
}

func TestConvertMentions(t *testing.T) {
	m := Convert(&fakeDownloader{}, event(&waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
		Text:        new("!namorar @5511999999999"),
		ContextInfo: &waE2E.ContextInfo{MentionedJID: []string{sender.String()}},
	}}))
	if len(m.Mentions) != 1 || m.Mentions[0] != sender {
		t.Fatalf("Mentions = %v", m.Mentions)
	}
}

func TestMarkSelf(t *testing.T) {
	botLID := types.NewJID("777", types.HiddenUserServer)
	m := &bot.Message{Mentions: []types.JID{sender, botLID}}
	MarkSelf(m, types.NewJID("5562900000000", types.DefaultUserServer), botLID)
	if !m.MentionsBot {
		t.Fatal("deveria detectar menção ao bot pelo LID")
	}
	m = &bot.Message{Mentions: []types.JID{sender}}
	MarkSelf(m, botLID)
	if m.MentionsBot {
		t.Fatal("não mencionou o bot")
	}
}

func TestConvertMediaMetadata(t *testing.T) {
	m := Convert(&fakeDownloader{}, event(&waE2E.Message{AudioMessage: &waE2E.AudioMessage{PTT: new(true), Seconds: new(uint32(7)), FileLength: new(uint64(900))}}))
	if !m.Media.PTT || m.Media.Seconds != 7 || m.Media.Size != 900 {
		t.Fatalf("metadados: %+v", m.Media)
	}
}

func TestConvertQuotedMentions(t *testing.T) {
	quoted := &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
		Text: new("oi @5511999999999"), ContextInfo: &waE2E.ContextInfo{MentionedJID: []string{sender.String()}},
	}}
	m := Convert(&fakeDownloader{}, event(&waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
		Text: new("!apagar"), ContextInfo: &waE2E.ContextInfo{StanzaID: new("Q"), QuotedMessage: quoted},
	}}))
	if m.Quoted == nil || len(m.Quoted.Mentions) != 1 || m.Quoted.Mentions[0] != sender {
		t.Fatalf("menções da citada: %+v", m.Quoted)
	}
}
