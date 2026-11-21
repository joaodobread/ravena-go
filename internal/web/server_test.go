package web

import (
	"context"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/bot/bottest"
	"github.com/joaodobread/ravena-go/internal/policy"
	"github.com/joaodobread/ravena-go/internal/store"
	"github.com/joaodobread/ravena-go/internal/wa"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type fakeSession struct{ st wa.SessionStatus }

func (f fakeSession) Status() wa.SessionStatus { return f.st }

type fakeGroups []*bot.GroupInfo

func (f fakeGroups) JoinedGroups(context.Context) ([]*bot.GroupInfo, error) { return f, nil }

type env struct {
	srv  *Server
	ts   *httptest.Server
	st   *store.Store
	pol  *policy.Policy
	http *http.Client
}

func setup(t *testing.T, sess wa.SessionStatus) env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	fc := &bottest.FakeClient{}
	pol, err := policy.New(ctx, st, fc, nil, policy.DefaultOptions, quiet)
	if err != nil {
		t.Fatal(err)
	}
	r := bot.NewRouter("!", fc, quiet)
	noop := func(context.Context, *bot.Context) error { return nil }
	if err := r.Register(
		&bot.Command{Name: "s", Aliases: []string{"fig"}, Category: "midia", Description: "Figurinha", Run: noop},
		&bot.Command{Name: "dado", Category: "jogos", Run: noop},
	); err != nil {
		t.Fatal(err)
	}
	srv, err := New(ctx, Deps{
		Store: st, Policy: pol, Router: r, Session: fakeSession{sess}, Password: "segredo", Log: quiet, Started: time.Now(),
		Groups: fakeGroups{{JID: bottest.Group, Name: "Família", Participants: make([]bot.Participant, 3)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	return env{srv, ts, st, pol, &http.Client{Jar: jar}}
}

func (e env) get(t *testing.T, path string) (int, string) {
	t.Helper()
	resp, err := e.http.Get(e.ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (e env) post(t *testing.T, path string, form url.Values) (int, string) {
	t.Helper()
	resp, err := e.http.PostForm(e.ts.URL+path, form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (e env) login(t *testing.T) {
	t.Helper()
	if _, body := e.post(t, "/login", url.Values{"password": {"segredo"}}); !strings.Contains(body, "Início") {
		t.Fatalf("login falhou: %.300s", body)
	}
}

func TestRequiresLogin(t *testing.T) {
	e := setup(t, wa.SessionStatus{Connected: true, LoggedIn: true})
	for _, p := range []string{"/", "/groups", "/commands", "/settings"} {
		if _, body := e.get(t, p); !strings.Contains(body, "Senha do painel") {
			t.Errorf("%s deveria pedir login", p)
		}
	}
	if code, _ := e.post(t, "/settings", url.Values{"bot_name": {"hacker"}}); code != http.StatusOK {
		t.Fatalf("POST sem login: %d", code) // redireciona para o login (200 após seguir)
	}
	if e.pol.Settings().BotName == "hacker" {
		t.Fatal("POST sem login alterou configurações")
	}
	if _, body := e.post(t, "/login", url.Values{"password": {"errada"}}); !strings.Contains(body, "Senha incorreta") {
		t.Fatal("senha errada deveria avisar")
	}
}

func TestDashboardShowsPairingQR(t *testing.T) {
	e := setup(t, wa.SessionStatus{Connected: true, QR: "2@abc,def,ghi", PairCode: "ABCD-EFGH"}) // socket conectado, sem login
	e.login(t)
	_, body := e.get(t, "/")
	for _, want := range []string{"data:image/png;base64,", "ABCD-EFGH", "Memória"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard sem %q", want)
		}
	}
	if code, body := e.get(t, "/healthz"); code != 200 || body != "aguardando pareamento" {
		t.Fatalf("healthz = %d %q", code, body)
	}
}

func TestHealthzWhenDisconnected(t *testing.T) {
	e := setup(t, wa.SessionStatus{LoggedIn: true})
	if code, _ := e.get(t, "/healthz"); code != http.StatusServiceUnavailable {
		t.Fatalf("healthz = %d", code)
	}
}

func TestGroupSettingsRoundTrip(t *testing.T) {
	e := setup(t, wa.SessionStatus{Connected: true, LoggedIn: true})
	e.login(t)
	_, list := e.get(t, "/groups")
	if !strings.Contains(list, "Família") || !strings.Contains(list, bottest.Group.String()) {
		t.Fatalf("lista de grupos sem o grupo do WhatsApp: %.500s", list)
	}

	jid := bottest.Group.String()
	_, body := e.post(t, "/groups/"+jid, url.Values{
		"name": {"Família"}, "prefix": {"/"}, "paused": {"on"},
		"interact_enabled": {"on"}, "interact_chance": {"9999"}, "interact_cooldown": {"1"}, "interact_proporcao": {"70"},
		"muted": {"dado"}, "muted_cat": {"midia"},
		"admins": {"+55 (11) 98888-8888\n5511777777777"}, "ignored": {"5511666666666, 5511666666666"},
		"greeting": {"  Bem-vindo {pessoa}!\n"},
	})
	if !strings.Contains(body, "Configurações salvas") {
		t.Fatalf("salvar grupo: %.300s", body)
	}
	g, ok, err := e.st.Group(context.Background(), jid)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if g.Prefix != "/" || !g.Paused || g.Silly || g.Interact.Chance != store.MaxInteractChance ||
		g.Interact.CooldownMin != store.MinInteractCooldownMin || g.Interact.Proporcao != 70 ||
		!g.IsMuted("dado", "") || !g.IsMuted("s", "midia") ||
		len(g.AdditionalAdmins) != 2 || g.AdditionalAdmins[0] != "5511988888888" || len(g.IgnoredUsers) != 1 ||
		g.Greeting != "Bem-vindo {pessoa}!" || g.Farewell != "" {
		t.Fatalf("grupo salvo errado: %+v", g)
	}
	_, page := e.get(t, "/groups/"+jid)
	if !strings.Contains(page, `value="/"`) || !strings.Contains(page, "5511988888888") {
		t.Fatal("formulário não reflete o que foi salvo")
	}
}

func TestToggleCommandReturnsRow(t *testing.T) {
	e := setup(t, wa.SessionStatus{Connected: true, LoggedIn: true})
	e.login(t)
	_, body := e.post(t, "/commands/fig/toggle", nil) // alias resolve para o nome
	if !strings.Contains(body, "Ativar") || strings.Contains(body, "<html") {
		t.Fatalf("deveria devolver só a linha, já desativada: %.300s", body)
	}
	if got := e.pol.Settings().DisabledCommands; len(got) != 1 || got[0] != "s" {
		t.Fatalf("DisabledCommands = %v", got)
	}
	e.post(t, "/commands/s/toggle", nil)
	if got := e.pol.Settings().DisabledCommands; len(got) != 0 {
		t.Fatalf("deveria reativar: %v", got)
	}
	if code, _ := e.post(t, "/commands/nada/toggle", nil); code != http.StatusNotFound {
		t.Fatalf("comando inexistente: %d", code)
	}
}

func TestSettingsAndCrossOriginPost(t *testing.T) {
	e := setup(t, wa.SessionStatus{Connected: true, LoggedIn: true})
	e.login(t)
	e.post(t, "/settings", url.Values{"bot_name": {"Corvo"}, "super_admins": {"556296975194"}, "ignore_private": {"on"}})
	s := e.pol.Settings()
	if s.BotName != "Corvo" || len(s.SuperAdmins) != 1 || !s.IgnorePrivate {
		t.Fatalf("settings = %+v", s)
	}

	req, _ := http.NewRequest(http.MethodPost, e.ts.URL+"/settings", strings.NewReader("bot_name=evil"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	resp, err := e.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || e.pol.Settings().BotName != "Corvo" {
		t.Fatalf("POST de outra origem deveria ser recusado: %d", resp.StatusCode)
	}
}

func TestMountedPageUsesLayoutAndAuth(t *testing.T) {
	e := setup(t, wa.SessionStatus{Connected: true, LoggedIn: true})
	e.srv.Mount(Page{Path: "/extra", Label: "Extra", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.srv.RenderPage(w, r, "Extra", template.HTML("<p>conteúdo extra</p>"))
	})})
	if _, body := e.get(t, "/extra"); strings.Contains(body, "conteúdo extra") {
		t.Fatal("página montada deveria exigir login")
	}
	e.login(t)
	_, body := e.get(t, "/extra/sub")
	if !strings.Contains(body, "conteúdo extra") || !strings.Contains(body, `href="/extra"`) {
		t.Fatalf("página montada sem layout/menu: %.300s", body)
	}
}
