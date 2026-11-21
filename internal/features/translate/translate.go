// Package translate implementa o !traduzir e a tradução por reação com a
// bandeira do idioma.
package translate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/joaodobread/ravena-go/internal/app"
	"github.com/joaodobread/ravena-go/internal/bot"
)

// Asker é a IA usada como alternativa quando o Google falha (d.LLM).
type Asker interface {
	Ask(ctx context.Context, system, prompt string) (string, error)
}

const (
	maxInput = 4000    // caracteres traduzidos por vez (a URL do GET tem limite)
	maxBody  = 1 << 20 // resposta do Google
)

// Feature agrupa os comandos de tradução.
type Feature struct {
	http    *http.Client
	baseURL string
	llm     Asker // nil: sem alternativa
	log     *slog.Logger
}

// New cria a funcionalidade. baseURL vazio usa https://translate.googleapis.com;
// llm pode ser nil.
func New(client *http.Client, baseURL string, llm Asker, log *slog.Logger) *Feature {
	if baseURL == "" {
		baseURL = "https://translate.googleapis.com"
	}
	if client == nil {
		client = app.NewHTTPClient()
	}
	return &Feature{http: client, baseURL: strings.TrimRight(baseURL, "/"), llm: llm, log: log}
}

// Commands devolve os comandos para registrar no Router.
func (f *Feature) Commands() []*bot.Command {
	return []*bot.Command{
		{
			Name:        "traduzir",
			Description: "Traduz um texto para o idioma especificado",
			Usage:       "!traduzir <idioma> <texto> (ou respondendo a uma mensagem); !traduzir <origem> <destino> <texto>",
			Category:    "utilidades",
			Reactions:   bot.Reactions{Before: "⌛️", After: "🌐", Error: "❌"},
			Run:         f.translateCmd,
		},
		{
			Name:        "traduzir-bandeira",
			Description: "Traduz a mensagem reagida com a bandeira do idioma",
			Category:    "utilidades",
			Hidden:      true,
			Triggers:    flagTriggers(),
			Run:         f.reaction,
		},
	}
}

func (f *Feature) usage(prefix string) error {
	return bot.Userf("Por favor, forneça o idioma de destino e o texto a ser traduzido.\n"+
		"Exemplo: %[1]straduzir en Olá, mundo!\n"+
		"Com origem: %[1]straduzir pt en Olá, mundo!\n"+
		"Ou responda a uma mensagem com: %[1]straduzir en\n"+
		"_Também dá para reagir a uma mensagem com a bandeira do país (🇺🇸, 🇪🇸, 🇯🇵...)._", prefix)
}

func (f *Feature) translateCmd(ctx context.Context, c *bot.Context) error {
	if len(c.Args) == 0 {
		return f.usage(c.Prefix)
	}
	quoted := ""
	if q := c.Message.Quoted; q != nil {
		quoted = strings.TrimSpace(q.Text)
	}

	first, ok := languageCode(c.Args[0])
	if !ok {
		return bot.Userf("Idioma não reconhecido: \"%s\".\nExemplo de idiomas suportados: en (inglês), es (espanhol), fr (francês), ja (japonês)...", c.Args[0])
	}
	src, dst, skip := "auto", first, 1
	// Forma antiga "origem destino texto" (ou "origem destino" respondendo).
	if len(c.Args) >= 2 {
		if second, ok := targetCode(c.Args[1]); ok && (len(c.Args) >= 3 || quoted != "") {
			src, dst, skip = first, second, 2
		}
	}

	text := dropFields(c.RawArgs(), skip)
	if text == "" {
		text = quoted
	}
	if text == "" {
		return bot.Userf("Por favor, responda a uma mensagem ou forneça um texto para traduzir.")
	}

	out, detected, err := f.translate(ctx, text, src, dst)
	if err != nil {
		return err
	}
	header := fmt.Sprintf("🌐 *Tradução para %s*", languageName(dst))
	if src == "auto" && detected != "" {
		header = fmt.Sprintf("🌐 *Tradução de %s para %s*", languageName(detected), languageName(dst))
	} else if src != "auto" {
		header = fmt.Sprintf("🌐 *Tradução de %s para %s*", languageName(src), languageName(dst))
	}
	return c.ReplyText(ctx, "%s\n\n%s", header, out)
}

