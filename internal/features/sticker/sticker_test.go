package sticker_test

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/bot/bottest"
	"github.com/joaodobread/ravena-go/internal/features/sticker"
	"github.com/joaodobread/ravena-go/internal/media"
)

// fakeWebP é um WebP VP8L mínimo, suficiente para SetStickerMetadata/InspectWebP.
func fakeWebP(alpha bool) []byte {
	bits := uint32(511) | uint32(511)<<14
	if alpha {
		bits |= 1 << 28
	}
	data := binary.LittleEndian.AppendUint32([]byte{0x2f}, bits)
	out := []byte("RIFF")
	out = binary.LittleEndian.AppendUint32(out, uint32(4+8+len(data)+1))
	out = append(out, "WEBPVP8L"...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(data)))
	return append(append(out, data...), 0)
}

func fakeAnimatedWebP() []byte {
	vp8x := []byte{0x02 | 0x10, 0, 0, 0, 0xff, 0x01, 0, 0xff, 0x01, 0}
	out := []byte("RIFF")
	out = binary.LittleEndian.AppendUint32(out, uint32(4+8+len(vp8x)))
	out = append(out, "WEBPVP8X"...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(vp8x)))
	return append(out, vp8x...)
}

type fakeConv struct {
	calls     []string
	crop      media.Crop
	failAll   bool
	dur       time.Duration
	soundFrom string
	noSound   bool
}

func (f *fakeConv) StaticSticker(_ context.Context, _ []byte, c media.Crop) ([]byte, error) {
	f.calls, f.crop = append(f.calls, "static"), c
	if f.failAll {
		return nil, errors.New("ffmpeg morreu")
	}
	return fakeWebP(true), nil
}

func (f *fakeConv) AnimatedSticker(_ context.Context, _ []byte, c media.Crop) ([]byte, error) {
	f.calls, f.crop = append(f.calls, "animated"), c
	return fakeAnimatedWebP(), nil
}

func (f *fakeConv) AnimatedStickerFor(_ context.Context, _ []byte, c media.Crop, d time.Duration) ([]byte, error) {
	f.calls, f.crop, f.dur = append(f.calls, "animated-for"), c, d
	return fakeAnimatedWebP(), nil
}

func (f *fakeConv) StickerSound(_ context.Context, in []byte, _ time.Duration) (media.Sound, error) {
	f.calls, f.soundFrom = append(f.calls, "sound"), string(in)
	if f.noSound {
		return media.Sound{}, errors.New("sem stream de áudio")
	}
	return media.Sound{Ogg: []byte("ogg"), Seconds: 3, Waveform: []byte{1, 2}}, nil
}

func (f *fakeConv) Frame(context.Context, []byte) ([]byte, error) {
	f.calls = append(f.calls, "frame")
	return []byte("jpeg"), nil
}

func (f *fakeConv) WebPToPNG(context.Context, []byte) ([]byte, error) {
	f.calls = append(f.calls, "png")
	return []byte("png"), nil
}

func (f *fakeConv) AnimatedWebPToMP4(context.Context, []byte) ([]byte, error) {
	f.calls = append(f.calls, "mp4")
	return []byte("mp4"), nil
}

type fakeFocus struct {
	p   float64
	err error
}

func (f fakeFocus) Focus(context.Context, []byte) (float64, error) { return f.p, f.err }

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type env struct {
	router *bot.Router
	client *bottest.FakeClient
	conv   *fakeConv
}

func setup(t *testing.T, focus sticker.FocusFinder) env {
	t.Helper()
	fc := &bottest.FakeClient{Names: map[types.JID]string{}}
	conv := &fakeConv{}
	feat := sticker.New(conv, focus, "ravena", quiet)
	r := bot.NewRouter("!", fc, quiet)
	if err := r.Register(feat.Commands()...); err != nil {
		t.Fatal(err)
	}
	r.Listen(feat.AutoSticker)
	return env{r, fc, conv}
}

func groupMsg(text string, m *bot.Media) *bot.Message {
	return &bot.Message{ID: "m1", Chat: bottest.Group, Sender: bottest.User, IsGroup: true, Text: text, Media: m}
}

func image() *bot.Media { return bottest.StaticMedia(bot.MediaImage, "image/jpeg", []byte("img")) }

func TestStickerFromImageUsesArgsAsPackName(t *testing.T) {
	e := setup(t, nil)
	e.router.Handle(context.Background(), groupMsg("!s Meu Pacote", image()))

	out := e.client.Last().Out
	if out.Kind != bot.OutSticker || out.Animated {
		t.Fatalf("saída inesperada: %+v", out)
	}
	meta, err := media.ReadStickerMetadata(out.Data)
	if err != nil {
		t.Fatal(err)
	}
	if meta.PackName != "Meu Pacote" || meta.Publisher != "ravena" {
		t.Fatalf("metadados = %+v", meta)
	}
	if e.conv.crop.Mode != media.Contain {
		t.Fatalf("!s deveria usar Contain, usou %v", e.conv.crop.Mode)
	}
}

func TestPackNameFallsBackToGroupName(t *testing.T) {
	e := setup(t, nil)
	e.client.Names[bottest.Group] = "Grupo da Família"
	e.router.Handle(context.Background(), groupMsg("!fig", image()))
	meta, _ := media.ReadStickerMetadata(e.client.Last().Out.Data)
	if meta.PackName != "Grupo da Família" {
		t.Fatalf("PackName = %q", meta.PackName)
	}
}

