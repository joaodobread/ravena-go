package ai

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/llm"
)

// tarotCooldown é o intervalo entre tiragens do mesmo usuário (só conta
// quando a leitura do LLM deu certo).
const tarotCooldown = 7 * 24 * time.Hour

type tarotCard struct{ name, meaning string }

var tarotCards = []tarotCard{
	{"O Louco", "Novos começos, espontaneidade, salto de fé."},
	{"O Mago", "Manifestação, recursos, poder pessoal, ação."},
	{"A Sacerdotisa", "Intuição, mistério, sabedoria interior."},
	{"A Imperatriz", "Abundância, criatividade, nutrição, natureza."},
	{"O Imperador", "Autoridade, estrutura, estabilidade, controle."},
	{"O Hierofante", "Tradição, conformidade, busca de sentido."},
	{"Os Enamorados", "Escolhas, relacionamentos, alinhamento de valores."},
	{"O Carro", "Determinação, vitória, autocontrole, foco."},
	{"A Justiça", "Equilíbrio, verdade, causa e efeito, integridade."},
	{"O Eremita", "Introspecção, solidão, busca pela verdade."},
	{"A Roda da Fortuna", "Ciclos, destino, mudanças inesperadas, sorte."},
	{"A Força", "Coragem, paciência, compaixão, domínio interior."},
	{"O Enforcado", "Pausa, rendição, ver as coisas por outro ângulo."},
	{"A Morte", "Fim de ciclo, transformação profunda, renovação."},
	{"A Temperança", "Equilíbrio, moderação, paciência, alquimia."},
	{"O Diabo", "Apego, sombras, materialismo, tentação."},
	{"A Torre", "Mudança súbita, destruição de falsas verdades."},
	{"A Estrela", "Esperança, inspiração, cura, serenidade."},
	{"A Lua", "Ilusão, medos, intuição, subconsciente."},
	{"O Sol", "Sucesso, alegria, vitalidade, clareza."},
	{"O Julgamento", "Renovação, despertar, chamado, reflexão."},
	{"O Mundo", "Conclusão, realização, plenitude, viagem."},
	{"Ás de Paus", "Nova inspiração, paixão, oportunidade criativa."},
	{"Dois de Paus", "Planejamento, decisão de avançar, visão inicial."},
	{"Três de Paus", "Expansão, olhar para o horizonte, progresso."},
	{"Quatro de Paus", "Celebração, harmonia no lar, estabilidade alegre."},
	{"Cinco de Paus", "Conflito, competição, tensão, defesa."},
	{"Seis de Paus", "Reconhecimento, sucesso público, vitória merecida."},
	{"Sete de Paus", "Persistência em meio a desafios, defesa de posição."},
	{"Oito de Paus", "Movimento rápido, comunicação veloz, conclusão próxima."},
	{"Nove de Paus", "Resiliência, defensiva, força final antes do fim."},
	{"Dez de Paus", "Sobrecarga, responsabilidade excessiva, cansaço."},
	{"Valete de Paus", "Mensageiro de ideias, entusiasmo, novidade."},
	{"Cavaleiro de Paus", "Impulsividade, aventura, ação enérgica."},
	{"Rainha de Paus", "Confiança, magnetismo, independência, calor."},
	{"Rei de Paus", "Liderança visionária, empreendedorismo, carisma."},
	{"Ás de Copas", "Amor puro, renovação emocional, intuição aberta."},
	{"Dois de Copas", "Conexão profunda, parceria, união equilibrada."},
	{"Três de Copas", "Amizade, celebração em grupo, alegria compartilhada."},
	{"Quatro de Copas", "Apatia, reflexão interior, recusa de ofertas."},
	{"Cinco de Copas", "Luto, foco no que foi perdido, decepção."},
	{"Seis de Copas", "Nostalgia, memórias de infância, inocência."},
	{"Sete de Copas", "Escolhas múltiplas, ilusão, sonhos excessivos."},
	{"Oito de Copas", "Abandono emocional, busca por algo mais profundo."},
	{"Nove de Copas", "Desejo realizado, satisfação emocional, prazer."},
	{"Dez de Copas", "Felicidade familiar completa, alinhamento total."},
	{"Valete de Copas", "Mensagem emocional, sensibilidade, convite amoroso."},
	{"Cavaleiro de Copas", "Romantismo, busca por ideais, proposta afetiva."},
	{"Rainha de Copas", "Empatia profunda, intuição guia, compaixão."},
	{"Rei de Copas", "Estabilidade emocional, controle dos sentimentos, calma."},
	{"Ás de Espadas", "Clareza mental radical, verdade, avanço intelectual."},
	{"Dois de Espadas", "Impasse, negação, decisão difícil a ser tomada."},
	{"Três de Espadas", "Coração partido, separação, dor necessária."},
	{"Quatro de Espadas", "Descanso mental, recuperação, tempo de pausa."},
	{"Cinco de Espadas", "Conflito agressivo, vitória vazia, má fé."},
	{"Seis de Espadas", "Transição difícil, busca por águas calmas, viagem."},
	{"Sete de Espadas", "Estratégia, sigilo, possível desonestidade."},
	{"Oito de Espadas", "Prisão mental, sensação de impotência, restrição."},
	{"Nove de Espadas", "Ansiedade, pesadelos, preocupação excessiva."},
	{"Dez de Espadas", "Fundo do poço, final doloroso, traição final."},
	{"Valete de Espadas", "Vigilância, curiosidade mental, novas ideias."},
	{"Cavaleiro de Espadas", "Foco total, rapidez de pensamento, ambição."},
	{"Rainha de Espadas", "Objetividade, clareza, honestidade direta."},
	{"Rei de Espadas", "Intelecto soberano, autoridade lógica, justiça."},
	{"Ás de Ouros", "Nova oportunidade material, prosperidade inicial."},
	{"Dois de Ouros", "Equilíbrio de recursos, adaptabilidade, prioridade."},
	{"Três de Ouros", "Trabalho em equipe, competência, construção."},
	{"Quatro de Ouros", "Apego material, segurança defensiva, controle financeiro."},
	{"Cinco de Ouros", "Dificuldade financeira, isolamento, perda material."},
	{"Seis de Ouros", "Generosidade, caridade, equilíbrio entre dar e receber."},
	{"Sete de Ouros", "Paciência, avaliação de progresso, investimento longo."},
	{"Oito de Ouros", "Diligência, aperfeiçoamento de técnica, aprendizado."},
	{"Nove de Ouros", "Independência financeira, luxo, colheita própria."},
	{"Dez de Ouros", "Riqueza geracional, herança, segurança a longo prazo."},
	{"Valete de Ouros", "Semente de oferta prática, ambição realista."},
	{"Cavaleiro de Ouros", "Consistência, trabalho árduo, avanço lento mas seguro."},
	{"Rainha de Ouros", "Abundância prática, conforto, generosidade nutridora."},
	{"Rei de Ouros", "Sucesso empresarial, estabilidade material, maestria."},
}

