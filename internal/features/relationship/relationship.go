// Package relationship implementa os relacionamentos de zoeira entre membros
// do grupo: !namorar, !casar, !separar, !recusar, !coisar, !trair,
// !relacionamento e !relacionamentos. Tudo fica no SQLite.
package relationship

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/joaodobread/ravena-go/internal/app"
	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/features/gamekit"
)

// Tipos e estados.
const (
	Namoro    = "namoro"
	Casamento = "casamento"

	Pendente  = "pendente"
	Ativo     = "ativo"
	Terminado = "terminado"
)

var migrations = []string{
	`CREATE TABLE relacionamentos (
		id             INTEGER PRIMARY KEY AUTOINCREMENT,
		group_id       TEXT NOT NULL,
		user1          TEXT NOT NULL,
		user2          TEXT NOT NULL,
		user1_jid      TEXT NOT NULL,
		user2_jid      TEXT NOT NULL,
		tipo           TEXT NOT NULL,
		status         TEXT NOT NULL,
		criado_em      INTEGER NOT NULL,
		terminado_em   INTEGER,
		coisas_count   INTEGER NOT NULL DEFAULT 0,
		traicoes_count INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE INDEX relacionamentos_group ON relacionamentos(group_id, status)`,
}

// Migrations são as migrações do escopo "relationship".
func Migrations() []string { return migrations }

// Feature é a funcionalidade.
type Feature struct {
	db   *sql.DB
	rand func(n int) int
	now  func() time.Time
}

// New cria a funcionalidade; rnd e now podem ser nil.
func New(db *sql.DB, rnd func(int) int, now func() time.Time) *Feature {
	if rnd == nil {
		rnd = rand.IntN
	}
	if now == nil {
		now = time.Now
	}
	return &Feature{db: db, rand: rnd, now: now}
}

// Commands devolve os comandos.
func (f *Feature) Commands() []*bot.Command {
	cmd := func(name, desc, usage, after string, run bot.Handler) *bot.Command {
		return &bot.Command{Name: name, Description: desc, Usage: usage, Category: "zoeira", GroupOnly: true,
			Reactions: bot.Reactions{After: after}, Run: run}
	}
	return []*bot.Command{
		cmd("namorar", "Pede em namoro ou aceita um pedido de namoro", "!namorar @pessoa", "💖", f.namorar),
		cmd("casar", "Pede em casamento ou aceita um pedido de casamento", "!casar @pessoa", "💍", f.casar),
		cmd("separar", "Termina o namoro/casamento com a pessoa marcada", "!separar @pessoa", "💔", f.separar),
		cmd("recusar", "Recusa um pedido de namoro ou casamento", "!recusar @pessoa", "💔", f.recusar),
		cmd("coisar", "Coisa com a pessoa marcada ou com um parceiro aleatório", "!coisar [@pessoa]", "🔥", f.coisar),
		cmd("trair", "Trai seu(s) parceiro(s) com a pessoa marcada ou um membro aleatório", "!trair [@pessoa]", "😈", f.trair),
		cmd("relacionamento", "Mostra o seu histórico de relacionamentos", "", "📊", f.historico),
		cmd("relacionamentos", "Mostra os relacionamentos e estatísticas do grupo", "", "📊", f.grupo),
	}
}

type rel struct {
	id                int64
	user1, user2      string
	jid1, jid2        types.JID
	tipo, status      string
	criado, terminado int64
	coisas, traicoes  int
}

// partner devolve o JID do outro lado.
func (r *rel) partner(user string) types.JID {
	if r.user1 == user {
		return r.jid2
	}
	return r.jid1
}

const cols = `id, user1, user2, user1_jid, user2_jid, tipo, status, criado_em, COALESCE(terminado_em, 0), coisas_count, traicoes_count`

