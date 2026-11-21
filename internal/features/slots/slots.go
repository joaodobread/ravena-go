// Package slots implementa o caça-coisas (!slots): moedinhas por usuário
// (globais, não por grupo), prêmios e ranking por grupo. As moedinhas são
// expostas a outras funcionalidades pela interface Wallet (a pesca dá
// moedinhas com a "Ficha de Cassino").
package slots

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/joaodobread/ravena-go/internal/app"
	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/store"
)

// Regras do jogo (iguais às do Ravena original).
const (
	MaxCoins  = 5
	CoinRegen = 2 * time.Minute
)

// Wallet é o saldo de moedinhas do caça-coisas. As moedinhas são globais por
// usuário (user é o número, sem @servidor). AddCoins respeita o teto MaxCoins
// e devolve o novo saldo.
type Wallet interface {
	AddCoins(ctx context.Context, user string, n int) (int, error)
	Coins(ctx context.Context, user string) (int, error)
}

// Tackle é o que o caça-coisas precisa da pesca para dar prêmios de pesca.
// É implementado por *fishing.Module.
type Tackle interface {
	// AddBaits dá n iscas a user.
	AddBaits(ctx context.Context, user string, n int) error
	// GiveRandomUpgrade dá um item de pesca aleatório e devolve seu rótulo
	// (ex.: "📡 Sonar Portátil").
	GiveRandomUpgrade(ctx context.Context, user string) (string, error)
}

var slotEmojis = []string{"🍎", "🍋", "🍒", "🍇", "🍉", "🍓", "🍑", "🍍", "🥭", "💎"}

var junkPrizes = []string{
	"Meia furada", "Papel de bala mascado", "Garrafa pet vazia", "Guarda-chuva quebrado",
	"Pilha estourada", "Chinelo de cor diferente", "Fio dental usado", "CD arranhado da Xuxa",
	"Controle remoto sem tampa", "Carregador com mau contato", "Pente faltando dente",
	"Tupperware sem tampa", "Pregador de roupa quebrado", "Caneta sem carga", "Bateria de 9V lambida",
}

var winMessages = []string{
	"INACREDITÁVEL! Você limpou a banca! 🎰💰",
	"Hoje é seu dia de sorte! O jackpot é seu! ✨",
	"A sorte sorriu para você! Parabéns! 🎉",
	"Você nasceu virado pra lua! Ganhou! 🌕",
	"Os deuses do azar tiraram folga hoje! 🏆",
}

var loseMessages = []string{
	"Não foi dessa vez... A casa sempre vence! 💸",
	"Quase! Só que não. Tente de novo daqui a pouco! 🤡",
	"O azar é seu sobrenome? Que triste... 🙊",
	"Mais sorte na próxima (ou não). 📉",
	"Seu dinheiro foi pro ralo! Literalmente. 🚽",
	"Talvez você devesse tentar algo menos arriscado, tipo... atravessar a rua de olhos fechados? 🚶‍♂️",
	"A banca agradece a sua doação generosa! 🤑",
	"Sinto cheiro de derrota... e é você. 👃",
	"Seu saldo de sorte está negativo. Favor recarregar. 🔋",
}

var migrations = []string{
	`CREATE TABLE slots_users (
		user_id         TEXT PRIMARY KEY,
		user_name       TEXT NOT NULL DEFAULT '',
		total_plays     INTEGER NOT NULL DEFAULT 0,
		total_wins      INTEGER NOT NULL DEFAULT 0,
		coins           INTEGER NOT NULL DEFAULT 5,
		last_coin_regen INTEGER NOT NULL
	)`,
	`CREATE TABLE slots_prizes (
		id         INTEGER PRIMARY KEY,
		user_id    TEXT NOT NULL,
		prize_name TEXT NOT NULL,
		prize_type TEXT NOT NULL,
		at         INTEGER NOT NULL
	)`,
	`CREATE INDEX slots_prizes_user ON slots_prizes(user_id, at)`,
	`CREATE TABLE slots_group_stats (
		group_id    TEXT NOT NULL,
		user_id     TEXT NOT NULL,
		user_name   TEXT NOT NULL DEFAULT '',
		total_plays INTEGER NOT NULL DEFAULT 0,
		total_wins  INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (group_id, user_id)
	)`,
}

