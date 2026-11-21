package mediafx

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/joaodobread/ravena-go/internal/bot"
)

// MsgVolumeUsage é a ajuda do !volume.
const MsgVolumeUsage = "Por favor, especifique o nível de volume (0-1000). Exemplo: !volume 200"

// Argumentos do ffmpeg para áudio comum (mp3) e nota de voz (ogg/opus).
var (
	mp3Args = []string{"-vn", "-t", maxAudioSeconds, "-c:a", "libmp3lame", "-ar", "44100", "-b:a", "128k", "-f", "mp3"}
	oggArgs = voiceArgs(maxAudioSeconds)
)

// voiceArgs converte para nota de voz cortando em seconds.
func voiceArgs(seconds string) []string {
	return []string{"-vn", "-t", seconds, "-c:a", "libopus", "-b:a", "48k", "-ac", "1", "-ar", "48000", "-f", "ogg"}
}

func (f *Feature) audioCommands() []*bot.Command {
	return []*bot.Command{
		{
			Name:        "extractaudio",
			Description: "Extrai o áudio da mídia, em mp3",
			Usage:       "!extractaudio (respondendo a um vídeo/áudio)",
			Category:    "midia",
			Reactions:   bot.Reactions{Before: "⌛️", After: "🎵", Error: "❌"},
			Run: func(ctx context.Context, c *bot.Context) error {
				return f.convertAudio(ctx, c, false)
			},
		},
		{
			Name:        "extractvoice",
			Description: "Extrai o áudio da mídia como mensagem de voz",
			Usage:       "!extractvoice (respondendo a um vídeo/áudio)",
			Category:    "midia",
			Reactions:   bot.Reactions{Before: "⌛️", After: "🎤", Error: "❌"},
			Run: func(ctx context.Context, c *bot.Context) error {
				return f.convertAudio(ctx, c, true)
			},
		},
		{
			Name:        "volume",
			Description: "Ajusta o volume da mídia (0-1000)",
			Usage:       "!volume 200 (respondendo a um vídeo/áudio)",
			Category:    "midia",
			Reactions:   bot.Reactions{Before: "⌛️", After: "🔊", Error: "❌"},
			Run:         f.volume,
		},
	}
}

// avMedia devolve a mídia de áudio/vídeo da mensagem (ou citada).
func avMedia(c *bot.Context) (*bot.Media, bool, error) {
	src := c.Message.AnyMedia()
	if src == nil {
		return nil, false, bot.Userf(MsgNoAV)
	}
	switch {
	case src.Kind == bot.MediaAudio:
		return src, false, nil
	case src.Kind == bot.MediaVideo:
		return src, true, nil
	case src.Kind == bot.MediaDocument && strings.HasPrefix(src.Mime, "audio/"):
		return src, false, nil
	case src.Kind == bot.MediaDocument && strings.HasPrefix(src.Mime, "video/"):
		return src, true, nil
	}
	return nil, false, bot.Userf(MsgNoAV)
}

func (f *Feature) convertAudio(ctx context.Context, c *bot.Context, voice bool) error {
	src, _, err := avMedia(c)
	if err != nil {
		return err
	}
	data, err := download(ctx, src)
	if err != nil {
		return err
	}
	out, err := f.toAudio(ctx, data, voice)
	if err != nil {
		return err
	}
	return c.Reply(ctx, out)
}

func (f *Feature) toAudio(ctx context.Context, data []byte, voice bool, extra ...string) (bot.Outgoing, error) {
	args, ext, mime := mp3Args, ".mp3", "audio/mpeg"
	if voice {
		args, ext, mime = oggArgs, ".ogg", "audio/ogg; codecs=opus"
	}
	args = append(append([]string{}, extra...), args...)
	out, err := f.conv.FFmpeg(ctx, data, ext, args...)
	if err != nil && strings.Contains(err.Error(), "does not contain any stream") {
		return bot.Outgoing{}, bot.Userf("Essa mídia não tem áudio.")
	}
	if err != nil {
		return bot.Outgoing{}, fmt.Errorf("convertendo áudio: %w", err)
	}
	if len(out) == 0 {
		return bot.Outgoing{}, bot.Userf("Essa mídia não tem áudio.")
	}
	return bot.Outgoing{Kind: bot.OutAudio, Data: out, Mime: mime, PTT: voice}, nil
}

// isVoice diz se o áudio parece uma nota de voz (ogg/opus).
func isVoice(mime string) bool {
	m := strings.ToLower(mime)
	return strings.Contains(m, "opus") || strings.Contains(m, "ogg")
}

func (f *Feature) volume(ctx context.Context, c *bot.Context) error {
	if len(c.Args) == 0 {
		return bot.Userf(MsgVolumeUsage)
	}
	level, err := strconv.Atoi(strings.TrimSuffix(c.Args[0], "%"))
	if err != nil || level < 0 || level > 1000 {
		return bot.Userf("Nível de volume inválido. Use um valor entre 0 e 1000.")
	}
	src, video, err := avMedia(c)
	if err != nil {
		return err
	}
	data, err := download(ctx, src)
	if err != nil {
		return err
	}
	vol := "volume=" + strconv.FormatFloat(float64(level)/100, 'f', 2, 64)

	if !video {
		out, err := f.toAudio(ctx, data, isVoice(src.Mime), "-af", vol)
		if err != nil {
			return err
		}
		return c.Reply(ctx, out)
	}
	out, err := f.conv.FFmpeg(ctx, data, ".mp4",
		"-t", maxAudioSeconds, "-af", vol,
		"-c:v", "copy", "-c:a", "aac", "-b:a", "128k", "-movflags", "faststart", "-f", "mp4")
	if err != nil {
		return fmt.Errorf("ajustando volume: %w", err)
	}
	return c.Reply(ctx, bot.Outgoing{Kind: bot.OutVideo, Data: out, Mime: "video/mp4",
		Caption: fmt.Sprintf("🔊 Volume ajustado para %d%%", level)})
}
