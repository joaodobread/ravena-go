package util

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/joaodobread/ravena-go/internal/bot"
)

type ddgTopic struct {
	Text     string     `json:"Text"`
	FirstURL string     `json:"FirstURL"`
	Topics   []ddgTopic `json:"Topics"` // grupos de tópicos (desambiguação)
}

type ddgAnswer struct {
	AbstractText   string     `json:"AbstractText"`
	AbstractSource string     `json:"AbstractSource"`
	AbstractURL    string     `json:"AbstractURL"`
	Answer         any        `json:"Answer"`
	RelatedTopics  []ddgTopic `json:"RelatedTopics"`
	Results        []ddgTopic `json:"Results"`
	Infobox        any        `json:"Infobox"` // "" ou {"content": [...]}
}

type ddgInfobox struct {
	Content []struct {
		Label string `json:"label"`
		Value any    `json:"value"`
	} `json:"content"`
}

func flatten(topics []ddgTopic, limit int) []ddgTopic {
	var out []ddgTopic
	for _, t := range topics {
		if len(out) >= limit {
			break
		}
		if t.Text != "" {
			out = append(out, t)
			continue
		}
		out = append(out, flatten(t.Topics, limit-len(out))...)
	}
	return out
}

func (f *Feature) buscar(ctx context.Context, c *bot.Context) error {
	query := c.ArgText()
	if query == "" {
		return bot.Userf("Por favor, forneça uma consulta de busca. Exemplo: %sbuscar tutorial javascript", c.Prefix)
	}
	q := url.Values{"q": {query}, "format": {"json"}, "skip_disambig": {"1"}, "no_html": {"1"}, "no_redirect": {"1"}}
	var r ddgAnswer
	if err := f.getJSON(ctx, f.urls.DuckDuckGo+"/?"+q.Encode(), &r); err != nil {
		return f.upstream(err, "duckduckgo", "Erro ao realizar busca na web. Por favor, tente novamente.")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "🔍 *Resultados para \"%s\":*\n\n", query)
	empty := b.Len()
	if s, ok := r.Answer.(string); ok && s != "" {
		b.WriteString("💡 " + html.UnescapeString(s) + "\n\n")
	}
	if r.AbstractText != "" {
		fmt.Fprintf(&b, "*%s:*\n%s\n", r.AbstractSource, truncate(html.UnescapeString(r.AbstractText), 800))
		if r.AbstractURL != "" {
			b.WriteString("🔗 " + r.AbstractURL + "\n")
		}
		b.WriteString("\n")
	}
	topics := flatten(r.RelatedTopics, 5)
	if len(topics) == 0 {
		topics = flatten(r.Results, 5)
	}
	for i, t := range topics {
		fmt.Fprintf(&b, "%d. %s\n", i+1, truncate(html.UnescapeString(t.Text), 300))
		if t.FirstURL != "" {
			b.WriteString("   🔗 " + t.FirstURL + "\n\n")
		}
	}
	if len(topics) == 0 {
		if raw, err := json.Marshal(r.Infobox); err == nil {
			var box ddgInfobox
			if json.Unmarshal(raw, &box) == nil {
				n := 0
				for _, it := range box.Content {
					if v, ok := it.Value.(string); ok && it.Label != "" && v != "" && n < 5 {
						fmt.Fprintf(&b, "%s: %s\n", it.Label, v)
						n++
					}
				}
			}
		}
	}
	if b.Len() == empty {
		b.WriteString("Não foram encontrados resultados específicos para esta busca.\n\n")
		b.WriteString("Tente buscar diretamente: https://duckduckgo.com/?q=" + url.QueryEscape(query))
	}
	return c.ReplyText(ctx, "%s", strings.TrimRight(b.String(), "\n"))
}

type giveaway struct {
	Title     string `json:"title"`
	Type      string `json:"type"`
	Platforms string `json:"platforms"`
	URL       string `json:"open_giveaway_url"`
}

var platformRe = regexp.MustCompile(`^[a-z0-9.\-]{1,30}$`)

