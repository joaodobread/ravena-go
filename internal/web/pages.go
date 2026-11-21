package web

import (
	"encoding/base64"
	"html/template"
	"net/http"
	"os"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"rsc.io/qr"

	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/store"
)

// ---- início ----

type statusView struct {
	Connected, LoggedIn bool
	JID, PushName       string
	QRImage             template.URL // data URI gerado aqui (seguro)
	PairCode            string
	LastError           string
}

func (s *Server) status() statusView {
	st := s.d.Session.Status()
	v := statusView{
		Connected: st.Connected, LoggedIn: st.LoggedIn, JID: st.JID, PushName: st.PushName,
		PairCode: st.PairCode, LastError: st.LastError,
	}
	if st.QR != "" {
		if code, err := qr.Encode(st.QR, qr.L); err == nil {
			code.Scale = 6
			v.QRImage = template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(code.PNG()))
		}
	}
	return v
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	day, _ := s.d.Store.TopCommands(ctx, time.Now().Add(-24*time.Hour), "", 10)
	week, _ := s.d.Store.TopCommands(ctx, time.Now().AddDate(0, 0, -7), "", 10)
	total := func(cs []store.CommandCount) (n int) {
		for _, c := range cs {
			n += c.Count
		}
		return n
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	s.render(w, r, "dashboard", map[string]any{
		"Title":      "Início",
		"Status":     s.status(),
		"Uptime":     time.Since(s.d.Started).Round(time.Minute).String(),
		"RSS":        mb(rss()),
		"Heap":       mb(ms.HeapAlloc),
		"Goroutines": runtime.NumGoroutine(),
		"Day":        day,
		"Week":       week,
		"DayTotal":   total(day),
		"Commands":   len(s.d.Router.Commands()),
	})
}

func (s *Server) statusPartial(w http.ResponseWriter, r *http.Request) {
	s.renderBlock(w, r, "dashboard", "status", map[string]any{"Status": s.status()})
}

// rss lê a memória residente do processo (Linux); 0 se indisponível.
func rss() uint64 {
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) < 2 {
		return 0
	}
	pages, _ := strconv.ParseUint(f[1], 10, 64)
	return pages * uint64(os.Getpagesize())
}

func mb(b uint64) string { return strconv.FormatFloat(float64(b)/(1<<20), 'f', 1, 64) + " MiB" }

// ---- grupos ----

type groupRow struct {
	JID, Name string
	Members   int
	Saved     bool
	Paused    bool
	Prefix    string
}

func (s *Server) groups(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	saved, err := s.d.Store.Groups(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rows := map[string]*groupRow{}
	for _, g := range saved {
		rows[g.JID] = &groupRow{JID: g.JID, Name: g.Name, Saved: true, Paused: g.Paused, Prefix: g.Prefix}
	}
	var listErr string
	if s.d.Groups != nil {
		joined, err := s.d.Groups.JoinedGroups(ctx)
		if err != nil {
			listErr = err.Error()
		}
		for _, g := range joined {
			row, ok := rows[g.JID.String()]
			if !ok {
				row = &groupRow{JID: g.JID.String()}
				rows[row.JID] = row
			}
			row.Name, row.Members = g.Name, len(g.Participants)
		}
	}
	list := make([]*groupRow, 0, len(rows))
	for _, row := range rows {
		list = append(list, row)
	}
	sort.Slice(list, func(i, j int) bool { return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name) })
	s.render(w, r, "groups", map[string]any{"Title": "Grupos", "Groups": list, "Error": listErr, "DefaultPrefix": s.d.Router.DefaultPrefix()})
}

type categoryView struct {
	Name     string
	Commands []*bot.Command
}

