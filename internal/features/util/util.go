// Package util reúne comandos utilitários: ping, QR codes (texto, Wi-Fi,
// PIX), Wikipedia, clima, busca, jogos grátis e links de build.
package util

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/joaodobread/ravena-go/internal/app"
	"github.com/joaodobread/ravena-go/internal/bot"
)

// URLs são as bases das APIs externas (trocáveis nos testes). Vazio = padrão.
type URLs struct {
	Wiki       string // https://pt.wikipedia.org
	Geo        string // https://geocoding-api.open-meteo.com
	Forecast   string // https://api.open-meteo.com
	DuckDuckGo string // https://api.duckduckgo.com
	GamerPower string // https://www.gamerpower.com
}

func (u URLs) withDefaults() URLs {
	def := func(s *string, v string) {
		if *s == "" {
			*s = v
		}
		*s = strings.TrimRight(*s, "/")
	}
	def(&u.Wiki, "https://pt.wikipedia.org")
	def(&u.Geo, "https://geocoding-api.open-meteo.com")
	def(&u.Forecast, "https://api.open-meteo.com")
	def(&u.DuckDuckGo, "https://api.duckduckgo.com")
	def(&u.GamerPower, "https://www.gamerpower.com")
	return u
}

// Feature agrupa os comandos utilitários.
type Feature struct {
	http    *http.Client
	urls    URLs
	log     *slog.Logger
	now     func() time.Time
	started time.Time
}

// New cria a funcionalidade.
func New(client *http.Client, urls URLs, log *slog.Logger, now func() time.Time) *Feature {
	if now == nil {
		now = time.Now
	}
	if client == nil {
		client = app.NewHTTPClient()
	}
	return &Feature{http: client, urls: urls.withDefaults(), log: log, now: now, started: now()}
}

// Commands devolve os comandos para registrar no Router.
func (f *Feature) Commands() []*bot.Command {
	loading := "⌛️"
	return []*bot.Command{
		{Name: "ping", Description: "Verifica se o bot está online", Category: "geral", Hidden: true,
			Reactions: bot.Reactions{After: "🍭"}, Run: f.ping},
		{Name: "qr", Description: "Gera um QR Code para um texto ou link", Usage: "!qr <texto>", Category: "utilidades",
			Reactions: bot.Reactions{After: "✅"}, Run: f.qr},
		{Name: "qr-wifi", Description: "Gera um QR Code para conexão WiFi", Usage: "!qr-wifi <SSID> <senha> [oculta]", Category: "utilidades",
			Reactions: bot.Reactions{After: "📶"}, Run: f.qrWifi},
		{Name: "qr-pix", Description: "Gera um QR Code para pagamento PIX", Usage: "!qr-pix <chave> [descrição] [valor]", Category: "utilidades",
			Reactions: bot.Reactions{After: "💠"}, Run: f.qrPix},
		{Name: "wiki", Description: "Busca informações na Wikipedia", Usage: "!wiki <termo>", Category: "utilidades",
			Reactions: bot.Reactions{Before: loading, After: "📚"}, Cooldown: 3 * time.Second, Run: f.wiki},
		{Name: "clima", Description: "Clima e previsão do tempo (Open-Meteo)", Usage: "!clima <cidade>[, UF]", Category: "utilidades",
			Reactions: bot.Reactions{Before: loading, After: "🌤️", Error: "❌"}, Cooldown: 3 * time.Second, Run: f.clima},
		{Name: "buscar", Aliases: []string{"google", "search"}, Description: "Busca na web", Usage: "!buscar <consulta>", Category: "utilidades",
			Reactions: bot.Reactions{Before: loading, After: "🔍"}, Cooldown: 3 * time.Second, Run: f.buscar},
		{Name: "0800", Aliases: []string{"giveaways", "freebies"}, Description: "Mostra jogos grátis e brindes atuais", Usage: "!0800 [plataforma]", Category: "jogos",
			Reactions: bot.Reactions{After: "🎮"}, Cooldown: 10 * time.Second, Run: f.giveaways},
		{Name: "lol-build", Description: "Link da build de um campeão do League of Legends", Usage: "!lol-build <campeão>", Category: "jogos",
			Reactions: bot.Reactions{After: "🎮"}, Run: buildLink("League of Legends", "lol-build", "https://op.gg/lol/champions/%s/build")},
		{Name: "wr-build", Description: "Link da build de um campeão do Wild Rift", Usage: "!wr-build <campeão>", Category: "jogos",
			Reactions: bot.Reactions{After: "🎮"}, Run: buildLink("Wild Rift", "wr-build", "https://www.wildriftfire.com/guide/%s")},
	}
}

