// Package fun reúne os jogos e zoeiras leves de grupo: dados, biscoito da
// sorte, cantadas, comandos de zoeira (violência, boleto...), o jogo do
// pinto e a roleta russa.
package fun

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"math/rand/v2"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/joaodobread/ravena-go/internal/app"
	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/store"
)

// Module adapta a funcionalidade ao contrato app.Feature.
type Module struct{}

// Name implementa app.Feature.
func (Module) Name() string { return "fun" }

// Register implementa app.Feature.
func (Module) Register(ctx context.Context, d *app.Deps) error {
	f, err := New(ctx, d.Store)
	if err != nil {
		return err
	}
	return d.Router.Register(f.Commands()...)
}

// Feature guarda o estado dos comandos. rand e now são injetáveis para testes.
type Feature struct {
	db   *sql.DB
	rand func(n int) int // inteiro em [0, n)
	now  func() time.Time
}

var migrations = []string{
	`CREATE TABLE fun_pinto (
		chat       TEXT NOT NULL,
		user       TEXT NOT NULL,
		name       TEXT NOT NULL,
		flaccid    REAL NOT NULL,
		erect      REAL NOT NULL,
		girth      REAL NOT NULL,
		curvature  REAL NOT NULL,
		score      INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		PRIMARY KEY (chat, user)
	)`,
	`CREATE INDEX fun_pinto_score ON fun_pinto(chat, score)`,
	`CREATE TABLE fun_roleta_groups (
		chat        TEXT PRIMARY KEY,
		timeout     INTEGER NOT NULL DEFAULT 300,
		last_player TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE TABLE fun_roleta_players (
		chat       TEXT NOT NULL,
		user       TEXT NOT NULL,
		name       TEXT NOT NULL,
		current    INTEGER NOT NULL DEFAULT 0,
		max_tries  INTEGER NOT NULL DEFAULT 0,
		deaths     INTEGER NOT NULL DEFAULT 0,
		dead_until INTEGER NOT NULL DEFAULT 0,
		total      INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (chat, user)
	)`,
}

// New aplica as migrações e cria a funcionalidade.
func New(ctx context.Context, st *store.Store) (*Feature, error) {
	if err := st.Migrate(ctx, "fun", migrations); err != nil {
		return nil, err
	}
	return &Feature{db: st.DB(), rand: rand.IntN, now: time.Now}, nil
}

// Commands devolve todos os comandos do pacote.
func (f *Feature) Commands() []*bot.Command {
	var cmds []*bot.Command
	cmds = append(cmds, f.diceCommands()...)
	cmds = append(cmds, f.zueiraCommands()...)
	cmds = append(cmds, f.pintoCommands()...)
	cmds = append(cmds, f.roletaCommands()...)
	return cmds
}

//go:embed phrases.json
var phrasesJSON []byte

// phrases são as listas de frases extraídas de data/custom-variables.json do
// Ravena (só as usadas aqui). O parse só acontece no primeiro uso.
var phrases = sync.OnceValue(func() map[string][]string {
	var m map[string][]string
	if err := json.Unmarshal(phrasesJSON, &m); err != nil {
		panic("fun: phrases.json inválido: " + err.Error())
	}
	return m
})

func (f *Feature) pick(list []string) string {
	if len(list) == 0 {
		return ""
	}
	return list[f.rand(len(list))]
}

// between devolve um inteiro em [lo, hi].
func (f *Feature) between(lo, hi int) int {
	if hi < lo {
		lo, hi = hi, lo
	}
	return lo + f.rand(hi-lo+1)
}

// brt é o fuso de Brasília (sem horário de verão desde 2019); fixo para não
// depender do tzdata da imagem.
var brt = time.FixedZone("BRT", -3*3600)

func formatDateTime(t time.Time) string { return t.In(brt).Format("02/01/2006, 15:04") }

func authorName(m *bot.Message, fallback string) string {
	if n := strings.TrimSpace(m.PushName); n != "" {
		return n
	}
	return fallback
}