func TestCropVariants(t *testing.T) {
	cases := map[string]media.CropMode{
		"!sq": media.Center, "!stickerqc": media.Top, "!sqb": media.Bottom, "!sqe": media.Stretch,
	}
	for text, want := range cases {
		e := setup(t, nil)
		e.router.Handle(context.Background(), groupMsg(text, image()))
		if e.conv.crop.Mode != want {
			t.Errorf("%s: modo %v, want %v", text, e.conv.crop.Mode, want)
		}
	}
}

func TestVideoFromQuotedMessageBecomesAnimated(t *testing.T) {
	e := setup(t, nil)
	m := groupMsg("!s", nil)
	m.Quoted = &bot.Message{ID: "q", Media: bottest.StaticMedia(bot.MediaVideo, "video/mp4", []byte("vid"))}
	e.router.Handle(context.Background(), m)
	if out := e.client.Last().Out; out.Kind != bot.OutSticker || !out.Animated {
		t.Fatalf("saída inesperada: %+v", out)
	}
}

func TestSqiUsesLLMFocusAndFallsBackToCenter(t *testing.T) {
	e := setup(t, fakeFocus{p: 70})
	e.router.Handle(context.Background(), groupMsg("!sqi", image()))
	if e.conv.crop != (media.Crop{Mode: media.Focus, Focus: 70}) {
		t.Fatalf("crop = %+v", e.conv.crop)
	}

	e = setup(t, fakeFocus{err: errors.New("timeout")})
	e.router.Handle(context.Background(), groupMsg("!sqi", image()))
	if e.conv.crop.Mode != media.Center || e.client.Last().Out.Kind != bot.OutSticker {
		t.Fatalf("deveria cair no centro e ainda mandar a figurinha: %+v", e.conv.crop)
	}

	e = setup(t, nil)
	e.router.Handle(context.Background(), groupMsg("!sqi", image()))
	if e.conv.crop.Mode != media.Center {
		t.Fatalf("sem LLM deveria usar centro: %+v", e.conv.crop)
	}
}

func TestExtractFromQuotedSticker(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want bot.OutKind
	}{
		{"animada vira vídeo", fakeAnimatedWebP(), bot.OutVideo},
		{"transparente vira documento", fakeWebP(true), bot.OutDocument},
		{"opaca vira imagem", fakeWebP(false), bot.OutImage},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			e := setup(t, nil)
			m := groupMsg("!s", nil)
			m.Quoted = &bot.Message{ID: "q", Media: bottest.StaticMedia(bot.MediaSticker, "image/webp", tt.data)}
			e.router.Handle(context.Background(), m)
			if got := e.client.Last().Out.Kind; got != tt.want {
				t.Fatalf("Kind = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUserFacingErrors(t *testing.T) {
	e := setup(t, nil)
	e.router.Handle(context.Background(), groupMsg("!s", nil))
	if got := e.client.Last().Out.Text; got != sticker.MsgNoMedia {
		t.Fatalf("sem mídia: %q", got)
	}

	e.router.Handle(context.Background(), groupMsg("!s", bottest.StaticMedia(bot.MediaAudio, "audio/ogg", nil)))
	if got := e.client.Last().Out.Text; got != sticker.MsgUnsupported {
		t.Fatalf("áudio: %q", got)
	}

	e.conv.failAll = true
	e.router.Handle(context.Background(), groupMsg("!s", image()))
	if got := e.client.Last().Out.Text; got == "" || got == "ffmpeg morreu" {
		t.Fatalf("falha interna deveria virar mensagem genérica: %q", got)
	}
}

func TestAutoStickerOnlyInPrivateChats(t *testing.T) {
	e := setup(t, nil)
	e.router.Handle(context.Background(), groupMsg("", image()))
	if len(e.client.Replies) != 0 {
		t.Fatal("não deveria gerar figurinha automática em grupo")
	}

	dm := &bot.Message{ID: "d", Chat: bottest.User, Sender: bottest.User, PushName: "João", Media: image()}
	e.router.Handle(context.Background(), dm)
	out := e.client.Last().Out
	meta, _ := media.ReadStickerMetadata(out.Data)
	if out.Kind != bot.OutSticker || meta.PackName != "João" {
		t.Fatalf("figurinha automática inesperada: %+v %+v", out, meta)
	}

	before := len(e.client.Replies)
	e.router.Handle(context.Background(), &bot.Message{ID: "t", Chat: bottest.User, Sender: bottest.User, Text: "oi"})
	if len(e.client.Replies) != before {
		t.Fatal("texto no privado não deveria virar figurinha")
	}
}

func TestReactingWithFrameEmojiMakesSticker(t *testing.T) {
	for _, emoji := range []string{"🖼", "🖼️"} {
		e := setup(t, nil)
		target := groupMsg("Pacote Reação", image())
		e.router.HandleReaction(context.Background(), target, bot.Reaction{Emoji: emoji, By: bottest.User})
		out := e.client.Last()
		meta, _ := media.ReadStickerMetadata(out.Out.Data)
		if out.Out.Kind != bot.OutSticker || out.To != target || meta.PackName != "Pacote Reação" {
			t.Fatalf("%s: resposta inesperada %+v %+v", emoji, out, meta)
		}
		if e.client.Reactions[0] != sticker.Loading {
			t.Fatalf("%s: primeira reação deveria ser %q, foi %q", emoji, sticker.Loading, e.client.Reactions[0])
		}
	}
}
