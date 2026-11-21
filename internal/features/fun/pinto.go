package fun

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/joaodobread/ravena-go/internal/bot"
)

const (
	minFlaccid, maxFlaccid = 3.0, 12.0
	minErect, maxErect     = 5.0, 25.0
	minGirth, maxGirth     = 8.0, 18.0
	maxPintoScore          = 1000
	pintoCooldown          = 3 * 24 * time.Hour
	pintoCooldownDays      = 3
)

var (
	pintoIntroA = []string{
		"Olá {pessoa}, entre e fique à vontade no consultório do Dr. Raveno! 🩺",
		"Seja bem-vindo(a), {pessoa}. Pode ir tirando a roupa para o exame... 🥼",
		"Ah, {pessoa}! Estava te esperando. Sente-se na maca, por favor. 🛋️",
		"Bom dia, {pessoa}. Pronto para sua avaliação trimestral? 📝",
		"Ora ora, se não é o(a) {pessoa}. Veio finalmente tirar a prova? 🔍",
		"Entre, {pessoa}. Não precisa ter vergonha, já vi de tudo por aqui. 🏥",
		"Aproxime-se, {pessoa}. Vamos ver como andam as coisas... 🧐",
		"Saudações, {pessoa}! O Dr. Raveno está pronto para atendê-lo(a). 🎩",
		"Oi {pessoa}, veio fazer o check-up de rotina? 💉",
		"{pessoa}, você de novo? Veio ver se mudou alguma coisa? 🔄",
		"Bem-vindo(a) à clínica de estética do Dr. Raveno, {pessoa}! ✨",
		"Olha só quem apareceu... Pode entrar, {pessoa}. 🚪",
		"Sente-se, {pessoa}. O procedimento será rápido e indolor (espero). ⚡",
		"Preparado(a) para o veredito, {pessoa}? A ciência não mente! 🔬",
		"Finalmente você criou coragem, {pessoa}! Vamos ao que interessa. 🧪",
	}
	pintoIntroB = []string{
		"Hmm, vejo que hoje o dia está... 'animado' por aqui. 🌡️",
		"Nossa, parece que alguém veio bem preparado para a consulta! 😋",
		"Interessante... O formato me parece bem peculiar. 📐",
		"Pelos meus cálculos iniciais, temos algo digno de nota aqui. 📊",
		"Rapaz, a genética é mesmo uma caixa de surpresas... 🎁",
		"Opa! Quase precisei de uma régua maior para essa primeira olhada. 📏",
		"Calma, deixe-me ajustar meus óculos... Agora sim. 👓",
		"É... Digamos que eu esperava algo diferente, mas vamos prosseguir. 😶",
		"Uau! Acho que o ar condicionado não está afetando em nada aqui. ❄️",
		"Sinto uma energia... potente vindo desta direção. ⚡",
		"O clima esquentou de repente ou é impressão minha? 🔥",
		"De acordo com os manuais de anatomia, isso aqui é raro... 📚",
		"Mantenha a calma. O Dr. Raveno é profissional. 🧤",
		"Sempre fico surpreso com o que encontro nesta profissão... 😮",
		"Tudo bem, respire fundo. O processo de medição vai começar. ⏱️",
	}
	pintoIntroC = []string{
		"Vamos lá, posicione-se para a medição oficial. 📋",
		"Iniciando o escaneamento biométrico em 3... 2... 1... 📡",
		"Vou usar o paquímetro digital de precisão para não haver erros. 🛠️",
		"Não se mova! Qualquer milímetro faz diferença no score final. 🎯",
		"Pronto, agora relaxe enquanto o sistema processa os dados. 💾",
		"Certo, já anotei os valores preliminares. Vamos ao cálculo! ✍️",
		"O resultado vai te surpreender (ou não). 🎰",
		"Lembrando que tamanho não é documento, mas o score é! 🏆",
		"A ciência é absoluta. Veja o que descobrimos: 🧬",
		"Terminei a inspeção visual. Agora, os números frios e calculistas: 🔢",
		"Interessante... Muito interessante mesmo. Veja só: 🧐",
		"Não precisa ficar vermelho(a)! Os dados são confidenciais. 🤫",
		"Segura a emoção que lá vem o resultado! 🎢",
		"Depois de cruzar os dados com o IBGE, chegamos a isto: 🌍",
		"Prontinho! O laudo médico está saindo do forno: 🍞",
	}
	pintoComments = []struct {
		min  int
		text string
	}{
		{1000, "🌌 *DEUS DO OLIMPO!* Isso não é um membro, é um monumento histórico!"},
		{950, "🔥 *LENDÁRIO!* As lendas urbanas falavam de algo assim, mas eu não acreditava!"},
		{900, "⚡ *IMPRESSIONANTE!* Você precisa de uma licença especial para carregar isso?"},
		{850, "🏆 *CAMPEÃO PESO-PESADO!* O Dr. Raveno ficou até sem fôlego!"},
		{800, "🌟 *EXCEPCIONAL!* Um verdadeiro espécime de elite, parabéns!"},
		{750, "💎 *JOIA RARA!* Acima de qualquer expectativa razoável!"},
		{700, "👏 *INCRÍVEL!* Um resultado que impõe respeito em qualquer lugar!"},
		{650, "✨ *BRILHANTE!* Você está muito bem servido, sem dúvidas!"},
		{600, "👍 *MUITO BOM!* Acima da média e com muito potencial!"},
		{550, "✅ *SÓLIDO!* Um resultado respeitável e equilibrado."},
		{500, "😊 *NA MÉDIA!* O famoso 'padrão brasileiro' de qualidade."},
		{450, "🙂 *ACEITÁVEL!* Não ganha medalha, mas também não passa vergonha."},
		{400, "😐 *OK.* Cumpre o que promete, sem grandes firulas."},
		{350, "📉 *UM POUCO ABAIXO.* Talvez seja o frio do consultório?"},
		{300, "😬 *EITA.* O importante é o que importa, certo?"},
		{250, "🔍 *ONDE ESTÁ?* Brincadeira! Mas uma lupinha ajudaria..."},
		{200, "🤏 *COMPACTO!* Ideal para viagens, ocupa pouco espaço."},
		{150, "🧸 *FOFINHO!* Pelo menos é fácil de cuidar."},
		{100, "💀 *F NO CHAT.* A natureza esqueceu de passar na sua casa?"},
		{50, "🐜 *FORMIGUINHA?* É... realmente... uma situação complicada."},
		{math.MinInt, "🔬 *MICROSCÓPICO!* A ciência agradece por poder estudar algo tão minúsculo!"},
	}
)

