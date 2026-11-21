// Package fishing implementa o jogo da pesca (!pescar): iscas que regeneram,
// peixes com peso, inventário limitado, lixo, buffs/debuffs, peixes lendários
// com limite mensal e rankings por grupo. Só texto: a imagem gerada por IA
// do original foi descartada.
package fishing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/joaodobread/ravena-go/internal/app"
	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/store"
)

// Wallet credita moedinhas do caça-coisas (implementada por *slots.Module).
type Wallet interface {
	AddCoins(ctx context.Context, user string, n int) (int, error)
}

var migrations = []string{
	`CREATE TABLE fishing_users (
		user_id            TEXT PRIMARY KEY,
		name               TEXT NOT NULL DEFAULT '',
		baits              INTEGER NOT NULL,
		last_bait_regen    INTEGER NOT NULL,
		total_weight       REAL NOT NULL DEFAULT 0,
		inventory_weight   REAL NOT NULL DEFAULT 0,
		total_catches      INTEGER NOT NULL DEFAULT 0,
		total_baits_used   INTEGER NOT NULL DEFAULT 0,
		total_trash_caught INTEGER NOT NULL DEFAULT 0,
		biggest_name       TEXT NOT NULL DEFAULT '',
		biggest_weight     REAL NOT NULL DEFAULT 0,
		biggest_emoji      TEXT NOT NULL DEFAULT '',
		biggest_rare       INTEGER NOT NULL DEFAULT 0,
		cooldown_until     INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE TABLE fishing_inventory (
		id        INTEGER PRIMARY KEY,
		user_id   TEXT NOT NULL,
		name      TEXT NOT NULL,
		weight    REAL NOT NULL,
		is_rare   INTEGER NOT NULL DEFAULT 0,
		emoji     TEXT NOT NULL DEFAULT '',
		caught_at INTEGER NOT NULL
	)`,
	`CREATE INDEX fishing_inventory_user ON fishing_inventory(user_id)`,
	`CREATE TABLE fishing_group_stats (
		group_id       TEXT NOT NULL,
		user_id        TEXT NOT NULL,
		name           TEXT NOT NULL DEFAULT '',
		total_weight   REAL NOT NULL DEFAULT 0,
		total_catches  INTEGER NOT NULL DEFAULT 0,
		biggest_name   TEXT NOT NULL DEFAULT '',
		biggest_weight REAL NOT NULL DEFAULT 0,
		biggest_emoji  TEXT NOT NULL DEFAULT '',
		biggest_rare   INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (group_id, user_id)
	)`,
	`CREATE TABLE fishing_legendary_history (
		id         INTEGER PRIMARY KEY,
		fish_name  TEXT NOT NULL,
		weight     REAL NOT NULL,
		user_id    TEXT NOT NULL,
		user_name  TEXT NOT NULL,
		group_id   TEXT NOT NULL DEFAULT '',
		group_name TEXT NOT NULL DEFAULT '',
		caught_at  INTEGER NOT NULL,
		year       INTEGER NOT NULL
	)`,
	`CREATE INDEX fishing_legendary_fish ON fishing_legendary_history(fish_name, caught_at)`,
	`CREATE TABLE fishing_buffs (
		id             INTEGER PRIMARY KEY,
		user_id        TEXT NOT NULL,
		effect         TEXT NOT NULL,
		is_debuff      INTEGER NOT NULL DEFAULT 0,
		value          REAL NOT NULL DEFAULT 0,
		min_value      REAL NOT NULL DEFAULT 0,
		max_value      REAL NOT NULL DEFAULT 0,
		remaining_uses INTEGER NOT NULL,
		name           TEXT NOT NULL
	)`,
	`CREATE INDEX fishing_buffs_user ON fishing_buffs(user_id)`,
}

// Migrate cria as tabelas.
func Migrate(ctx context.Context, st *store.Store) error {
	return st.Migrate(ctx, "fishing", migrations)
}

// brt é o fuso das datas e do "mês" do limite de lendários.
var brt = time.FixedZone("BRT", -3*3600)

// Game é o jogo da pesca.
type Game struct {
	db     *sql.DB
	wallet Wallet // nil: sem moedinhas
	rand   func() float64
	now    func() time.Time
	log    *slog.Logger
}