func scan(row interface{ Scan(...any) error }) (*rel, error) {
	var (
		r      rel
		j1, j2 string
	)
	if err := row.Scan(&r.id, &r.user1, &r.user2, &j1, &j2, &r.tipo, &r.status, &r.criado, &r.terminado, &r.coisas, &r.traicoes); err != nil {
		return nil, err
	}
	r.jid1, _ = types.ParseJID(j1)
	r.jid2, _ = types.ParseJID(j2)
	return &r, nil
}

func (f *Feature) one(ctx context.Context, where string, args ...any) (*rel, error) {
	r, err := scan(f.db.QueryRowContext(ctx, `SELECT `+cols+` FROM relacionamentos WHERE `+where+` LIMIT 1`, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

func (f *Feature) all(ctx context.Context, where string, args ...any) ([]*rel, error) {
	rows, err := f.db.QueryContext(ctx, `SELECT `+cols+` FROM relacionamentos WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*rel
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (f *Feature) exec(ctx context.Context, q string, args ...any) error {
	_, err := f.db.ExecContext(ctx, q, args...)
	return err
}

// pair é o filtro "entre a e b, em qualquer ordem".
const pair = `group_id = ? AND ((user1 = ? AND user2 = ?) OR (user1 = ? AND user2 = ?))`

func pairArgs(g, a, b string) []any { return []any{g, a, b, b, a} }

// actors resolve autor e alvo (primeira menção) nas identidades canônicas.
type actors struct {
	group          string
	info           *bot.GroupInfo
	author, target types.JID
}

func (f *Feature) actors(ctx context.Context, c *bot.Context) actors {
	info := gamekit.Group(ctx, c.Client, c.Message.Chat)
	a := actors{group: c.Message.Chat.String(), info: info, author: gamekit.Author(info, c.Message)}
	if len(c.Message.Mentions) > 0 {
		a.target = gamekit.Canonical(info, c.Message.Mentions[0])
	}
	return a
}

func m(j types.JID) string { return bot.MentionText(j) }

func reply(ctx context.Context, c *bot.Context, text string, mentions ...types.JID) error {
	return c.Reply(ctx, bot.Outgoing{Kind: bot.OutText, Text: text, Mentions: mentions})
}

func (f *Feature) namorar(ctx context.Context, c *bot.Context) error {
	a := f.actors(ctx, c)
	if a.target.User == "" {
		return bot.Userf("❌ Você precisa marcar alguém para pedir em namoro! Ex: *%snamorar @pessoa* 😉", c.Prefix)
	}
	if a.target.User == a.author.User {
		return bot.Userf("❌ Você não pode namorar consigo mesmo(a)! Procure alguém especial no grupo. 😉💖")
	}
	g, me, them := a.group, a.author.User, a.target.User
	active, err := f.one(ctx, pair+` AND status = 'ativo' AND tipo IN ('namoro', 'casamento')`, pairArgs(g, me, them)...)
	if err != nil {
		return err
	}
	if active != nil {
		what := "namorando 💖"
		if active.tipo == Casamento {
			what = "casados(as) 💍"
		}
		return reply(ctx, c, fmt.Sprintf("❌ Vocês já estão %s! Não precisa pedir de novo. 🥰", what), a.target)
	}
	// Pedido dele(a) para mim: aceitar.
	theirs, err := f.one(ctx, `group_id = ? AND user1 = ? AND user2 = ? AND tipo = 'namoro' AND status = 'pendente'`, g, them, me)
	if err != nil {
		return err
	}
	if theirs != nil {
		if err := f.exec(ctx, `UPDATE relacionamentos SET status = 'ativo', criado_em = ? WHERE id = ?`, f.now().UnixMilli(), theirs.id); err != nil {
			return err
		}
		if err := f.exec(ctx, `DELETE FROM relacionamentos WHERE group_id = ? AND user1 = ? AND user2 = ? AND tipo = 'namoro' AND status = 'pendente'`, g, me, them); err != nil {
			return err
		}
		return reply(ctx, c, fmt.Sprintf("🎉 *OFICIALIZADO!* 🎉\n\n💖 %s aceitou o pedido de namoro de %s! Agora vocês estão oficialmente namorando! 😍💑\n\nQue essa união seja repleta de momentos felizes e muitas coisadas! 🔥😉",
			m(a.author), m(theirs.jid1)), a.author, theirs.jid1)
	}
	mine, err := f.one(ctx, `group_id = ? AND user1 = ? AND user2 = ? AND tipo = 'namoro' AND status = 'pendente'`, g, me, them)
	if err != nil {
		return err
	}
	if mine != nil {
		return reply(ctx, c, fmt.Sprintf("⌛ Você já pediu %s em namoro! Aguarde a resposta dele(a). 😉", m(a.target)), a.target)
	}
	if err := f.insert(ctx, a, Namoro); err != nil {
		return err
	}
	return reply(ctx, c, fmt.Sprintf("💖 %s, você foi pedido(a) em namoro por %s!\n\nSe aceita, envie também:\n%snamorar %s\n\nSe recusa, envie:\n%srecusar %s",
		m(a.target), m(a.author), c.Prefix, m(a.author), c.Prefix, m(a.author)), a.author, a.target)
}

func (f *Feature) insert(ctx context.Context, a actors, tipo string) error {
	return f.exec(ctx, `INSERT INTO relacionamentos(group_id, user1, user2, user1_jid, user2_jid, tipo, status, criado_em) VALUES(?, ?, ?, ?, ?, ?, 'pendente', ?)`,
		a.group, a.author.User, a.target.User, a.author.String(), a.target.String(), tipo, f.now().UnixMilli())
}

func (f *Feature) casar(ctx context.Context, c *bot.Context) error {
	a := f.actors(ctx, c)
	if a.target.User == "" {
		return bot.Userf("❌ Você precisa marcar alguém para pedir em casamento! Ex: *%scasar @pessoa* 😉", c.Prefix)
	}
	if a.target.User == a.author.User {
		return bot.Userf("❌ Você não pode se casar com você mesmo(a)! 👰🤵")
	}
	g, me, them := a.group, a.author.User, a.target.User
	active, err := f.one(ctx, pair+` AND status = 'ativo' AND tipo IN ('namoro', 'casamento')`, pairArgs(g, me, them)...)
	if err != nil {
		return err
	}
	if active != nil && active.tipo == Casamento {
		return reply(ctx, c, "❌ Vocês já estão casados! 💍 Que esse amor continue forte! 🥰", a.target)
	}
	if active == nil {
		return reply(ctx, c, fmt.Sprintf("❌ Ops! Só pode casar depois de namorar! Você precisa estar namorando %s primeiro. 💔", m(a.target)), a.target)
	}
	theirs, err := f.one(ctx, `group_id = ? AND user1 = ? AND user2 = ? AND tipo = 'casamento' AND status = 'pendente'`, g, them, me)
	if err != nil {
		return err
	}
	if theirs != nil {
		if err := f.exec(ctx, `UPDATE relacionamentos SET tipo = 'casamento', criado_em = ? WHERE id = ?`, f.now().UnixMilli(), active.id); err != nil {
			return err
		}
		if err := f.exec(ctx, `DELETE FROM relacionamentos WHERE `+pair+` AND tipo = 'casamento' AND status = 'pendente'`, pairArgs(g, me, them)...); err != nil {
			return err
		}
		return reply(ctx, c, fmt.Sprintf("💍 *OFICIALMENTE CASADOS!* 💍\n\n🎉 Que momento lindo! %s e %s subiram ao altar e agora estão oficialmente casados! 🤵👰💐\n\nDesejamos uma vida inteira de cumplicidade, muito amor e coisadas sem fim! 🥂❤️",
			m(active.jid1), m(active.jid2)), active.jid1, active.jid2)
	}
	mine, err := f.one(ctx, `group_id = ? AND user1 = ? AND user2 = ? AND tipo = 'casamento' AND status = 'pendente'`, g, me, them)
	if err != nil {
		return err
	}
	if mine != nil {
		return reply(ctx, c, fmt.Sprintf("⌛ Você já pediu %s em casamento! Aguarde a resposta dele(a). 💍", m(a.target)), a.target)
	}
	if err := f.insert(ctx, a, Casamento); err != nil {
		return err
	}
	return reply(ctx, c, fmt.Sprintf("💍 %s, você foi pedido(a) em casamento por %s!\n\nSe aceita, envie também:\n%scasar %s\n\nSe recusa, envie:\n%srecusar %s",
		m(a.target), m(a.author), c.Prefix, m(a.author), c.Prefix, m(a.author)), a.author, a.target)
}

func (f *Feature) separar(ctx context.Context, c *bot.Context) error {
	a := f.actors(ctx, c)
	if a.target.User == "" {
		return bot.Userf("❌ Você precisa marcar de quem deseja se separar! Ex: *%sseparar @pessoa* 🧐", c.Prefix)
	}
	if a.target.User == a.author.User {
		return bot.Userf("❌ Como você vai se separar de si mesmo(a)? 🤔")
	}
	g, me, them := a.group, a.author.User, a.target.User
	active, err := f.one(ctx, pair+` AND status = 'ativo' AND tipo IN ('namoro', 'casamento')`, pairArgs(g, me, them)...)
	if err != nil {
		return err
	}
	if active == nil {
		return reply(ctx, c, fmt.Sprintf("❌ Ops! Você não tem nenhum relacionamento ativo (namoro ou casamento) com %s! 🧐", m(a.target)), a.target)
	}
	if err := f.exec(ctx, `UPDATE relacionamentos SET status = 'terminado', terminado_em = ? WHERE id = ?`, f.now().UnixMilli(), active.id); err != nil {
		return err
	}
	if err := f.exec(ctx, `DELETE FROM relacionamentos WHERE `+pair+` AND status = 'pendente'`, pairArgs(g, me, them)...); err != nil {
		return err
	}
	what := "namoro 💖"
	if active.tipo == Casamento {
		what = "casamento 💍"
	}
	return reply(ctx, c, fmt.Sprintf("💔 *FIM DE RELACIONAMENTO!* 💔\n\nÉ oficial: o %s entre %s e %s chegou ao fim. 👋🚶‍♂️🚶‍♀️",
		what, m(active.jid1), m(active.jid2)), active.jid1, active.jid2)
}

func (f *Feature) recusar(ctx context.Context, c *bot.Context) error {
	a := f.actors(ctx, c)
	if a.target.User == "" {
		return bot.Userf("❌ Você precisa marcar quem fez o pedido para recusar! Ex: *%srecusar @pessoa* 🧐", c.Prefix)
	}
	p, err := f.one(ctx, `group_id = ? AND user1 = ? AND user2 = ? AND status = 'pendente'`, a.group, a.target.User, a.author.User)
	if err != nil {
		return err
	}
	if p == nil {
		return reply(ctx, c, fmt.Sprintf("❌ Não há nenhum pedido pendente de %s para você! 🧐", m(a.target)), a.target)
	}
	if err := f.exec(ctx, `DELETE FROM relacionamentos WHERE id = ?`, p.id); err != nil {
		return err
	}
	text := fmt.Sprintf("💔 *PEDIDO RECUSADO!* %s recusou o pedido de namoro de %s! O coração foi partido em pedacinhos... 😭", m(p.jid2), m(p.jid1))
	if p.tipo == Casamento {
		text = fmt.Sprintf("💔 *PEDIDO RECUSADO!* %s deixou %s esperando no altar e recusou o casamento! 👰🤵💥", m(p.jid2), m(p.jid1))
	}
	return reply(ctx, c, text, p.jid1, p.jid2)
}

var coisarFrases = []string{
	"🔥 Eita! {autor} e {pessoa} foram vistos saindo de um motel de beira de estrada com cara de sapecas! 😏",
	"🛏️ O colchão rangeu! {autor} e {pessoa} coisaram gostoso hoje! 😳",
	"🔥 Que calor! {autor} e {pessoa} deram aquela coisada violenta nos bastidores do grupo! 🤫",
	"❤️ Amor está no ar (e outras coisas mais)! {autor} coisou com {pessoa} atrás da moita! 🌿",
	"💥 PÁ! {autor} e {pessoa} fizeram o terremoto acontecer na cama! 🥵",
	"🍿 Assistindo de camarote: {autor} coisou com {pessoa} com direito a performance! 🎭",
	"🐱 Huumm... {autor} coisou com {pessoa} e saíram miando! 🐈",
	"🍩 Com direito a cobertura: {autor} e {pessoa} coisaram bem doce hoje! 🤤",
	"⚡ Sintonia elétrica! {autor} coisou com {pessoa} e saíram faíscas para todo lado! 🔋",
}

var trairFrases = []string{
	"😱 *ESCÂNDALO!* {autor} foi flagrado(a) traindo {conjugue} com {pessoa}! O amor não vale nada mesmo... 💔",
	"🔥 *BABADO NO GRUPO!* {autor} deu uma escapadinha com {pessoa} pelas costas de {conjugue}! 🤫",
	"🕵️‍♂️ *FLAGRANTE!* {conjugue} pegou {autor} no colo de {pessoa}! O clima esquentou! 🍿",
	"💔 Que audácia! {autor} traiu a confiança de {conjugue} nos braços de {pessoa}! 💋",
	"🚨 *ALERTA CORNO!* {conjugue} acaba de ganhar um par de chifres novinho 🐂 porque {autor} coisou escondido com {pessoa}! 😭",
	"🫣 *PULO DA CERCA!* {autor} resolveu diversificar e traiu {conjugue} com {pessoa}! 🙊",
	"🤐 *SEGREDO REVELADO!* A casa caiu para {autor}, que andava de romance com {pessoa} ignorando {conjugue}! 🪓",
}

const activeOf = `group_id = ? AND (user1 = ? OR user2 = ?) AND status = 'ativo' AND tipo IN ('namoro', 'casamento')`

func (f *Feature) coisar(ctx context.Context, c *bot.Context) error {
	a := f.actors(ctx, c)
	me := a.author.User
	var r *rel
	if a.target.User != "" {
		if a.target.User == me {
			return bot.Userf("❌ Coisar consigo mesmo(a) é solitário demais! Chame seu parceiro(a). 😉")
		}
		var err error
		r, err = f.one(ctx, pair+` AND status = 'ativo' AND tipo IN ('namoro', 'casamento')`, pairArgs(a.group, me, a.target.User)...)
		if err != nil {
			return err
		}
		if r == nil {
			return reply(ctx, c, fmt.Sprintf("❌ Ops! Você só pode coisar com quem está namorando ou casado! %s não tem nada sério com você. 😳", m(a.target)), a.target)
		}
	} else {
		rels, err := f.all(ctx, activeOf, a.group, me, me)
		if err != nil {
			return err
		}
		if len(rels) == 0 {
			return bot.Userf("❌ Você não está em nenhum relacionamento neste grupo para coisar! Vá namorar ou casar primeiro! 😉")
		}
		r = rels[f.rand(len(rels))]
	}
	if err := f.exec(ctx, `UPDATE relacionamentos SET coisas_count = coisas_count + 1 WHERE id = ?`, r.id); err != nil {
		return err
	}
	partner := r.partner(me)
	text := strings.NewReplacer("{autor}", m(a.author), "{pessoa}", m(partner)).Replace(coisarFrases[f.rand(len(coisarFrases))])
	return reply(ctx, c, text, a.author, partner)
}

func (f *Feature) trair(ctx context.Context, c *bot.Context) error {
	a := f.actors(ctx, c)
	me := a.author.User
	rels, err := f.all(ctx, activeOf, a.group, me, me)
	if err != nil {
		return err
	}
	if len(rels) == 0 {
		return bot.Userf("❌ Você não pode trair ninguém se não estiver namorando nem casado(a)! 😉")
	}
	var partners []types.JID
	for _, r := range rels {
		partners = append(partners, r.partner(me))
	}
	isPartner := func(j types.JID) bool {
		return slices.ContainsFunc(partners, func(p types.JID) bool { return p.User == j.User })
	}

	lover := a.target
	switch {
	case lover.User == me:
		return bot.Userf("❌ Você não pode se trair consigo mesmo(a)! 🤔")
	case lover.User != "" && isPartner(lover):
		return reply(ctx, c, "❌ Ué? Como você vai trair seu parceiro(a) com ele(a) mesmo(a)? Isso é só amor! 🥰", lover)
	case lover.User == "" && a.info != nil:
		botID := c.Client.Me()
		var cands []types.JID
		for _, p := range a.info.Participants {
			if p.Is(a.author) || p.Is(c.Message.Sender) || p.Is(botID) {
				continue
			}
			j := gamekit.Canonical(a.info, p.JID)
			if !isPartner(j) {
				cands = append(cands, j)
			}
		}
		if len(cands) > 0 {
			lover = cands[f.rand(len(cands))]
		}
	}

	if err := f.exec(ctx, `UPDATE relacionamentos SET traicoes_count = traicoes_count + 1 WHERE `+activeOf, a.group, me, me); err != nil {
		return err
	}
	names := make([]string, len(partners))
	for i, p := range partners {
		names[i] = m(p)
	}
	mentions := append([]types.JID{a.author}, partners...)
	who := "um(a) amante misterioso(a) 🕵️‍♂️"
	if lover.User != "" {
		who = m(lover)
		mentions = append(mentions, lover)
	}
	text := strings.NewReplacer("{autor}", m(a.author), "{conjugue}", strings.Join(names, ", "), "{pessoa}", who).
		Replace(trairFrases[f.rand(len(trairFrases))])
	return reply(ctx, c, text, mentions...)
}

// Duration formata d como "N dia(s) e M hora(s)" etc.
func Duration(d time.Duration) string {
	if d <= 0 {
		return "0 segundo(s)"
	}
	s := int64(d / time.Second)
	mins, hours, days := s/60, s/3600, s/86400
	switch {
	case days > 0:
		return fmt.Sprintf("%d dia(s) e %d hora(s)", days, hours%24)
	case hours > 0:
		return fmt.Sprintf("%d hora(s) e %d minuto(s)", hours, mins%60)
	case mins > 0:
		return fmt.Sprintf("%d minuto(s) e %d segundo(s)", mins, s%60)
	}
	return fmt.Sprintf("%d segundo(s)", s)
}

func (f *Feature) duration(r *rel) string {
	switch {
	case r.status == Ativo:
		return Duration(f.now().Sub(time.UnixMilli(r.criado)))
	case r.terminado > 0:
		return Duration(time.Duration(r.terminado-r.criado) * time.Millisecond)
	}
	return "Sem registro"
}

func (f *Feature) historico(ctx context.Context, c *bot.Context) error {
	a := f.actors(ctx, c)
	me := a.author.User
	rels, err := f.all(ctx, `group_id = ? AND (user1 = ? OR user2 = ?) AND status != 'pendente' ORDER BY status ASC, criado_em DESC LIMIT 30`, a.group, me, me)
	if err != nil {
		return err
	}
	if len(rels) == 0 {
		return reply(ctx, c, fmt.Sprintf("💔 %s, você não tem nenhum relacionamento registrado neste grupo! Que tal tentar *%snamorar* alguém? 😉", m(a.author), c.Prefix), a.author)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "📊 *HISTÓRICO DE RELACIONAMENTOS* 📊\n\n👤 Perfil: %s\n\n", m(a.author))
	mentions := []types.JID{a.author}
	for i, r := range rels {
		p := r.partner(me)
		mentions = append(mentions, p)
		kind, status := "💖 Namoro", "✅ Ativo"
		if r.tipo == Casamento {
			kind = "💍 Casamento"
		}
		if r.status != Ativo {
			status = "💔 Terminado"
		}
		fmt.Fprintf(&b, "%d. *Parceiro(a):* %s\n   *Tipo:* %s\n   *Status:* %s\n   *Duração:* %s\n   *Coisaram:* %d vez(es) 🔥\n   *Traições:* %d vez(es) 😈\n\n",
			i+1, m(p), kind, status, f.duration(r), r.coisas, r.traicoes)
	}
	b.WriteString("Use bastante amor (ou safadeza) no grupo! 😉")
	return reply(ctx, c, b.String(), uniq(mentions)...)
}

func uniq(js []types.JID) []types.JID {
	var out []types.JID
	for _, j := range js {
		if !slices.ContainsFunc(out, func(o types.JID) bool { return o.User == j.User }) {
			out = append(out, j)
		}
	}
	return out
}

func (f *Feature) grupo(ctx context.Context, c *bot.Context) error {
	rels, err := f.all(ctx, `group_id = ? AND status != 'pendente' ORDER BY status ASC, criado_em DESC`, c.Message.Chat.String())
	if err != nil {
		return err
	}
	if len(rels) == 0 {
		return bot.Userf("💔 Nenhum relacionamento foi registrado neste grupo ainda! Sejam os pioneiros pedindo alguém em namoro! 😉")
	}
	var (
		b                                     strings.Builder
		mentions                              []types.JID
		namoros, casamentos, fim, coisas, tra int
		safado, traido                        *rel
		listed                                int
	)
	b.WriteString("👥 *RELACIONAMENTOS DO GRUPO* 👥\n\n")
	for _, r := range rels {
		coisas += r.coisas
		tra += r.traicoes
		if safado == nil || r.coisas > safado.coisas {
			safado = r
		}
		if traido == nil || r.traicoes > traido.traicoes {
			traido = r
		}
		if r.status != Ativo {
			fim++
			continue
		}
		emoji := "💖"
		if r.tipo == Casamento {
			casamentos++
			emoji = "💍"
		} else {
			namoros++
		}
		if listed == 0 {
			b.WriteString("*Relacionamentos Ativos:*\n")
		}
		listed++
		if listed <= 30 {
			fmt.Fprintf(&b, "%d. %s %s %s (Duração: %s)\n", listed, m(r.jid1), emoji, m(r.jid2), f.duration(r))
			mentions = append(mentions, r.jid1, r.jid2)
		}
	}
	if listed == 0 {
		b.WriteString("Não há relacionamentos ativos neste momento. 💔\n")
	}
	fmt.Fprintf(&b, "\n📊 *RESUMO GERAL* 📊\n• 💖 Namoros Ativos: %d\n• 💍 Casamentos Ativos: %d\n• 💔 Relacionamentos Terminados: %d\n• 🔥 Total de Coisadas: %d\n• 😈 Total de Traições: %d\n",
		namoros, casamentos, fim, coisas, tra)
	if safado.coisas > 0 {
		fmt.Fprintf(&b, "\n👑 *Casal Safado:* %s & %s coisaram %d vez(es)! 🔥", m(safado.jid1), m(safado.jid2), safado.coisas)
		mentions = append(mentions, safado.jid1, safado.jid2)
	}
	if traido.traicoes > 0 {
		fmt.Fprintf(&b, "\n🐂 *Casal Mais Traído:* %s & %s tiveram %d traição(ões)! 😱", m(traido.jid1), m(traido.jid2), traido.traicoes)
		mentions = append(mentions, traido.jid1, traido.jid2)
	}
	return reply(ctx, c, strings.TrimRight(b.String(), "\n"), uniq(mentions)...)
}

// Module adapta a funcionalidade ao contrato app.Feature.
type Module struct{}

// Name implementa app.Feature.
func (Module) Name() string { return "relationship" }

// Register implementa app.Feature.
func (Module) Register(ctx context.Context, d *app.Deps) error {
	if err := d.Store.Migrate(ctx, "relationship", migrations); err != nil {
		return err
	}
	return d.Router.Register(New(d.Store.DB(), nil, nil).Commands()...)
}