func (s *Server) categories() []categoryView {
	byCat := map[string][]*bot.Command{}
	for _, c := range s.d.Router.Commands() {
		if c.SuperAdminOnly {
			continue
		}
		cat := c.Category
		if cat == "" {
			cat = "geral"
		}
		byCat[cat] = append(byCat[cat], c)
	}
	out := make([]categoryView, 0, len(byCat))
	for name, cmds := range byCat {
		out = append(out, categoryView{Name: name, Commands: cmds})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *Server) group(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	jid := r.PathValue("jid")
	g, err := s.d.Store.GroupOrDefault(ctx, jid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	usage, _ := s.d.Store.TopCommands(ctx, time.Now().AddDate(0, 0, -7), jid, 10)
	s.render(w, r, "group", map[string]any{
		"Title": "Grupo " + g.Name, "G": g, "Categories": s.categories(), "Usage": usage,
		"Saved": r.URL.Query().Get("salvo") != "", "DefaultPrefix": s.d.Router.DefaultPrefix(),
	})
}

func (s *Server) saveGroup(w http.ResponseWriter, r *http.Request) {
	jid := r.PathValue("jid")
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f := r.Form
	_, err := s.d.Store.UpdateGroup(r.Context(), jid, func(g *store.Group) {
		if name := strings.TrimSpace(f.Get("name")); name != "" {
			g.Name = name
		}
		g.Prefix = strings.TrimSpace(f.Get("prefix"))
		g.Paused = f.Get("paused") == "on"
		g.Silly = f.Get("silly") == "on"
		g.Interact.Enabled = f.Get("interact_enabled") == "on"
		g.Interact.UseCmds = f.Get("interact_usecmds") == "on"
		g.Interact.CooldownMin = clampInt(f.Get("interact_cooldown"), store.MinInteractCooldownMin, 24*60, g.Interact.CooldownMin)
		g.Interact.Chance = clampInt(f.Get("interact_chance"), 0, store.MaxInteractChance, g.Interact.Chance)
		g.Interact.Proporcao = clampInt(f.Get("interact_proporcao"), 0, 100, g.Interact.Proporcao)
		g.MutedCommands = f["muted"]
		g.MutedCategories = f["muted_cat"]
		g.IgnoredUsers = numbers(f.Get("ignored"))
		g.AdditionalAdmins = numbers(f.Get("admins"))
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/groups/"+jid+"?salvo=1", http.StatusSeeOther)
}

func clampInt(v string, lo, hi, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	return min(max(n, lo), hi)
}

// numbers lê um número por linha (ou separado por vírgula), só dígitos.
func numbers(text string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(text, func(r rune) bool { return r == '\n' || r == ',' || r == ';' }) {
		var b strings.Builder
		for _, r := range part {
			if r >= '0' && r <= '9' {
				b.WriteRune(r)
			}
		}
		if b.Len() > 0 && !slices.Contains(out, b.String()) {
			out = append(out, b.String())
		}
	}
	return out
}

// ---- comandos ----

type commandRow struct {
	Cmd      *bot.Command
	Disabled bool
	Uses     int
	Errors   int
}

func (s *Server) commandRows(r *http.Request) []commandRow {
	usage, _ := s.d.Store.TopCommands(r.Context(), time.Now().AddDate(0, 0, -7), "", 1000)
	counts := map[string]store.CommandCount{}
	for _, u := range usage {
		counts[u.Command] = u
	}
	disabled := s.d.Policy.Settings().DisabledCommands
	var rows []commandRow
	for _, c := range s.d.Router.Commands() {
		rows = append(rows, commandRow{Cmd: c, Disabled: slices.Contains(disabled, c.Name), Uses: counts[c.Name].Count, Errors: counts[c.Name].Errors})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Cmd.Category != rows[j].Cmd.Category {
			return rows[i].Cmd.Category < rows[j].Cmd.Category
		}
		return rows[i].Cmd.Name < rows[j].Cmd.Name
	})
	return rows
}

func (s *Server) commands(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "commands", map[string]any{"Title": "Comandos", "Rows": s.commandRows(r), "Prefix": s.d.Router.DefaultPrefix()})
}

func (s *Server) toggleCommand(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	cmd, ok := s.d.Router.Lookup(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	set := s.d.Policy.Settings()
	if i := slices.Index(set.DisabledCommands, cmd.Name); i >= 0 {
		set.DisabledCommands = slices.Delete(set.DisabledCommands, i, i+1)
	} else {
		set.DisabledCommands = append(set.DisabledCommands, cmd.Name)
	}
	if err := s.d.Policy.SaveSettings(r.Context(), set); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, row := range s.commandRows(r) {
		if row.Cmd == cmd {
			s.renderBlock(w, r, "commands", "row", map[string]any{"Row": row, "Prefix": s.d.Router.DefaultPrefix()})
			return
		}
	}
}

// ---- configurações ----

func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "settings", map[string]any{
		"Title": "Configurações", "S": s.d.Policy.Settings(), "Saved": r.URL.Query().Get("salvo") != "",
	})
}

func (s *Server) saveSettings(w http.ResponseWriter, r *http.Request) {
	set := s.d.Policy.Settings()
	if name := strings.TrimSpace(r.FormValue("bot_name")); name != "" {
		set.BotName = name
	}
	set.SuperAdmins = numbers(r.FormValue("super_admins"))
	set.IgnorePrivate = r.FormValue("ignore_private") == "on"
	if err := s.d.Policy.SaveSettings(r.Context(), set); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings?salvo=1", http.StatusSeeOther)
}
