// Package menu implementa o !menu (ou !cmd, !comandos, !info): a lista de
// comandos registrados, agrupada por categoria.
package menu

import (
	"cmp"
	"context"
	_ "embed"
	"fmt"
	"slices"
	"strings"
	"sync"

	"go.mau.fi/whatsmeow/types"

	"github.com/joaodobread/ravena-go/internal/app"
	"github.com/joaodobread/ravena-go/internal/bot"
)

//go:embed cmd_header.txt
var header string

// ExtraLister fornece linhas extras para o menu (ex.: comandos personalizados
// do grupo). Cada entrada é uma linha já formatada, sem o marcador "• ".
type ExtraLister interface {
	MenuEntries(ctx context.Context, chat types.JID) []string
}

// Lister é o que o menu precisa do Router.
type Lister interface {
	Commands() []*bot.Command
}

// categoryOrder é a ordem de apresentação (MenuOrder.js); categorias fora da
// lista aparecem depois, em ordem alfabética, e "resto" por último.
var categoryOrder = []string{
	"geral", "grupo", "midia", "ia", "utilidades", "jogos", "áudio", "diversao",
	"zoeira", "downloaders", "imagens", "tts", "busca", "cultura", "listas",
	"saude", "general", "info", "arquivos",
}

var categoryEmoji = map[string]string{
	"geral": "📃", "grupo": "👥", "midia": "📱", "ia": "🤖", "utilidades": "🛠️",
	"jogos": "🎮", "áudio": "🔈", "diversao": "🧸", "zoeira": "🤪", "downloaders": "📤",
	"imagens": "📷", "tts": "🗣", "busca": "🔎", "cultura": "🍿", "listas": "📜",
	"saude": "🥗", "general": "🖨️", "info": "ℹ️", "arquivos": "📂", "resto": "❓",
}

// commandOrder é a ordem preferida dos comandos dentro de uma categoria
// (COMMAND_ORDER do MenuOrder.js, só com os comandos que existem por aqui).
var commandOrder = []string{
	"cmd", "menu", "ajuda", "avisos", "status", "doar", "convite", "grupao", "codigo",
	"atencao", "ignorar", "apagar", "apelido", "faladores", "clima", "news", "stt",
	"traduzir", "lembretes", "lembrar", "comida", "sticker", "s", "fig", "sq", "sqc",
	"sqb", "sqe", "sqi", "removebg", "distort", "neon", "oil", "pixelate", "sketch",
	"ai", "imagine", "resumo", "interagir", "roletarussa", "roletaranking", "pescar",
	"pesca-iscas", "pesca-ranking", "pescados", "pesca-info", "pesca-lendas",
	"sequencia", "pinto", "pinto-ranking", "slots", "slots-premios", "tarot", "anonimo",
	"roll", "d10", "lol", "lol-build", "valorant", "wr", "wr-build", "anime", "imdb",
	"tts", "buscar", "gif", "wiki", "lastfm", "qr", "qr-wifi", "qr-pix",
}

const maxCached = 16 // prefixos diferentes guardados

// Feature monta o menu a partir dos comandos registrados.
type Feature struct {
	cmds  Lister
	extra ExtraLister   // nil: sem comandos extras
	title func() string // nome do bot (pode mudar no painel)

	mu    sync.Mutex
	count int               // nº de comandos quando o cache foi montado
	cache map[string]string // prefixo -> corpo renderizado
}

// New cria o menu. extra e botName podem ser nil.
func New(cmds Lister, extra ExtraLister, botName func() string) *Feature {
	if botName == nil {
		botName = func() string { return "Ravena" }
	}
	return &Feature{cmds: cmds, extra: extra, title: botName}
}

// Commands devolve o comando !menu.
func (f *Feature) Commands() []*bot.Command {
	return []*bot.Command{{
		Name:        "menu",
		Aliases:     []string{"cmd", "comandos", "info"},
		Description: "Lista os comandos (ou só os de uma categoria)",
		Usage:       "!menu [categoria]",
		Category:    "geral",
		Run:         f.run,
	}}
}

func (f *Feature) run(ctx context.Context, c *bot.Context) error {
	if len(c.Args) > 0 {
		text, err := f.category(c.Prefix, c.ArgText())
		if err != nil {
			return err
		}
		return c.ReplyText(ctx, "%s", text)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "🤖 *Comandos %s* 🐦‍⬛\n", capitalize(f.title()))
	if c.Message.IsGroup {
		if name, err := c.Client.ChatName(ctx, c.Message.Chat); err == nil && name != "" {
			fmt.Fprintf(&b, "> _Grupo %s_\n", name)
		}
	}
	b.WriteString(strings.TrimSpace(header))
	b.WriteString("\n")
	b.WriteString(f.body(c.Prefix))
	if f.extra != nil {
		if lines := f.extra.MenuEntries(ctx, c.Message.Chat); len(lines) > 0 {
			b.WriteString("\n🧩 *Comandos personalizados:*\n")
			for _, l := range lines {
				b.WriteString("• " + l + "\n")
			}
		}
	}
	fmt.Fprintf(&b, "\n💡 Use *%smenu <categoria>* para ver só uma categoria.", c.Prefix)
	return c.ReplyText(ctx, "%s", b.String())
}