// New cria o jogo; wallet, rnd, now e log podem ser nil.
func New(db *sql.DB, wallet Wallet, rnd func() float64, now func() time.Time, log *slog.Logger) *Game {
	if rnd == nil {
		rnd = rand.Float64
	}
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.Default()
	}
	return &Game{db: db, wallet: wallet, rand: rnd, now: now, log: log}
}

func (g *Game) intn(n int) int {
	return min(max(int(g.rand()*float64(n)), 0), n-1)
}

func (g *Game) between(lo, hi int) int { return g.intn(hi-lo+1) + lo }

// UserKey é a identidade do autor: o número quando conhecido (o WhatsApp
// pode mandar o LID como Sender). Igual a slots.UserKey.
func UserKey(m *bot.Message) string {
	if m.Sender.Server == types.HiddenUserServer && m.SenderAlt.Server == types.DefaultUserServer && m.SenderAlt.User != "" {
		return m.SenderAlt.User
	}
	return m.Sender.User
}

// --- estado ---

type fish struct {
	id     int64
	name   string
	emoji  string
	weight float64
	rare   bool
	chance float64 // só lendários, já com o fator global
}

type buff struct {
	id       int64
	effect   string
	debuff   bool
	value    float64
	min, max float64
	uses     int
	name     string
}

type player struct {
	id, name                 string
	baits                    int
	lastRegen                int64 // unix ms
	totalWeight, invWeight   float64
	catches, baitsUsed, junk int
	biggest                  fish
	cooldownUntil            int64 // unix ms
	fishes                   []fish
	buffs, debuffs           []*buff
}

func (p *player) maxBaits() int {
	n := MaxBaits
	for _, b := range p.buffs {
		if b.effect == effMaxBaits && b.uses > 0 {
			n += int(b.value)
		}
	}
	return n
}

func (p *player) maxInventory() int {
	n := MaxFishPerUser
	for _, b := range p.buffs {
		if b.effect == effInventorySlot && b.uses > 0 {
			n += int(b.value)
		}
	}
	return n
}

