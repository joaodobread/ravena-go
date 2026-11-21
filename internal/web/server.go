// Package web é o painel administrativo (Go templates + htmx), embutido no binário.
package web

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/policy"
	"github.com/joaodobread/ravena-go/internal/store"
	"github.com/joaodobread/ravena-go/internal/wa"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// SessionStatus é o que o painel precisa saber da conexão.
type SessionStatus interface {
	Status() wa.SessionStatus
}

// GroupLister lista os grupos em que o bot está (implementado por wa.Client).
type GroupLister interface {
	JoinedGroups(ctx context.Context) ([]*bot.GroupInfo, error)
}

// Deps são as dependências do painel.
type Deps struct {
	Store    *store.Store
	Policy   *policy.Policy
	Router   *bot.Router
	Session  SessionStatus
	Groups   GroupLister // opcional
	Password string
	Log      *slog.Logger
	Started  time.Time
}

// Page é uma página extra montada por uma funcionalidade (ex.: comandos personalizados).
type Page struct {
	Path    string // ex.: "/custom"; recebe também os subcaminhos
	Label   string // texto no menu
	Handler http.Handler
}

// Server é o painel.
type Server struct {
	d      Deps
	mux    *http.ServeMux
	pages  map[string]*template.Template
	secret []byte

	mu    sync.Mutex
	extra []Page
	fails map[string]int // tentativas de login por IP
}

const cookieName = "ravena_session"

// New cria o painel. O segredo das sessões fica no banco para sobreviver a reinícios.
func New(ctx context.Context, d Deps) (*Server, error) {
	secret, err := loadSecret(ctx, d.Store)
	if err != nil {
		return nil, err
	}
	pages, err := parseTemplates()
	if err != nil {
		return nil, err
	}
	s := &Server{d: d, mux: http.NewServeMux(), pages: pages, secret: secret, fails: map[string]int{}}
	s.routes()
	return s, nil
}

// Mount adiciona uma página de funcionalidade, protegida pelo login do painel.
func (s *Server) Mount(p Page) {
	s.mu.Lock()
	s.extra = append(s.extra, p)
	s.mu.Unlock()
	h := s.auth(p.Handler)
	s.mux.Handle(p.Path, h)
	s.mux.Handle(strings.TrimSuffix(p.Path, "/")+"/", h)
}

// Handler é o http.Handler do painel (inclui /healthz).
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	static, _ := fs.Sub(staticFS, "static")
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", cacheForever(http.FileServerFS(static))))
	s.mux.HandleFunc("GET /healthz", s.healthz)
	s.mux.HandleFunc("GET /login", s.loginPage)
	s.mux.HandleFunc("POST /login", s.login)
	s.mux.HandleFunc("POST /logout", s.logout)

	s.mux.Handle("GET /{$}", s.auth(http.HandlerFunc(s.dashboard)))
	s.mux.Handle("GET /status", s.auth(http.HandlerFunc(s.statusPartial)))
	s.mux.Handle("GET /groups", s.auth(http.HandlerFunc(s.groups)))
	s.mux.Handle("GET /groups/{jid}", s.auth(http.HandlerFunc(s.group)))
	s.mux.Handle("POST /groups/{jid}", s.auth(http.HandlerFunc(s.saveGroup)))
	s.mux.Handle("GET /commands", s.auth(http.HandlerFunc(s.commands)))
	s.mux.Handle("POST /commands/{name}/toggle", s.auth(http.HandlerFunc(s.toggleCommand)))
	s.mux.Handle("GET /settings", s.auth(http.HandlerFunc(s.settings)))
	s.mux.Handle("POST /settings", s.auth(http.HandlerFunc(s.saveSettings)))
}

func cacheForever(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=604800")
		h.ServeHTTP(w, r)
	})
}

// healthz é usado pelo healthcheck do deploy: 200 enquanto o processo estiver
// vivo e (se já pareado) conectado.
func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	st := s.d.Session.Status()
	switch {
	case !st.LoggedIn:
		_, _ = w.Write([]byte("aguardando pareamento"))
	case !st.Connected:
		http.Error(w, "desconectado", http.StatusServiceUnavailable)
	default:
		_, _ = w.Write([]byte("ok"))
	}
}

// ---- autenticação ----

func loadSecret(ctx context.Context, st *store.Store) ([]byte, error) {
	var hexSecret string
	ok, err := st.GetJSON(ctx, "web_secret", &hexSecret)
	if err != nil {
		return nil, err
	}
	if ok {
		return hex.DecodeString(hexSecret)
	}
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return b, st.SetJSON(ctx, "web_secret", hex.EncodeToString(b))
}

func (s *Server) sign(exp int64) string {
	m := hmac.New(sha256.New, s.secret)
	m.Write([]byte(strconv.FormatInt(exp, 10)))
	return strconv.FormatInt(exp, 10) + "." + hex.EncodeToString(m.Sum(nil))
}