// brt é o fuso usado nas datas mostradas (o Brasil não tem mais horário de verão).
var brt = time.FixedZone("BRT", -3*3600)

// Game é o caça-coisas.
type Game struct {
	db     *sql.DB
	tackle Tackle // nil: prêmios de pesca viram moedinhas
	rand   func() float64
	now    func() time.Time
}

// New cria o jogo; rnd e now podem ser nil (padrões).
func New(db *sql.DB, tackle Tackle, rnd func() float64, now func() time.Time) *Game {
	if rnd == nil {
		rnd = rand.Float64
	}
	if now == nil {
		now = time.Now
	}
	return &Game{db: db, tackle: tackle, rand: rnd, now: now}
}

// Migrate cria as tabelas.
func Migrate(ctx context.Context, st *store.Store) error {
	return st.Migrate(ctx, "slots", migrations)
}

func (g *Game) intn(n int) int {
	i := int(g.rand() * float64(n))
	return min(max(i, 0), n-1)
}

// UserKey é a identidade do autor usada nos jogos: o número de telefone
// quando conhecido (o WhatsApp pode mandar o LID como Sender).
func UserKey(m *bot.Message) string {
	if m.Sender.Server == types.HiddenUserServer && m.SenderAlt.Server == types.DefaultUserServer && m.SenderAlt.User != "" {
		return m.SenderAlt.User
	}
	return m.Sender.User
}

type user struct {
	id, name           string
	plays, wins, coins int
	lastRegen          int64 // unix ms
}

// load lê (ou cria) o usuário e aplica a regeneração de moedinhas.
func (g *Game) load(ctx context.Context, tx *sql.Tx, id, name string) (*user, error) {
	now := g.now().UnixMilli()
	u := &user{id: id}
	err := tx.QueryRowContext(ctx, `SELECT user_name, total_plays, total_wins, coins, last_coin_regen FROM slots_users WHERE user_id = ?`, id).
		Scan(&u.name, &u.plays, &u.wins, &u.coins, &u.lastRegen)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		u.coins, u.lastRegen = MaxCoins, now
		if _, err := tx.ExecContext(ctx, `INSERT INTO slots_users(user_id, user_name, coins, last_coin_regen) VALUES(?, ?, ?, ?)`, id, name, u.coins, now); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	}
	if name != "" {
		u.name = name
	}
	g.regen(u, now)
	return u, nil
}

func (g *Game) regen(u *user, now int64) {
	if u.coins >= MaxCoins {
		u.lastRegen = now
		return
	}
	step := CoinRegen.Milliseconds()
	elapsed := now - u.lastRegen
	if n := elapsed / step; n > 0 {
		u.coins = min(u.coins+int(n), MaxCoins)
		u.lastRegen = now - elapsed%step
	}
}

func (u *user) save(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `UPDATE slots_users SET user_name = ?, total_plays = ?, total_wins = ?, coins = ?, last_coin_regen = ? WHERE user_id = ?`,
		u.name, u.plays, u.wins, u.coins, u.lastRegen, u.id)
	return err
}

func (g *Game) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := g.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// AddCoins implementa Wallet.
func (g *Game) AddCoins(ctx context.Context, id string, n int) (int, error) {
	var coins int
	err := g.inTx(ctx, func(tx *sql.Tx) error {
		u, err := g.load(ctx, tx, id, "")
		if err != nil {
			return err
		}
		u.coins = max(min(u.coins+n, MaxCoins), 0)
		coins = u.coins
		return u.save(ctx, tx)
	})
	return coins, err
}

