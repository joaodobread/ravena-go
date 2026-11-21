package media

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Options configura o Converter. Zeros viram os valores padrão.
type Options struct {
	FFmpeg      string        // binário do ffmpeg (padrão "ffmpeg")
	Magick      string        // binário do ImageMagick (padrão "magick")
	TempDir     string        // diretório de arquivos temporários (padrão os.TempDir())
	Concurrency int           // processos externos simultâneos (padrão 2)
	MaxDuration time.Duration // duração máxima de figurinha animada (padrão 6s)
	// MaxAnimatedBytes é o tamanho alvo de figurinha animada; acima disso o
	// Converter tenta de novo com menos qualidade/fps (padrão 500 KiB).
	MaxAnimatedBytes int
}

// Converter transforma mídias usando ffmpeg/ImageMagick como processos externos.
// O semáforo limita quantos rodam ao mesmo tempo, o que segura o pico de RAM.
type Converter struct {
	opts Options
	sem  chan struct{}
}

// NewConverter cria um Converter.
func NewConverter(o Options) *Converter {
	if o.FFmpeg == "" {
		o.FFmpeg = "ffmpeg"
	}
	if o.Magick == "" {
		o.Magick = "magick"
	}
	if o.TempDir == "" {
		o.TempDir = os.TempDir()
	}
	if o.Concurrency <= 0 {
		o.Concurrency = 2
	}
	if o.MaxDuration <= 0 {
		o.MaxDuration = 6 * time.Second
	}
	if o.MaxAnimatedBytes <= 0 {
		o.MaxAnimatedBytes = 500 << 10
	}
	return &Converter{opts: o, sem: make(chan struct{}, o.Concurrency)}
}

// animatedAttempts vai baixando a qualidade até caber em MaxAnimatedBytes.
var animatedAttempts = []struct{ fps, quality int }{
	{15, 60}, {12, 45}, {10, 30}, {8, 20},
}

// StaticSticker converte uma imagem em figurinha WebP 512×512.
func (c *Converter) StaticSticker(ctx context.Context, in []byte, crop Crop) ([]byte, error) {
	return c.ffmpeg(ctx, in, ".webp",
		"-frames:v", "1",
		"-vf", crop.Filter()+",format=yuva420p",
		"-c:v", "libwebp", "-lossless", "0", "-q:v", "80", "-compression_level", "4",
		"-f", "webp")
}

// AnimatedSticker converte vídeo/GIF em figurinha WebP animada 512×512.
func (c *Converter) AnimatedSticker(ctx context.Context, in []byte, crop Crop) ([]byte, error) {
	var out []byte
	for _, a := range animatedAttempts {
		var err error
		out, err = c.ffmpeg(ctx, in, ".webp",
			"-t", strconv.FormatFloat(c.opts.MaxDuration.Seconds(), 'f', -1, 64),
			"-an",
			"-vf", "fps="+strconv.Itoa(a.fps)+","+crop.Filter()+",format=yuva420p",
			"-c:v", "libwebp", "-loop", "0", "-lossless", "0",
			"-q:v", strconv.Itoa(a.quality), "-compression_level", "4",
			"-f", "webp")
		if err != nil {
			return nil, err
		}
		if len(out) <= c.opts.MaxAnimatedBytes {
			break
		}
	}
	return out, nil
}

// Frame extrai o primeiro quadro como JPEG de no máximo 768px (para o LLM).
func (c *Converter) Frame(ctx context.Context, in []byte) ([]byte, error) {
	return c.ffmpeg(ctx, in, ".jpg",
		"-frames:v", "1",
		"-vf", "scale='min(768,iw)':-2",
		"-q:v", "4", "-f", "image2", "-c:v", "mjpeg")
}

// WebPToPNG converte um WebP estático em PNG (preserva a transparência).
func (c *Converter) WebPToPNG(ctx context.Context, in []byte) ([]byte, error) {
	return c.ffmpeg(ctx, in, ".png", "-frames:v", "1", "-f", "image2", "-c:v", "png")
}

