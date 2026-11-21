package customcmd

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/joaodobread/ravena-go/internal/bot/bottest"
	"github.com/joaodobread/ravena-go/internal/policy"
	"github.com/joaodobread/ravena-go/internal/wa"
	"github.com/joaodobread/ravena-go/internal/web"
)

type fakeSession struct{}

func (fakeSession) Status() wa.SessionStatus {
	return wa.SessionStatus{Connected: true, LoggedIn: true}
}

func TestPanel(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	pol, err := policy.New(ctx, e.st, e.fc, nil, policy.DefaultOptions, quiet)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := web.New(ctx, web.Deps{Store: e.st, Policy: pol, Router: e.router, Session: fakeSession{}, Password: "segredo", Log: quiet, Started: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	e.m.mountPanel(srv)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar}
	do := func(method, path string, form url.Values) (int, string) {
		t.Helper()
		var resp *http.Response
		var err error
		if method == http.MethodPost {
			resp, err = hc.PostForm(ts.URL+path, form)
		} else {
			resp, err = hc.Get(ts.URL + path)
		}
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	e.admin("!g-addCmd oi olá <b>")
	e.admin("!g-addCmdReply oi segunda")

	if _, body := do("GET", "/custom", nil); strings.Contains(body, "Comandos personalizados") {
		t.Fatal("deveria exigir login")
	}
	if _, body := do("POST", "/login", url.Values{"password": {"segredo"}}); !strings.Contains(body, "Início") {
		t.Fatal("login falhou")
	}

	_, body := do("GET", "/custom", nil)
	chatPath := "/custom/" + bottest.Group.String()
	if !strings.Contains(body, chatPath) || !strings.Contains(body, "<td>1</td>") {
		t.Fatalf("lista de grupos: %.500s", body)
	}
	_, body = do("GET", chatPath, nil)
	if !strings.Contains(body, "olá &lt;b&gt;") || !strings.Contains(body, "segunda") || !strings.Contains(body, "ativo") {
		t.Fatalf("página do grupo: %.800s", body)
	}
	if code, _ := do("GET", "/custom/123@s.whatsapp.net", nil); code != http.StatusNotFound {
		t.Fatalf("chat que não é grupo deveria dar 404: %d", code)
	}

	// Editar e apagar resposta.
	if code, _ := do("POST", chatPath+"/response", url.Values{"trigger": {"oi"}, "index": {"0"}, "text": {"editado {pessoa}"}, "action": {"save"}}); code != 200 {
		t.Fatalf("editar: %d", code)
	}
	if code, _ := do("POST", chatPath+"/response", url.Values{"trigger": {"oi"}, "index": {"1"}, "action": {"delete"}}); code != 200 {
		t.Fatalf("apagar resposta: %d", code)
	}
	e.reset()
	e.user("!oi")
	if got := e.fc.Texts(); len(got) != 1 || got[0] != "editado Fulano" {
		t.Fatalf("após edição: %q", got)
	}

	// Desativar.
	do("POST", chatPath+"/toggle", url.Values{"trigger": {"oi"}})
	e.reset()
	e.user("!oi")
	if len(e.fc.Replies) != 0 {
		t.Fatal("comando desativado pelo painel ainda responde")
	}
	if code, _ := do("POST", chatPath+"/toggle", url.Values{"trigger": {"nada"}}); code != http.StatusNotFound {
		t.Fatalf("toggle inexistente: %d", code)
	}

	// POST de outra origem é recusado pelo painel.
	req, _ := http.NewRequest(http.MethodPost, ts.URL+chatPath+"/delete", strings.NewReader("trigger=oi"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://malvado.example")
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("origem externa: %d", resp.StatusCode)
	}

	do("POST", chatPath+"/delete", url.Values{"trigger": {"oi"}})
	if got := e.m.MenuEntries(ctx, bottest.Group); len(got) != 0 {
		t.Fatalf("comando não foi excluído: %q", got)
	}
}
