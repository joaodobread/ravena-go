package config

import (
	"log/slog"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestDefaults(t *testing.T) {
	c, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Prefix != "!" || c.DataDir != "./data" || c.Workers != 8 || c.FFmpegJobs != 2 ||
		c.MaxMessageAge != 2*time.Minute || c.LogLevel != slog.LevelInfo || c.HTTPAddr != ":8080" {
		t.Fatalf("padrões inesperados: %+v", c)
	}
}

func TestOverrides(t *testing.T) {
	c, err := Load(env(map[string]string{
		"PREFIX": "/", "PORT": "8080", "PAIR_PHONE": "+5562999999999",
		"WORKERS": "3", "MAX_MESSAGE_AGE": "30s", "LOG_LEVEL": "debug",
		"SUPER_ADMINS": "556296975194@c.us, 5511999999999",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Prefix != "/" || c.HTTPAddr != ":8080" || c.PairPhone != "5562999999999" ||
		c.Workers != 3 || c.MaxMessageAge != 30*time.Second || c.LogLevel != slog.LevelDebug || len(c.SuperAdmins) != 2 {
		t.Fatalf("overrides não aplicados: %+v", c)
	}
}

func TestInvalidValues(t *testing.T) {
	for k, v := range map[string]string{"WORKERS": "0", "FFMPEG_JOBS": "x", "MAX_MESSAGE_AGE": "ontem", "LOG_LEVEL": "verbose"} {
		if _, err := Load(env(map[string]string{k: v})); err == nil {
			t.Errorf("%s=%q deveria falhar", k, v)
		}
	}
}