// active devolve o primeiro buff/debuff ativo com o efeito.
func active(list []*buff, effect string) *buff {
	for _, b := range list {
		if b.effect == effect && b.uses > 0 {
			return b
		}
	}
	return nil
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

// load lê o jogador (criando se não existir, quando create) e regenera iscas.
// Devolve nil sem erro se não existir e !create.
func (g *Game) load(ctx context.Context, tx *sql.Tx, id string, create bool) (*player, error) {
	now := g.now().UnixMilli()
	p := &player{id: id}
	var rare int
	err := tx.QueryRowContext(ctx, `SELECT name, baits, last_bait_regen, total_weight, inventory_weight, total_catches,
		total_baits_used, total_trash_caught, biggest_name, biggest_weight, biggest_emoji, biggest_rare, cooldown_until
		FROM fishing_users WHERE user_id = ?`, id).
		Scan(&p.name, &p.baits, &p.lastRegen, &p.totalWeight, &p.invWeight, &p.catches, &p.baitsUsed, &p.junk,
			&p.biggest.name, &p.biggest.weight, &p.biggest.emoji, &rare, &p.cooldownUntil)
	p.biggest.rare = rare != 0
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if !create {
			return nil, nil
		}
		p.baits, p.lastRegen = MaxBaits, now
		if _, err := tx.ExecContext(ctx, `INSERT INTO fishing_users(user_id, baits, last_bait_regen) VALUES(?, ?, ?)`, id, p.baits, now); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	}

	rows, err := tx.QueryContext(ctx, `SELECT id, name, emoji, weight, is_rare FROM fishing_inventory WHERE user_id = ? ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var f fish
		if err := rows.Scan(&f.id, &f.name, &f.emoji, &f.weight, &rare); err != nil {
			rows.Close()
			return nil, err
		}
		f.rare = rare != 0
		p.fishes = append(p.fishes, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = tx.QueryContext(ctx, `SELECT id, effect, is_debuff, value, min_value, max_value, remaining_uses, name FROM fishing_buffs WHERE user_id = ? ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		b := &buff{}
		if err := rows.Scan(&b.id, &b.effect, &b.debuff, &b.value, &b.min, &b.max, &b.uses, &b.name); err != nil {
			rows.Close()
			return nil, err
		}
		if b.debuff {
			p.debuffs = append(p.debuffs, b)
		} else {
			p.buffs = append(p.buffs, b)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	g.regen(p, now)
	return p, nil
}

func (g *Game) regen(p *player, now int64) {
	maxB := p.maxBaits()
	if p.baits >= maxB {
		p.lastRegen = now
		return
	}
	step := BaitRegen.Milliseconds()
	elapsed := now - p.lastRegen
	if n := elapsed / step; n > 0 {
		p.baits = min(p.baits+int(n), maxB)
		p.lastRegen = now - elapsed%step
	}
}

func (p *player) save(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `UPDATE fishing_users SET name = ?, baits = ?, last_bait_regen = ?, total_weight = ?,
		inventory_weight = ?, total_catches = ?, total_baits_used = ?, total_trash_caught = ?, biggest_name = ?,
		biggest_weight = ?, biggest_emoji = ?, biggest_rare = ?, cooldown_until = ? WHERE user_id = ?`,
		p.name, p.baits, p.lastRegen, p.totalWeight, p.invWeight, p.catches, p.baitsUsed, p.junk, p.biggest.name,
		p.biggest.weight, p.biggest.emoji, p.biggest.rare, p.cooldownUntil, p.id)
	return err
}

func addBuff(ctx context.Context, tx *sql.Tx, user string, b *buff) error {
	res, err := tx.ExecContext(ctx, `INSERT INTO fishing_buffs(user_id, effect, is_debuff, value, min_value, max_value, remaining_uses, name)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, user, b.effect, b.debuff, b.value, b.min, b.max, b.uses, b.name)
	if err != nil {
		return err
	}
	b.id, err = res.LastInsertId()
	return err
}

// buffFromItem converte um item fisgado/ganho em buff persistente.
func buffFromItem(it item, value float64, debuff bool) *buff {
	uses := max(it.duration, 1)
	if it.effect == effInventorySlot || it.effect == effMaxBaits {
		uses = permanentUses
	}
	return &buff{effect: it.effect, debuff: debuff, value: value, min: float64(it.min), max: float64(it.max), uses: uses, name: it.name}
}

func deleteFish(ctx context.Context, tx *sql.Tx, id int64) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM fishing_inventory WHERE id = ?`, id)
	return err
}

// --- sorteios ---

func round2(x float64) float64 { return math.Round(x*100) / 100 }

// randomFish sorteia o peixe: primeiro os lendários (respeitando o limite
// mensal), depois um peixe comum com peso em 1–80 kg.
func (g *Game) randomFish(ctx context.Context, tx *sql.Tx, p *player) (fish, error) {
	for _, rf := range rareFishes {
		if g.rand() >= rf.finalChance() {
			continue
		}
		n, err := g.monthlyCount(ctx, tx, rf.name)
		if err != nil {
			return fish{}, err
		}
		if n >= rf.limit {
			continue
		}
		base := round2(g.rand()*(legendaryBaseMax-minFishWeight) + minFishWeight)
		return fish{name: rf.name, emoji: rf.emoji, weight: base + rf.bonus, rare: true, chance: rf.finalChance()}, nil
	}

	names := fishNames()
	f := fish{name: names[g.intn(len(names))]}
	if b := active(p.buffs, effGuaranteedWeight); b != nil {
		f.weight = round2(g.rand()*(b.max-b.min) + b.min)
		return f, nil
	}
	if g.rand() < 0.8 {
		f.weight = round2(g.rand()*(difficultyCeiling-minFishWeight) + minFishWeight)
	} else {
		// Faixa "difícil": quanto mais pesado, mais raro.
		factor := 1 - math.Pow(g.rand(), 3)
		f.weight = round2(difficultyCeiling + factor*(difficultyFloor-difficultyCeiling))
	}
	return f, nil
}

func (g *Game) monthlyCount(ctx context.Context, tx *sql.Tx, name string) (int, error) {
	now := g.now().In(brt)
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, brt).UnixMilli()
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM fishing_legendary_history WHERE fish_name = ? AND caught_at >= ?`, name, start).Scan(&n)
	return n, err
}

// roll é o que veio junto (ou no lugar) do peixe.
type roll struct {
	trash  *trash
	item   item
	debuff bool
	value  float64
}

func (r *roll) is(effect string) bool { return r != nil && r.trash == nil && r.item.effect == effect }

// randomItem sorteia lixo, buff ou debuff (ou nada).
func (g *Game) randomItem() *roll {
	if g.rand() < trashChance {
		t := trashItems[g.intn(len(trashItems))]
		return &roll{trash: &t}
	}
	for _, it := range upgrades {
		if g.rand() >= it.chance*buffFactor || (it.effect == effSlotsCoins && g.wallet == nil) {
			continue
		}
		r := &roll{item: it, value: it.value}
		if it.ranged() {
			r.value = float64(g.between(it.min, it.max))
		}
		return r
	}
	for _, it := range downgrades {
		if g.rand() >= it.chance*debuffFactor {
			continue
		}
		r := &roll{item: it, value: it.value, debuff: true}
		if it.ranged() {
			r.value = float64(g.between(it.min, it.max))
		}
		return r
	}
	return nil
}

// --- pescaria ---

// outcome é o resultado de uma pescaria, para montar a resposta.
type outcome struct {
	caught *fish // nil: lixo, roubado ou nada
	effect strings.Builder
	coins  int // moedinhas a creditar depois da transação
	baits  int
	max    int
	big    fish
}

var mentionArg = regexp.MustCompile(`^@\d\d`)

func (g *Game) fish(ctx context.Context, c *bot.Context) error {
	msg := c.Message
	id, name := UserKey(msg), msg.PushName
	if name == "" {
		name = "Pescador"
	}
	var (
		out      outcome
		cooldown bool
		noBaits  bool
	)
	err := g.inTx(ctx, func(tx *sql.Tx) error {
		p, err := g.load(ctx, tx, id, true)
		if err != nil {
			return err
		}
		p.name = name
		now := g.now().UnixMilli()
		if now < p.cooldownUntil {
			cooldown = true
			return nil
		}
		if p.baits <= 0 {
			noBaits = true
			return p.save(ctx, tx) // grava a criação/regeneração
		}
		if err := g.cast(ctx, tx, p, msg, &out); err != nil {
			return err
		}
		return p.save(ctx, tx)
	})
	if err != nil {
		return err
	}
	switch {
	case cooldown:
		_ = c.Client.React(ctx, msg, "😴")
		return bot.ErrSilent
	case noBaits:
		_ = c.Client.React(ctx, msg, "🍥")
		return bot.ErrSilent
	}

	if out.coins > 0 {
		if _, err := g.wallet.AddCoins(ctx, id, out.coins); err != nil {
			g.log.Error("pesca: falha ao creditar moedinhas", "err", err, "user", id)
		}
	}
	if f := out.caught; f != nil && f.rare {
		g.recordLegendary(ctx, c, id, name, *f)
	}
	return c.Reply(ctx, bot.Outgoing{Kind: bot.OutText, Text: g.castText(c, name, &out), Mentions: msg.Mentions})
}

// cast é uma pescaria (dentro da transação): sorteia, aplica itens e buffs,
// gasta a isca, controla o inventário e o cooldown.
func (g *Game) cast(ctx context.Context, tx *sql.Tx, p *player, msg *bot.Message, out *outcome) error {
	cooldown := BaseCooldown
	if d := active(p.debuffs, effLongerCooldown); d != nil {
		cooldown = time.Duration(float64(cooldown) * d.value)
	}

	f, err := g.randomFish(ctx, tx, p)
	if err != nil {
		return err
	}
	var r *roll
	if !f.rare {
		r = g.randomItem()
		if err := g.applyItem(ctx, tx, p, r, out); err != nil {
			return err
		}
	}
	isTrash := r != nil && r.trash != nil
	trashProtected := false
	if isTrash {
		p.junk++
		trashProtected = active(p.debuffs, effBaitOnTrash) != nil || active(p.buffs, effBaitOnTrash) != nil
	} else {
		var msgs []string
		f, msgs = applyBuffs(p, f)
		if len(msgs) > 0 {
			out.effect.WriteString("\n" + strings.Join(msgs, "\n"))
		}
		if r.is(effLoseRecent) {
			// O gato levou: o peixe não entra no inventário nem nas estatísticas.
		} else {
			if err := g.keep(ctx, tx, p, &f, msg); err != nil {
				return err
			}
			out.caught = &f
		}
	}
	if err := decrement(ctx, tx, p, !isTrash, isTrash, f.rare); err != nil {
		return err
	}
	if (!isTrash || !trashProtected) && p.baits > 0 {
		p.baits--
	}
	p.baitsUsed++
	if err := g.trimInventory(ctx, tx, p, out); err != nil {
		return err
	}
	p.cooldownUntil = g.now().Add(cooldown).UnixMilli()
	out.baits, out.max, out.big = p.baits, p.maxBaits(), p.biggest
	return nil
}

// keep guarda o peixe pescado e atualiza as estatísticas.
func (g *Game) keep(ctx context.Context, tx *sql.Tx, p *player, f *fish, msg *bot.Message) error {
	res, err := tx.ExecContext(ctx, `INSERT INTO fishing_inventory(user_id, name, weight, is_rare, emoji, caught_at) VALUES(?, ?, ?, ?, ?, ?)`,
		p.id, f.name, f.weight, f.rare, f.emoji, g.now().UnixMilli())
	if err != nil {
		return err
	}
	if f.id, err = res.LastInsertId(); err != nil {
		return err
	}
	p.fishes = append(p.fishes, *f)
	p.totalWeight += f.weight
	p.invWeight += f.weight
	p.catches++
	if f.weight > p.biggest.weight {
		p.biggest = *f
	}
	if !msg.IsGroup {
		return nil
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO fishing_group_stats(group_id, user_id, name, total_weight, total_catches, biggest_name, biggest_weight, biggest_emoji, biggest_rare)
		VALUES(?, ?, ?, ?, 1, ?, ?, ?, ?)
		ON CONFLICT(group_id, user_id) DO UPDATE SET
			name = excluded.name,
			total_weight = total_weight + excluded.total_weight,
			total_catches = total_catches + 1,
			biggest_name   = CASE WHEN excluded.biggest_weight > biggest_weight THEN excluded.biggest_name ELSE biggest_name END,
			biggest_emoji  = CASE WHEN excluded.biggest_weight > biggest_weight THEN excluded.biggest_emoji ELSE biggest_emoji END,
			biggest_rare   = CASE WHEN excluded.biggest_weight > biggest_weight THEN excluded.biggest_rare ELSE biggest_rare END,
			biggest_weight = MAX(biggest_weight, excluded.biggest_weight)`,
		msg.Chat.String(), p.id, p.name, f.weight, f.name, f.weight, f.emoji, f.rare)
	return err
}

