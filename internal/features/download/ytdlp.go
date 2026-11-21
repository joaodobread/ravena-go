package download

import (
	"bytes"
	"context"
	"fmt"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// YTDLP baixa com o binário yt-dlp, um download por vez (cada processo usa
// dezenas de MB enquanto roda; fora disso não custa nada).
type YTDLP struct {
	Bin      string
	TempDir  string
	MaxBytes int64
	sem      chan struct{}
}

// NewYTDLP cria o downloader. Zeros viram padrões.
func NewYTDLP(bin, tempDir string, maxBytes int64) *YTDLP {
	if bin == "" {
		bin = "yt-dlp"
	}
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	return &YTDLP{Bin: bin, TempDir: tempDir, MaxBytes: maxBytes, sem: make(chan struct{}, 1)}
}

// Args monta os argumentos do yt-dlp (exportado para teste).
func (y *YTDLP) Args(target, outDir string, audio bool) []string {
	args := []string{
		"--no-playlist", "--no-warnings", "--quiet", "--no-progress", "--restrict-filenames",
		"--max-filesize", fmt.Sprint(y.MaxBytes),
		"-o", filepath.Join(outDir, "%(title).80B.%(ext)s"),
	}
	if audio {
		args = append(args, "-f", "ba/b", "-x", "--audio-format", "mp3", "--audio-quality", "5")
	} else {
		// Prefere MP4 até 720p, que o WhatsApp toca sem reconverter.
		args = append(args, "-f", "b[ext=mp4][height<=720]/bv*[ext=mp4][height<=720]+ba[ext=m4a]/b[height<=720]/b",
			"--merge-output-format", "mp4")
	}
	return append(args, "--", target)
}

// Fetch implementa Fetcher.
func (y *YTDLP) Fetch(ctx context.Context, target string, audio bool) (File, error) {
	select {
	case y.sem <- struct{}{}:
	case <-ctx.Done():
		return File{}, ctx.Err()
	}
	defer func() { <-y.sem }()

	dir, err := os.MkdirTemp(y.TempDir, "ravena-dl-")
	if err != nil {
		return File{}, err
	}
	defer os.RemoveAll(dir)

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, y.Bin, y.Args(target, dir, audio)...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return File{}, ctx.Err()
		}
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 300 {
			msg = msg[len(msg)-300:]
		}
		return File{}, fmt.Errorf("yt-dlp: %w: %s", err, msg)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return File{}, err
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasSuffix(name, ".part") || strings.HasSuffix(name, ".ytdl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return File{}, err
		}
		if info.Size() > y.MaxBytes {
			return File{}, ErrTooBig
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return File{}, err
		}
		ext := filepath.Ext(name)
		m := mime.TypeByExtension(ext)
		if m == "" {
			m = "application/octet-stream"
		}
		title := strings.ReplaceAll(strings.TrimSuffix(name, ext), "_", " ")
		return File{Data: data, Name: name, Mime: strings.Split(m, ";")[0], Title: title}, nil
	}
	// yt-dlp sai com 0 e não grava nada quando o arquivo passa do --max-filesize.
	return File{}, ErrTooBig
}