// visible devolve os comandos que aparecem no menu.
func (f *Feature) visible() []*bot.Command {
	all := f.cmds.Commands()
	return slices.DeleteFunc(all, func(c *bot.Command) bool { return c.Hidden || c.SuperAdminOnly })
}

// body devolve as categorias renderizadas para prefix, com cache invalidado
// quando o número de comandos registrados muda.
func (f *Feature) body(prefix string) string {
	all := f.cmds.Commands()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cache == nil || f.count != len(all) {
		f.cache, f.count = map[string]string{}, len(all)
	}
	if s, ok := f.cache[prefix]; ok {
		return s
	}
	s := render(prefix, f.visible())
	if len(f.cache) >= maxCached {
		clear(f.cache)
	}
	f.cache[prefix] = s
	return s
}

func render(prefix string, cmds []*bot.Command) string {
	groups := groupByCategory(cmds)
	var b strings.Builder
	for _, cat := range sortedCategories(groups) {
		fmt.Fprintf(&b, "\n%s *%s:*\n", emojiFor(cat), categoryTitle(cat))
		for _, cmd := range groups[cat] {
			b.WriteString(formatCommand(prefix, cmd))
			b.WriteString("\n")
		}
	}
	return b.String()
}

// category renderiza só uma categoria.
func (f *Feature) category(prefix, name string) (string, error) {
	groups := groupByCategory(f.visible())
	want := fold(name)
	for cat, cmds := range groups {
		if fold(cat) != want {
			continue
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%s *Comandos de %s:*\n\n", emojiFor(cat), categoryTitle(cat))
		for _, cmd := range cmds {
			b.WriteString(formatCommand(prefix, cmd))
			b.WriteString("\n")
		}
		return strings.TrimRight(b.String(), "\n"), nil
	}
	cats := sortedCategories(groups)
	return "", bot.Userf("Categoria '%s' não encontrada. Categorias: %s", name, strings.Join(cats, ", "))
}

func groupByCategory(cmds []*bot.Command) map[string][]*bot.Command {
	groups := map[string][]*bot.Command{}
	for _, c := range cmds {
		cat := strings.ToLower(strings.TrimSpace(c.Category))
		if cat == "" {
			cat = "resto"
		}
		groups[cat] = append(groups[cat], c)
	}
	for _, list := range groups {
		slices.SortStableFunc(list, compareCommands)
	}
	return groups
}

func compareCommands(a, b *bot.Command) int {
	ia, ib := slices.Index(commandOrder, a.Name), slices.Index(commandOrder, b.Name)
	switch {
	case ia >= 0 && ib >= 0:
		return cmp.Compare(ia, ib)
	case ia >= 0:
		return -1
	case ib >= 0:
		return 1
	}
	return cmp.Compare(a.Name, b.Name)
}

func sortedCategories(groups map[string][]*bot.Command) []string {
	cats := make([]string, 0, len(groups))
	for c := range groups {
		cats = append(cats, c)
	}
	rank := func(c string) int {
		if c == "resto" {
			return len(categoryOrder) + 1
		}
		if i := slices.Index(categoryOrder, c); i >= 0 {
			return i
		}
		return len(categoryOrder)
	}
	slices.SortFunc(cats, func(a, b string) int {
		return cmp.Or(cmp.Compare(rank(a), rank(b)), cmp.Compare(a, b))
	})
	return cats
}

// formatCommand gera "• *!nome* (!a, !b) 🖼: descrição".
func formatCommand(prefix string, c *bot.Command) string {
	var b strings.Builder
	b.WriteString("• *" + prefix + c.Name + "*")
	if len(c.Aliases) > 0 {
		b.WriteString(" (" + prefix + strings.Join(c.Aliases, ", "+prefix) + ")")
	}
	if len(c.Triggers) > 0 {
		b.WriteString(" " + c.Triggers[0])
	}
	if c.Description != "" {
		b.WriteString(": " + c.Description)
	}
	return b.String()
}

func emojiFor(cat string) string {
	if e, ok := categoryEmoji[cat]; ok {
		return e
	}
	return "❓"
}

func categoryTitle(cat string) string {
	if len([]rune(cat)) < 4 {
		return strings.ToUpper(cat)
	}
	return capitalize(cat)
}

func capitalize(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

var accents = strings.NewReplacer("á", "a", "à", "a", "â", "a", "ã", "a", "é", "e", "ê", "e",
	"í", "i", "ó", "o", "ô", "o", "õ", "o", "ú", "u", "ç", "c")

// fold normaliza para comparar nomes de categoria ("Mídia" == "midia").
func fold(s string) string { return accents.Replace(strings.ToLower(strings.TrimSpace(s))) }

// Module adapta o menu ao contrato app.Feature.
type Module struct {
	// Extra lista comandos adicionais (personalizados do grupo); nil = nenhum.
	Extra ExtraLister
}

// Name implementa app.Feature.
func (Module) Name() string { return "menu" }

// Register implementa app.Feature.
func (m Module) Register(_ context.Context, d *app.Deps) error {
	var name func() string
	if d.Policy != nil {
		name = d.BotName
	}
	return d.Router.Register(New(d.Router, m.Extra, name).Commands()...)
}
