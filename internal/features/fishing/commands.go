package fishing

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/joaodobread/ravena-go/internal/bot"
)

// Commands devolve os comandos do jogo.
func (g *Game) Commands() []*bot.Command {
	return []*bot.Command{
		{
			Name: "pescar", Aliases: []string{"pesca"}, Description: "Pesque um peixe", Category: "jogos",
			Usage: "!pescar [@alguém]", Reactions: bot.Reactions{Before: "🎣", After: "🐟", Error: "❌"}, Run: g.fish,
		},
		{
			Name: "meus-pescados", Aliases: []string{"pesca-ficha", "pesca-dados", "pesca-iscas"}, Description: "Ficha do Pescador",
			Category: "jogos", Cooldown: 5 * time.Second, Reactions: bot.Reactions{After: "🐠", Error: "❌"}, Run: g.sheet,
		},
		{
			Name: "pesca-ranking", Aliases: []string{"pescados"}, Description: "Mostra o ranking de pescaria do grupo atual",
			Usage: "!pesca-ranking [pesado|quantidade]", Category: "jogos", GroupOnly: true, Cooldown: 5 * time.Second,
			Reactions: bot.Reactions{After: "🏆", Error: "❌"}, Run: g.ranking,
		},
		{
			Name: "pesca-info", Description: "Informações do jogo", Category: "jogos", Cooldown: time.Minute,
			Reactions: bot.Reactions{After: "📕", Error: "❌"}, Run: g.info,
		},
		{
			Name: "pesca-lendas", Description: "Mostra os peixes lendários que foram pescados", Usage: "!pesca-lendas [ano]",
			Category: "jogos", Cooldown: 10 * time.Second, Reactions: bot.Reactions{After: "🐉", Error: "❌"}, Run: g.legends,
		},
		{
			Name: "pesca-peixes", Description: "Lista todos os tipos de peixes disponíveis", Category: "jogos",
			Cooldown: 5 * time.Second, Reactions: bot.Reactions{After: "📋", Error: "❌"}, Run: g.species,
		},
		{
			Name: "pesca-reset", Description: "Reseta os dados de pesca para o grupo atual", Category: "jogos",
			AdminOnly: true, GroupOnly: true, Cooldown: 10 * time.Second,
			Reactions: bot.Reactions{Before: "⌛️", After: "✅", Error: "❌"}, Run: g.reset,
		},
	}
}

// sheet é a ficha do pescador.
func (g *Game) sheet(ctx context.Context, c *bot.Context) error {
	name := c.Message.PushName
	if name == "" {
		name = "Pescador"
	}
	var p *player
	err := g.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		if p, err = g.load(ctx, tx, UserKey(c.Message), false); err != nil || p == nil {
			return err
		}
		return p.save(ctx, tx) // grava a regeneração
	})
	if err != nil {
		return err
	}
	if p == nil {
		return c.ReplyText(ctx, "🎣 %s, use %spescar para começar.", name, c.Prefix)
	}

	var b strings.Builder
	maxB := p.maxBaits()
	fmt.Fprintf(&b, "🎣 *Ficha do Pescador: %s*\n\n", name)
	fmt.Fprintf(&b, "🐛 *Iscas:* %d/%d\n", p.baits, maxB)
	if p.baits < maxB {
		step := BaitRegen.Milliseconds()
		next := step - (g.now().UnixMilli()-p.lastRegen)%step
		all := next + int64(maxB-p.baits-1)*step
		fmt.Fprintf(&b, "> Próxima em %d min, todas em %d min\n", ceilMin(next), ceilMin(all))
	}
	fmt.Fprintf(&b, "🎒 *Inventário:* %d/%d\n", len(p.fishes), p.maxInventory())
	fmt.Fprintf(&b, "🎣 *Arremessos:* %d\n🗑️ *Lixos:* %d\n🐟 *Capturas:* %d\n\n", p.baitsUsed, p.junk, p.catches)

	type effect struct {
		icon, name, effect string
		uses, count        int
		value              float64
	}
	var actives, equips []*effect
	find := func(list []*effect, name string) *effect {
		for _, e := range list {
			if e.name == name {
				return e
			}
		}
		return nil
	}
	for _, bf := range slices.Concat(p.buffs, p.debuffs) {
		if bf.uses > equipmentUses {
			e := find(equips, bf.name)
			if e == nil {
				e = &effect{icon: itemEmoji(bf.name, bf.debuff), name: bf.name, effect: bf.effect}
				equips = append(equips, e)
			}
			e.value += bf.value
			e.count++
			continue
		}
		e := find(actives, bf.name)
		if e == nil {
			e = &effect{icon: itemEmoji(bf.name, bf.debuff), name: bf.name}
			actives = append(actives, e)
		}
		e.uses += bf.uses
	}
	if len(actives) > 0 {
		slices.SortStableFunc(actives, func(a, b *effect) int { return a.uses - b.uses })
		b.WriteString("✨ *Efeitos Ativos:*\n")
		for _, e := range actives {
			fmt.Fprintf(&b, "%s %s (%dx)\n", e.icon, e.name, e.uses)
		}
		b.WriteString("\n")
	}
	if len(equips) > 0 {
		slices.SortStableFunc(equips, func(a, b *effect) int { return strings.Compare(a.name, b.name) })
		b.WriteString("🧳 *Equipamentos:*\n")
		for _, e := range equips {
			bonus := "(Perm.)"
			switch e.effect {
			case effInventorySlot:
				bonus = fmt.Sprintf("(+%s inv.)", num(e.value))
			case effMaxBaits:
				bonus = fmt.Sprintf("(+%s iscas)", num(e.value))
			}
			prefix := ""
			if e.count > 1 {
				prefix = fmt.Sprintf("%dx ", e.count)
			}
			fmt.Fprintf(&b, "%s %s%s %s\n", e.icon, prefix, e.name, bonus)
		}
		b.WriteString("\n")
	}

	b.WriteString("📦 *Meus Pescados:*\n")
	if len(p.fishes) == 0 {
		b.WriteString("_Nenhum peixe no momento._\n")
	} else {
		fishes := slices.Clone(p.fishes)
		slices.SortStableFunc(fishes, func(a, b fish) int { return cmp.Compare(b.weight, a.weight) })
		total := 0.0
		for i, f := range fishes {
			total += f.weight
			mark := ""
			if f.rare {
				mark = " " + f.emoji + " RARO!"
			}
			fmt.Fprintf(&b, "%d. %s: %s kg%s\n", i+1, f.name, kg(f.weight), mark)
		}
		fmt.Fprintf(&b, "\n⚖️ *Peso Total:* %s kg\n", kg(total))
	}
	fmt.Fprintf(&b, "\n> Saiba mais sobre o jogo enviando:\n> %spesca-info", c.Prefix)
	return c.ReplyText(ctx, b.String())
}

