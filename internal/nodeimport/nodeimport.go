// Package nodeimport traz os dados do Ravena em Node (pasta data/ com
// sqlites/core.db, sqlites/custom_commands.db e media/) para o banco do
// ravena-go: configurações dos grupos e comandos personalizados.
//
// É idempotente: rodar de novo sobrescreve o que foi importado antes.
package nodeimport

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/joaodobread/ravena-go/internal/features/customcmd"
	"github.com/joaodobread/ravena-go/internal/store"
)

// Report resume uma importação.
type Report struct {
	Groups, Commands, Media int
	Skipped                 []string // respostas não importadas, com o motivo
}

// Run importa de src (a pasta data/ do Node) para st, guardando mídias em dataDir.
func Run(ctx context.Context, src string, st *store.Store, dataDir string, log *slog.Logger) (*Report, error) {
	rep := &Report{}
	imp, err := customcmd.NewImporter(ctx, st, dataDir)
	if err != nil {
		return nil, err
	}
	sqlites := filepath.Join(src, "sqlites")
	if _, err := os.Stat(filepath.Join(sqlites, "core.db")); err == nil {
		if err := importGroups(ctx, filepath.Join(sqlites, "core.db"), st, imp, rep); err != nil {
			return rep, fmt.Errorf("grupos: %w", err)
		}
	} else {
		log.Warn("core.db não encontrado, pulando grupos", "dir", sqlites)
	}
	if _, err := os.Stat(filepath.Join(sqlites, "custom_commands.db")); err == nil {
		if err := importCommands(ctx, filepath.Join(sqlites, "custom_commands.db"), filepath.Join(src, "media"), imp, rep); err != nil {
			return rep, fmt.Errorf("comandos: %w", err)
		}
	} else {
		log.Warn("custom_commands.db não encontrado, pulando comandos", "dir", sqlites)
	}
	return rep, nil
}

func openRO(path string) (*sql.DB, error) {
	return sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
}

// ---- grupos ----

type oldInteract struct {
	Enabled   *bool `json:"enabled"`
	UseCmds   *bool `json:"useCmds"`
	Cooldown  *int  `json:"cooldown"`
	Chance    *int  `json:"chance"`
	Proporcao *int  `json:"proporcao"`
}

type oldMessage struct {
	Text string `json:"text"`
}

