package sticker_test

import (
	"context"
	"testing"

	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/bot/bottest"
	"github.com/joaodobread/ravena-go/internal/features/sticker"
)

func video() *bot.Media { return bottest.StaticMedia(bot.MediaVideo, "video/mp4", []byte("vid")) }
func audio() *bot.Media { return bottest.StaticMedia(bot.MediaAudio, "audio/ogg", []byte("aud")) }

func TestSoundStickerFromVideo(t *testing.T) {
	e := setup(t, nil)
	e.router.Handle(context.Background(), groupMsg("!ssom", video()))
	out := e.client.Last().Out
	if out.Kind != bot.OutSticker || !out.Animated || out.Sound == nil {
		t.Fatalf("saída inesperada: %+v", out)
	}
	if string(out.Sound.Data) != "ogg" || out.Sound.Seconds != 3 {
		t.Fatalf("som = %+v", out.Sound)
	}
	if e.conv.soundFrom != "vid" || e.conv.dur != sticker.MaxSound {
		t.Fatalf("som de %q, animação de %v", e.conv.soundFrom, e.conv.dur)
	}
}

func TestSoundStickerImageReplyingToAudio(t *testing.T) {
	e := setup(t, nil)
	m := groupMsg("!saudio", image())
	m.Quoted = &bot.Message{ID: "q", Media: audio()}
	e.router.Handle(context.Background(), m)
	out := e.client.Last().Out
	if out.Sound == nil || out.Animated {
		t.Fatalf("saída inesperada: %+v", out)
	}
	if e.conv.soundFrom != "aud" {
		t.Fatalf("som veio de %q", e.conv.soundFrom)
	}
}

func TestSoundStickerQuotedVideo(t *testing.T) {
	e := setup(t, nil)
	m := groupMsg("!ssom", nil)
	m.Quoted = &bot.Message{ID: "q", Media: video()}
	e.router.Handle(context.Background(), m)
	if out := e.client.Last().Out; out.Sound == nil || !out.Animated {
		t.Fatalf("saída inesperada: %+v", out)
	}
}

func TestSoundStickerNeedsSound(t *testing.T) {
	e := setup(t, nil)
	e.router.Handle(context.Background(), groupMsg("!ssom", image()))
	if got := e.client.Last().Out.Text; got != sticker.MsgSoundUsage {
		t.Fatalf("resposta = %q", got)
	}

	e = setup(t, nil)
	e.conv.noSound = true
	e.router.Handle(context.Background(), groupMsg("!ssom", video()))
	if got := e.client.Last().Out.Text; got != sticker.MsgNoSound {
		t.Fatalf("resposta = %q", got)
	}
}