var (
	tarotEntrances = []string{
		"Sente-se... sinto uma energia densa ao seu redor. 🕯️",
		"As estrelas se alinharam para revelar seu destino hoje. 🌠",
		"O véu entre os mundos está fino... vamos ver o que ele esconde. 🌫️",
		"Respire fundo. As cartas não mentem, apenas revelam o que você já sabe. 🧘",
		"Em cada lâmina, uma verdade; em cada símbolo, um caminho. Vamos começar? 🎴",
		"As energias do cosmos me sussurram sua presença... aproxime-se. 🌌",
		"O destino é um rio que corre, e as cartas são os marcos na margem. 🌊",
		"Sinto que você carrega perguntas que apenas o oculto pode responder. 🔮",
		"Entre na luz da clarividência, o futuro aguarda sua primeira olhada. ✨",
		"Os guias sussurram seu nome. Deixe-me ver o que o amanhã lhe reserva. 🎩",
	}
	tarotDraws = []string{
		"As cartas que guiarão este momento são:",
		"As cartas reveladas para sua jornada são:",
		"O destino se manifesta nestas lâminas sagradas:",
		"O que o universo decidiu te mostrar agora:",
		"A mensagem do oculto está nestas três verdades:",
		"Velas acesas e cartas postas... contemple seu sorteio:",
		"As forças invisíveis escolheram estas cartas para você:",
	}
	tarotFallbacks = []string{
		"O véu entre os mundos está muito espesso agora... não consegui uma leitura clara. 🌫️",
		"As energias estão em conflito e minha visão falhou por um momento místico. ⚡",
		"Os oráculos se silenciaram momentaneamente, mas as cartas ainda têm significado. 🤫",
		"Sinto uma interferência astral de Marte... tente novamente mais tarde para uma análise profunda! 🪐",
		"As cartas estão relutantes em falar detalhadamente comigo agora. Vamos ver o básico? 🔮",
		"Uma névoa espiritual cobriu minha bola de cristal hoje. 🌫️",
	}
)