// Coins implementa Wallet.
func (g *Game) Coins(ctx context.Context, id string) (int, error) {
	var coins int
	err := g.inTx(ctx, func(tx *sql.Tx) error {
		u, err := g.load(ctx, tx, id, "")
		if err != nil {
			return err
		}
		coins = u.coins
		return u.save(ctx, tx)
	})
	return coins, err
}

// Commands devolve os comandos do jogo.
func (g *Game) Commands() []*bot.Command {
	return []*bot.Command{
		{
			Name: "slots", Description: "Joga o caça-coisas", Category: "jogos",
			Reactions: bot.Reactions{After: "🎰", Error: "❌"}, Run: g.play,
		},
		{
			Name: "slots-premios", Description: "Lista seus prêmios do caça-coisas", Category: "jogos",
			Cooldown: 10 * time.Second, Reactions: bot.Reactions{After: "🏆", Error: "❌"}, Run: g.prizes,
		},
		{
			Name: "slots-ranking", Description: "Mostra o ranking de vitórias do caça-coisas no grupo", Category: "jogos",
			Cooldown: 10 * time.Second, GroupOnly: true, Reactions: bot.Reactions{After: "🏆", Error: "❌"}, Run: g.ranking,
		},
	}
}

// prize é um prêmio de vitória; os de pesca são entregues depois da transação.
type prize struct {
	kind  string // "junk", "coins", "baits", "upgrade"
	name  string
	count int
}

func (g *Game) rollPrize() prize {
	if g.rand() < 0.3 {
		return prize{kind: "junk", name: junkPrizes[g.intn(len(junkPrizes))]}
	}
	r := g.rand()
	if g.tackle == nil || r < 0.4 {
		return prize{kind: "coins", count: g.intn(5) + 1}
	}
	if r < 0.8 {
		return prize{kind: "baits", count: g.intn(10) + 1}
	}
	return prize{kind: "upgrade"}
}

func (g *Game) play(ctx context.Context, c *bot.Context) error {
	msg := c.Message
	id, name := UserKey(msg), msg.PushName
	if name == "" {
		name = "Jogador"
	}
	var (
		rolls [3]int
		win   bool
		p     prize
		u     *user
		empty bool
	)
	err := g.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		if u, err = g.load(ctx, tx, id, name); err != nil {
			return err
		}
		if u.coins <= 0 {
			empty = true
			return u.save(ctx, tx)
		}
		u.coins--
		u.plays++
		for i := range rolls {
			rolls[i] = g.intn(len(slotEmojis))
		}
		win = rolls[0] == rolls[1] && rolls[1] == rolls[2]
		if msg.IsGroup {
			if _, err := tx.ExecContext(ctx, `INSERT INTO slots_group_stats(group_id, user_id, user_name, total_plays, total_wins) VALUES(?, ?, ?, 1, ?)
				ON CONFLICT(group_id, user_id) DO UPDATE SET total_plays = total_plays + 1, total_wins = total_wins + excluded.total_wins, user_name = excluded.user_name`,
				msg.Chat.String(), id, name, boolInt(win)); err != nil {
				return err
			}
		}
		if !win {
			return u.save(ctx, tx)
		}
		u.wins++
		p = g.rollPrize()
		var label, kind = "", "bom"
		switch p.kind {
		case "junk":
			label, kind = p.name, "normal"
		case "coins":
			u.coins = min(u.coins+p.count, MaxCoins)
			label = fmt.Sprintf("%d Moedinhas", p.count)
		case "baits":
			label = fmt.Sprintf("%d Iscas", p.count)
		}
		if label != "" {
			if err := savePrize(ctx, tx, id, label, kind, g.now()); err != nil {
				return err
			}
		}
		return u.save(ctx, tx)
	})
	if err != nil {
		return err
	}
	if empty {
		wait := CoinRegen - time.Duration(g.now().UnixMilli()-u.lastRegen)*time.Millisecond%CoinRegen
		return c.ReplyText(ctx, "❌ Você não tem moedinhas suficientes para jogar! \n\n🪙 Próxima moedinha em: *%s*", formatWait(wait))
	}

	var b strings.Builder
	b.WriteString("🎰 *CAÇA-COISAS* 🎰\n```----------------------\n")
	fmt.Fprintf(&b, "|  [ %s ] [ %s ] [ %s ]  |\n", slotEmojis[rolls[0]], slotEmojis[rolls[1]], slotEmojis[rolls[2]])
	b.WriteString("----------------------```\n\n")
	if !win {
		fmt.Fprintf(&b, "❌ %s", loseMessages[g.intn(len(loseMessages))])
	} else {
		fmt.Fprintf(&b, "🎊 *%s* 🎊\n\n", winMessages[g.intn(len(winMessages))])
		text, err := g.deliver(ctx, id, p)
		if err != nil {
			return err
		}
		b.WriteString(text)
	}
	fmt.Fprintf(&b, "\n\n> 🪙 Moedinhas: %d/%d", u.coins, MaxCoins)
	return c.ReplyText(ctx, b.String())
}

