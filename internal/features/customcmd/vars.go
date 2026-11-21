package customcmd

import (
	"context"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/joaodobread/ravena-go/internal/bot"
)

// brt é o horário de Brasília (sem horário de verão desde 2019). Fixo para não
// depender do tzdata da imagem.
var brt = time.FixedZone("BRT", -3*60*60)

var weekdays = [...]string{"Domingo", "Segunda-feira", "Terça-feira", "Quarta-feira", "Quinta-feira", "Sexta-feira", "Sábado"}

// Variables documenta as variáveis aceitas (mostradas no !g-variaveis).
var Variables = [][2]string{
	{"{pessoa} / {nomeAutor}", "Nome de quem usou o comando"},
	{"{group} / {nomeGrupo}", "Nome de cadastro do grupo"},
	{"{tituloGrupo}", "Título do grupo no WhatsApp"},
	{"{contador}", "Quantas vezes o comando já foi usado"},
	{"{membroRandom}", "Marca um membro aleatório"},
	{"{mention}", "Marca quem foi mencionado, o autor da mensagem respondida ou alguém aleatório (uma pessoa diferente a cada ocorrência)"},
	{"{singleMention}", "Igual ao {mention}, mas a mesma pessoa em todas as ocorrências"},
	{"{mentionOuEu}", "Igual ao {singleMention}, mas marca quem enviou se não houver menção"},
	{"{mention-5511999999999}", "Marca esse número"},
	{"{day} / {dia}", "Dia da semana"},
	{"{date} / {data}", "Data de hoje (dd/mm/aaaa)"},
	{"{time} / {hora}", "Hora atual (hh:mm:ss)"},
	{"{data-hora} {data-minuto} {data-segundo}", "Partes da hora"},
	{"{data-dia} {data-mes} {data-ano}", "Partes da data"},
	{"{randomPequeno} {randomMedio} {randomGrande} {randomMuitoGrande}", "Número aleatório até 10 / 100 / 1000 / 10000"},
	{"{rndDado-X}", "Dado de X lados"},
	{"{rndDadoRange-X-Y}", "Número aleatório entre X e Y"},
	{"{somaRandoms}", "Soma dos números aleatórios anteriores"},
	{"{cmd-!comando args}", "Sozinho na resposta: roda comandos do bot"},
}

var (
	varRe     = regexp.MustCompile(`\{([A-Za-z][A-Za-z0-9@._-]*)\}`)
	atRe      = regexp.MustCompile(`@(\d{8,25})\b`)
	cmdOnlyRe = regexp.MustCompile(`^(?:\s*\{cmd-[^{}]+\})+\s*$`)
	cmdRe     = regexp.MustCompile(`\{cmd-([^{}]+)\}`)
)

// expander resolve as variáveis de uma resposta.
type expander struct {
	m     *Module
	ctx   context.Context
	msg   *bot.Message
	count int
	now   time.Time

	info     *bot.GroupInfo
	infoDone bool

	mentions []types.JID
	pool     []types.JID // menções ainda não usadas por {mention}
	poolInit bool
	quoted   bool // autor da citada já usado
	single   *types.JID
	sum      int
}

func (m *Module) newExpander(ctx context.Context, msg *bot.Message, count int) *expander {
	return &expander{m: m, ctx: ctx, msg: msg, count: count, now: m.now().In(brt)}
}

func (e *expander) groupInfo() *bot.GroupInfo {
	if !e.infoDone {
		e.infoDone = true
		if e.msg.IsGroup {
			info, err := e.m.client.GroupInfo(e.ctx, e.msg.Chat)
			if err != nil {
				e.m.log.Warn("falha ao ler dados do grupo", "err", err, "chat", e.msg.Chat)
			}
			e.info = info
		}
	}
	return e.info
}

func (e *expander) rnd(lo, hi int) int {
	if hi < lo {
		lo, hi = hi, lo
	}
	n := lo + e.m.rand(hi-lo+1)
	e.sum += n
	return n
}

func (e *expander) mention(jid types.JID) string {
	if !slices.ContainsFunc(e.mentions, func(j types.JID) bool { return j.User == jid.User }) {
		e.mentions = append(e.mentions, jid)
	}
	return bot.MentionText(jid)
}

// randomMember sorteia um membro (exceto o bot).
func (e *expander) randomMember() (types.JID, bool) {
	info := e.groupInfo()
	if info == nil {
		return types.JID{}, false
	}
	me := e.m.client.Me()
	var pool []types.JID
	for _, p := range info.Participants {
		if !p.Is(me) && !p.JID.IsEmpty() {
			pool = append(pool, p.JID)
		}
	}
	if len(pool) == 0 {
		return types.JID{}, false
	}
	return pool[e.m.rand(len(pool))], true
}

