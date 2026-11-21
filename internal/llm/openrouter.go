// Package llm fala com LLMs via API compatível com OpenAI (OpenRouter).
package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // DecodeConfig do quadro enviado pelo Converter
	"io"
	"net/http"
	"strings"
	"time"
)

// Client é um cliente mínimo de /chat/completions.
type Client struct {
	BaseURL string // padrão https://openrouter.ai/api/v1
	APIKey  string
	Model   string
	HTTP    *http.Client
}

// New cria um Client com timeout razoável.
func New(apiKey, model string) *Client {
	return &Client{
		BaseURL: "https://openrouter.ai/api/v1",
		APIKey:  apiKey,
		Model:   model,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

type contentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

type imageURL struct {
	URL string `json:"url"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type chatRequest struct {
	Model          string        `json:"model"`
	Messages       []chatMessage `json:"messages"`
	Temperature    float64       `json:"temperature"`
	ResponseFormat any           `json:"response_format,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

var focusSchema = map[string]any{
	"type": "json_schema",
	"json_schema": map[string]any{
		"name": "crop_analysis",
		"schema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"percentage": map[string]any{"type": "number", "description": "Centro ideal (0-100) para um corte quadrado"},
				"reason":     map[string]any{"type": "string", "description": "Breve motivo da escolha"},
			},
			"required": []string{"percentage"},
		},
	},
}

// Focus pergunta ao modelo onde está a área de interesse de um JPEG e devolve
// o centro (0–100) no eixo maior da imagem.
func (c *Client) Focus(ctx context.Context, jpeg []byte) (float64, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(jpeg))
	if err != nil {
		return 0, fmt.Errorf("llm: quadro inválido: %w", err)
	}
	orientation, axis, start, end := "retrato (mais alta que larga)", "vertical (eixo Y)", "o topo", "a base"
	if cfg.Width > cfg.Height {
		orientation, axis, start, end = "paisagem (mais larga que alta)", "horizontal (eixo X)", "a esquerda", "a direita"
	}
	prompt := fmt.Sprintf(`Analise esta imagem e identifique a área de interesse (a parte mais relevante, como um rosto ou objeto principal).
A imagem está no formato %s.
Retorne o centro %s desta área como uma porcentagem (0 a 100), onde 0 é %s e 100 é %s.
Responda APENAS com JSON no formato {"percentage": número}.`, orientation, axis, start, end)

	content, err := c.complete(ctx, chatRequest{
		Model:       c.Model,
		Temperature: 0.2,
		Messages: []chatMessage{
			{Role: "system", Content: "Você é um especialista em processamento e análise de imagens."},
			{Role: "user", Content: []contentPart{
				{Type: "text", Text: prompt},
				{Type: "image_url", ImageURL: &imageURL{URL: "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpeg)}},
			}},
		},
		ResponseFormat: focusSchema,
	})
	if err != nil {
		return 0, err
	}

	var parsed struct {
		Percentage *float64 `json:"percentage"`
	}
	if err := json.Unmarshal([]byte(stripFences(content)), &parsed); err != nil || parsed.Percentage == nil {
		return 0, fmt.Errorf("llm: resposta inesperada: %q", content)
	}
	return min(max(*parsed.Percentage, 0), 100), nil
}

func (c *Client) complete(ctx context.Context, req chatRequest) (string, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("llm: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("llm: lendo resposta: %w", err)
	}

	var out chatResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("llm: HTTP %d: %.200s", resp.StatusCode, raw)
	}
	if out.Error != nil {
		return "", fmt.Errorf("llm: HTTP %d: %s", resp.StatusCode, out.Error.Message)
	}
	if resp.StatusCode != http.StatusOK || len(out.Choices) == 0 {
		return "", fmt.Errorf("llm: HTTP %d sem resposta", resp.StatusCode)
	}
	if out.Choices[0].Message.Content == "" {
		return "", errors.New("llm: resposta vazia")
	}
	return out.Choices[0].Message.Content, nil
}

// stripFences remove ```json ... ``` que alguns modelos insistem em mandar.
func stripFences(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimPrefix(s, "json")
	return strings.TrimSpace(strings.TrimSuffix(s, "```"))
}