func tarotKey(user string) string { return "ai:tarot:" + user }

func (f *Feature) pick(list []string) string { return list[f.randN(len(list))] }

// drawCards sorteia 3 cartas distintas.
func (f *Feature) drawCards() [3]tarotCard {
	idx := make([]int, len(tarotCards))
	for i := range idx {
		idx[i] = i
	}
	var out [3]tarotCard
	for i := range out {
		j := i + f.randN(len(idx)-i)
		idx[i], idx[j] = idx[j], idx[i]
		out[i] = tarotCards[idx[i]]
	}
	return out
}

func (f *Feature) runTarot(ctx context.Context, c *bot.Context) error {
	msg := c.Message
	user := msg.Sender.User
	name := msg.PushName
	if name == "" {
		name = "Busca-Destino"
	}
	now := f.now()

	if f.Store != nil {
		var last int64
		if ok, err := f.Store.GetJSON(ctx, tarotKey(user), &last); err != nil {
			return err
		} else if ok {
			if left := tarotCooldown - now.Sub(time.Unix(last, 0)); left > 0 {
				days := int(math.Ceil(left.Hours() / 24))
				return c.ReplyText(ctx, "⚖️ *Destino em Pausa* ⏳\n\n*%s*, você já consultou as cartas recentemente.\n\nAguarde o ciclo lunar se completar (%d dias restantes) para uma nova tiragem profunda.", name, days)
			}
		}
	}

	cards := f.drawCards()
	var sb strings.Builder
	fmt.Fprintf(&sb, "🔮 *Cartomante _%s_* 🎩\n\n✨ _%s_ 🪄\n\n🎴 %s\n*%s*, *%s*, *%s*\n\n",
		f.name(), f.pick(tarotEntrances), f.pick(tarotDraws), cards[0].name, cards[1].name, cards[2].name)

	analysis := ""
	if f.LLM != nil {
		prompt := fmt.Sprintf("Faça uma leitura de tarô mística e encorajadora para %s.\nCartas:\n1. %s (Situação)\n2. %s (Desafio)\n3. %s (Caminho)\n",
			name, cards[0].name, cards[1].name, cards[2].name)
		if q := strings.TrimSpace(c.RawArgs()); q != "" {
			prompt += fmt.Sprintf("Foco da tiragem: \"%s\".\n", truncate(q, 300))
		}
		prompt += "Mostre \"Situação, Desafio e Caminho\" numa interpretação única, fluida e pessoal, sem perguntas. " +
			"No fim, escolha UMA área da vida ligada às cartas (finanças, carreira, saúde, amizades, família...) e dê um conselho curto. Evite clichês sobre emoções. Português do Brasil."
		var err error
		analysis, err = f.LLM.Chat(ctx, llm.ChatRequest{
			System:      fmt.Sprintf("Você é uma cartomante experiente, futurista e misteriosa chamada %s. Responda em até 800 caracteres.", f.name()),
			Messages:    []llm.Message{{Role: "user", Text: prompt}},
			Temperature: 0.9,
			MaxTokens:   400,
		})
		if err != nil {
			f.log().Warn("tarot: LLM falhou", "err", err)
		}
		analysis = strings.TrimSpace(analysis)
	}

	if len(analysis) > 50 {
		if f.Store != nil {
			if err := f.Store.SetJSON(ctx, tarotKey(user), now.Unix()); err != nil {
				f.log().Warn("tarot: falha ao gravar cooldown", "err", err)
			}
		}
		sb.WriteString("> ")
		sb.WriteString(strings.ReplaceAll(analysis, "\n", "\n> "))
		return c.Reply(ctx, bot.Text(sb.String()))
	}

	fmt.Fprintf(&sb, "> %s\n\n📝 *Significados Rápidos:*\n", f.pick(tarotFallbacks))
	for _, card := range cards {
		fmt.Fprintf(&sb, "• *%s*: %s\n", card.name, card.meaning)
	}
	sb.WriteString("\n⚠️ _Sua leitura não pôde ser analisada profundamente agora. Por esse motivo, você poderá tentar novamente quando desejar sem aguardar os 7 dias!_")
	return c.Reply(ctx, bot.Text(sb.String()))
}