func pintoComment(score int) string {
	for _, c := range pintoComments {
		if score >= c.min {
			return c.text
		}
	}
	return ""
}

func pintoScore(flaccid, erect, girth, curvature float64) int {
	nf := (flaccid - minFlaccid) / (maxFlaccid - minFlaccid)
	ne := (erect - minErect) / (maxErect - minErect)
	ng := (girth - minGirth) / (maxGirth - minGirth)
	nc := 1 - math.Abs(curvature)/30
	return int(math.Round((nf*0.2 + ne*0.5 + ng*0.2 + nc*0.1) * maxPintoScore))
}

// tenth sorteia um valor em [lo, hi] com uma casa decimal.
func (f *Feature) tenth(lo, hi float64) float64 {
	steps := int(math.Round((hi - lo) * 10))
	return math.Round((lo+float64(f.rand(steps+1))/10)*10) / 10
}

func (f *Feature) pintoCommands() []*bot.Command {
	return []*bot.Command{
		{
			Name:        "pinto",
			Description: "Gera uma avaliação de tamanho aleatória",
			Category:    "jogos",
			GroupOnly:   true,
			Reactions:   bot.Reactions{Before: "📏", After: "🍆", Error: "❌"},
			Run:         f.pinto,
		},
		{
			Name:        "pinto-ranking",
			Description: "Mostra o ranking do jogo",
			Category:    "jogos",
			GroupOnly:   true,
			Cooldown:    30 * time.Second,
			Reactions:   bot.Reactions{After: "🏆", Error: "❌"},
			Run: func(ctx context.Context, c *bot.Context) error {
				text, err := f.pintoRanking(ctx, c)
				if err != nil {
					return err
				}
				return c.Reply(ctx, bot.Text(text))
			},
		},
		{
			Name:        "pinto-reset",
			Description: "Reseta os dados do jogo para este grupo",
			Category:    "jogos",
			GroupOnly:   true,
			AdminOnly:   true,
			Cooldown:    time.Minute,
			Reactions:   bot.Reactions{After: "🔄", Error: "❌"},
			Run:         f.pintoReset,
		},
	}
}

// existingKey acha a identidade já usada pelo autor no grupo (número ou LID).
func (f *Feature) existingKey(ctx context.Context, table string, c *bot.Context) (string, bool, error) {
	id := ids(c.Message)
	var key string
	err := f.db.QueryRowContext(ctx,
		`SELECT user FROM `+table+` WHERE chat = ? AND user IN (?, ?) LIMIT 1`,
		c.Message.Chat.String(), id[0], id[1]).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return id[0], false, nil
	}
	return key, err == nil, err
}