// applyItem aplica o efeito do que foi fisgado junto com o peixe.
func (g *Game) applyItem(ctx context.Context, tx *sql.Tx, p *player, r *roll, out *outcome) error {
	if r == nil {
		return nil
	}
	w := &out.effect
	if t := r.trash; t != nil {
		protector := active(p.debuffs, effBaitOnTrash)
		if protector == nil {
			protector = active(p.buffs, effBaitOnTrash)
		}
		if protector != nil {
			fmt.Fprintf(w, "\n\n%s Você pescou um(a) %s, mas seu %s te salvou de perder a isca!", t.emoji, t.name, protector.name)
		} else {
			fmt.Fprintf(w, "\n\n%s Você pescou um(a) %s. Que pena!", t.emoji, t.name)
		}
		return nil
	}
	it := r.item
	if !r.debuff {
		switch it.effect {
		case effExtraBaits:
			p.baits += int(r.value)
			fmt.Fprintf(w, "\n\n%s Você encontrou um %s! +%d iscas adicionadas (%d/%d).", it.emoji, it.name, int(r.value), p.baits, p.maxBaits())
		case effSlotsCoins:
			out.coins += int(r.value)
			fmt.Fprintf(w, "\n\n%s Você encontrou um %s! +%d moedinhas adicionadas ao seu saldo do !slots.", it.emoji, it.name, int(r.value))
		case effInventorySlot, effMaxBaits:
			b := buffFromItem(it, r.value, false)
			if err := addBuff(ctx, tx, p.id, b); err != nil {
				return err
			}
			p.buffs = append(p.buffs, b) // equipamento vale já nesta pescaria
			if it.effect == effInventorySlot {
				fmt.Fprintf(w, "\n\n%s Você equipou %s! Espaço extra: +%d", it.emoji, it.name, int(r.value))
			} else {
				fmt.Fprintf(w, "\n\n%s Você equipou %s! Limite de iscas aumentado: +%d", it.emoji, it.name, int(r.value))
			}
		default: // buffs com usos: valem a partir da próxima pescaria
			if err := addBuff(ctx, tx, p.id, buffFromItem(it, r.value, false)); err != nil {
				return err
			}
			fmt.Fprintf(w, "\n\n%s Você encontrou um %s! %s", it.emoji, it.name, it.desc)
		}
		return nil
	}

	switch it.effect {
	case effWeightLoss:
		if err := addBuff(ctx, tx, p.id, buffFromItem(it, r.value, true)); err != nil {
			return err
		}
		fmt.Fprintf(w, "\n\n%s 𝕍𝕠𝕔ê 𝕡𝕖𝕤𝕔𝕠𝕦 𝕦𝕞𝕒... 🕯️𝕍𝔼𝕃𝔸 𝔸ℂ𝔼𝕊𝔸?! 😱 𝒪𝒷𝓇𝒶 𝒹𝑜 𝒸𝒶𝓅𝒾𝓇𝑜𝓉𝑜! 🔥👹🩸", it.emoji)
	case effClearInventory:
		if _, err := tx.ExecContext(ctx, `DELETE FROM fishing_inventory WHERE user_id = ?`, p.id); err != nil {
			return err
		}
		p.fishes = nil
		p.totalWeight -= p.invWeight
		p.invWeight = 0
		fmt.Fprintf(w, "\n\n%s OH NÃO! Você encontrou uma %s! Seu inventário de peixes foi destruído!", it.emoji, it.name)
	case effRemoveBaits:
		lost := min(p.baits, int(r.value))
		p.baits -= lost
		fmt.Fprintf(w, "\n\n%s Uma %s apareceu e comeu %d de suas iscas! (%d/%d iscas restantes).", it.emoji, it.name, lost, p.baits, p.maxBaits())
	case effBaitOnTrash, effLongerCooldown:
		if err := addBuff(ctx, tx, p.id, buffFromItem(it, r.value, true)); err != nil {
			return err
		}
		text := "Proteção contra lixo (estranhamente)."
		if it.effect == effLongerCooldown {
			text = "Aumenta o tempo de espera."
		}
		fmt.Fprintf(w, "\n\n%s Você pescou um %s! %s", it.emoji, it.name, text)
	case effLoseSmallest:
		if len(p.fishes) == 0 {
			fmt.Fprintf(w, "\n\n%s Uma %s revirou suas coisas, mas não havia peixes para levar.", it.emoji, it.name)
			return nil
		}
		removed, err := g.dropLight(ctx, tx, p, -1)
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "\n\n%s Uma %s levou seu %s embora!", it.emoji, it.name, removed.name)
	case effLoseRecent:
		fmt.Fprintf(w, "\n\n%s Maldito %s! Ele roubou o peixe que você acabou de pegar!", it.emoji, it.name)
	}
	return nil
}

