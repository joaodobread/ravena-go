package sticker

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/media"
)

// MaxSound é a duração máxima do som (e da animação) de uma figurinha com som.
const MaxSound = 10 * time.Second

// Mensagens do !ssom.
const (
	MsgSoundUsage = "Para fazer figurinha com som:\n" +
		"• mande ou responda a um vídeo com *!ssom*; ou\n" +
		"• responda a um áudio mandando uma imagem/vídeo/GIF com a legenda *!ssom*."
	MsgNoSound = "Não achei som nessa mídia."
)

func (f *Feature) soundCommand() *bot.Command {
	return &bot.Command{
		Name:        "ssom",
		Aliases:     []string{"saudio", "stickersom", "figsom"},
		Description: "Figurinha com som (vídeo, ou imagem/vídeo respondendo a um áudio)",
		Usage:       "!ssom [pacote] (em um vídeo, ou numa imagem respondendo a um áudio)",
		Category:    "midia",
		Reactions:   bot.Reactions{Before: Loading, After: "🔊", Error: "❌"},
		Run:         f.sound,
	}
}

func isAudio(m *bot.Media) bool {
	return m != nil && (m.Kind == bot.MediaAudio || m.Kind == bot.MediaDocument && strings.HasPrefix(m.Mime, "audio/"))
}

func isVideo(m *bot.Media) bool {
	return m != nil && m.Kind == bot.MediaVideo
}

func isVisual(m *bot.Media) bool {
	if m == nil {
		return false
	}
	switch m.Kind {
	case bot.MediaImage, bot.MediaVideo, bot.MediaGIF, bot.MediaSticker:
		return true
	}
	return false
}

// soundSources escolhe de onde vem a imagem e de onde vem o som.
func soundSources(m *bot.Message) (visual, sound *bot.Media, err error) {
	var quoted *bot.Media
	if m.Quoted != nil {
		quoted = m.Quoted.Media
	}
	switch {
	case isVisual(m.Media) && (isAudio(quoted) || isVideo(quoted)):
		return m.Media, quoted, nil // imagem/vídeo respondendo a um áudio/vídeo
	case isVideo(m.Media):
		return m.Media, m.Media, nil
	case m.Media == nil && isVideo(quoted):
		return quoted, quoted, nil
	}
	return nil, nil, bot.Userf(MsgSoundUsage)
}

func (f *Feature) sound(ctx context.Context, c *bot.Context) error {
	visual, audio, err := soundSources(c.Message)
	if err != nil {
		return err
	}
	vdata, err := visual.Download(ctx)
	if err != nil {
		return fmt.Errorf("baixando mídia: %w", err)
	}
	adata := vdata
	if audio != visual {
		if adata, err = audio.Download(ctx); err != nil {
			return fmt.Errorf("baixando áudio: %w", err)
		}
	}

	snd, err := f.conv.StickerSound(ctx, adata, MaxSound)
	if err != nil {
		f.log.Info("ssom: sem áudio utilizável", "err", err)
		return bot.Userf(MsgNoSound)
	}

	crop := media.Crop{Mode: media.Contain}
	var (
		webp     []byte
		animated bool
	)
	switch visual.Kind {
	case bot.MediaImage:
		webp, err = f.conv.StaticSticker(ctx, vdata, crop)
	case bot.MediaVideo, bot.MediaGIF:
		animated = true
		webp, err = f.conv.AnimatedStickerFor(ctx, vdata, crop, MaxSound)
	case bot.MediaSticker: // reaproveita a figurinha respondida
		webp = vdata
		if info, ierr := media.InspectWebP(vdata); ierr == nil {
			animated = info.Animated
		}
	}
	if err != nil {
		return fmt.Errorf("convertendo: %w", err)
	}
	webp, err = media.SetStickerMetadata(webp, media.StickerMetadata{
		PackID: randomID(), PackName: f.packName(ctx, c), Publisher: f.publisher,
	})
	if err != nil {
		return fmt.Errorf("gravando metadados: %w", err)
	}
	return c.Reply(ctx, bot.Outgoing{
		Kind: bot.OutSticker, Data: webp, Mime: "image/webp", Animated: animated,
		Sound: &bot.Sound{Data: snd.Ogg, Seconds: snd.Seconds, Waveform: snd.Waveform},
	})
}