// deliver entrega prêmios de pesca (fora da transação: o banco tem uma só
// conexão) e monta o texto do prêmio.
func (g *Game) deliver(ctx context.Context, id string, p prize) (string, error) {
	switch p.kind {
	case "junk":
		return fmt.Sprintf("🎁 Você ganhou: *%s*!\n_Incrível como você tem sorte pra ganhar porcaria._ 🙄", p.name), nil
	case "coins":
		return fmt.Sprintf("🎁 VOCÊ GANHOU: *%d Moedinhas*! 🪙\n_Mais chances de girar a sorte!_", p.count), nil
	case "baits":
		if err := g.tackle.AddBaits(ctx, id, p.count); err != nil {
			return "", fmt.Errorf("dando iscas: %w", err)
		}
		return fmt.Sprintf("🎁 VOCÊ GANHOU: *%d Iscas de Pesca*! 🎣\n_Vão direto pro seu balde de pesca._", p.count), nil
	default:
		label, err := g.tackle.GiveRandomUpgrade(ctx, id)
		if err != nil {
			return "", fmt.Errorf("dando item de pesca: %w", err)
		}
		if err := g.inTx(ctx, func(tx *sql.Tx) error { return savePrize(ctx, tx, id, label, "bom", g.now()) }); err != nil {
			return "", err
		}
		return fmt.Sprintf("🎁 VOCÊ GANHOU: *%s*! 🛠️\n_Um item de elite para sua pescaria!_", label), nil
	}
}