// dropLight solta um dos 3 peixes mais leves (sorteado), ignorando o de id
// keep se houver outros.
func (g *Game) dropLight(ctx context.Context, tx *sql.Tx, p *player, keep int64) (fish, error) {
	idx := make([]int, 0, len(p.fishes))
	for i, f := range p.fishes {
		if f.id != keep {
			idx = append(idx, i)
		}
	}
	if len(idx) == 0 {
		for i := range p.fishes {
			idx = append(idx, i)
		}
	}
	slices.SortStableFunc(idx, func(a, b int) int {
		switch wa, wb := p.fishes[a].weight, p.fishes[b].weight; {
		case wa < wb:
			return -1
		case wa > wb:
			return 1
		}
		return 0
	})
	i := idx[g.intn(min(3, len(idx)))]
	removed := p.fishes[i]
	if err := deleteFish(ctx, tx, removed.id); err != nil {
		return fish{}, err
	}
	p.fishes = slices.Delete(p.fishes, i, i+1)
	p.invWeight -= removed.weight
	return removed, nil
}

func (g *Game) trimInventory(ctx context.Context, tx *sql.Tx, p *player, out *outcome) error {
	var keep int64 = -1
	if out.caught != nil {
		keep = out.caught.id
	}
	for len(p.fishes) > p.maxInventory() {
		removed, err := g.dropLight(ctx, tx, p, keep)
		if err != nil {
			return err
		}
		fmt.Fprintf(&out.effect, "\n\n⚠️ Inventário cheio! O peixe *%s* (%skg) foi solto.", removed.name, kg(removed.weight))
	}
	return nil
}