func (f *Feature) ping(ctx context.Context, c *bot.Context) error {
	now := f.now()
	text := "Pong! 🏓"
	if ts := c.Message.Timestamp; !ts.IsZero() {
		d := max(now.Sub(ts), 0)
		text += fmt.Sprintf(" _(%.1fs)_", d.Seconds())
	}
	text += "\n⏱️ Online há " + humanDuration(now.Sub(f.started))
	return c.ReplyText(ctx, "%s", text)
}

// humanDuration formata como "2d 3h 4min" (mínimo "0min").
func humanDuration(d time.Duration) string {
	d = d.Truncate(time.Minute)
	days := int(d / (24 * time.Hour))
	hours := int(d/time.Hour) % 24
	mins := int(d/time.Minute) % 60
	var parts []string
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%dd", days))
	}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if mins > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%dmin", mins))
	}
	return strings.Join(parts, " ")
}

// buildLink cria o handler de !lol-build/!wr-build (só monta o link).
func buildLink(game, cmd, format string) bot.Handler {
	return func(ctx context.Context, c *bot.Context) error {
		slug := championSlug(c.Args)
		if slug == "" {
			return bot.Userf("❌ Por favor, informe o nome do campeão.\n\n*Exemplo:* %s%s aatrox", c.Prefix, cmd)
		}
		name := strings.Join(c.Args, " ")
		return c.ReplyText(ctx, "🎮 *Build do %s*\n\n*Campeão:* %s\n*Link:* %s", game, titleCase(name), fmt.Sprintf(format, slug))
	}
}

// championSlug junta os argumentos com "-" e remove o que não for [a-z0-9-]
// ("Kai'Sa" → "kaisa", "lee sin" → "lee-sin").
func championSlug(args []string) string {
	var b strings.Builder
	for i, a := range args {
		if i > 0 && b.Len() > 0 {
			b.WriteByte('-')
		}
		for _, r := range strings.ToLower(a) {
			if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
				b.WriteRune(r)
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func titleCase(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		r := []rune(strings.ToLower(w))
		words[i] = strings.ToUpper(string(r[:1])) + string(r[1:])
	}
	return strings.Join(words, " ")
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

const maxBody = 2 << 20 // 2 MB por resposta de API

// httpError é uma resposta HTTP fora de 2xx.
type httpError struct{ Status int }

func (e *httpError) Error() string { return fmt.Sprintf("HTTP %d", e.Status) }

// get faz GET e devolve o corpo (limitado a limit bytes).
func (f *Feature) get(ctx context.Context, url string, limit int64) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "ravena-bot/1.0 (WhatsApp bot)")
	resp, err := f.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return nil, "", &httpError{resp.StatusCode}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(data)) > limit {
		return nil, "", fmt.Errorf("resposta maior que %d bytes", limit)
	}
	return data, resp.Header.Get("Content-Type"), nil
}

func (f *Feature) getJSON(ctx context.Context, url string, v any) error {
	data, _, err := f.get(ctx, url, maxBody)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("json de %s: %w", url, err)
	}
	return nil
}

// upstream registra a falha da API e devolve uma mensagem amigável.
func (f *Feature) upstream(err error, what, msg string) error {
	f.log.Warn("API externa falhou", "api", what, "err", err)
	return bot.Userf("%s", msg)
}

// Module adapta a funcionalidade ao contrato app.Feature.
type Module struct {
	// URLs troca as bases das APIs (testes); vazio usa as reais.
	URLs URLs
	// Now substitui o relógio (testes); nil usa time.Now.
	Now func() time.Time
}

// Name implementa app.Feature.
func (Module) Name() string { return "util" }

// Register implementa app.Feature.
func (m Module) Register(_ context.Context, d *app.Deps) error {
	log := d.Log
	if log == nil {
		log = slog.Default()
	}
	return d.Router.Register(New(d.HTTP, m.URLs, log.With("feature", "util"), m.Now).Commands()...)
}
