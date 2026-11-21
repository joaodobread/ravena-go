package sequence

import (
	"strconv"
	"strings"
)

// Puzzle é um desafio: a sequência mostrada, as respostas aceitas (já
// normalizadas com gamekit.Fold na comparação) e a explicação do fim.
type Puzzle struct {
	Type        string
	Sequence    string
	Answers     []string
	Explanation string
}

type generator func(rnd func(int) int) Puzzle

const blank = "___"

// hide troca o termo i por "___" e junta com vírgulas.
func hide(seq []string, i int) string {
	out := make([]string, len(seq))
	copy(out, seq)
	out[i] = blank
	return strings.Join(out, ", ")
}

func ints(n []int) []string {
	out := make([]string, len(n))
	for i, v := range n {
		out[i] = strconv.Itoa(v)
	}
	return out
}

func numeric(kind, explanation string, seq []int, missing int) Puzzle {
	s := ints(seq)
	return Puzzle{Type: kind, Sequence: hide(s, missing), Answers: []string{s[missing]}, Explanation: explanation}
}

func reversed[T any](s []T) []T {
	out := make([]T, len(s))
	for i, v := range s {
		out[len(s)-1-i] = v
	}
	return out
}

func inverse(rev bool, s string) string {
	if rev {
		return s
	}
	return ""
}

var fibonacci = func() []int {
	pool := []int{1, 1}
	for {
		next := pool[len(pool)-1] + pool[len(pool)-2]
		if next > 1<<53 {
			return pool
		}
		pool = append(pool, next)
	}
}()

var generators = []generator{
	func(rnd func(int) int) Puzzle { // aritmética
		start, diff := rnd(100), rnd(19)+2
		seq := make([]int, 8)
		for i := range seq {
			seq[i] = start + diff*i
		}
		return numeric("Aritmética", "Progressão Aritmética com razão "+strconv.Itoa(diff)+".", seq, rnd(6)+2)
	},
	func(rnd func(int) int) Puzzle { // geométrica
		cur, ratio := rnd(5)+1, rnd(4)+2
		seq := make([]int, 8)
		for i := range seq {
			seq[i] = cur
			cur *= ratio
		}
		return numeric("Geométrica", "Progressão Geométrica com razão "+strconv.Itoa(ratio)+".", seq, rnd(6)+2)
	},
	func(rnd func(int) int) Puzzle { // fibonacci
		start := rnd(len(fibonacci) - 8)
		return numeric("Fibonacci", "Sequência de Fibonacci (soma dos dois anteriores).", fibonacci[start:start+8], rnd(6)+2)
	},
	func(rnd func(int) int) Puzzle { // quadrados
		start := rnd(10) + 1
		seq := make([]int, 8)
		for i := range seq {
			seq[i] = (start + i) * (start + i)
		}
		return numeric("Quadrados", "Sequência de quadrados perfeitos.", seq, rnd(6)+2)
	},
	func(rnd func(int) int) Puzzle { // dias da semana
		days := []string{"Domingo", "Segunda", "Terça", "Quarta", "Quinta", "Sexta", "Sábado"}
		rev := rnd(2) == 0
		if rev {
			days = reversed(days)
		}
		start := rnd(7)
		seq := make([]string, 5)
		for i := range seq {
			seq[i] = days[(start+i)%7]
		}
		m := rnd(3) + 1
		return Puzzle{Type: "Dias da Semana", Sequence: hide(seq, m), Answers: []string{seq[m]},
			Explanation: "Dias da semana em ordem" + inverse(rev, " inversa") + "."}
	},
	func(rnd func(int) int) Puzzle { // alfabeto
		alpha := strings.Split("ABCDEFGHIJKLMNOPQRSTUVWXYZ", "")
		rev := rnd(2) == 0
		if rev {
			alpha = reversed(alpha)
		}
		start, skip := rnd(len(alpha)-12), rnd(2)+1
		seq := make([]string, 6)
		for i := range seq {
			seq[i] = alpha[start+i*skip]
		}
		m := rnd(4) + 2
		exp := "Letras do alfabeto" + inverse(rev, " (inverso)")
		if skip > 1 {
			exp += " pulando uma"
		}
		return Puzzle{Type: "Alfabeto", Sequence: hide(seq, m), Answers: []string{seq[m]}, Explanation: exp + "."}
	},
	func(rnd func(int) int) Puzzle { // padrão visual
		emojis := []string{"🍎", "⭐", "🔵", "🐱", "🌺"}
		e := emojis[rnd(len(emojis))]
		seq := []string{e, strings.Repeat(e, 2), strings.Repeat(e, 3), strings.Repeat(e, 4)}
		m := rnd(3) + 1
		answers := []string{seq[m], strconv.Itoa(m + 1)}
		switch m + 1 {
		case 2:
			answers = append(answers, "dois", "duas")
		case 3:
			answers = append(answers, "tres")
		case 4:
			answers = append(answers, "quatro")
		}
		return Puzzle{Type: "Padrão Visual", Sequence: hide(seq, m), Answers: answers, Explanation: "Contagem crescente de emojis."}
	},
	func(rnd func(int) int) Puzzle { // planetas
		planets := []string{"Mercúrio", "Vênus", "Terra", "Marte", "Júpiter", "Saturno", "Urano", "Netuno"}
		rev := rnd(2) == 0
		if rev {
			planets = reversed(planets)
		}
		seq := planets[:5]
		m := rnd(4) + 1
		return Puzzle{Type: "Astronomia", Sequence: hide(seq, m), Answers: []string{seq[m]},
			Explanation: "Planetas do sistema solar em ordem" + inverse(rev, " inversa") + " a partir do Sol."}
	},
	func(rnd func(int) int) Puzzle { // contagem regressiva
		start := rnd(20) + 30
		return numeric("Contagem Regressiva", "Números em ordem decrescente.",
			[]int{start, start - 1, start - 2, start - 3, start - 4}, rnd(3)+2)
	},
	func(rnd func(int) int) Puzzle { // lógica mista
		start, add, sub := rnd(20)+5, rnd(4)+2, rnd(3)+1
		seq := []int{start}
		for i := range 7 {
			if i%2 == 0 {
				seq = append(seq, seq[i]+add)
			} else {
				seq = append(seq, seq[i]-sub)
			}
		}
		return numeric("Lógica Mista", "Padrão alternado: soma "+strconv.Itoa(add)+", subtrai "+strconv.Itoa(sub)+".", seq, rnd(5)+3)
	},
	func(rnd func(int) int) Puzzle { // matemática de emojis
		c := emojiAlgebra[rnd(len(emojiAlgebra))]
		return Puzzle{Type: "Matemática de Emojis", Sequence: c.eq, Answers: c.ans, Explanation: c.exp}
	},
}