func (s *Server) validSession(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	expStr, _, ok := strings.Cut(c.Value, ".")
	if !ok {
		return false
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	return hmac.Equal([]byte(c.Value), []byte(s.sign(exp)))
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.validSession(r) {
			if r.Header.Get("HX-Request") == "true" {
				w.Header().Set("HX-Redirect", "/login")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		// Formulários POST só valem vindos do próprio painel.
		if r.Method == http.MethodPost && !sameOrigin(r) {
			http.Error(w, "origem inválida", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = r.Header.Get("Referer")
	}
	if origin == "" {
		return true // clientes sem Origin (curl); o cookie SameSite=Strict já protege
	}
	host := strings.TrimPrefix(strings.TrimPrefix(origin, "https://"), "http://")
	host, _, _ = strings.Cut(host, "/")
	return host == r.Host
}

func clientIP(r *http.Request) string {
	if ip := r.Header.Get("X-Forwarded-For"); ip != "" {
		ip, _, _ = strings.Cut(ip, ",")
		return strings.TrimSpace(ip)
	}
	ip, _, _ := strings.Cut(r.RemoteAddr, ":")
	return ip
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	if s.validSession(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.render(w, r, "login", map[string]any{"Error": r.URL.Query().Get("erro") != ""})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	s.mu.Lock()
	fails := s.fails[ip]
	s.mu.Unlock()
	if fails >= 10 {
		http.Error(w, "muitas tentativas; reinicie o bot ou aguarde", http.StatusTooManyRequests)
		return
	}
	pass := r.FormValue("password")
	if s.d.Password == "" || subtle.ConstantTimeCompare([]byte(pass), []byte(s.d.Password)) != 1 {
		s.mu.Lock()
		s.fails[ip]++
		s.mu.Unlock()
		time.Sleep(time.Second) // encarece força bruta
		http.Redirect(w, r, "/login?erro=1", http.StatusSeeOther)
		return
	}
	s.mu.Lock()
	delete(s.fails, ip)
	s.mu.Unlock()
	exp := time.Now().Add(30 * 24 * time.Hour)
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: s.sign(exp.Unix()), Path: "/", Expires: exp,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// ---- templates ----

var funcs = template.FuncMap{
	"join": strings.Join,
	"lines": func(xs []string) string {
		return strings.Join(xs, "\n")
	},
	"dict2": func(row any, prefix string) map[string]any { return map[string]any{"Row": row, "Prefix": prefix} },
	"pct":   func(chance int) string { return strconv.FormatFloat(float64(chance)/100, 'f', 2, 64) + "%" },
	"contains": func(xs []string, x string) bool {
		for _, v := range xs {
			if v == x {
				return true
			}
		}
		return false
	},
}

func parseTemplates() (map[string]*template.Template, error) {
	layout, err := template.New("layout.html").Funcs(funcs).ParseFS(templateFS, "templates/layout.html")
	if err != nil {
		return nil, err
	}
	pages := map[string]*template.Template{}
	entries, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	for _, path := range entries {
		name := strings.TrimSuffix(strings.TrimPrefix(path, "templates/"), ".html")
		if name == "layout" {
			continue
		}
		t, err := template.Must(layout.Clone()).ParseFS(templateFS, path)
		if err != nil {
			return nil, err
		}
		pages[name] = t
	}
	return pages, nil
}

type navItem struct{ Path, Label string }

// render desenha a página name dentro do layout; com fragment, só o bloco.
func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, data map[string]any) {
	s.renderBlock(w, r, name, "layout.html", data)
}

func (s *Server) renderBlock(w http.ResponseWriter, r *http.Request, page, block string, data map[string]any) {
	t, ok := s.pages[page]
	if !ok {
		http.Error(w, "página inexistente", http.StatusInternalServerError)
		return
	}
	if data == nil {
		data = map[string]any{}
	}
	s.mu.Lock()
	nav := []navItem{{"/", "Início"}, {"/groups", "Grupos"}, {"/commands", "Comandos"}}
	for _, p := range s.extra {
		nav = append(nav, navItem{p.Path, p.Label})
	}
	nav = append(nav, navItem{"/settings", "Configurações"})
	s.mu.Unlock()
	data["Nav"] = nav
	data["Path"] = r.URL.Path
	data["Authed"] = s.validSession(r)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, block, data); err != nil {
		s.d.Log.Error("template", "page", page, "err", err)
	}
}

// RenderPage permite que páginas montadas por funcionalidades usem o layout:
// o conteúdo HTML já vem pronto em body.
func (s *Server) RenderPage(w http.ResponseWriter, r *http.Request, title string, body template.HTML) {
	s.render(w, r, "custom", map[string]any{"Title": title, "Body": body})
}