func (f *Feature) reaction(ctx context.Context, c *bot.Context) error {
	if c.Reaction == nil {
		return bot.ErrSilent // digitado como comando
	}
	lang, ok := flagLanguage[c.Reaction.Emoji]
	text := strings.TrimSpace(c.Message.Text)
	if !ok || text == "" {
		return bot.ErrSilent // figurinha, foto sem legenda...
	}
	out, _, err := f.translate(ctx, text, "auto", lang)
	if err != nil {
		return err
	}
	return c.ReplyText(ctx, "🌐 *Tradução para %s (%s)*\n\n%s", languageName(lang), c.Reaction.Emoji, out)
}

// dropFields remove as n primeiras palavras de s preservando o resto
// (inclusive quebras de linha).
func dropFields(s string, n int) string {
	s = strings.TrimSpace(s)
	for range n {
		i := strings.IndexAny(s, " \t\r\n")
		if i < 0 {
			return ""
		}
		s = strings.TrimSpace(s[i:])
	}
	return s
}

// translate traduz text; devolve também o idioma detectado (se src="auto").
func (f *Feature) translate(ctx context.Context, text, src, dst string) (string, string, error) {
	if r := []rune(text); len(r) > maxInput {
		text = string(r[:maxInput])
	}
	out, detected, err := f.google(ctx, text, src, dst)
	if err == nil {
		return out, detected, nil
	}
	f.log.Warn("google translate falhou", "err", err)
	if f.llm != nil {
		from := ""
		if src != "auto" {
			from = " do " + languageName(src)
		}
		system := fmt.Sprintf("Você é um tradutor. Traduza o texto do usuário%s para o idioma %s (código %s). Responda só com a tradução, sem comentários.", from, languageName(dst), dst)
		if out, lerr := f.llm.Ask(ctx, system, text); lerr == nil && strings.TrimSpace(out) != "" {
			return strings.TrimSpace(out), "", nil
		} else if lerr != nil {
			f.log.Warn("tradução por IA falhou", "err", lerr)
		}
	}
	return "", "", bot.Userf("Erro ao traduzir o texto. Por favor, tente novamente mais tarde.")
}

// google usa o endpoint sem chave do Google Tradutor. A resposta é um array
// aninhado: [[["trecho traduzido","original",...],...],null,"idioma detectado",...].
func (f *Feature) google(ctx context.Context, text, src, dst string) (string, string, error) {
	q := url.Values{"client": {"gtx"}, "sl": {apiCode(src)}, "tl": {apiCode(dst)}, "dt": {"t"}, "q": {text}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.baseURL+"/translate_a/single?"+q.Encode(), nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := f.http.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return "", "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("google translate: HTTP %d", resp.StatusCode)
	}
	return parseGoogle(data)
}

func parseGoogle(data []byte) (string, string, error) {
	var root []json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return "", "", fmt.Errorf("google translate: %w", err)
	}
	if len(root) == 0 {
		return "", "", errors.New("google translate: resposta vazia")
	}
	var chunks [][]any
	if err := json.Unmarshal(root[0], &chunks); err != nil {
		return "", "", fmt.Errorf("google translate: trechos: %w", err)
	}
	var b strings.Builder
	for _, ch := range chunks {
		if len(ch) > 0 {
			if s, ok := ch[0].(string); ok {
				b.WriteString(s)
			}
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "", "", errors.New("google translate: sem tradução")
	}
	var detected string
	if len(root) > 2 {
		_ = json.Unmarshal(root[2], &detected)
	}
	return out, strings.ToLower(detected), nil
}

// Module adapta a funcionalidade ao contrato app.Feature.
type Module struct {
	// BaseURL troca o endpoint do Google (testes); vazio usa o real.
	BaseURL string
	// LLM substitui d.LLM como alternativa (testes).
	LLM Asker
}

// Name implementa app.Feature.
func (Module) Name() string { return "translate" }

// Register implementa app.Feature.
func (m Module) Register(_ context.Context, d *app.Deps) error {
	llm := m.LLM
	if llm == nil && d.LLM != nil {
		llm = d.LLM
	}
	log := d.Log
	if log == nil {
		log = slog.Default()
	}
	return d.Router.Register(New(d.HTTP, m.BaseURL, llm, log.With("feature", "translate")).Commands()...)
}
