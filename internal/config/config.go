// Package config lê a configuração das variáveis de ambiente.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config é a configuração do bot.
type Config struct {
	DataDir  string // banco SQLite e sessão do WhatsApp
	Prefix   string
	LogLevel slog.Level
	// PairPhone, se definido, pareia por código (sem QR) com esse número.
	PairPhone string
	// HTTPAddr expõe o painel e o /healthz (padrão :8080).
	HTTPAddr string
	// AdminPassword é a senha do painel; vazia, o bot gera uma e mostra no log.
	AdminPassword string
	// SuperAdmins são números com acesso total (além dos do painel).
	SuperAdmins []string

	MaxMessageAge  time.Duration // mensagens mais velhas são ignoradas (ex.: ao reconectar)
	Workers        int           // mensagens processadas em paralelo
	FFmpegJobs     int           // conversões simultâneas
	StickerAuthor  string
	OpenRouterKey  string
	OpenRouterBase string
	LLMModel       string
}

// Load lê a configuração de getenv (os.Getenv em produção).
func Load(getenv func(string) string) (Config, error) {
	c := Config{
		DataDir:        or(getenv("DATA_DIR"), "./data"),
		Prefix:         or(getenv("PREFIX"), "!"),
		PairPhone:      strings.TrimLeft(getenv("PAIR_PHONE"), "+"),
		StickerAuthor:  or(getenv("STICKER_AUTHOR"), "ravena"),
		OpenRouterKey:  getenv("OPENROUTER_API_KEY"),
		OpenRouterBase: or(getenv("OPENROUTER_BASE_URL"), "https://openrouter.ai/api/v1"),
		LLMModel:       or(getenv("LLM_MODEL"), "google/gemini-3.1-flash-lite"),
	}
	c.HTTPAddr = ":" + or(getenv("PORT"), "8080")
	c.AdminPassword = getenv("ADMIN_PASSWORD")
	for _, n := range strings.Split(getenv("SUPER_ADMINS"), ",") {
		if n = strings.TrimSpace(n); n != "" {
			c.SuperAdmins = append(c.SuperAdmins, n)
		}
	}

	var err error
	if c.MaxMessageAge, err = duration(getenv, "MAX_MESSAGE_AGE", 2*time.Minute); err != nil {
		return c, err
	}
	if c.Workers, err = integer(getenv, "WORKERS", 8); err != nil {
		return c, err
	}
	if c.FFmpegJobs, err = integer(getenv, "FFMPEG_JOBS", 2); err != nil {
		return c, err
	}
	if err = c.LogLevel.UnmarshalText([]byte(or(getenv("LOG_LEVEL"), "info"))); err != nil {
		return c, fmt.Errorf("LOG_LEVEL: %w", err)
	}
	return c, nil
}

func or(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func integer(getenv func(string) string, key string, def int) (int, error) {
	v := getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s: esperava inteiro positivo, veio %q", key, v)
	}
	return n, nil
}

func duration(getenv func(string) string, key string, def time.Duration) (time.Duration, error) {
	v := getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s: esperava duração (ex.: 2m), veio %q", key, v)
	}
	return d, nil
}

// Getenv é os.Getenv, exposto para o main.
var Getenv = os.Getenv
