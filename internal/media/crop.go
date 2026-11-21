package media

import (
	"fmt"
	"strconv"
)

// StickerSize é o lado (em px) das figurinhas do WhatsApp.
const StickerSize = 512

// CropMode define como a mídia vira um quadrado.
type CropMode uint8

const (
	// Contain encaixa a mídia inteira e completa com transparência (padrão do !s).
	Contain CropMode = iota
	// Center corta um quadrado no centro.
	Center
	// Top corta um quadrado preservando o topo.
	Top
	// Bottom corta um quadrado preservando a base.
	Bottom
	// Stretch estica para o quadrado, sem cortar.
	Stretch
	// Focus corta um quadrado centrado em Crop.Focus (0–100 no eixo maior).
	Focus
)

// Crop é o modo de corte e, para Focus, a posição do centro.
type Crop struct {
	Mode  CropMode
	Focus float64
}

const square = "w='min(iw,ih)':h='min(iw,ih)'"

// Filter devolve a cadeia de filtros do ffmpeg que leva a mídia ao quadrado
// StickerSize×StickerSize. O formato de pixel com alfa é aplicado por quem chama.
func (c Crop) Filter() string {
	size := strconv.Itoa(StickerSize)
	scale := "scale=" + size + ":" + size + ":flags=lanczos"
	switch c.Mode {
	case Center:
		return "crop=" + square + ":x='(iw-min(iw,ih))/2':y='(ih-min(iw,ih))/2'," + scale
	case Top:
		return "crop=" + square + ":x='(iw-min(iw,ih))/2':y=0," + scale
	case Bottom:
		return "crop=" + square + ":x='(iw-min(iw,ih))/2':y='ih-min(iw,ih)'," + scale
	case Stretch:
		return scale
	case Focus:
		p := strconv.FormatFloat(clamp(c.Focus, 0, 100), 'f', 2, 64)
		// Só o eixo maior tem folga; no outro o clip resulta sempre em 0.
		return fmt.Sprintf(
			"crop=%s:x='clip(iw*%[2]s/100-min(iw,ih)/2,0,iw-min(iw,ih))':y='clip(ih*%[2]s/100-min(iw,ih)/2,0,ih-min(iw,ih))',%s",
			square, p, scale)
	default: // Contain
		return scale + ":force_original_aspect_ratio=decrease,format=rgba," +
			"pad=" + size + ":" + size + ":(ow-iw)/2:(oh-ih)/2:color=black@0"
	}
}

func clamp(v, lo, hi float64) float64 {
	switch {
	case v != v: // NaN
		return (lo + hi) / 2
	case v < lo:
		return lo
	case v > hi:
		return hi
	}
	return v
}
