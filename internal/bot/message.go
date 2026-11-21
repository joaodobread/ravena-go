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
	ID     string
	Chat   types.JID
	Sender types.JID
	// SenderAlt é a outra identidade do autor (número se Sender for LID e vice-versa).
	SenderAlt types.JID
	PushName  string
	IsGroup   bool
	FromMe    bool
	Timestamp time.Time
	Text      string
	// Mentions são os usuários marcados (@) na mensagem.
	Mentions []types.JID
	// MentionsBot diz se o próprio bot foi marcado.
	MentionsBot bool
	Media       *Media
	// Quoted é a mensagem respondida (só ID, Sender, Text e Media).
	Quoted *Message
	// Ref guarda dados do adaptador (ex.: a mensagem crua para citar na resposta).
	Ref any
}

// SenderIs diz se o autor é jid (comparando número e LID).
func (m *Message) SenderIs(jid types.JID) bool {
	return jid.User != "" && (m.Sender.User == jid.User || m.SenderAlt.User == jid.User)
}

// MentionText devolve o texto "@numero" que, junto de Outgoing.Mentions, marca jid.
func MentionText(jid types.JID) string { return "@" + jid.User }

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
	OutAudio
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
	PTT      bool   // OutAudio: mensagem de voz
	// Mentions são os usuários marcados (o texto deve conter @numero).
	Mentions []types.JID
}

// Text cria uma resposta de texto.
func Text(s string) Outgoing { return Outgoing{Kind: OutText, Text: s} }

// Participant é um membro de grupo. LID e Phone são identidades alternativas
// do mesmo usuário (o WhatsApp pode mandar qualquer uma delas como Sender).
type Participant struct {
	JID, LID, Phone types.JID
	IsAdmin         bool // admin ou dono
}

// Is diz se jid é este participante, em qualquer uma das identidades.
func (p Participant) Is(jid types.JID) bool {
	u := jid.User
	return u != "" && (p.JID.User == u || p.LID.User == u || p.Phone.User == u)
}

// GroupInfo são os dados de um grupo.
type GroupInfo struct {
	JID          types.JID
	Name         string
	Topic        string
	Announce     bool // só admins mandam mensagem
	Participants []Participant
}

// Member devolve o participante jid.
func (g *GroupInfo) Member(jid types.JID) (Participant, bool) {
	for _, p := range g.Participants {
		if p.Is(jid) {
			return p, true
		}
	}
	return Participant{}, false
}

// IsAdmin diz se jid é admin do grupo.
func (g *GroupInfo) IsAdmin(jid types.JID) bool {
	p, ok := g.Member(jid)
	return ok && p.IsAdmin
}

// ParticipantAction é uma ação de admin sobre membros.
type ParticipantAction string

const (
	ActionAdd     ParticipantAction = "add"
	ActionRemove  ParticipantAction = "remove"
	ActionPromote ParticipantAction = "promote"
	ActionDemote  ParticipantAction = "demote"
)

// Client é o que o bot precisa do WhatsApp.
type Client interface {
	// Reply envia out no chat de to, citando to.
	Reply(ctx context.Context, to *Message, out Outgoing) error
	// Send envia out em chat, sem citar nada.
	Send(ctx context.Context, chat types.JID, out Outgoing) error
	// React reage a msg com emoji ("" remove a reação).
	React(ctx context.Context, msg *Message, emoji string) error
	// ChatName devolve o nome do grupo (ou "" para conversas privadas).
	ChatName(ctx context.Context, chat types.JID) (string, error)
	// GroupInfo devolve os dados do grupo (com cache curto).
	GroupInfo(ctx context.Context, chat types.JID) (*GroupInfo, error)
	// Delete apaga msg para todos (o bot precisa ser admin se não for dele).
	Delete(ctx context.Context, msg *Message) error
	// UpdateParticipants adiciona/remove/promove/rebaixa membros.
	UpdateParticipants(ctx context.Context, chat types.JID, users []types.JID, action ParticipantAction) error
	// Me é o JID do próprio bot.
	Me() types.JID
}