// AnimatedWebPToMP4 converte uma figurinha animada em MP4. O ffmpeg não
// decodifica WebP animado, então o ImageMagick faz o WebP → GIF antes.
func (c *Converter) AnimatedWebPToMP4(ctx context.Context, in []byte) ([]byte, error) {
	if err := c.acquire(ctx); err != nil {
		return nil, err
	}
	defer c.release()

	dir, err := os.MkdirTemp(c.opts.TempDir, "ravena-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	src, gif, mp4 := filepath.Join(dir, "in.webp"), filepath.Join(dir, "mid.gif"), filepath.Join(dir, "out.mp4")
	if err := os.WriteFile(src, in, 0o600); err != nil {
		return nil, err
	}
	if err := run(ctx, c.opts.Magick, src, "-coalesce", gif); err != nil {
		return nil, err
	}
	if err := run(ctx, c.opts.FFmpeg, "-hide_banner", "-loglevel", "error", "-y", "-i", gif,
		"-an", "-c:v", "libx264", "-preset", "veryfast", "-crf", "28",
		"-pix_fmt", "yuv420p", "-movflags", "faststart",
		"-vf", "scale=trunc(iw/2)*2:trunc(ih/2)*2", mp4); err != nil {
		return nil, err
	}
	return os.ReadFile(mp4)
}

// FFmpeg roda "ffmpeg -i <entrada> <args> <saída>" respeitando o limite de
// concorrência. outExt define a extensão (e o formato) da saída, ex.: ".mp3".
func (c *Converter) FFmpeg(ctx context.Context, in []byte, outExt string, args ...string) ([]byte, error) {
	return c.ffmpeg(ctx, in, outExt, args...)
}

// Magick roda "magick <entrada><inExt> <args> <saída><outExt>" respeitando o
// limite de concorrência (ex.: efeitos de imagem).
func (c *Converter) Magick(ctx context.Context, in []byte, inExt, outExt string, args ...string) ([]byte, error) {
	if err := c.acquire(ctx); err != nil {
		return nil, err
	}
	defer c.release()
	dir, err := os.MkdirTemp(c.opts.TempDir, "ravena-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	src, dst := filepath.Join(dir, "in"+inExt), filepath.Join(dir, "out"+outExt)
	if err := os.WriteFile(src, in, 0o600); err != nil {
		return nil, err
	}
	full := append([]string{src}, args...)
	if err := run(ctx, c.opts.Magick, append(full, dst)...); err != nil {
		return nil, err
	}
	return os.ReadFile(dst)
}

// ffmpeg roda "ffmpeg -i <entrada> <args> <saída>" usando arquivos temporários:
// MP4 por pipe falha quando o moov fica no fim do arquivo.
func (c *Converter) ffmpeg(ctx context.Context, in []byte, outExt string, args ...string) ([]byte, error) {
	if err := c.acquire(ctx); err != nil {
		return nil, err
	}
	defer c.release()

	dir, err := os.MkdirTemp(c.opts.TempDir, "ravena-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	src, dst := filepath.Join(dir, "in"), filepath.Join(dir, "out"+outExt)
	if err := os.WriteFile(src, in, 0o600); err != nil {
		return nil, err
	}
	full := append([]string{"-hide_banner", "-loglevel", "error", "-y", "-i", src}, args...)
	if err := run(ctx, c.opts.FFmpeg, append(full, dst)...); err != nil {
		return nil, err
	}
	return os.ReadFile(dst)
}

func (c *Converter) acquire(ctx context.Context) error {
	select {
	case c.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Converter) release() { <-c.sem }

func run(ctx context.Context, bin string, args ...string) error {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 500 {
			msg = msg[len(msg)-500:]
		}
		return fmt.Errorf("%s: %w: %s", filepath.Base(bin), err, msg)
	}
	return nil
}
