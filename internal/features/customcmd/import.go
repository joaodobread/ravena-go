package customcmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/joaodobread/ravena-go/internal/store"
)

// Importer grava comandos personalizados sem o bot rodando (usado pelo
// importador dos dados do Ravena em Node).
type Importer struct{ m *Module }

// NewImporter prepara as tabelas e a pasta de mídias em dataDir.
func NewImporter(ctx context.Context, st *store.Store, dataDir string) (*Importer, error) {
	if err := st.Migrate(ctx, "customcmd", migrations); err != nil {
		return nil, err
	}
	return &Importer{m: &Module{
		store: st, db: st.DB(), dir: filepath.Join(dataDir, "customcmd"),
		now: time.Now, cache: map[string]*cacheEntry{},
	}}, nil
}

// Put grava (ou substitui) o comando c do chat.
func (im *Importer) Put(ctx context.Context, chat string, c *Command) error {
	return im.m.save(ctx, chat, c)
}

// SetIgnorePrefix liga os gatilhos sem prefixo no chat.
func (im *Importer) SetIgnorePrefix(ctx context.Context, chat string, on bool) error {
	return im.m.setIgnorePrefix(ctx, chat, on)
}

// CopyMedia copia src para a pasta de mídias do chat e devolve o nome do arquivo.
func (im *Importer) CopyMedia(chat, src string) (string, error) {
	info, err := os.Stat(src)
	if err != nil {
		return "", err
	}
	if info.Size() > MaxMediaBytes {
		return "", fmt.Errorf("%s: %d bytes, acima do limite", src, info.Size())
	}
	name := safeName(filepath.Base(src))
	dir := im.m.chatDir(chat)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return "", err
	}
	return name, out.Close()
}
