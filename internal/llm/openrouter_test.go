package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewGray(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// server responde com content e guarda o corpo da requisição em *got.
func server(t *testing.T, status int, content string, got *chatRequest) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("requisição inesperada: %s %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		if got != nil {
			_ = json.NewDecoder(r.Body).Decode(got)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(content))
	}))
	t.Cleanup(srv.Close)
	c := New("k", "modelo")
	c.BaseURL = srv.URL
	return c
}

func completion(content string) string {
	b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}}}})
	return string(b)
}

func TestFocusParsesAndClamps(t *testing.T) {
	tests := []struct {
		content string
		want    float64
	}{
		{`{"percentage": 30}`, 30},
		{"```json\n{\"percentage\": 72.5, \"reason\": \"rosto\"}\n```", 72.5},
		{`{"percentage": 140}`, 100},
		{`{"percentage": -3}`, 0},
	}
	for _, tt := range tests {
		got, err := server(t, 200, completion(tt.content), nil).Focus(context.Background(), testJPEG(t, 64, 32))
		if err != nil || got != tt.want {
			t.Errorf("content %q: got %v, %v; want %v", tt.content, got, err, tt.want)
		}
	}
}

func TestFocusPromptMatchesOrientation(t *testing.T) {
	var req chatRequest
	if _, err := server(t, 200, completion(`{"percentage":50}`), &req).Focus(context.Background(), testJPEG(t, 32, 64)); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(req.Messages)
	if s := string(b); !strings.Contains(s, "retrato") || !strings.Contains(s, "data:image/jpeg;base64,") {
		t.Fatalf("prompt sem orientação ou imagem: %s", s)
	}
	if req.Model != "modelo" || req.ResponseFormat == nil {
		t.Fatalf("requisição incompleta: %+v", req)
	}
}

func TestFocusErrors(t *testing.T) {
	img := testJPEG(t, 10, 10)
	cases := map[string]*Client{
		"http 500":       server(t, 500, `{"error":{"message":"caiu"}}`, nil),
		"sem percentage": server(t, 200, completion(`{"reason":"x"}`), nil),
		"não-json":       server(t, 200, completion("não sei"), nil),
		"corpo inválido": server(t, 502, "<html>bad gateway</html>", nil),
	}
	for name, c := range cases {
		if _, err := c.Focus(context.Background(), img); err == nil {
			t.Errorf("%s: esperava erro", name)
		}
	}
	if _, err := New("k", "m").Focus(context.Background(), []byte("não é jpeg")); err == nil {
		t.Error("imagem inválida: esperava erro")
	}
}

func TestChatBuildsMultimodalRequest(t *testing.T) {
	var req chatRequest
	c := server(t, 200, completion("olá!"), &req)
	got, err := c.Chat(context.Background(), ChatRequest{
		System:    "seja breve",
		Messages:  []Message{{Role: "user", Text: "oi"}, {Role: "assistant", Text: "e aí"}, {Role: "user", Text: "e isso?", Images: [][]byte{testJPEG(t, 4, 4)}}},
		MaxTokens: 50,
	})
	if err != nil || got != "olá!" {
		t.Fatalf("Chat = %q, %v", got, err)
	}
	if len(req.Messages) != 4 || req.Messages[0].Role != "system" || req.MaxTokens != 50 {
		t.Fatalf("requisição inesperada: %+v", req)
	}
	b, _ := json.Marshal(req.Messages[3])
	if !strings.Contains(string(b), "data:image/jpeg;base64,") {
		t.Fatalf("imagem ausente: %s", b)
	}
}