// nextMention segue a ordem do Ravena: marcados (sem repetir), autor da
// mensagem respondida e, por fim, um membro aleatório.
func (e *expander) nextMention(fallbackSelf bool) (types.JID, bool) {
	if !e.poolInit {
		e.poolInit = true
		e.pool = slices.Clone(e.msg.Mentions)
		for i := len(e.pool) - 1; i > 0; i-- { // embaralha
			j := e.m.rand(i + 1)
			e.pool[i], e.pool[j] = e.pool[j], e.pool[i]
		}
	}
	if len(e.pool) > 0 {
		j := e.pool[0]
		e.pool = e.pool[1:]
		return j, true
	}
	if q := e.msg.Quoted; q != nil && !e.quoted && !q.Sender.IsEmpty() {
		e.quoted = true
		return q.Sender, true
	}
	if fallbackSelf {
		return e.msg.Sender, true
	}
	return e.randomMember()
}

func (e *expander) authorName() string {
	if e.msg.FromMe || e.msg.SenderIs(e.m.client.Me()) {
		return "ravena"
	}
	if e.msg.PushName != "" {
		return e.msg.PushName
	}
	return "Pessoa"
}

func (e *expander) groupName() string {
	if g := e.m.groupConfig(e.ctx, e.msg.Chat); g != nil && g.Name != "" {
		return g.Name
	}
	if info := e.groupInfo(); info != nil {
		return info.Name
	}
	return ""
}

func two(n int) string { return strconv.Itoa(100 + n)[1:] }

// expand substitui as variáveis da esquerda para a direita e transforma
// "@número" em menções.
func (e *expander) expand(text string) string {
	out := varRe.ReplaceAllStringFunc(text, func(tok string) string {
		if v, ok := e.resolve(tok[1 : len(tok)-1]); ok {
			return v
		}
		return tok
	})
	for _, sm := range atRe.FindAllStringSubmatch(out, -1) {
		e.mention(e.resolveNumber(sm[1]))
	}
	return out
}

func (e *expander) resolve(name string) (string, bool) {
	n := e.now
	switch name {
	case "day", "dia":
		return weekdays[n.Weekday()], true
	case "date", "data":
		return n.Format("02/01/2006"), true
	case "time", "hora":
		return n.Format("15:04:05"), true
	case "data-hora":
		return two(n.Hour()), true
	case "data-minuto":
		return two(n.Minute()), true
	case "data-segundo":
		return two(n.Second()), true
	case "data-dia":
		return two(n.Day()), true
	case "data-mes":
		return two(int(n.Month())), true
	case "data-ano":
		return strconv.Itoa(n.Year()), true
	case "randomPequeno":
		return strconv.Itoa(e.rnd(1, 10)), true
	case "randomMedio":
		return strconv.Itoa(e.rnd(1, 100)), true
	case "randomGrande":
		return strconv.Itoa(e.rnd(1, 1000)), true
	case "randomMuitoGrande":
		return strconv.Itoa(e.rnd(1, 10000)), true
	case "somaRandoms":
		return strconv.Itoa(e.sum), true
	case "pessoa", "nomeAutor":
		return e.authorName(), true
	case "group", "nomeGrupo", "nomeCanal":
		return e.groupName(), true
	case "tituloGrupo":
		if info := e.groupInfo(); info != nil {
			return info.Name, true
		}
		return "", true
	case "contador":
		return strconv.Itoa(e.count), true
	case "membroRandom":
		if j, ok := e.randomMember(); ok {
			return e.mention(j), true
		}
		return "Alguém", true
	case "mention":
		if j, ok := e.nextMention(false); ok {
			return e.mention(j), true
		}
		return "Alguém", true
	case "singleMention", "mentionOuEu":
		if e.single == nil {
			if j, ok := e.nextMention(name == "mentionOuEu"); ok {
				e.single = &j
			}
		}
		if e.single == nil {
			return "Alguém", true
		}
		return e.mention(*e.single), true
	}
	if rest, ok := strings.CutPrefix(name, "rndDadoRange-"); ok {
		a, b, ok := strings.Cut(rest, "-")
		x, err1 := strconv.Atoi(a)
		y, err2 := strconv.Atoi(b)
		if !ok || err1 != nil || err2 != nil || x > 1e9 || y > 1e9 {
			return "", false
		}
		return strconv.Itoa(e.rnd(x, y)), true
	}
	if rest, ok := strings.CutPrefix(name, "rndDado-"); ok {
		x, err := strconv.Atoi(rest)
		if err != nil || x < 1 || x > 1e9 {
			return "", false
		}
		return strconv.Itoa(e.rnd(1, x)), true
	}
	if rest, ok := strings.CutPrefix(name, "mention-"); ok {
		num, _, _ := strings.Cut(rest, "@")
		if len(num) < 8 || len(num) > 25 || strings.Trim(num, "0123456789") != "" {
			return "", false
		}
		return e.mention(e.resolveNumber(num)), true
	}
	return "", false
}

// resolveNumber acha o JID de um número/LID entre os membros do grupo; se não
// achar, assume número de telefone.
func (e *expander) resolveNumber(num string) types.JID {
	if info := e.groupInfo(); info != nil {
		for _, p := range info.Participants {
			switch num {
			case p.LID.User:
				return p.LID
			case p.Phone.User:
				return p.Phone
			case p.JID.User:
				return p.JID
			}
		}
	}
	return types.NewJID(num, types.DefaultUserServer)
}
