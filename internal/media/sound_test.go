package media

import (
	"context"
	"encoding/binary"
	"testing"
	"time"
)

func TestStickerSoundFromVideo(t *testing.T) {
	requireBin(t, "ffmpeg")
	// 15 s de vídeo com tom: o som tem que ser cortado em 10 s.
	vid := lavfi(t, "testsrc=size=320x240:rate=10:duration=15", ".mp4",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=15", "-shortest", "-c:v", "libx264", "-c:a", "aac")
	s, err := newTestConverter(t).StickerSound(context.Background(), vid, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if string(s.Ogg[:4]) != "OggS" {
		t.Fatalf("não é ogg: %q", s.Ogg[:4])
	}
	if s.Seconds != 10 {
		t.Fatalf("Seconds = %d, want 10", s.Seconds)
	}
	if len(s.Waveform) != WaveformBars {
		t.Fatalf("waveform com %d barras", len(s.Waveform))
	}
	top := byte(0)
	for _, b := range s.Waveform {
		top = max(top, b)
	}
	if top != 100 {
		t.Fatalf("maior barra = %d, want 100", top)
	}
}

func TestStickerSoundWithoutAudioFails(t *testing.T) {
	requireBin(t, "ffmpeg")
	vid := lavfi(t, "testsrc=size=320x240:rate=10:duration=2", ".mp4", "-c:v", "libx264")
	if _, err := newTestConverter(t).StickerSound(context.Background(), vid, 10*time.Second); err == nil {
		t.Fatal("vídeo sem áudio deveria falhar")
	}
}

func TestAnimatedStickerForDuration(t *testing.T) {
	requireBin(t, "ffmpeg")
	vid := lavfi(t, "testsrc=size=320x240:rate=10:duration=12", ".mp4", "-c:v", "libx264")
	out, err := newTestConverter(t).AnimatedStickerFor(context.Background(), vid, Crop{Mode: Contain}, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := InspectWebP(out); err != nil || !info.Animated {
		t.Fatalf("info = %+v, err = %v", info, err)
	}
}

func TestWaveformScalesToHundred(t *testing.T) {
	var pcm []byte
	for _, v := range []int16{0, 100, -200, 50} {
		pcm = binary.LittleEndian.AppendUint16(pcm, uint16(v))
	}
	got := Waveform(pcm, 4)
	want := []byte{0, 50, 100, 25}
	if string(got) != string(want) {
		t.Fatalf("Waveform = %v, want %v", got, want)
	}
	if w := Waveform(nil, 64); len(w) != 64 {
		t.Fatalf("PCM vazio: %d barras", len(w))
	}
}

func TestOggOpusDurationGarbage(t *testing.T) {
	if d := OggOpusDuration([]byte("lixo")); d != 0 {
		t.Fatalf("duração de lixo = %v", d)
	}
}
