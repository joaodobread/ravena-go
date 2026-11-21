package mediafx

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/joaodobread/ravena-go/internal/bot"
)

// MsgAnimeUsage é a ajuda do !anime.
const MsgAnimeUsage = "Por favor, forneça o nome de um anime para buscar. Exemplo: !anime Naruto"

const maxSynopsis = 900

type named struct {
	Name string `json:"name"`
}

// jikanAnime é o subconjunto usado de /v4/anime.
type jikanAnime struct {
	Title         string   `json:"title"`
	TitleJapanese string   `json:"title_japanese"`
	Type          string   `json:"type"`
	Source        string   `json:"source"`
	Episodes      *int     `json:"episodes"`
	Status        string   `json:"status"`
	Duration      string   `json:"duration"`
	Score         *float64 `json:"score"`
	Rank          *int     `json:"rank"`
	Popularity    *int     `json:"popularity"`
	Synopsis      string   `json:"synopsis"`
	Aired         struct {
		String string `json:"string"`
	} `json:"aired"`
	Genres  []named `json:"genres"`
	Studios []named `json:"studios"`
	Images  struct {
		JPG struct {
			Image string `json:"image_url"`
			Large string `json:"large_image_url"`
		} `json:"jpg"`
	} `json:"images"`
}

func (f *Feature) animeCommand() *bot.Command {
	return &bot.Command{
		Name:        "anime",
		Description: "Busca informações sobre um anime no MyAnimeList",
		Usage:       "!anime Naruto",
		Category:    "utilidades",
		Reactions:   bot.Reactions{Before: "⌛️", After: "🗾", Error: "❌"},
		Run:         f.anime,
	}
}

func (f *Feature) anime(ctx context.Context, c *bot.Context) error {
	name := strings.TrimSpace(c.ArgText())
	if name == "" {
		return bot.Userf(MsgAnimeUsage)
	}
	u := f.o.JikanURL + "/v4/anime?limit=1&q=" + url.QueryEscape(name)
	body, _, err := f.fetchOK(ctx, u, maxPage)
	if err != nil {
		return fmt.Errorf("jikan: %w", err)
	}
	var resp struct {
		Data []jikanAnime `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("jikan: %w", err)
	}
	if len(resp.Data) == 0 || resp.Data[0].Title == "" {
		return bot.Userf("❌ Não foi possível encontrar informações sobre \"%s\". Verifique se o nome está correto.", name)
	}
	a := resp.Data[0]
	text := formatAnime(a)

	img := a.Images.JPG.Large
	if img == "" {
		img = a.Images.JPG.Image
	}
	if img != "" {
		data, ct, err := f.fetchOK(ctx, img, maxRemote)
		if err == nil && strings.HasPrefix(ct, "image/") {
			return c.Reply(ctx, bot.Outgoing{Kind: bot.OutImage, Data: data, Mime: mimeOnly(ct), Caption: text})
		}
		f.o.Log.Warn("anime: falha ao baixar capa", "url", img, "err", err)
	}
	return c.Reply(ctx, bot.Text(text))
}

func orNA(s string) string {
	if strings.TrimSpace(s) == "" {
		return "N/A"
	}
	return s
}

func intNA(p *int) string {
	if p == nil {
		return "N/A"
	}
	return strconv.Itoa(*p)
}

func names(ns []named) string {
	out := make([]string, 0, len(ns))
	for _, n := range ns {
		out = append(out, n.Name)
	}
	return orNA(strings.Join(out, ", "))
}

func formatAnime(a jikanAnime) string {
	score := "N/A"
	if a.Score != nil {
		score = strconv.FormatFloat(*a.Score, 'f', 2, 64)
	}
	aired, _, _ := strings.Cut(a.Aired.String, " to ")
	synopsis := strings.TrimSpace(a.Synopsis)
	if synopsis == "" {
		synopsis = "Sinopse não disponível."
	} else if r := []rune(synopsis); len(r) > maxSynopsis {
		synopsis = string(r[:maxSynopsis-3]) + "..."
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "🗾 *%s* (%s)\n\n", a.Title, orNA(a.TitleJapanese))
	fmt.Fprintf(&sb, "📅 *Lançamento*: %s (%s @ %s)\n", orNA(aired), orNA(a.Status), orNA(a.Type))
	fmt.Fprintf(&sb, "🏢 *Estúdio*: %s\n", names(a.Studios))
	fmt.Fprintf(&sb, "📖 *Fonte*: %s\n", orNA(a.Source))
	fmt.Fprintf(&sb, "🍿 *Gênero*: %s\n", names(a.Genres))
	fmt.Fprintf(&sb, "🔢 *Episódios*: %s (_%s_)\n", intNA(a.Episodes), orNA(a.Duration))
	fmt.Fprintf(&sb, "🏆 *Nota:* %s, #%s no ranking, #%s em popularidade\n\n", score, intNA(a.Rank), intNA(a.Popularity))
	fmt.Fprintf(&sb, "💬 *Sinopse:* %s", synopsis)
	return sb.String()
}