func (f *Feature) pinto(ctx context.Context, c *bot.Context) error {
	chat := c.Message.Chat.String()
	name := authorName(c.Message, "Fulano")
	key, found, err := f.existingKey(ctx, "fun_pinto", c)
	if err != nil {
		return err
	}
	now := f.now()
	if found {
		var last int64
		if err := f.db.QueryRowContext(ctx, `SELECT updated_at FROM fun_pinto WHERE chat = ? AND user = ?`, chat, key).Scan(&last); err != nil {
			return err
		}
		next := time.UnixMilli(last).Add(pintoCooldown)
		if now.Before(next) {
			days := int(math.Ceil(next.Sub(now).Hours() / 24))
			return c.ReplyText(ctx, "⌛️ %s, você já realizou sua avaliação recentemente.\n\nPróxima avaliação disponível em %d dia(s), dia %s.",
				name, days, formatDateTime(next))
		}
	}

	flaccid := f.tenth(minFlaccid, maxFlaccid)
	erect := f.tenth(max(flaccid, minErect), maxErect)
	girth := f.tenth(minGirth, maxGirth)
	curvature := f.tenth(-30, 30)
	score := pintoScore(flaccid, erect, girth, curvature)

	if _, err := f.db.ExecContext(ctx, `
		INSERT INTO fun_pinto(chat, user, name, flaccid, erect, girth, curvature, score, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(chat, user) DO UPDATE SET name = excluded.name, flaccid = excluded.flaccid,
			erect = excluded.erect, girth = excluded.girth, curvature = excluded.curvature,
			score = excluded.score, updated_at = excluded.updated_at`,
		chat, key, name, flaccid, erect, girth, curvature, score, now.UnixMilli()); err != nil {
		return fmt.Errorf("salvando pinto: %w", err)
	}

	abs := math.Abs(curvature)
	curv := fmt.Sprintf("%.1f°, retinho!", abs)
	switch {
	case curvature < -5:
		curv = fmt.Sprintf("%.1f°, torto para esquerda", abs)
	case curvature > 5:
		curv = fmt.Sprintf("%.1f°, torto para direita", abs)
	}
	intro := strings.Replace(f.pick(pintoIntroA), "{pessoa}", "*"+name+"*", 1) + "\n" +
		f.pick(pintoIntroB) + "\n" + f.pick(pintoIntroC)
	return c.ReplyText(ctx, "%s\n\n"+
		"• *Comprimento Flácido:* %.1f cm\n"+
		"• *Comprimento Ereto:* %.1f cm\n"+
		"• *Circunferência:* %.1f cm\n"+
		"• *Curvatura:* %s\n"+
		"• *Score:* _%d pontos_\n\n"+
		"%s\n\n"+
		"> Você pode voltar daqui a %d dias para refazermos sua avaliação.",
		intro, flaccid, erect, girth, curv, score, pintoComment(score), pintoCooldownDays)
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

func (f *Feature) groupName(ctx context.Context, c *bot.Context) string {
	if n, err := c.Client.ChatName(ctx, c.Message.Chat); err == nil && n != "" {
		return n
	}
	return "Grupo"
}

func (f *Feature) pintoRanking(ctx context.Context, c *bot.Context) (string, error) {
	chat := c.Message.Chat.String()
	rows, err := f.db.QueryContext(ctx,
		`SELECT user, name, score FROM fun_pinto WHERE chat = ? ORDER BY score DESC, updated_at LIMIT 10`, chat)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	id := ids(c.Message)
	var b strings.Builder
	n, inTop := 0, false
	for rows.Next() {
		var user, name string
		var score int
		if err := rows.Scan(&user, &name, &score); err != nil {
			return "", err
		}
		if n == 0 {
			fmt.Fprintf(&b, "🍆 *Ranking do Tamanho - %s*\n\n", f.groupName(ctx, c))
		}
		inTop = inTop || user == id[0] || user == id[1]
		fmt.Fprintf(&b, "%s %s: %d pontos\n", medal(n), name, score)
		n++
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if n == 0 {
		return "🏆 Ainda não há dados para o ranking neste grupo. Use " + c.Prefix + "pinto para participar!", nil
	}
	if !inTop {
		var score int
		err := f.db.QueryRowContext(ctx, `SELECT score FROM fun_pinto WHERE chat = ? AND user IN (?, ?) LIMIT 1`, chat, id[0], id[1]).Scan(&score)
		if err == nil {
			var better int
			if err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM fun_pinto WHERE chat = ? AND score > ?`, chat, score).Scan(&better); err != nil {
				return "", err
			}
			fmt.Fprintf(&b, "\n...\n\n%d. Você: %d pontos", better+1, score)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func (f *Feature) pintoReset(ctx context.Context, c *bot.Context) error {
	chat := c.Message.Chat.String()
	var count int
	if err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM fun_pinto WHERE chat = ?`, chat).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return c.ReplyText(ctx, "⚠️ Não há dados do jogo para este grupo.")
	}
	ranking, err := f.pintoRanking(ctx, c)
	if err != nil {
		return err
	}
	if _, err := f.db.ExecContext(ctx, `DELETE FROM fun_pinto WHERE chat = ?`, chat); err != nil {
		return err
	}
	if err := c.Client.Send(ctx, c.Message.Chat, bot.Text(ranking)); err != nil {
		return err
	}
	return c.ReplyText(ctx, "🔄 *Dados do Jogo Pinto Resetados*\n\nForam removidos dados de %d jogadores deste grupo.\n\nO ranking acima mostra como estava antes do reset.", count)
}
