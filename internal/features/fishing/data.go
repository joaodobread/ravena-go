package fishing

import (
	_ "embed"
	"slices"
	"strings"
	"sync"
	"time"
)

// Regras do jogo (iguais às do Ravena original).
const (
	BaseCooldown      = 5 * time.Second
	MaxBaits          = 5
	BaitRegen         = time.Hour
	MaxFishPerUser    = 10
	minFishWeight     = 1.0
	difficultyCeiling = 80.0 // DIFFICULTY_THRESHOLD: peso máximo de um peixe comum
	difficultyFloor   = 30.0 // MAX_FISH_WEIGHT: mínimo da faixa "difícil" (20% das vezes)
	legendaryBaseMax  = 30.0
	monsterWeight     = 180.0
	hugeWeight        = 120.0
	permanentUses     = 999999 // equipamentos (inventário/iscas extras)
	equipmentUses     = 100    // acima disso, a ficha mostra como equipamento
)

// Fatores globais (DEFAULT_GLOBAL_FACTORS do original).
const (
	trashChance    = 0.15
	buffFactor     = 1.0
	debuffFactor   = 0.8
	rareFishFactor = 1.65
)

//go:embed peixes.txt
var peixesTxt string

// fishNames é a lista de peixes comuns (extraída de custom-variables.json).
var fishNames = sync.OnceValue(func() []string {
	var out []string
	for l := range strings.Lines(peixesTxt) {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
})

// rareFish é um peixe lendário.
type rareFish struct {
	name   string
	chance float64
	limit  int // por mês, somando todos os jogadores
	bonus  float64
	emoji  string
}

// finalChance é a chance efetiva de cada tentativa.
func (r rareFish) finalChance() float64 { return r.chance * rareFishFactor }

// rareFishes, em ordem crescente de chance (a ordem em que são sorteados).
var rareFishes = sortedBy([]rareFish{
	{"Cthulhu", 0.000003, 1, 26665, "🐙"},
	{"Jörmungandr", 0.000005, 1, 17200, "🌏"},
	{"Ryūjin", 0.00001, 1, 12050, "⛩️"},
	{"Dai Gum Loong", 0.00001, 1, 9100, "🐲"},
	{"Godzilla", 0.00001, 1, 8190, "🦖"},
	{"Leviathan", 0.00001, 1, 7280, "🐉"},
	{"Bakunawa", 0.000015, 1, 7020, "🌑"},
	{"Hydra", 0.000015, 1, 5005, "🐍"},
	{"Charybdis", 0.000015, 1, 5980, "⌛️"},
	{"Megalodon", 0.000015, 1, 5460, "🦈"},
	{"Aspidochelone", 0.000018, 1, 4290, "🏝️"},
	{"Kraken", 0.00002, 1, 6825, "🦑"},
	{"Makara", 0.000021, 1, 3705, "🐊"},
	{"Umibōzu", 0.000024, 1, 3250, "🌫️"},
	{"Nessie", 0.000025, 1, 4095, "🦕"},
	{"Taniwha", 0.000028, 1, 4550, "🗿"},
	{"Moby Dick", 0.00003, 1, 2795, "🐳"},
	{"Kelpie", 0.000035, 1, 2210, "🐎"},
	{"Sedna", 0.000045, 1, 1690, "🧜‍♀️"},
	{"Baleia", 0.00005, 2, 1200, "🐋"},
}, func(r rareFish) float64 { return r.chance })

type trash struct{ name, emoji string }

var trashItems = []trash{
	{"Bota velha", "👢"}, {"Sacola plástica", "🛍️"}, {"Latinha", "🥫"}, {"Mochila rasgada", "🎒"},
	{"Saco de lixo", "🧹"}, {"Pneu furado", "🛞"}, {"Garrafa vazia", "🍾"}, {"Chapéu de pirata", "👒"},
	{"Celular quebrado", "📱"}, {"Relógio parado", "⌚"}, {"Bebê Reborn", "👶"}, {"Faca Velha", "🔪"},
	{"Tesoura Enferrujada", "✂"}, {"Cadeado Sem Chave", "🔒"}, {"Botão de salvar?", "💾"}, {"Hétero", "🔝"},
	{"Microscópio Sujo", "🔬"}, {"Extintor Velho", "🧯"}, {"Camisinha Furada", "🎈"}, {"Conta de Energia", "📜"},
	{"Conta de Água", "📜"}, {"Boleto do Condomínio", "📜"}, {"Siso Cariado", "🦷"}, {"Maiô Rasgado", "🩱"},
	{"Biquíni", "👙"}, {"Anel de Plástico", "💍"}, {"Fita Mimosa", "🎗"}, {"Boia Seca", "🛟"},
	{"Relógio Enferrujado", "⏲"}, {"Imã", "🧲"}, {"Tijolo 6 Furo", "🧱"}, {"Chapa de Raio X", "🩻"},
	{"Fita Fofinha", "🎀"}, {"CD do Araketu", "💿"}, {"Vinil da Xuxa", "💽"}, {"Tamagotchi sem bateria", "🤖"},
	{"Cartucho de Polystation", "🕹️"}, {"Nota de 3 reais", "💸"}, {"Meia furada", "🧦"},
	{"Cigarro de paieiro apagado", "🚬"}, {"Panela de pressão sem pino", "🥘"},
	{"Controle remoto universal que não funciona", "📺"}, {"Convite de casamento de 2005", "💌"},
	{"Resto de marmita", "🥡"}, {"Espelho quebrado", "🪞"}, {"Baralho faltando carta", "🃏"},
	{"Pente sem dente", "🪮"}, {"Óculos sem lente", "👓"}, {"Pacote da Shopee", "📦"}, {"Pacote da OLX", "📦"},
	{"Pacote do Mercado Livre", "📦"}, {"Pacote do AliExpress", "📦"}, {"Pacote da Amazon", "📦"},
	{"Chinelo Havaiana (só o pé esquerdo)", "🩴"}, {"Chinelo Havaiana (só o pé direito)", "🩴"},
	{"Bola 8 de Bilhar", "🎱"}, {"Ursinho de Pelúcia Encharcado", "🧸"},
	{"Semáforo (Como isso veio parar aqui?)", "🚦"}, {"Caixão de Vampiro (miniatura)", "🧛"},
	{"DVD Pirata do Shrek", "📀"}, {"Estátua da Liberdade de Plástico", "🗽"}, {"Cérebro em Formol (Credo!)", "🧠"},
	{"Remo Quebrado", "🛶"}, {"Skate sem Rodas", "🛹"}, {"Assento Sanitário", "🚽"},
	{"Escudo Medieval de Isopor", "🛡️"}, {"Cabeça da Ilha de Páscoa (Peso de papel)", "🗿"},
	{"Teste de DNA (Negativo)", "🧬"}, {"Peruca de Sereia", "🧜‍♀️"}, {"Múmia de Gato", "🐈‍⬛"},
	{"Cabo USB que não conecta", "🔌"}, {"Pizza de Ontem (Molhada)", "🍕"},
}

// Efeitos de itens.
const (
	effGuaranteedWeight = "guaranteed_weight"
	effNextFishBonus    = "next_fish_bonus"
	effWeightBoost      = "weight_boost"
	effExtraBaits       = "extra_baits"
	effSlotsCoins       = "slots_coins"
	effBaitOnTrash      = "bait_on_trash"
	effInventorySlot    = "inventory_slot"
	effMaxBaits         = "max_baits"
	effClearInventory   = "clear_inventory"
	effWeightLoss       = "weight_loss"
	effRemoveBaits      = "remove_baits"
	effLongerCooldown   = "longer_cooldown"
	effLoseSmallest     = "lose_smallest_fish"
	effLoseRecent       = "lose_recent_fish"
)

// item é um buff (upgrade) ou debuff (downgrade) que pode ser fisgado.
type item struct {
	name     string
	chance   float64
	emoji    string
	effect   string
	value    float64
	min, max int // faixa sorteada (iscas, kg, moedinhas) ou de peso garantido
	duration int
	desc     string
}

// ranged diz se o valor do item é sorteado em [min, max] ao ser fisgado.
func (it item) ranged() bool {
	switch it.effect {
	case effExtraBaits, effNextFishBonus, effSlotsCoins, effRemoveBaits:
		return true
	}
	return false
}

var upgrades = sortedBy([]item{
	{name: "Sonar Portátil", chance: 0.02, emoji: "📡", effect: effGuaranteedWeight, min: 40, max: 90, desc: "Garante que o próximo peixe tenha entre 40kg e 90kg."},
	{name: "Minhocão", chance: 0.05, emoji: "🐛", effect: effNextFishBonus, min: 10, max: 30, desc: "Adiciona um bônus de 10 a 30kg ao próximo peixe."},
	{name: "Chapéu de Pescador", chance: 0.05, emoji: "👒", effect: effWeightBoost, value: 0.2, duration: 3, desc: "Aumenta o peso dos próximos 3 peixes em 20%."},
	{name: "Carretel", chance: 0.02, emoji: "🧵", effect: effWeightBoost, value: 0.75, duration: 3, desc: "Aumenta o peso dos próximos 3 peixes em 75%."},
	{name: "Pacote de Iscas", chance: 0.1, emoji: "🎁", effect: effExtraBaits, min: 1, max: 3, desc: "Ganha de 1 a 3 iscas extras."},
	{name: "Ficha de Cassino", chance: 0.04, emoji: "🎰", effect: effSlotsCoins, min: 1, max: 5, desc: "Ganha de 1 a 5 moedinhas para usar no !slots!"},
	{name: "Balança Adulterada", chance: 0.01, emoji: "⚖️", effect: effWeightBoost, value: 1.5, duration: 1, desc: "Aumenta o peso do próximo peixe em 150%!"},
	{name: "Anzol de Titânio", chance: 0.025, emoji: "🔩", effect: effBaitOnTrash, duration: 10, desc: "Evita a perda de isca ao pescar lixo pelas próximas 10 vezes."},
	{name: "Bolso de Pesca", chance: 0.008, emoji: "👖", effect: effInventorySlot, value: 1, desc: "Aumenta seu inventário em 1."},
	{name: "Calça de Pesca", chance: 0.004, emoji: "👖", effect: effInventorySlot, value: 2, desc: "Aumenta seu inventário em 2."},
	{name: "Mochilão", chance: 0.0001, emoji: "🎒", effect: effInventorySlot, value: 4, desc: "Aumenta seu inventário em 4."},
	{name: "Pochete de Iscas", chance: 0.0056, emoji: "👜", effect: effMaxBaits, value: 1, desc: "Aumenta seu limite de iscas em 1."},
	{name: "Caixa de Iscas", chance: 0.0028, emoji: "🧰", effect: effMaxBaits, value: 2, desc: "Aumenta seu limite de iscas em 2."},
	{name: "Viveiro Portátil", chance: 0.0005, emoji: "⛲", effect: effMaxBaits, value: 4, desc: "Aumenta seu limite de iscas em 4."},
}, func(it item) float64 { return it.chance })

var downgrades = sortedBy([]item{
	{name: "Mina Aquática", chance: 0.0003, emoji: "💣", effect: effClearInventory, desc: "Esvazia seu inventário de peixes."},
	{name: "Vela Acesa do 𝒸𝒶𝓅𝒾𝓇𝑜𝓉𝑜", chance: 0.006, emoji: "🕯", effect: effWeightLoss, value: -0.4, duration: 3, desc: "sǝxᴉǝd Ɛ soɯᴉxóɹd sop osǝd o znpǝɹ"},
	{name: "Tartaruga Gulosa", chance: 0.015, emoji: "🐢", effect: effRemoveBaits, min: 1, max: 3, desc: "Remove de 1 a 3 iscas."},
	{name: "Anzol Enferrujado", chance: 0.02, emoji: "🪝", effect: effBaitOnTrash, duration: 3, desc: "Você não perde a isca ao pescar lixo nas próximas 3 vezes que isso acontecer."},
	{name: "Fiscalização Ambiental", chance: 0.005, emoji: "👮", effect: effLongerCooldown, value: 3, duration: 3, desc: "Aumenta o tempo de espera para pescar em 3x nas próximas 3 pescarias."},
	{name: "Enchente Súbita", chance: 0.01, emoji: "🌊", effect: effLoseSmallest, desc: "A correnteza levou seu peixe mais leve embora."},
	{name: "Gato Ladrão", chance: 0.01, emoji: "🐈", effect: effLoseRecent, desc: "Um gato pulou e roubou o peixe que você acabou de pegar!"},
	{name: "Balde Furado", chance: 0.02, emoji: "🗑️", effect: effRemoveBaits, min: 2, max: 4, desc: "Seu balde furou! Você perdeu entre 2 e 4 iscas."},
	{name: "Olho Gordo", chance: 0.03, emoji: "🧿", effect: effWeightLoss, value: -0.8, duration: 2, desc: "O olho gordo dos invejosos reduziu 80% do peso dos seus próximos 2 peixes."},
}, func(it item) float64 { return it.chance })

// itemEmoji acha o emoji de um item pelo nome (para a ficha).
func itemEmoji(name string, debuff bool) string {
	for _, list := range [][]item{upgrades, downgrades} {
		for _, it := range list {
			if it.name == name {
				return it.emoji
			}
		}
	}
	if debuff {
		return "🕯️"
	}
	return "✨"
}

func sortedBy[T any](s []T, key func(T) float64) []T {
	slices.SortStableFunc(s, func(a, b T) int {
		ka, kb := key(a), key(b)
		switch {
		case ka < kb:
			return -1
		case ka > kb:
			return 1
		}
		return 0
	})
	return s
}
