package nodeimport

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/joaodobread/ravena-go/internal/features/customcmd"
	"github.com/joaodobread/ravena-go/internal/store"
)

const chat = "120363000000000001@g.us"

func mkdb(t *testing.T, path string, stmts ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%v\n%s", err, s)
		}
	}
}

// fixture monta uma pasta data/ como a do Ravena em Node.
func fixture(t *testing.T) string {
	src := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(src, "sqlites"), 0o700))
	must(t, os.MkdirAll(filepath.Join(src, "media"), 0o700))
	must(t, os.WriteFile(filepath.Join(src, "media", "1700-1.jpg"), []byte("jpg"), 0o600))

	mkdb(t, filepath.Join(src, "sqlites", "core.db"),
		`CREATE TABLE groups (id TEXT PRIMARY KEY, name TEXT, prefix TEXT, paused INTEGER, custom_ignores_prefix INTEGER,
			additional_admins TEXT, greetings TEXT, farewells TEXT, interact TEXT, ignored_users TEXT, ignored_numbers TEXT,
			muted_commands TEXT, muted_categories TEXT)`,
		`INSERT INTO groups VALUES ('`+chat+`', 'Turma', '#', 1, 1, '["5562999990000@c.us"]',
			'{"text":"Oi {pessoa}"}', '{}', '{"enabled":false,"chance":300,"cooldown":60,"proporcao":20}',
			'["123@lid"]', '["5511888887777"]', '["Pescar"]', '["jogos"]')`,
		`INSERT INTO groups (id, name) VALUES ('5562999990000@c.us', 'privado')`,
	)
	mkdb(t, filepath.Join(src, "sqlites", "custom_commands.db"),
		`CREATE TABLE custom_commands (group_id TEXT, trigger TEXT, responses TEXT, admin_only INTEGER, active INTEGER,
			deleted INTEGER, count INTEGER, last_used INTEGER, created_by TEXT, created_at INTEGER, metadata TEXT, json_data TEXT)`,
		`INSERT INTO custom_commands VALUES ('`+chat+`', 'Bom  Dia', '["bom dia {pessoa}","{image-1700-1.jpg} legenda","{stickerGif-https://x/y.gif}","{audio-sumiu.ogg}"]',
			1, 1, 0, 7, 1700000000000, '5562999990000@c.us', 1690000000000, '{}',
			'{"sendAllResponses":true,"reply":false,"reactions":{"before":"⏳","after":"✅"},"cooldown":30}')`,
		`INSERT INTO custom_commands VALUES ('`+chat+`', 'apagado', '["x"]', 0, 1, 1, 0, 0, '', 0, '{}', '{}')`,
	)
	return src
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestImport(t *testing.T) {
	ctx := context.Background()
	src := fixture(t)
	dataDir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dataDir, "app.db"))
	must(t, err)
	defer st.Close()

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	rep, err := Run(ctx, src, st, dataDir, quiet)
	must(t, err)
	if rep.Groups != 1 || rep.Commands != 1 || rep.Media != 1 || len(rep.Skipped) != 2 {
		t.Fatalf("relatório: %+v", rep)
	}

	g, ok, err := st.Group(ctx, chat)
	must(t, err)
	if !ok || g.Name != "Turma" || g.Prefix != "#" || !g.Paused || g.Greeting != "Oi {pessoa}" || g.Farewell != "" {
		t.Fatalf("grupo: %+v", g)
	}
	if !slices.Equal(g.AdditionalAdmins, []string{"5562999990000"}) ||
		!slices.Equal(g.IgnoredUsers, []string{"123", "5511888887777"}) ||
		!slices.Equal(g.MutedCommands, []string{"pescar"}) || !slices.Equal(g.MutedCategories, []string{"jogos"}) {
		t.Fatalf("listas: %+v", g)
	}
	if g.Interact.Enabled || g.Interact.Chance != 300 || g.Interact.CooldownMin != 60 || g.Interact.Proporcao != 20 || !g.Interact.UseCmds {
		t.Fatalf("interact: %+v", g.Interact)
	}

	var raw string
	must(t, st.DB().QueryRow(`SELECT data FROM customcmd_commands WHERE chat = ? AND trigger = 'bom dia'`, chat).Scan(&raw))
	var c customcmd.Command
	must(t, json.Unmarshal([]byte(raw), &c))
	if !c.AdminOnly || !c.SendAll || !c.NoQuote || c.StartReact != "⏳" || c.React != "✅" || c.CooldownSec != 30 ||
		c.Count != 7 || c.LastUsed != 1700000000 || c.CreatedBy != "5562999990000" || len(c.Responses) != 2 {
		t.Fatalf("comando: %+v", c)
	}
	img := c.Responses[1]
	if img.Kind != "image" || img.Text != "legenda" || img.Mime != "image/jpeg" {
		t.Fatalf("mídia: %+v", img)
	}
	if b, err := os.ReadFile(filepath.Join(dataDir, "customcmd", "120363000000000001", img.File)); err != nil || string(b) != "jpg" {
		t.Fatalf("arquivo copiado: %q %v", b, err)
	}
	var ignore int
	must(t, st.DB().QueryRow(`SELECT ignore_prefix FROM customcmd_groups WHERE chat = ?`, chat).Scan(&ignore))
	if ignore != 1 {
		t.Fatal("ignore_prefix não importado")
	}

	// Idempotente.
	rep, err = Run(ctx, src, st, dataDir, quiet)
	must(t, err)
	var n int
	must(t, st.DB().QueryRow(`SELECT COUNT(*) FROM customcmd_commands`).Scan(&n))
	if n != 1 || rep.Commands != 1 {
		t.Fatalf("reimportar duplicou: %d", n)
	}
}

func TestMissingFilesAreSkipped(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dataDir, "app.db"))
	must(t, err)
	defer st.Close()
	rep, err := Run(ctx, t.TempDir(), st, dataDir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil || rep.Groups != 0 || rep.Commands != 0 {
		t.Fatalf("%+v %v", rep, err)
	}
}
