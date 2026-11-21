package customcmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"go.mau.fi/whatsmeow/types"

	"github.com/joaodobread/ravena-go/internal/bot"
)

// chatDir é a pasta das mídias do chat (só o usuário do JID, sem "@g.us").
func (m *Module) chatDir(chat string) string {
	jid, err := types.ParseJID(chat)
	name := jid.User
	if err != nil || name == "" {
		name = chat
	}
	return filepath.Join(m.dir, safeName(name))
}

// safeName deixa só caracteres seguros para nome de arquivo.
func safeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		}
	}
	out := strings.TrimLeft(b.String(), ".")
	if out == "" {
		return "x"
	}
	return out
}

// validFile diz se name é um nome gerado por saveMedia (sem caminho).
func validFile(name string) bool {
	return name != "" && name == safeName(name) && !strings.Contains(name, "..")
}

var extByMime = map[string]string{
	"image/jpeg": "jpg", "image/png": "png", "image/webp": "webp", "image/gif": "gif",
	"video/mp4": "mp4", "video/3gpp": "3gp", "video/quicktime": "mov",
	"audio/ogg": "ogg", "audio/mpeg": "mp3", "audio/mp4": "m4a", "audio/aac": "aac", "audio/wav": "wav",
	"application/pdf": "pdf", "text/plain": "txt", "application/zip": "zip",
}

func extFor(mime string, kind bot.MediaKind) string {
	base, _, _ := strings.Cut(mime, ";")
	if ext, ok := extByMime[strings.TrimSpace(base)]; ok {
		return ext
	}
	switch kind {
	case bot.MediaImage:
		return "jpg"
	case bot.MediaVideo, bot.MediaGIF:
		return "mp4"
	case bot.MediaSticker:
		return "webp"
	case bot.MediaAudio:
		return "ogg"
	}
	return "bin"
}

func kindName(media *bot.Media) string {
	switch media.Kind {
	case bot.MediaImage:
		return "image"
	case bot.MediaVideo:
		return "video"
	case bot.MediaGIF:
		return "gif"
	case bot.MediaSticker:
		return "sticker"
	case bot.MediaAudio:
		if strings.Contains(media.Mime, "opus") {
			return "voice"
		}
		return "audio"
	}
	return "document"
}

// dirSize soma o tamanho dos arquivos da pasta (0 se não existir).
func dirSize(dir string) int64 {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var total int64
	for _, e := range entries {
		if info, err := e.Info(); err == nil && !e.IsDir() {
			total += info.Size()
		}
	}
	return total
}

// saveMedia baixa a mídia e grava na pasta do chat, respeitando os limites.
func (m *Module) saveMedia(ctx context.Context, chat string, media *bot.Media, caption string) (Response, error) {
	data, err := media.Download(ctx)
	if err != nil {
		return Response{}, fmt.Errorf("baixando mídia: %w", err)
	}
	if len(data) > MaxMediaBytes {
		return Response{}, bot.Userf("Essa mídia é grande demais (máximo %d MB).", MaxMediaBytes>>20)
	}
	dir := m.chatDir(chat)
	if dirSize(dir)+int64(len(data)) > MaxGroupBytes {
		return Response{}, bot.Userf("O grupo atingiu o limite de %d MB de mídias em comandos. Apague alguns com !g-delCmd.", MaxGroupBytes>>20)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Response{}, err
	}
	rnd := make([]byte, 3)
	_, _ = rand.Read(rnd)
	name := strconv.FormatInt(m.now().UnixMilli(), 10) + "-" + hex.EncodeToString(rnd) + "." + extFor(media.Mime, media.Kind)
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		return Response{}, err
	}
	return Response{Kind: kindName(media), Text: caption, File: name, Mime: media.Mime, Animated: media.Animated}, nil
}

// removeFiles apaga os arquivos de mídia das respostas (erros só no log).
func (m *Module) removeFiles(chat string, rs []Response) {
	dir := m.chatDir(chat)
	for _, r := range rs {
		if r.IsMedia() && validFile(r.File) {
			if err := os.Remove(filepath.Join(dir, r.File)); err != nil && !os.IsNotExist(err) {
				m.log.Warn("falha ao apagar mídia", "err", err, "file", r.File)
			}
		}
	}
}

// mediaOut lê o arquivo e monta a mensagem de mídia.
func (m *Module) mediaOut(chat string, r Response, caption string) (bot.Outgoing, error) {
	if !validFile(r.File) {
		return bot.Outgoing{}, fmt.Errorf("arquivo inválido %q", r.File)
	}
	data, err := os.ReadFile(filepath.Join(m.chatDir(chat), r.File))
	if err != nil {
		return bot.Outgoing{}, err
	}
	out := bot.Outgoing{Data: data, Mime: r.Mime, Caption: caption}
	switch r.Kind {
	case "image":
		out.Kind = bot.OutImage
	case "video", "gif":
		out.Kind = bot.OutVideo
	case "sticker":
		out.Kind, out.Caption, out.Animated = bot.OutSticker, "", r.Animated
	case "audio", "voice":
		out.Kind, out.Caption, out.PTT = bot.OutAudio, "", r.Kind == "voice"
	default:
		out.Kind = bot.OutDocument
		out.FileName = r.Name
		if out.FileName == "" {
			out.FileName = r.File
		}
	}
	if out.Mime == "" {
		out.Mime = "application/octet-stream"
	}
	return out, nil
}
