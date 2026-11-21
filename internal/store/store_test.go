package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMigrateIsIdempotentAndIncremental(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "app.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	v1 := []string{`CREATE TABLE fish (id INTEGER PRIMARY KEY)`}
	if err := s.Migrate(ctx, "fishing", v1); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx, "fishing", v1); err != nil {
		t.Fatalf("reaplicar não deveria falhar: %v", err)
	}
	s.Close()

	s, err = Open(ctx, path) // reabrir não reaplica o núcleo
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	v2 := append(v1, `ALTER TABLE fish ADD COLUMN name TEXT`)
	if err := s.Migrate(ctx, "fishing", v2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO fish(name) VALUES('tilápia')`); err != nil {
		t.Fatalf("coluna nova não existe: %v", err)
	}
	if err := s.Migrate(ctx, "broken", []string{`NOT SQL`}); err == nil {
		t.Fatal("migração inválida deveria falhar")
	}
}

func TestGroupDefaultsAndRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if _, ok, err := s.Group(ctx, "g1"); ok || err != nil {
		t.Fatalf("grupo inexistente: ok=%v err=%v", ok, err)
	}
	g, err := s.GroupOrDefault(ctx, "g1")
	if err != nil {
		t.Fatal(err)
	}
	if !g.Interact.Enabled || g.Interact.Chance != 100 || g.Interact.CooldownMin != 30 || !g.Silly {
		t.Fatalf("padrões inesperados: %+v", g)
	}

	g.Prefix = "/"
	g.MutedCommands = []string{"s"}
	if err := s.SaveGroup(ctx, g); err != nil {
		t.Fatal(err)
	}
	g.MutedCommands[0] = "alterado-fora" // o cache não pode ser afetado
	got, ok, err := s.Group(ctx, "g1")
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if got.Prefix != "/" || !got.IsMuted("s", "") || got.IsMuted("alterado-fora", "") {
		t.Fatalf("grupo inesperado: %+v", got)
	}

	s.groups = map[string]*Group{} // força leitura do banco
	got, _, _ = s.Group(ctx, "g1")
	if got.Prefix != "/" || got.CreatedAt.IsZero() {
		t.Fatalf("grupo do banco inesperado: %+v", got)
	}
}

func TestUpdateGroupAndList(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	for _, jid := range []string{"a", "b"} {
		if _, err := s.UpdateGroup(ctx, jid, func(g *Group) { g.Name = "grupo " + jid }); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.UpdateGroup(ctx, "a", func(g *Group) { g.Paused = true }); err != nil {
		t.Fatal(err)
	}
	all, err := s.Groups(ctx)
	if err != nil || len(all) != 2 {
		t.Fatalf("Groups: %v %v", all, err)
	}
	a, _, _ := s.Group(ctx, "a")
	if !a.Paused || a.Name != "grupo a" {
		t.Fatalf("update perdeu dados: %+v", a)
	}
}

func TestInteractLimits(t *testing.T) {
	i := Interact{CooldownMin: 5, Chance: 9000}
	if i.EffectiveCooldown() != 30*time.Minute || i.EffectiveChance() != 500 {
		t.Fatalf("limites não aplicados: %v %d", i.EffectiveCooldown(), i.EffectiveChance())
	}
}

func TestKV(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	var v map[string]int
	if ok, err := s.GetJSON(ctx, "x", &v); ok || err != nil {
		t.Fatal(ok, err)
	}
	if err := s.SetJSON(ctx, "x", map[string]int{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.GetJSON(ctx, "x", &v); !ok || err != nil || v["a"] != 1 {
		t.Fatal(ok, err, v)
	}
}

func TestUsageStats(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	now := time.Now()
	for _, u := range []Usage{
		{Command: "s", Chat: "g1", OK: true, At: now},
		{Command: "s", Chat: "g1", OK: false, At: now},
		{Command: "s", Chat: "g2", OK: true, At: now},
		{Command: "dado", Chat: "g1", OK: true, At: now},
		{Command: "velho", Chat: "g1", OK: true, At: now.Add(-48 * time.Hour)},
	} {
		if err := s.LogUsage(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	top, err := s.TopCommands(ctx, now.Add(-time.Hour), "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 2 || top[0] != (CommandCount{Command: "s", Count: 3, Errors: 1}) {
		t.Fatalf("top = %+v", top)
	}
	inG2, _ := s.TopCommands(ctx, now.Add(-time.Hour), "g2", 10)
	if len(inG2) != 1 || inG2[0].Count != 1 {
		t.Fatalf("filtro por chat: %+v", inG2)
	}
	if n, err := s.PruneUsage(ctx, now.Add(-24*time.Hour)); err != nil || n != 1 {
		t.Fatalf("prune: %d %v", n, err)
	}
}

func TestMissingGroupIsCachedUntilSaved(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if _, ok, _ := s.Group(ctx, "x"); ok {
		t.Fatal("não deveria existir")
	}
	if g, present := s.groups["x"]; !present || g != nil {
		t.Fatal("ausência deveria ficar em cache")
	}
	if _, ok, _ := s.Group(ctx, "x"); ok {
		t.Fatal("cache de ausência devolveu grupo")
	}
	if _, err := s.UpdateGroup(ctx, "x", func(g *Group) { g.Prefix = "#" }); err != nil {
		t.Fatal(err)
	}
	if g, ok, _ := s.Group(ctx, "x"); !ok || g.Prefix != "#" {
		t.Fatalf("depois de salvar: %+v %v", g, ok)
	}
}