// applyBuffs aplica os buffs e debuffs de peso ao peixe.
func applyBuffs(p *player, f fish) (fish, []string) {
	var msgs []string
	for _, b := range p.buffs {
		if b.uses <= 0 {
			continue
		}
		before := f.weight
		switch b.effect {
		case effWeightBoost:
			f.weight = round2(f.weight * (1 + b.value))
			msgs = append(msgs, fmt.Sprintf("🎯 Buff do %s: +%s%% de peso (%skg → %skg)", b.name, num(math.Round(b.value*100)), num(before), num(f.weight)))
		case effNextFishBonus:
			f.weight = round2(f.weight + b.value)
			msgs = append(msgs, fmt.Sprintf("🎯 Buff do %s: +%skg (%skg → %skg)", b.name, num(b.value), num(before), num(f.weight)))
		}
	}
	for _, d := range p.debuffs {
		if d.uses <= 0 || d.effect != effWeightLoss {
			continue
		}
		before := f.weight
		f.weight = round2(f.weight * (1 + d.value))
		msgs = append(msgs, fmt.Sprintf("⬇️ Peixe magro... (%skg → %skg)", num(before), num(f.weight)))
	}
	return f, msgs
}

// decrement gasta um uso dos buffs/debuffs que valeram nesta pescaria.
func decrement(ctx context.Context, tx *sql.Tx, p *player, caughtFish, caughtTrash, rare bool) error {
	for _, list := range [][]*buff{p.buffs, p.debuffs} {
		for _, b := range list {
			if b.uses <= 0 || b.id == 0 {
				continue
			}
			var use bool
			switch b.effect {
			case effLongerCooldown:
				use = true
			case effWeightBoost, effWeightLoss, effNextFishBonus:
				use = caughtFish
			case effGuaranteedWeight:
				use = caughtFish && !rare
			case effBaitOnTrash:
				use = caughtTrash
			}
			if !use {
				continue
			}
			b.uses--
			var err error
			if b.uses <= 0 {
				_, err = tx.ExecContext(ctx, `DELETE FROM fishing_buffs WHERE id = ?`, b.id)
			} else {
				_, err = tx.ExecContext(ctx, `UPDATE fishing_buffs SET remaining_uses = ? WHERE id = ?`, b.uses, b.id)
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (g *Game) recordLegendary(ctx context.Context, c *bot.Context, id, name string, f fish) {
	group, groupName := "", "chat privado"
	if c.Message.IsGroup {
		group = c.Message.Chat.String()
		if n, err := c.Client.ChatName(ctx, c.Message.Chat); err == nil && n != "" {
			groupName = n
		} else {
			groupName = "grupo"
		}
	}
	now := g.now()
	_, err := g.db.ExecContext(ctx, `INSERT INTO fishing_legendary_history(fish_name, weight, user_id, user_name, group_id, group_name, caught_at, year)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, f.name, f.weight, id, name, group, groupName, now.UnixMilli(), now.In(brt).Year())
	if err != nil {
		g.log.Error("pesca: falha ao registrar lendário", "err", err, "fish", f.name)
	}
}

func (g *Game) castText(c *bot.Context, name string, out *outcome) string {
	extra := ""
	if len(c.Args) > 0 && mentionArg.MatchString(c.Args[0]) {
		extra = fmt.Sprintf(", segurando firme na vara de %s, ", c.Args[0])
	}
	effect := out.effect.String()
	f := out.caught
	if f == nil {
		return fmt.Sprintf("🎣 %s jogou a linha %se... %s\n\n> 🐛 Iscas restantes: %d/%d", name, extra, effect, out.baits, out.max)
	}
	var b strings.Builder
	if f.rare {
		fmt.Fprintf(&b, "🏆 INCRÍVEL! _%s_ capturou um(a) _raríssimo_ *%s* de _%s kg_! (%s %s%% de chance)", name, f.name, kg(f.weight), f.emoji, strconv.FormatFloat(f.chance*100, 'f', 5, 64))
	} else {
		fmt.Fprintf(&b, "🎣 %s %spescou um *%s* de _%s kg_!", name, extra, f.name, kg(f.weight))
	}
	switch {
	case f.weight > monsterWeight:
		effect = "\n\n👏 *UM MONSTRO!*" + effect
	case f.weight > hugeWeight:
		effect = "\n\n👏 *ENORME!*" + effect
	}
	fmt.Fprintf(&b, "\n\n> 🐳 Seu maior peixe: %s (%s kg)", out.big.name, kg(out.big.weight))
	fmt.Fprintf(&b, "\n> 🐛 Iscas restantes: %d/%d", out.baits, out.max)
	b.WriteString(effect)
	return b.String()
}

func kg(w float64) string  { return strconv.FormatFloat(w, 'f', 2, 64) }
func num(w float64) string { return strconv.FormatFloat(w, 'f', -1, 64) }

// --- API para o caça-coisas (slots.Tackle) ---

// AddBaits dá n iscas a user (pode passar do limite).
func (g *Game) AddBaits(ctx context.Context, user string, n int) error {
	return g.inTx(ctx, func(tx *sql.Tx) error {
		p, err := g.load(ctx, tx, user, true)
		if err != nil {
			return err
		}
		p.baits += n
		p.lastRegen = g.now().UnixMilli()
		return p.save(ctx, tx)
	})
}

// GiveRandomUpgrade dá a user um item de pesca aleatório e devolve
// "emoji nome".
func (g *Game) GiveRandomUpgrade(ctx context.Context, user string) (string, error) {
	pool := upgrades
	if g.wallet == nil {
		pool = slices.DeleteFunc(slices.Clone(upgrades), func(it item) bool { return it.effect == effSlotsCoins })
	}
	it := pool[g.intn(len(pool))]
	value := it.value
	if it.ranged() {
		value = float64(g.between(it.min, it.max))
	}
	if it.effect == effSlotsCoins {
		if _, err := g.wallet.AddCoins(ctx, user, int(value)); err != nil {
			return "", err
		}
		return it.emoji + " " + it.name, nil
	}
	err := g.inTx(ctx, func(tx *sql.Tx) error {
		p, err := g.load(ctx, tx, user, true)
		if err != nil {
			return err
		}
		if it.effect == effExtraBaits {
			p.baits += int(value)
		} else if err := addBuff(ctx, tx, user, buffFromItem(it, value, false)); err != nil {
			return err
		}
		return p.save(ctx, tx)
	})
	return it.emoji + " " + it.name, err
}

// --- módulo ---

var errNotReady = errors.New("fishing: módulo ainda não registrado")

// Module adapta o jogo ao contrato app.Feature e implementa slots.Tackle.
// Use como ponteiro: &fishing.Module{Wallet: slotsModule}.
type Module struct {
	// Wallet, opcional, recebe as moedinhas da "Ficha de Cassino".
	Wallet Wallet
	game   *Game
}

// Name implementa app.Feature.
func (*Module) Name() string { return "fishing" }

// Register implementa app.Feature.
func (m *Module) Register(ctx context.Context, d *app.Deps) error {
	if err := Migrate(ctx, d.Store); err != nil {
		return err
	}
	m.game = New(d.Store.DB(), m.Wallet, nil, nil, d.Log)
	return d.Router.Register(m.game.Commands()...)
}

// AddBaits implementa slots.Tackle.
func (m *Module) AddBaits(ctx context.Context, user string, n int) error {
	if m.game == nil {
		return errNotReady
	}
	return m.game.AddBaits(ctx, user, n)
}

// GiveRandomUpgrade implementa slots.Tackle.
func (m *Module) GiveRandomUpgrade(ctx context.Context, user string) (string, error) {
	if m.game == nil {
		return "", errNotReady
	}
	return m.game.GiveRandomUpgrade(ctx, user)
}

var _ app.Feature = (*Module)(nil)
