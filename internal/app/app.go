// Package app define o contrato entre o main e as funcionalidades: cada
// funcionalidade recebe Deps e se registra (comandos, listeners, páginas).
package app

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/llm"
	"github.com/joaodobread/ravena-go/internal/media"
	"github.com/joaodobread/ravena-go/internal/policy"
	"github.com/joaodobread/ravena-go/internal/store"
	"github.com/joaodobread/ravena-go/internal/web"
)

// Deps é tudo o que uma funcionalidade pode usar.
type Deps struct {
	Router *bot.Router
	Client bot.Client
	Store  *store.Store
	Policy *policy.Policy
	// LLM é nil quando não há OPENROUTER_API_KEY: a funcionalidade deve
	// degradar (responder que está indisponível) em vez de falhar.
	LLM   *llm.Client
	Media *media.Converter
	Web   *web.Server
	HTTP  *http.Client // timeout curto, para APIs externas
	Log   *slog.Logger
	// DataDir é o diretório persistente (volume); use subpastas por funcionalidade.
	DataDir string
}

// BotName é o nome do bot configurado no painel.
func (d *Deps) BotName() string {
	if n := d.Policy.Settings().BotName; n != "" {
		return n
	}
	return "ravena"
}

// Feature é uma funcionalidade do bot.
type Feature interface {
	// Name identifica a funcionalidade (logs e escopo de migrações).
	Name() string
	// Register adiciona comandos/listeners/páginas. ctx vive enquanto o bot
	// viver: use-o para goroutines de fundo (agendamentos).
	Register(ctx context.Context, d *Deps) error
}

// NewHTTPClient é o cliente HTTP padrão das funcionalidades.
func NewHTTPClient() *http.Client {
	return &http.Client{Timeout: 20 * time.Second}
}