const (
	maxGiveaways   = 40
	maxMessageSize = 20000
)

func (f *Feature) giveaways(ctx context.Context, c *bot.Context) error {
	platform := ""
	if len(c.Args) > 0 {
		platform = strings.ToLower(c.Args[0])
		if !platformRe.MatchString(platform) {
			return bot.Userf("❌ Plataforma inválida: %s", c.Args[0])
		}
	}
	q := url.Values{"sort-by": {"popularity"}}
	wq := url.Values{}
	if platform != "" {
		q.Set("platform", platform)
		wq.Set("platform", platform)
	}

	// As duas consultas em paralelo; o valor total é opcional.
	var (
		wg    sync.WaitGroup
		worth struct {
			USD string `json:"worth_estimation_usd"`
		}
	)
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	wg.Go(func() {
		u := f.urls.GamerPower + "/api/worth"
		if len(wq) > 0 {
			u += "?" + wq.Encode()
		}
		if err := f.getJSON(wctx, u, &worth); err != nil {
			f.log.Debug("gamerpower worth falhou", "err", err)
		}
	})
	data, _, err := f.get(ctx, f.urls.GamerPower+"/api/giveaways?"+q.Encode(), maxBody)
	wg.Wait()
	if err != nil {
		return f.upstream(err, "gamerpower", "❌ Erro ao buscar brindes de jogos. Tente novamente mais tarde.")
	}
	// Sem resultados a API devolve um objeto {"status":0,...} em vez de lista.
	var list []giveaway
	if json.Unmarshal(data, &list) != nil || len(list) == 0 {
		suffix := ""
		if platform != "" {
			suffix = " para a plataforma *" + platform + "*"
		}
		return bot.Userf("❌ Nenhum brinde encontrado%s.", suffix)
	}

	var games, others []giveaway
	for _, g := range list {
		if strings.EqualFold(g.Type, "game") {
			games = append(games, g)
		} else {
			others = append(others, g)
		}
	}

	var b strings.Builder
	b.WriteString("🎮 *JOGOS GRÁTIS ATUALMENTE* 🎮\n_Lista de jogos e no final lista de DLCs/cupons/gifts de jogos_\n")
	if platform != "" {
		b.WriteString("📍 Filtro: *" + strings.ToUpper(platform) + "*\n")
	}
	count := 0
	section := func(title, emoji string, items []giveaway) {
		if len(items) == 0 || count >= maxGiveaways {
			return
		}
		b.WriteString(title)
		for _, g := range items {
			if count >= maxGiveaways || b.Len() > maxMessageSize {
				return
			}
			fmt.Fprintf(&b, "*%s*\n%s 💻 %s\n🔗 %s\n\n", g.Title, emoji, g.Platforms, g.URL)
			count++
		}
	}
	section("\n🕹️ *JOGOS:*\n\n", "👾", games)
	section("━━━━━━━━━━━━━━━\n🎁 *DLCs, CUPONS E GIFTS:*\n\n", "🎁", others)
	if count < len(list) {
		fmt.Fprintf(&b, "_…e mais %d na lista completa._\n\n", len(list)-count)
	}

	b.WriteString("━━━━━━━━━━━━━━━\n\n")
	if w := strings.TrimSpace(worth.USD); w != "" && w != "$0" && w != "0" {
		fmt.Fprintf(&b, "💰 *ECONOMIA TOTAL:*\nAo todo, você pode economizar aproximadamente *US%s* hoje!\n\n", ensureDollar(w))
	}
	fmt.Fprintf(&b, "💡 *Dica:* Você pode filtrar por plataforma.\n> Exemplo: %s0800 pc ou %s0800 steam\n\n", c.Prefix, c.Prefix)
	b.WriteString("*Sugestões de filtro:*\n_pc, steam, epic-games-store, gog, itch.io, ps4, ps5, xbox-one, switch, android, ios_")
	return c.ReplyText(ctx, "%s", b.String())
}

func ensureDollar(s string) string {
	if strings.HasPrefix(s, "$") {
		return s
	}
	return "$" + s
}