func savePrize(ctx context.Context, tx *sql.Tx, id, name, kind string, at time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO slots_prizes(user_id, prize_name, prize_type, at) VALUES(?, ?, ?, ?)`, id, name, kind, at.UnixMilli())
	return err
}

func (g *Game) prizes(ctx context.Context, c *bot.Context) error {
	rows, err := g.db.QueryContext(ctx, `SELECT prize_name, prize_type, at FROM slots_prizes WHERE user_id = ? ORDER BY at DESC, id DESC LIMIT 20`, UserKey(c.Message))
	if err != nil {
		return err
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var (
			name, kind string
			at         int64
		)
		if err := rows.Scan(&name, &kind, &at); err != nil {
			return err
		}
		emoji := "🗑️"
		if kind == "bom" {
			emoji = "🌟"
		}
		fmt.Fprintf(&b, "%s *%s* - _%s_\n", emoji, name, time.UnixMilli(at).In(brt).Format("02/01/2006, 15:04:05"))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if b.Len() == 0 {
		return c.ReplyText(ctx, "🎰 Você ainda não ganhou nenhum prêmio no caça-coisas. Tente a sorte com %sslots!", c.Prefix)
	}
	return c.ReplyText(ctx, "🏆 *SEUS PRÊMIOS* 🏆\n\n"+b.String())
}

var rankEmojis = []string{"🥇", "🥈", "🥉", "🐅", "🐆", "🦌", "🐐", "🐏", "🐓", "🐇"}

func (g *Game) ranking(ctx context.Context, c *bot.Context) error {
	chat := c.Message.Chat.String()
	type row struct {
		name string
		n    int
	}
	query := func(q string) ([]row, error) {
		rows, err := g.db.QueryContext(ctx, q, chat)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.name, &r.n); err != nil {
				return nil, err
			}
			if r.name == "" {
				r.name = "Desconhecido"
			}
			out = append(out, r)
		}
		return out, rows.Err()
	}
	winners, err := query(`SELECT user_name, total_wins FROM slots_group_stats WHERE group_id = ? AND total_wins > 0 ORDER BY total_wins DESC LIMIT 10`)
	if err != nil {
		return err
	}
	losers, err := query(`SELECT user_name, total_plays - total_wins AS losses FROM slots_group_stats WHERE group_id = ? AND losses > 0 ORDER BY losses DESC LIMIT 10`)
	if err != nil {
		return err
	}
	if len(winners) == 0 && len(losers) == 0 {
		return c.ReplyText(ctx, "🎰 Ainda não há dados para o ranking neste grupo. Use *%sslots* para participar!", c.Prefix)
	}
	groupName, _ := c.Client.ChatName(ctx, c.Message.Chat)
	if groupName == "" {
		groupName = "Grupo"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "🎰 *Rankings Caça-coisas - %s* 🏆\n\n", groupName)
	if len(winners) > 0 {
		b.WriteString("🍀 *Top Vencedores (Vitórias)*\n")
		for i, r := range winners {
			fmt.Fprintf(&b, "  %s %d°: %d - %s\n", rankEmojis[i], i+1, r.n, r.name)
		}
		b.WriteString("\n")
	}
	if len(losers) > 0 {
		b.WriteString("🤡 *Ranking de Jogadas sem Vitória (Perdedores)*\n")
		for i, r := range losers {
			fmt.Fprintf(&b, "  %s %d perdas - %s\n", medal(i), r.n, r.name)
		}
	}
	return c.ReplyText(ctx, strings.TrimRight(b.String(), "\n"))
}

func medal(i int) string {
	switch i {
	case 0:
		return "🥇"
	case 1:
		return "🥈"
	case 2:
		return "🥉"
	}
	return fmt.Sprintf("%d.", i+1)
}

func formatWait(d time.Duration) string {
	s := int(d.Round(time.Second) / time.Second)
	return fmt.Sprintf("%dm %ds", s/60, s%60)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// errNotReady é devolvido pela Wallet antes do Register.
var errNotReady = errors.New("slots: módulo ainda não registrado")

// Module adapta o jogo ao contrato app.Feature e é a Wallet usada por outras
// funcionalidades. Use como ponteiro: &slots.Module{Tackle: pesca}.
type Module struct {
	// Tackle, opcional, entrega prêmios de pesca (iscas e itens).
	Tackle Tackle
	game   *Game
}

// Name implementa app.Feature.
func (*Module) Name() string { return "slots" }

// Register implementa app.Feature.
func (m *Module) Register(ctx context.Context, d *app.Deps) error {
	if err := Migrate(ctx, d.Store); err != nil {
		return err
	}
	m.game = New(d.Store.DB(), m.Tackle, nil, nil)
	return d.Router.Register(m.game.Commands()...)
}

// AddCoins implementa Wallet.
func (m *Module) AddCoins(ctx context.Context, user string, n int) (int, error) {
	if m.game == nil {
		return 0, errNotReady
	}
	return m.game.AddCoins(ctx, user, n)
}

// Coins implementa Wallet.
func (m *Module) Coins(ctx context.Context, user string) (int, error) {
	if m.game == nil {
		return 0, errNotReady
	}
	return m.game.Coins(ctx, user)
}

var (
	_ Wallet      = (*Module)(nil)
	_ Wallet      = (*Game)(nil)
	_ app.Feature = (*Module)(nil)
)