func importGroups(ctx context.Context, path string, st *store.Store, imp *customcmd.Importer, rep *Report) error {
	db, err := openRO(path)
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT id, COALESCE(name,''), COALESCE(prefix,''), COALESCE(paused,0),
		COALESCE(custom_ignores_prefix,0), additional_admins, greetings, farewells, interact,
		ignored_users, ignored_numbers, muted_commands, muted_categories FROM groups`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type row struct {
		id, name, prefix                              string
		paused, ignorePrefix                          int
		admins, greet, farewell, interact, ignU, ignN sql.NullString
		mutedCmds, mutedCats                          sql.NullString
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.name, &r.prefix, &r.paused, &r.ignorePrefix, &r.admins, &r.greet,
			&r.farewell, &r.interact, &r.ignU, &r.ignN, &r.mutedCmds, &r.mutedCats); err != nil {
			return err
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close() // libera antes de escrever no outro banco

	for _, r := range all {
		if !strings.HasSuffix(r.id, "@g.us") {
			continue
		}
		var greet, farewell oldMessage
		var inter oldInteract
		parseJSON(r.greet, &greet)
		parseJSON(r.farewell, &farewell)
		parseJSON(r.interact, &inter)

		_, err := st.UpdateGroup(ctx, r.id, func(g *store.Group) {
			if r.name != "" {
				g.Name = r.name
			}
			g.Prefix = r.prefix
			g.Paused = r.paused != 0
			g.AdditionalAdmins = numbers(r.admins)
			g.IgnoredUsers = dedupe(append(numbers(r.ignU), numbers(r.ignN)...))
			g.MutedCommands = lowerList(r.mutedCmds)
			g.MutedCategories = lowerList(r.mutedCats)
			g.Greeting, g.Farewell = greet.Text, farewell.Text
			if inter.Enabled != nil {
				g.Interact.Enabled = *inter.Enabled
			}
			if inter.UseCmds != nil {
				g.Interact.UseCmds = *inter.UseCmds
			}
			if inter.Cooldown != nil {
				g.Interact.CooldownMin = *inter.Cooldown
			}
			if inter.Chance != nil {
				g.Interact.Chance = *inter.Chance
			}
			if inter.Proporcao != nil {
				g.Interact.Proporcao = *inter.Proporcao
			}
		})
		if err != nil {
			return fmt.Errorf("%s: %w", r.id, err)
		}
		if err := imp.SetIgnorePrefix(ctx, r.id, r.ignorePrefix != 0); err != nil {
			return err
		}
		rep.Groups++
	}
	return nil
}

func parseJSON(s sql.NullString, v any) {
	if s.Valid && s.String != "" {
		_ = json.Unmarshal([]byte(s.String), v) // dado antigo inválido vira padrão
	}
}

func stringList(s sql.NullString) []string {
	var raw []any
	parseJSON(s, &raw)
	var out []string
	for _, v := range raw {
		switch v := v.(type) {
		case string:
			out = append(out, v)
		case map[string]any: // alguns registros guardam objetos {id: ...}
			if id, ok := v["id"].(string); ok {
				out = append(out, id)
			}
		}
	}
	return out
}

// numbers converte "5562...@c.us"/"123@lid" no usuário do JID, como o ravena-go guarda.
func numbers(s sql.NullString) []string {
	var out []string
	for _, v := range stringList(s) {
		if user := userOf(v); user != "" {
			out = append(out, user)
		}
	}
	return dedupe(out)
}

func userOf(jid string) string {
	user, _, _ := strings.Cut(strings.TrimSpace(jid), "@")
	user, _, _ = strings.Cut(user, ":")
	return user
}

func lowerList(s sql.NullString) []string {
	var out []string
	for _, v := range stringList(s) {
		if v = strings.ToLower(strings.TrimSpace(v)); v != "" {
			out = append(out, v)
		}
	}
	return dedupe(out)
}

func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ---- comandos personalizados ----

type oldCommand struct {
	SendAll        bool     `json:"sendAllResponses"`
	IgnoreInteract bool     `json:"ignoreInteract"`
	IgnorePrefix   bool     `json:"ignorePrefix"`
	ReplyPrivate   bool     `json:"replyPrivate"`
	Mentions       []string `json:"mentions"`
	Cooldown       int      `json:"cooldown"`
	React          string   `json:"react"`
	Reply          *bool    `json:"reply"`
	Reactions      struct {
		Before string `json:"before"`
		After  string `json:"after"`
	} `json:"reactions"`
}

var mediaRe = regexp.MustCompile(`(?s)^\{(audio|voice|image|video|gif|document|sticker|stickerGif)-([^}]+)\}\s*(.*)`)

var mimeByExt = map[string]string{
	"jpg": "image/jpeg", "jpeg": "image/jpeg", "png": "image/png", "webp": "image/webp", "gif": "image/gif",
	"mp4": "video/mp4", "3gp": "video/3gpp", "mov": "video/quicktime",
	"ogg": "audio/ogg; codecs=opus", "opus": "audio/ogg; codecs=opus", "mp3": "audio/mpeg", "m4a": "audio/mp4",
	"aac": "audio/aac", "wav": "audio/wav", "pdf": "application/pdf", "txt": "text/plain", "zip": "application/zip",
}

func importCommands(ctx context.Context, path, mediaDir string, imp *customcmd.Importer, rep *Report) error {
	db, err := openRO(path)
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT group_id, trigger, COALESCE(responses,'[]'), COALESCE(admin_only,0),
		COALESCE(active,1), COALESCE(count,0), COALESCE(last_used,0), COALESCE(created_by,''),
		COALESCE(created_at,0), json_data FROM custom_commands WHERE COALESCE(deleted,0) = 0`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type row struct {
		chat, trigger, responses, createdBy string
		admin, active, count                int
		lastUsed, createdAt                 int64
		data                                sql.NullString
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.chat, &r.trigger, &r.responses, &r.admin, &r.active, &r.count,
			&r.lastUsed, &r.createdBy, &r.createdAt, &r.data); err != nil {
			return err
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()

	for _, r := range all {
		trigger := strings.Join(strings.Fields(strings.ToLower(r.trigger)), " ")
		if trigger == "" || !strings.HasSuffix(r.chat, "@g.us") {
			continue
		}
		var old oldCommand
		parseJSON(r.data, &old)
		var texts []string
		_ = json.Unmarshal([]byte(r.responses), &texts)

		c := &customcmd.Command{
			Trigger:        trigger,
			Active:         r.active != 0,
			AdminOnly:      r.admin != 0,
			IgnoreInteract: old.IgnoreInteract,
			SendAll:        old.SendAll,
			IgnorePrefix:   old.IgnorePrefix,
			NoQuote:        old.Reply != nil && !*old.Reply,
			Private:        old.ReplyPrivate,
			Mentions:       old.Mentions,
			CooldownSec:    min(max(old.Cooldown, 0), customcmd.MaxCooldown),
			StartReact:     old.Reactions.Before,
			React:          cmp.Or(old.Reactions.After, old.React),
			Count:          r.count,
			LastUsed:       msToSec(r.lastUsed),
			CreatedBy:      userOf(r.createdBy),
			CreatedAt:      msToSec(r.createdAt),
		}
		for _, t := range texts {
			resp, ok, why := convertResponse(imp, r.chat, mediaDir, t)
			if !ok {
				rep.Skipped = append(rep.Skipped, fmt.Sprintf("%s %q: %s", r.chat, trigger, why))
				continue
			}
			if resp.IsMedia() {
				rep.Media++
			}
			c.Responses = append(c.Responses, resp)
			if len(c.Responses) == customcmd.MaxResponses {
				break
			}
		}
		if len(c.Responses) == 0 {
			continue
		}
		if err := imp.Put(ctx, r.chat, c); err != nil {
			return fmt.Errorf("%s %q: %w", r.chat, trigger, err)
		}
		rep.Commands++
	}
	return nil
}

