package util

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/joaodobread/ravena-go/internal/bot"
)

const maxThumb = 1 << 20 // miniatura da Wikipedia (as de verdade têm ~30 KB)

type wikiSearch struct {
	Query struct {
		Search []struct {
			Title string `json:"title"`
		} `json:"search"`
	} `json:"query"`
}

type wikiSummary struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Extract     string `json:"extract"`
	Thumbnail   struct {
		Source string `json:"source"`
	} `json:"thumbnail"`
	ContentURLs struct {
		Desktop struct {
			Page string `json:"page"`
		} `json:"desktop"`
	} `json:"content_urls"`
}

func (f *Feature) wiki(ctx context.Context, c *bot.Context) error {
	term := c.ArgText()
	if term == "" {
		return bot.Userf("Por favor, forneça um termo para buscar na Wikipedia. Exemplo: %swiki Brasil", c.Prefix)
	}

	q := url.Values{"action": {"query"}, "list": {"search"}, "srsearch": {term}, "format": {"json"}, "utf8": {"1"}, "srlimit": {"1"}}
	var search wikiSearch
	if err := f.getJSON(ctx, f.urls.Wiki+"/w/api.php?"+q.Encode(), &search); err != nil {
		return f.wikiError(err, term)
	}
	if len(search.Query.Search) == 0 {
		return bot.Userf("❌ Não foi possível encontrar informações sobre \"%s\" na Wikipedia.", term)
	}
	title := search.Query.Search[0].Title

	var sum wikiSummary
	path := url.PathEscape(strings.ReplaceAll(title, " ", "_"))
	if err := f.getJSON(ctx, f.urls.Wiki+"/api/rest_v1/page/summary/"+path, &sum); err != nil {
		return f.wikiError(err, term)
	}
	if sum.Title == "" {
		return bot.Userf("❌ Erro ao buscar informações detalhadas sobre \"%s\".", term)
	}

	var b strings.Builder
	b.WriteString("📚 *" + sum.Title + "*\n\n")
	if sum.Description != "" {
		b.WriteString("*" + sum.Description + "*\n\n")
	}
	b.WriteString(truncate(sum.Extract, 1000))
	link := sum.ContentURLs.Desktop.Page
	if link == "" {
		link = f.urls.Wiki + "/wiki/" + path
	}
	b.WriteString("\n\n🔗 *Leia mais:* " + link)
	text := b.String()

	if src := sum.Thumbnail.Source; src != "" {
		data, mime, err := f.get(ctx, src, maxThumb)
		if err == nil && strings.HasPrefix(mime, "image/") {
			return c.Reply(ctx, bot.Outgoing{Kind: bot.OutImage, Data: data, Mime: mime, Caption: text})
		}
		if err != nil {
			f.log.Debug("miniatura da wikipedia falhou", "err", err)
		}
	}
	return c.ReplyText(ctx, "%s", text)
}

func (f *Feature) wikiError(err error, term string) error {
	if he, ok := errors.AsType[*httpError](err); ok {
		switch he.Status {
		case 404:
			return bot.Userf("❌ Não foi possível encontrar uma página sobre \"%s\" na Wikipedia.", term)
		case 429:
			return bot.Userf("❌ Muitas solicitações à Wikipedia. Por favor, tente novamente mais tarde.")
		}
	}
	return f.upstream(err, "wikipedia", "❌ Erro ao buscar informações da Wikipedia. Por favor, tente novamente.")
}
