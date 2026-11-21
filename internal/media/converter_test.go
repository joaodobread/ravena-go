package media

import (
	"bytes"
	"context"
	"image"
	_ "image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Testes de integração: rodam o ffmpeg de verdade e são pulados sem ele.

func requireBin(t *testing.T, bins ...string) {
	t.Helper()
	for _, b := range bins {
		if _, err := exec.LookPath(b); err != nil {
			t.Skipf("%s não instalado", b)
		}
	}
}

// lavfi gera uma mídia de teste com o ffmpeg.
func lavfi(t *testing.T, src, ext string, args ...string) []byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), "src"+ext)
	full := append([]string{"-v", "error", "-y", "-f", "lavfi", "-i", src}, args...)
	if b, err := exec.Command("ffmpeg", append(full, out)...).CombinedOutput(); err != nil {
		t.Fatalf("gerando mídia: %v: %s", err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newTestConverter(t *testing.T) *Converter {
	return NewConverter(Options{TempDir: t.TempDir()})
}

func TestStaticStickerAllCropModes(t *testing.T) {
	requireBin(t, "ffmpeg")
	img := lavfi(t, "testsrc=size=640x360", ".png", "-frames:v", "1")
	c := newTestConverter(t)
	for _, crop := range []Crop{{Mode: Contain}, {Mode: Center}, {Mode: Top}, {Mode: Bottom}, {Mode: Stretch}, {Mode: Focus, Focus: 80}} {
		out, err := c.StaticSticker(context.Background(), img, crop)
		if err != nil {
			t.Fatalf("modo %d: %v", crop.Mode, err)
		}
		info, err := InspectWebP(out)
		if err != nil {
			t.Fatalf("modo %d: %v", crop.Mode, err)
		}
		if info.Width != StickerSize || info.Height != StickerSize || info.Animated {
			t.Fatalf("modo %d: info inesperada %+v", crop.Mode, info)
		}
	}
}

func TestContainKeepsTransparentBorders(t *testing.T) {
	requireBin(t, "ffmpeg")
	c := newTestConverter(t)
	out, err := c.StaticSticker(context.Background(), lavfi(t, "color=red:size=600x200", ".png", "-frames:v", "1"), Crop{Mode: Contain})
	if err != nil {
		t.Fatal(err)
	}
	pngBytes, err := c.WebPToPNG(context.Background(), out)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, a := img.At(256, 5).RGBA(); a != 0 {
		t.Fatalf("borda deveria ser transparente, alfa=%d", a)
	}
	if _, _, _, a := img.At(256, 256).RGBA(); a == 0 {
		t.Fatal("centro deveria ser opaco")
	}
}

func TestAnimatedStickerRespectsLimits(t *testing.T) {
	requireBin(t, "ffmpeg")
	video := lavfi(t, "testsrc2=size=480x270:rate=30", ".mp4", "-t", "10", "-pix_fmt", "yuv420p")
	c := NewConverter(Options{TempDir: t.TempDir(), MaxDuration: 2 * time.Second})
	out, err := c.AnimatedSticker(context.Background(), video, Crop{Mode: Center})
	if err != nil {
		t.Fatal(err)
	}
	info, err := InspectWebP(out)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Animated || info.Width != StickerSize || info.Height != StickerSize {
		t.Fatalf("info inesperada: %+v", info)
	}
	if len(out) > c.opts.MaxAnimatedBytes {
		t.Fatalf("figurinha com %d bytes passou do limite %d", len(out), c.opts.MaxAnimatedBytes)
	}
}

func TestFrameIsSmallJPEG(t *testing.T) {
	requireBin(t, "ffmpeg")
	c := newTestConverter(t)
	out, err := c.Frame(context.Background(), lavfi(t, "testsrc=size=1920x1080", ".png", "-frames:v", "1"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if format != "jpeg" || cfg.Width != 768 {
		t.Fatalf("got %s %dx%d", format, cfg.Width, cfg.Height)
	}
}

func TestAnimatedWebPToMP4(t *testing.T) {
	requireBin(t, "ffmpeg", "magick")
	c := newTestConverter(t)
	sticker, err := c.AnimatedSticker(context.Background(),
		lavfi(t, "testsrc=size=320x240:rate=10", ".mp4", "-t", "1", "-pix_fmt", "yuv420p"), Crop{Mode: Contain})
	if err != nil {
		t.Fatal(err)
	}
	mp4, err := c.AnimatedWebPToMP4(context.Background(), sticker)
	if err != nil {
		t.Fatal(err)
	}
	if len(mp4) < 12 || string(mp4[4:8]) != "ftyp" {
		t.Fatalf("saída não parece MP4: % x", mp4[:min(12, len(mp4))])
	}
}

func TestConverterReportsFFmpegErrors(t *testing.T) {
	requireBin(t, "ffmpeg")
	_, err := newTestConverter(t).StaticSticker(context.Background(), []byte("lixo"), Crop{})
	if err == nil || !strings.Contains(err.Error(), "ffmpeg") {
		t.Fatalf("esperava erro do ffmpeg, veio %v", err)
	}
}

func TestConverterHonoursContext(t *testing.T) {
	c := NewConverter(Options{Concurrency: 1})
	c.sem <- struct{}{} // ocupa o único slot
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.StaticSticker(ctx, nil, Crop{}); err != context.Canceled {
		t.Fatalf("esperava context.Canceled, veio %v", err)
	}
}

func TestGenericRunners(t *testing.T) {
	requireBin(t, "ffmpeg", "magick")
	c := newTestConverter(t)
	img := lavfi(t, "testsrc=size=64x64", ".png", "-frames:v", "1")
	out, err := c.Magick(context.Background(), img, ".png", ".jpg", "-implode", "0.5")
	if err != nil || len(out) < 3 || out[0] != 0xff || out[1] != 0xd8 {
		t.Fatalf("Magick: %v (% x)", err, out[:min(4, len(out))])
	}
	audio := lavfi(t, "sine=frequency=440:duration=1", ".wav")
	mp3, err := c.FFmpeg(context.Background(), audio, ".mp3", "-c:a", "libmp3lame", "-b:a", "64k")
	if err != nil || len(mp3) == 0 {
		t.Fatalf("FFmpeg: %v", err)
	}
}