// ids são as identidades do autor (número e LID) para consultas no banco;
// sempre dois valores, repetindo o primeiro se não houver alternativa.
func ids(m *bot.Message) [2]string {
	alt := m.SenderAlt.User
	if alt == "" {
		alt = m.Sender.User
	}
	return [2]string{m.Sender.User, alt}
}

var placeholderRe = regexp.MustCompile(`\{([^{}]+)\}`)

// renderer substitui as variáveis das frases ({nomeAutor}, {mention},
// {rndDadoRange-1-6}, listas como {genshinWeapon3Emoji}).
type renderer struct {
	f        *Feature
	c        *bot.Context
	used     []types.JID
	members  []bot.Participant
	loaded   bool
	mentions []types.JID
}

// render processa tmpl e devolve a mensagem com as menções preenchidas.
func (f *Feature) render(ctx context.Context, c *bot.Context, tmpl string) bot.Outgoing {
	r := &renderer{f: f, c: c}
	text := placeholderRe.ReplaceAllStringFunc(tmpl, func(tok string) string {
		name := tok[1 : len(tok)-1]
		switch {
		case name == "nomeAutor" || name == "pessoa":
			return authorName(c.Message, "Pessoa")
		case name == "mention":
			return r.mention(ctx)
		case strings.HasPrefix(name, "rndDadoRange-"):
			lo, hi, ok := strings.Cut(strings.TrimPrefix(name, "rndDadoRange-"), "-")
			a, err1 := strconv.Atoi(lo)
			b, err2 := strconv.Atoi(hi)
			if !ok || err1 != nil || err2 != nil {
				return tok
			}
			return strconv.Itoa(f.between(a, b))
		}
		if list, ok := phrases()[name]; ok {
			return f.pick(list)
		}
		return tok
	})
	out := bot.Text(text)
	out.Mentions = r.mentions
	return out
}

func (r *renderer) isUsed(p bot.Participant) bool {
	for _, u := range r.used {
		if p.Is(u) {
			return true
		}
	}
	return false
}

func (r *renderer) usedJID(j types.JID) bool {
	for _, u := range r.used {
		if u.User == j.User {
			return true
		}
	}
	return false
}

// mention escolhe, nesta ordem, alguém marcado, o autor da mensagem citada
// ou um membro aleatório do grupo, sem repetir pessoas no mesmo texto.
func (r *renderer) mention(ctx context.Context) string {
	msg := r.c.Message
	var avail []types.JID
	for _, m := range msg.Mentions {
		if !r.usedJID(m) {
			avail = append(avail, m)
		}
	}
	if len(avail) > 0 {
		return r.use(avail[r.f.rand(len(avail))])
	}
	if q := msg.Quoted; q != nil && !q.Sender.IsEmpty() && !r.usedJID(q.Sender) {
		return r.use(q.Sender)
	}
	if msg.IsGroup {
		if !r.loaded {
			r.loaded = true
			if info, err := r.c.Client.GroupInfo(ctx, msg.Chat); err == nil {
				me := r.c.Client.Me()
				for _, p := range info.Participants {
					if !p.Is(me) {
						r.members = append(r.members, p)
					}
				}
			}
		}
		var cands []bot.Participant
		for _, p := range r.members {
			if !r.isUsed(p) {
				cands = append(cands, p)
			}
		}
		if len(cands) == 0 {
			cands = r.members // todos já usados: repete alguém
		}
		if len(cands) > 0 {
			return r.use(cands[r.f.rand(len(cands))].JID)
		}
	}
	return "Usuário"
}

func (r *renderer) use(j types.JID) string {
	r.used = append(r.used, j)
	if !r.usedIn(j) {
		r.mentions = append(r.mentions, j)
	}
	return bot.MentionText(j)
}

func (r *renderer) usedIn(j types.JID) bool {
	for _, m := range r.mentions {
		if m == j {
			return true
		}
	}
	return false
}
