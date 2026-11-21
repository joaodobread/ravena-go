package customcmd

import (
	"bytes"
	_ "embed"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"go.mau.fi/whatsmeow/types"

	"github.com/joaodobread/ravena-go/internal/web"
)

//go:embed panel.html
var panelHTML string

var panelTmpl = template.Must(template.New("panel").Parse(panelHTML))

// Renderer é o que a página usa do painel (web.Server).
type Renderer interface {
	Mount(p web.Page)
	RenderPage(w http.ResponseWriter, r *http.Request, title string, body template.HTML)
}

func (m *Module) mountPanel(srv Renderer) {
	mux := http.NewServeMux()
	page := func(w http.ResponseWriter, r *http.Request, title, block string, data any) {
		var buf bytes.Buffer
		if err := panelTmpl.ExecuteTemplate(&buf, block, data); err != nil {
			m.log.Error("template", "err", err)
			http.Error(w, "erro interno", http.StatusInternalServerError)
			return
		}
		srv.RenderPage(w, r, title, template.HTML(buf.String()))
	}

	mux.HandleFunc("GET /custom", func(w http.ResponseWriter, r *http.Request) {
		chats, err := m.chats(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for i := range chats {
			chats[i].Name = m.chatName(r, chats[i].Chat)
		}
		page(w, r, "Comandos personalizados", "index", map[string]any{"Chats": chats})
	})
	mux.HandleFunc("GET /custom/{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/custom", http.StatusSeeOther)
	})
	mux.HandleFunc("GET /custom/{chat}", func(w http.ResponseWriter, r *http.Request) {
		chat, ok := chatParam(w, r)
		if !ok {
			return
		}
		cmds, err := m.list(r.Context(), chat)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		page(w, r, "Comandos personalizados", "chat", map[string]any{
			"Chat": chat, "Name": m.chatName(r, chat), "Cmds": cmds, "Flash": r.URL.Query().Get("ok"),
		})
	})
	mux.HandleFunc("GET /custom/{chat}/media/{file}", func(w http.ResponseWriter, r *http.Request) {
		chat, ok := chatParam(w, r)
		if !ok {
			return
		}
		file := r.PathValue("file")
		if !validFile(file) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; media-src 'self'")
		http.ServeFile(w, r, filepath.Join(m.chatDir(chat), file))
	})

	back := func(w http.ResponseWriter, r *http.Request, chat, flash string) {
		http.Redirect(w, r, "/custom/"+url.PathEscape(chat)+"?ok="+url.QueryEscape(flash), http.StatusSeeOther)
	}
	fail := func(w http.ResponseWriter, err error) {
		if errors.Is(err, errNotFound) {
			http.Error(w, "comando não encontrado", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
	}

	mux.HandleFunc("POST /custom/{chat}/toggle", func(w http.ResponseWriter, r *http.Request) {
		chat, ok := chatParam(w, r)
		if !ok {
			return
		}
		trigger := r.FormValue("trigger")
		if _, err := m.update(r.Context(), chat, trigger, func(c *Command) error {
			c.Active = !c.Active
			return nil
		}); err != nil {
			fail(w, err)
			return
		}
		back(w, r, chat, "Comando "+trigger+" atualizado.")
	})
	mux.HandleFunc("POST /custom/{chat}/delete", func(w http.ResponseWriter, r *http.Request) {
		chat, ok := chatParam(w, r)
		if !ok {
			return
		}
		trigger := r.FormValue("trigger")
		m.writeMu.Lock()
		cmd, err := m.get(r.Context(), chat, trigger)
		if err == nil && cmd == nil {
			err = errNotFound
		}
		if err == nil {
			err = m.remove(r.Context(), chat, trigger)
		}
		m.writeMu.Unlock()
		if err != nil {
			fail(w, err)
			return
		}
		m.removeFiles(chat, cmd.Responses)
		back(w, r, chat, "Comando "+trigger+" excluído.")
	})
	mux.HandleFunc("POST /custom/{chat}/response", func(w http.ResponseWriter, r *http.Request) {
		chat, ok := chatParam(w, r)
		if !ok {
			return
		}
		trigger := r.FormValue("trigger")
		idx, err := strconv.Atoi(r.FormValue("index"))
		if err != nil {
			http.Error(w, "índice inválido", http.StatusBadRequest)
			return
		}
		del := r.FormValue("action") == "delete"
		text := strings.ReplaceAll(r.FormValue("text"), "\r\n", "\n")
		var removed []Response
		_, err = m.update(r.Context(), chat, trigger, func(c *Command) error {
			if idx < 0 || idx >= len(c.Responses) {
				return errors.New("resposta inexistente")
			}
			if del {
				removed = append(removed, c.Responses[idx])
				c.Responses = append(c.Responses[:idx], c.Responses[idx+1:]...)
				return nil
			}
			if !c.Responses[idx].IsMedia() && strings.TrimSpace(text) == "" {
				return errors.New("texto vazio; use Apagar resposta")
			}
			c.Responses[idx].Text = text
			return nil
		})
		if err != nil {
			fail(w, err)
			return
		}
		m.removeFiles(chat, removed)
		back(w, r, chat, "Resposta salva.")
	})

	srv.Mount(web.Page{Path: "/custom", Label: "Personalizados", Handler: mux})
}

// chatParam valida o JID de grupo do caminho.
func chatParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	jid, err := types.ParseJID(r.PathValue("chat"))
	if err != nil || jid.Server != types.GroupServer || jid.User == "" {
		http.NotFound(w, r)
		return "", false
	}
	return jid.String(), true
}

func (m *Module) chatName(r *http.Request, chat string) string {
	if g, ok, err := m.store.Group(r.Context(), chat); err == nil && ok && g.Name != "" {
		return g.Name
	}
	if jid, err := types.ParseJID(chat); err == nil {
		if n, err := m.client.ChatName(r.Context(), jid); err == nil {
			return n
		}
	}
	return ""
}
