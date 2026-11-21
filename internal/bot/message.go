// Package bot contém o núcleo do bot: tipos de mensagem independentes do
// WhatsApp, a interface de envio e o roteador de comandos.
package bot

import (
	"context"
	"time"

	"go.mau.fi/whatsmeow/types"
)

// MediaKind é o tipo de mídia de uma mensagem.
type MediaKind uint8

const (
	MediaImage MediaKind = iota + 1
	MediaVideo
	MediaGIF
	MediaSticker
	MediaAudio
	MediaDocument
)

// Media é uma mídia recebida. O conteúdo só é baixado quando alguém pede,
// para não gastar memória com mídias que nenhum comando vai usar.
type Media struct {
	Kind     MediaKind
	Mime     string
	Animated bool
	Download func(ctx context.Context) ([]byte, error)
}

// Message é uma mensagem recebida, já normalizada.
type Message struct {
	ID        string
	Chat      types.JID
	Sender    types.JID
	PushName  string
	IsGroup   bool
	FromMe    bool
	Timestamp time.Time
	Text      string
	Media     *Media
	// Quoted é a mensagem respondida (só ID, Sender, Text e Media).
	Quoted *Message
	// Ref guarda dados do adaptador (ex.: a mensagem crua para citar na resposta).
	Ref any
}

// AnyMedia devolve a mídia da mensagem ou, se não tiver, a da mensagem citada.
func (m *Message) AnyMedia() *Media {
	if m.Media != nil {
		return m.Media
	}
	if m.Quoted != nil {
		return m.Quoted.Media
	}
	return nil
}

// OutKind é o tipo de mensagem enviada.
type OutKind uint8

const (
	OutText OutKind = iota
	OutSticker
	OutImage
	OutVideo
	OutDocument
)

// Outgoing é uma mensagem a ser enviada.
type Outgoing struct {
	Kind     OutKind
	Text     string // OutText
	Data     []byte // mídias
	Mime     string
	Caption  string
	FileName string // OutDocument
	Animated bool   // OutSticker
}

// Text cria uma resposta de texto.
func Text(s string) Outgoing { return Outgoing{Kind: OutText, Text: s} }

// Client é o que o bot precisa do WhatsApp.
type Client interface {
	// Reply envia out no chat de to, citando to.
	Reply(ctx context.Context, to *Message, out Outgoing) error
	// React reage a msg com emoji ("" remove a reação).
	React(ctx context.Context, msg *Message, emoji string) error
	// ChatName devolve o nome do grupo (ou "" para conversas privadas).
	ChatName(ctx context.Context, chat types.JID) (string, error)
}