func ceilMin(ms int64) int64 { return int64(math.Ceil(float64(ms) / 60000)) }

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

// ranking mostra o ranking do grupo: maior peixe (padrão), peso total ou quantidade.
func (g *Game) ranking(ctx context.Context, c *bot.Context) error {
	kind := "biggest"
	for _, a := range c.Args {
		switch strings.ToLower(a) {
		case "quantidade":
			kind = "count"
		case "pesado":
			kind = "weight"
		}
	}
	order := map[string]string{
		"biggest": "biggest_weight DESC",
		"weight":  "total_weight DESC",
		"count":   "total_catches DESC",
	}[kind]
	rows, err := g.db.QueryContext(ctx, `SELECT name, total_weight, total_catches, biggest_name, biggest_weight, biggest_emoji, biggest_rare
		FROM fishing_group_stats WHERE group_id = ? ORDER BY `+order+` LIMIT 10`, c.Message.Chat.String())
	if err != nil {
		return err
	}
	defer rows.Close()
	title := map[string]string{"biggest": "Maior Peixe", "weight": "Peso Total", "count": "Quantidade Total"}[kind]
	var b strings.Builder
	fmt.Fprintf(&b, "🏆 *Ranking de Pescaria deste Grupo* (%s)\n\n", title)
	n := 0
	for rows.Next() {
		var (
			name, bigName, bigEmoji string
			total, bigWeight        float64
			catches                 int
			rare                    bool
		)
		if err := rows.Scan(&name, &total, &catches, &bigName, &bigWeight, &bigEmoji, &rare); err != nil {
			return err
		}
		m := medal(n)
		n++
		switch kind {
		case "weight":
			fmt.Fprintf(&b, "%s %s: %s kg (%d peixes)\n", m, name, kg(total), catches)
		case "count":
			fmt.Fprintf(&b, "%s %s: %d peixes (%s kg)\n", m, name, catches, kg(total))
		default:
			if bigName == "" {
				fmt.Fprintf(&b, "%s %s: Ainda não pescou nenhum peixe\n", m, name)
				continue
			}
			mark := ""
			if rare {
				mark = " " + bigEmoji
			}
			fmt.Fprintf(&b, "%s %s: %s de %s kg%s\n", m, name, bigName, kg(bigWeight), mark)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if n == 0 {
		return c.ReplyText(ctx, "🎣 Ainda não há dados de pescaria neste grupo.")
	}
	b.WriteString("\nOutros rankings disponíveis:")
	if kind != "biggest" {
		fmt.Fprintf(&b, "\n- %spesca-ranking (sem argumentos)", c.Prefix)
	}
	if kind != "weight" {
		fmt.Fprintf(&b, "\n- %spesca-ranking pesado", c.Prefix)
	}
	if kind != "count" {
		fmt.Fprintf(&b, "\n- %spesca-ranking quantidade", c.Prefix)
	}
	return c.ReplyText(ctx, b.String())
}

// legendCounts conta lendários por nome (ano 0 = todos).
func (g *Game) legendCounts(ctx context.Context, year int) (map[string]int, error) {
	q, args := `SELECT fish_name, COUNT(*) FROM fishing_legendary_history`, []any{}
	if year > 0 {
		q += ` WHERE year = ?`
		args = append(args, year)
	}
	rows, err := g.db.QueryContext(ctx, q+` GROUP BY fish_name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var (
			name string
			n    int
		)
		if err := rows.Scan(&name, &n); err != nil {
			return nil, err
		}
		out[name] = n
	}
	return out, rows.Err()
}

func pct(chance float64, digits int) string {
	return strconv.FormatFloat(chance*100, 'f', digits, 64)
}

var yearArg = regexp.MustCompile(`^\d{4}$`)

func (g *Game) legends(ctx context.Context, c *bot.Context) error {
	year := g.now().In(brt).Year()
	if len(c.Args) > 0 && yearArg.MatchString(c.Args[0]) {
		year, _ = strconv.Atoi(c.Args[0])
	}
	rows, err := g.db.QueryContext(ctx, `SELECT fish_name, weight, user_name, group_name, caught_at FROM fishing_legendary_history
		WHERE year = ? ORDER BY caught_at DESC, id DESC LIMIT 50`, year)
	if err != nil {
		return err
	}
	defer rows.Close()
	var reg strings.Builder
	n := 0
	for rows.Next() {
		var (
			name, user, group string
			weight            float64
			at                int64
		)
		if err := rows.Scan(&name, &weight, &user, &group, &at); err != nil {
			return err
		}
		if group == "" {
			group = "misterioso"
		}
		m := medal(n) + " "
		if n > 2 {
			m = fmt.Sprintf("%d. ", n+1)
		}
		n++
		fmt.Fprintf(&reg, "%s*%s* (%s kg)\n   Pescador: %s\n   Local: %s\n   Data: %s\n\n",
			m, name, kg(weight), user, group, time.UnixMilli(at).In(brt).Format("02/01/2006"))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if n == 0 {
		return c.ReplyText(ctx, "🐉 Ainda não foram pescados peixes lendários em %d.", year)
	}
	counts, err := g.legendCounts(ctx, year)
	if err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("🌊 *Lista de Peixes Lendários* 🎣\n")
	for _, f := range rareFishes {
		fmt.Fprintf(&b, "\t%s %s _(~%skg, %s%% de chance, %d pescados em %d)_\n", f.emoji, f.name, num(f.bonus), pct(f.finalChance(), 5), counts[f.name], year)
	}
	fmt.Fprintf(&b, "\n🏆 *REGISTRO DE PEIXES LENDÁRIOS _%d_* 🎖️\n\n", year)
	b.WriteString(reg.String())
	return c.ReplyText(ctx, strings.TrimRight(b.String(), "\n"))
}

func (g *Game) species(ctx context.Context, c *bot.Context) error {
	names := slices.Clone(fishNames())
	slices.Sort(names)
	rows, err := g.db.QueryContext(ctx, `SELECT name, COUNT(*) FROM fishing_inventory WHERE is_rare = 0 GROUP BY name`)
	if err != nil {
		return err
	}
	defer rows.Close()
	inv := map[string]int{}
	for rows.Next() {
		var (
			name string
			n    int
		)
		if err := rows.Scan(&name, &n); err != nil {
			return err
		}
		inv[name] = n
	}
	if err := rows.Err(); err != nil {
		return err
	}
	legends, err := g.legendCounts(ctx, 0)
	if err != nil {
		return err
	}

	var b strings.Builder
	b.WriteString("🐟 *Lista de Peixes Disponíveis*\n_(número de pescados entre parêntese)_\n\n")
	half := (len(names) + 1) / 2
	for i := range half {
		fmt.Fprintf(&b, "%s (%d)", names[i], inv[names[i]])
		if j := i + half; j < len(names) {
			fmt.Fprintf(&b, " | %s (%d)", names[j], inv[names[j]])
		}
		b.WriteString("\n")
	}
	b.WriteString("\n*Peixes Raríssimos*:\n")
	for _, f := range rareFishes {
		fmt.Fprintf(&b, "%s %s: ~%skg (%s%% de chance, %d pescados até hoje)\n", f.emoji, f.name, num(f.bonus), pct(f.finalChance(), 5), legends[f.name])
	}
	fmt.Fprintf(&b, "\n🐛 Use `%spesca-info` para mais informações", c.Prefix)
	return c.ReplyText(ctx, b.String())
}

func (g *Game) info(ctx context.Context, c *bot.Context) error {
	legends, err := g.legendCounts(ctx, 0)
	if err != nil {
		return err
	}
	var totalCatches, totalBaits, totalTrash int
	if err := g.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(total_catches), 0), COALESCE(SUM(total_baits_used), 0), COALESCE(SUM(total_trash_caught), 0) FROM fishing_users`).
		Scan(&totalCatches, &totalBaits, &totalTrash); err != nil {
		return err
	}
	totalLegends := 0
	for _, n := range legends {
		totalLegends += n
	}

	var b strings.Builder
	b.WriteString("🎣 *Informações & Estatísticas do Jogo da Pesca* 🎣\n\n📜 *Regras e Informações Gerais*\n")
	fmt.Fprintf(&b, "- *Iscas Máximas:* `%d` (expansível com itens)\n", MaxBaits)
	fmt.Fprintf(&b, "- *Recarga de Isca:* 1 a cada %d minutos. _(Não é possível alterar este tempo)_\n", int(BaitRegen.Minutes()))
	fmt.Fprintf(&b, "- *Peso dos Peixes:* de `%skg` a `%skg`\n", num(minFishWeight), num(difficultyCeiling))
	fmt.Fprintf(&b, "- *Peixes:* `%d` tipos (`%spesca-peixes` para ver)\n\n", len(fishNames()), c.Prefix)

	b.WriteString("🐲 *Peixes Lendários*\n_Chance de encontrar um destes seres místicos:_\n")
	for _, f := range rareFishes {
		fmt.Fprintf(&b, "  %s *%s*: `%s%%` de chance, %d pescados até hoje\n", f.emoji, f.name, pct(f.finalChance(), 4), legends[f.name])
	}
	b.WriteString("\n✨ *Buffs*\n_Itens que te ajudam na pescaria:_\n")
	for _, it := range upgrades {
		fmt.Fprintf(&b, "  %s *%s*: %s\n", it.emoji, it.name, it.desc)
	}
	b.WriteString("\n🔥 *Debuffs*\n_Cuidado com o que você fisga!_\n")
	for _, it := range downgrades {
		fmt.Fprintf(&b, "  %s *%s*: %s\n", it.emoji, it.name, it.desc)
	}
	b.WriteString("\n🧹 *Lixos Pescáveis*\n_Nem tudo que reluz é peixe..._\n`")
	for i, t := range trashItems {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(t.emoji + " " + t.name)
	}
	b.WriteString("`\n\n📊 *Estatísticas Globais de Pesca*\n")
	fmt.Fprintf(&b, "🐟 *Total de Peixes Pescados:* %d\n🐛 *Total de Iscas Usadas:* %d\n🧹 *Total de Lixo Coletado:* %d\n🐲 *Total de Lendas Encontradas:* %d\n",
		totalCatches, totalBaits, totalTrash, totalLegends)

	var user, fishName string
	var weight float64
	err = g.db.QueryRowContext(ctx, `SELECT name, biggest_name, biggest_weight FROM fishing_users WHERE biggest_weight > 0 ORDER BY biggest_weight DESC LIMIT 1`).Scan(&user, &fishName, &weight)
	if err == nil {
		fmt.Fprintf(&b, "🏆 *Maior Peixe da História:* %s com `%s kg`, pescado por _%s_\n", fishName, kg(weight), user)
	} else if err != sql.ErrNoRows {
		return err
	}
	var catches int
	err = g.db.QueryRowContext(ctx, `SELECT name, total_catches FROM fishing_users WHERE total_catches > 0 ORDER BY total_catches DESC LIMIT 1`).Scan(&user, &catches)
	if err == nil {
		fmt.Fprintf(&b, "🥇 *Pescador Mais Dedicado:* _%s_ com `%d` peixes pescados\n", user, catches)
	} else if err != sql.ErrNoRows {
		return err
	}
	return c.ReplyText(ctx, strings.TrimRight(b.String(), "\n"))
}

func (g *Game) reset(ctx context.Context, c *bot.Context) error {
	res, err := g.db.ExecContext(ctx, `DELETE FROM fishing_group_stats WHERE group_id = ?`, c.Message.Chat.String())
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return c.ReplyText(ctx, "ℹ️ Não há dados de pesca para este grupo.")
	}
	return c.ReplyText(ctx, "✅ Dados de pesca resetados com sucesso!\n\n%d jogadores tiveram seus dados de pesca neste grupo apagados.", n)
}