func convertResponse(imp *customcmd.Importer, chat, mediaDir, text string) (customcmd.Response, bool, string) {
	m := mediaRe.FindStringSubmatch(text)
	if m == nil {
		return customcmd.Response{Text: text}, strings.TrimSpace(text) != "", "resposta vazia"
	}
	kind, file, caption := m[1], m[2], m[3]
	if kind == "stickerGif" {
		return customcmd.Response{}, false, "figurinha animada guardada por URL"
	}
	src := filepath.Join(mediaDir, filepath.Base(file))
	name, err := imp.CopyMedia(chat, src)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return customcmd.Response{}, false, "arquivo " + file + " não existe"
		}
		return customcmd.Response{}, false, err.Error()
	}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	resp := customcmd.Response{Kind: kind, Text: caption, File: name, Mime: mimeByExt[ext]}
	if kind == "document" {
		resp.Name = file
	}
	if kind == "sticker" && ext == "webp" {
		if b, err := os.ReadFile(src); err == nil && strings.Contains(string(b[:min(len(b), 4096)]), "ANIM") {
			resp.Animated = true
		}
	}
	return resp, true, ""
}

// msToSec converte o Date.now() do Node em segundos Unix (aceita já em segundos).
func msToSec(v int64) int64 {
	if v > 1e12 {
		return v / 1000
	}
	return v
}
