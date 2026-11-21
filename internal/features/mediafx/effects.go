package mediafx

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/joaodobread/ravena-go/internal/bot"
)

// effect é um efeito de imagem feito pelo ImageMagick.
type effect struct {
	name    string
	desc    string
	after   string
	caption func(args []string) string
	// args devolve os argumentos do magick (entre a entrada e a saída).
	args func(cmdArgs []string) []string
	// fallback, se não-nil, é tentado quando args falha (ex.: magick sem liblqr).
	fallback func(cmdArgs []string) []string
	jpeg     bool // saída "jpeg ruim" proposital (morejpeg)
}

// distortIntensity lê a intensidade (1–100) e limita a 30–70, como o original.
func distortIntensity(args []string) int {
	n := 50
	if len(args) > 0 {
		if v, err := strconv.Atoi(args[0]); err == nil {
			n = max(1, min(100, v))
		}
	}
	return max(30, min(70, n))
}

var effects = []effect{
	{
		name: "distort", desc: "Aplica efeito de distorção a uma imagem", after: "🌀",
		caption: func(a []string) string {
			return fmt.Sprintf("Distorção aplicada (intensidade: %d%%)", distortIntensity(a))
		},
		args: func(a []string) []string {
			p := strconv.Itoa(distortIntensity(a))
			// liquid-rescale é caro: trabalha sobre no máx. 640px.
			return []string{"-resize", "640x640>", "-liquid-rescale", p + "x" + p + "%!", "-resize", "200%"}
		},
		fallback: func(a []string) []string {
			amt := strconv.FormatFloat(float64(distortIntensity(a))/100, 'f', 2, 64)
			return []string{"-resize", "800x800>", "-implode", amt, "-swirl", strconv.Itoa(distortIntensity(a) * 3)}
		},
	},
	{
		name: "morejpeg", desc: "Aplica compressão JPEG extrema", after: "💩", jpeg: true,
		caption: func([]string) string { return "Needs more JPEG!" },
		args: func([]string) []string {
			return []string{"-resize", "100x", "-filter", "point", "-resize", "1024x", "-quality", "1"}
		},
	},
	{
		name: "sketch", desc: "Aplica efeito sketch a uma imagem", after: "🎨",
		args: func([]string) []string {
			return []string{"-resize", maxImageSide, "-colorspace", "gray", "-sketch", "0x20+120"}
		},
	},
	{
		name: "oil", desc: "Aplica efeito oil a uma imagem", after: "🎨",
		args: func([]string) []string { return []string{"-resize", maxImageSide, "-paint", "6"} },
	},
	{
		name: "neon", desc: "Aplica efeito neon a uma imagem", after: "🎨",
		args: func([]string) []string {
			return []string{"-resize", maxImageSide, "-negate", "-edge", "2", "-negate", "-normalize",
				"-channel", "RGB", "-blur", "0x.5", "-colorspace", "sRGB"}
		},
	},
	{
		name: "pixelate", desc: "Aplica efeito pixelate a uma imagem", after: "🎨",
		args: func([]string) []string {
			return []string{"-resize", maxImageSide, "-scale", "10%", "-scale", "1000%"}
		},
	},
}

func (f *Feature) effectCommands() []*bot.Command {
	cmds := make([]*bot.Command, 0, len(effects))
	for _, e := range effects {
		cmds = append(cmds, &bot.Command{
			Name:        e.name,
			Description: e.desc,
			Usage:       "!" + e.name + " (respondendo a uma imagem)",
			Category:    "midia",
			Reactions:   bot.Reactions{Before: "⌛️", After: e.after, Error: "❌"},
			Run: func(ctx context.Context, c *bot.Context) error {
				return f.applyEffect(ctx, c, e)
			},
		})
	}
	return cmds
}

// imageExt escolhe a extensão de entrada para o magick.
func imageExt(m *bot.Media) (string, bool) {
	if m.Kind == bot.MediaSticker {
		return ".webp", true
	}
	if m.Kind != bot.MediaImage && !(m.Kind == bot.MediaDocument && strings.HasPrefix(m.Mime, "image/")) {
		return "", false
	}
	mime, _, _ := strings.Cut(m.Mime, ";")
	switch strings.TrimSpace(mime) {
	case "image/png":
		return ".png", true
	case "image/webp":
		return ".webp", true
	case "image/gif":
		return ".gif", true
	default:
		return ".jpg", true
	}
}

func (f *Feature) applyEffect(ctx context.Context, c *bot.Context, e effect) error {
	src := c.Message.AnyMedia()
	if src == nil {
		return bot.Userf(MsgNoImage)
	}
	inExt, ok := imageExt(src)
	if !ok {
		return bot.Userf(MsgNoImage)
	}
	data, err := download(ctx, src)
	if err != nil {
		return err
	}
	isSticker := src.Kind == bot.MediaSticker
	outExt := ".jpg"
	if isSticker && !e.jpeg {
		outExt = ".png" // preserva a transparência até virar figurinha
	}

	run := func(args []string) ([]byte, error) {
		// "-delete 1--1" fica só com o 1º quadro (GIF/figurinha animada).
		full := append([]string{"-delete", "1--1", "-auto-orient"}, args...)
		if outExt == ".jpg" && !e.jpeg {
			full = append(full, "-background", "white", "-flatten", "-quality", "88")
		}
		return f.conv.Magick(ctx, data, inExt, outExt, full...)
	}
	out, err := run(e.args(c.Args))
	if err != nil && e.fallback != nil {
		f.o.Log.Warn("efeito falhou, usando alternativa", "effect", e.name, "err", err)
		out, err = run(e.fallback(c.Args))
	}
	if err != nil {
		return fmt.Errorf("efeito %s: %w", e.name, err)
	}

	if isSticker {
		st, err := f.sticker(ctx, out, e.name)
		if err != nil {
			return err
		}
		return c.Reply(ctx, st)
	}
	caption := "Efeito " + e.name + " aplicado"
	if e.caption != nil {
		caption = e.caption(c.Args)
	}
	return c.Reply(ctx, bot.Outgoing{Kind: bot.OutImage, Data: out, Mime: "image/jpeg", Caption: caption})
}
