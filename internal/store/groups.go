package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

// Interact configura as interações automáticas ("interagir") de um grupo.
type Interact struct {
	Enabled bool `json:"enabled"`
	// UseCmds deixa a interação usar comandos personalizados do grupo.
	UseCmds bool `json:"useCmds"`
	// CooldownMin é o intervalo mínimo entre interações, em minutos (mín. 30).
	CooldownMin int `json:"cooldown"`
	// Chance por mensagem, em 1/10000 (100 = 1%, máx. 500).
	Chance int `json:"chance"`
	// Proporcao é a % de interações via LLM (o resto usa comandos do grupo).
	Proporcao int       `json:"proporcao"`
	Last      time.Time `json:"lastInteraction"`
}

// Limites do Ravena original para não virar spam.
const (
	MinInteractCooldownMin = 30
	MaxInteractChance      = 500
)

// EffectiveCooldown aplica o mínimo de 30 minutos.
func (i Interact) EffectiveCooldown() time.Duration {
	return time.Duration(max(i.CooldownMin, MinInteractCooldownMin)) * time.Minute
}

// EffectiveChance aplica o teto de 5%.
func (i Interact) EffectiveChance() int { return min(max(i.Chance, 0), MaxInteractChance) }

// Group são as configurações de um grupo. É guardado como JSON: campos novos
// não precisam de migração, só de um valor padrão em NewGroup.
type Group struct {
	JID  string `json:"jid"`
	Name string `json:"name"`
	// Prefix vazio usa o prefixo global.
	Prefix           string   `json:"prefix,omitempty"`
	Paused           bool     `json:"paused"`
	MutedCommands    []string `json:"mutedCommands,omitempty"`
	MutedCategories  []string `json:"mutedCategories,omitempty"`
	IgnoredUsers     []string `json:"ignoredUsers,omitempty"`
	AdditionalAdmins []string `json:"additionalAdmins,omitempty"`
	Interact         Interact `json:"interact"`
	// Silly liga as respostas a elogios/xingamentos ao bot.
	Silly bool `json:"silly"`
	// Greeting e Farewell são as mensagens de boas-vindas/despedida ("" desliga).
	// Aceitam {pessoa} (menção a quem entrou/saiu) e {grupo}.
	Greeting  string    `json:"greeting,omitempty"`
	Farewell  string    `json:"farewell,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// NewGroup devolve um grupo com os padrões do Ravena.
func NewGroup(jid string) *Group {
	return &Group{
		JID:       jid,
		Interact:  Interact{Enabled: true, UseCmds: true, CooldownMin: 30, Chance: 100, Proporcao: 50},
		Silly:     true,
		CreatedAt: time.Now(),
	}
}

// IsMuted diz se o comando (ou sua categoria) está silenciado no grupo.
func (g *Group) IsMuted(command, category string) bool {
	return slices.Contains(g.MutedCommands, command) || (category != "" && slices.Contains(g.MutedCategories, category))
}

// IsIgnored diz se o usuário é ignorado no grupo.
func (g *Group) IsIgnored(user string) bool { return slices.Contains(g.IgnoredUsers, user) }

func (g *Group) clone() *Group {
	c := *g
	c.MutedCommands = slices.Clone(g.MutedCommands)
	c.MutedCategories = slices.Clone(g.MutedCategories)
	c.IgnoredUsers = slices.Clone(g.IgnoredUsers)
	c.AdditionalAdmins = slices.Clone(g.AdditionalAdmins)
	return &c
}

// Group devolve as configurações do grupo jid (ok=false se nunca foi salvo).
// O valor devolvido é uma cópia: altere e chame SaveGroup.
func (s *Store) Group(ctx context.Context, jid string) (*Group, bool, error) {
	s.mu.RLock()
	g, ok := s.groups[jid]
	s.mu.RUnlock()
	if ok {
		if g == nil {
			return nil, false, nil
		}
		return g.clone(), true, nil
	}

	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT data FROM groups WHERE jid = ?`, jid).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		s.mu.Lock()
		if _, saved := s.groups[jid]; !saved {
			s.groups[jid] = nil
		}
		s.mu.Unlock()
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	g = NewGroup(jid) // padrões para campos que não existiam quando foi salvo
	if err := json.Unmarshal([]byte(raw), g); err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	s.groups[jid] = g
	s.mu.Unlock()
	return g.clone(), true, nil
}

// GroupOrDefault devolve o grupo salvo ou um novo com os padrões (sem salvar).
func (s *Store) GroupOrDefault(ctx context.Context, jid string) (*Group, error) {
	g, ok, err := s.Group(ctx, jid)
	if err != nil {
		return nil, err
	}
	if !ok {
		g = NewGroup(jid)
	}
	return g, nil
}

// SaveGroup grava g (insere ou atualiza).
func (s *Store) SaveGroup(ctx context.Context, g *Group) error {
	g.UpdatedAt = time.Now()
	if g.CreatedAt.IsZero() {
		g.CreatedAt = g.UpdatedAt
	}
	raw, err := json.Marshal(g)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO groups(jid, data, updated_at) VALUES(?, ?, ?)
		 ON CONFLICT(jid) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
		g.JID, string(raw), g.UpdatedAt.Unix()); err != nil {
		return err
	}
	s.mu.Lock()
	s.groups[g.JID] = g.clone()
	s.mu.Unlock()
	return nil
}

// UpdateGroup lê, aplica fn e grava, criando o grupo se preciso.
func (s *Store) UpdateGroup(ctx context.Context, jid string, fn func(*Group)) (*Group, error) {
	g, err := s.GroupOrDefault(ctx, jid)
	if err != nil {
		return nil, err
	}
	fn(g)
	return g, s.SaveGroup(ctx, g)
}

// Groups lista todos os grupos salvos, do mais recente para o mais antigo.
func (s *Store) Groups(ctx context.Context) ([]*Group, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT jid, data FROM groups ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Group
	for rows.Next() {
		var jid, raw string
		if err := rows.Scan(&jid, &raw); err != nil {
			return nil, err
		}
		g := NewGroup(jid)
		if err := json.Unmarshal([]byte(raw), g); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