var emojiAlgebra = []struct {
	eq  string
	ans []string
	exp string
}{
	{"👨 + 👩 = ?", []string{"bebê", "filho", "criança", "baby", "👶"}, "Homem + Mulher = Bebê"},
	{"🌧️ + ☀️ = ?", []string{"arco-íris", "arco íris", "rainbow", "🌈"}, "Chuva + Sol = Arco-íris"},
	{"⚡ + 🌲 = ?", []string{"fogo", "incêndio", "fire", "🔥"}, "Raio + Árvore = Fogo"},
	{"💧 + ❄️ = ?", []string{"gelo", "ice", "cubo de gelo", "🧊"}, "Água + Frio = Gelo"},
	{"🥚 + ⏳ = ?", []string{"pinto", "pintinho", "galinha", "pássaro", "ave", "🐣", "🐥", "🐔"}, "Ovo + Tempo = Nascimento/Ave"},
	{"🐝 + 🌼 = ?", []string{"mel", "honey", "🍯"}, "Abelha + Flor = Mel"},
	{"🍇 + ⏳ = ?", []string{"vinho", "wine", "🍷"}, "Uva + Tempo/Processo = Vinho"},
	{"🐄 + 🥛 = ?", []string{"queijo", "cheese", "🧀"}, "Leite de vaca processado = Queijo"},
	{"🔴 + 🔵 = ?", []string{"roxo", "purple", "violeta", "🟣", "🟪"}, "Vermelho + Azul = Roxo"},
	{"🔵 + 🟡 = ?", []string{"verde", "green", "🟢", "🟩"}, "Azul + Amarelo = Verde"},
	{"⚪ + ⚫ = ?", []string{"cinza", "grey", "gray", "🩶"}, "Branco + Preto = Cinza"},
	{"🔪 + 🍅 = ?", []string{"salada", "salad", "🥗", "fatias"}, "Tomate cortado = Salada"},
	{"🥶 + 🌧️ = ?", []string{"neve", "snow", "❄️", "🌨️"}, "Frio + Chuva = Neve"},
	{"🌎 + 🚀 = ?", []string{"lua", "moon", "espaço", "space", "marte", "alien", "👽", "🌑", "🌕"}, "Terra + Foguete = Viagem Espacial (Lua/Marte)"},
	{"🐛 + ⏳ = ?", []string{"borboleta", "butterfly", "🦋"}, "Lagarta + Tempo = Borboleta"},
	{"👑 + 🦁 = ?", []string{"rei leão", "lion king", "simba"}, "Referência ao filme Rei Leão"},
	{"🧱 + 🧱 + 🧱 = ?", []string{"parede", "muro", "casa", "construção", "wall", "house", "🏠"}, "Tijolos formam parede/casa"},
}

// Random sorteia um desafio.
func Random(rnd func(int) int) Puzzle { return generators[rnd(len(generators))](rnd) }
