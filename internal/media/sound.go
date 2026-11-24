package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"strconv"
	"time"
)

// Sound é o áudio de uma figurinha com som: nota de voz ogg/opus mais a
// duração e a forma de onda que o WhatsApp mostra.
type Sound struct {
	Ogg      []byte
	Seconds  uint32
	Waveform []byte // 64 barras de 0 a 100
}

// WaveformBars é quantas barras a forma de onda de uma nota de voz tem.
const WaveformBars = 64

// soundArgs converte para opus mono 16 kHz. Os celulares mostram a figurinha
// mas não tocam o som sem -vbr, -frame_duration e -avoid_negative_ts.
func soundArgs(limit time.Duration) []string {
	return []string{
		"-vn", "-t", strconv.FormatFloat(limit.Seconds(), 'f', -1, 64),
		"-ar", "16000", "-ac", "1",
		"-c:a", "libopus", "-b:a", "32k", "-vbr", "on", "-compression_level", "10",
		"-frame_duration", "20", "-application", "voip",
		"-avoid_negative_ts", "make_zero", "-map_metadata", "-1",
		"-f", "ogg",
	}
}

// StickerSound extrai até limit do áudio de in como som de figurinha.
func (c *Converter) StickerSound(ctx context.Context, in []byte, limit time.Duration) (Sound, error) {
	ogg, err := c.ffmpeg(ctx, in, ".ogg", soundArgs(limit)...)
	if err != nil {
		return Sound{}, err
	}
	if len(ogg) == 0 {
		return Sound{}, errors.New("áudio vazio")
	}
	secs := OggOpusDuration(ogg)
	if secs <= 0 {
		return Sound{}, errors.New("áudio sem duração")
	}
	// A forma de onda é só enfeite: se falhar, a figurinha vai sem ela.
	var wave []byte
	if pcm, err := c.ffmpeg(ctx, ogg, ".raw", "-ac", "1", "-ar", "8000", "-f", "s16le"); err == nil {
		wave = Waveform(pcm, WaveformBars)
	}
	return Sound{Ogg: ogg, Seconds: max(1, uint32(math.Round(secs.Seconds()))), Waveform: wave}, nil
}

// OggOpusDuration lê a duração de um ogg/opus pela granule position da última
// página (sempre em 48 kHz no opus) menos o pre-skip do cabeçalho. 0 se não der.
func OggOpusDuration(ogg []byte) time.Duration {
	head := bytes.Index(ogg, []byte("OpusHead"))
	last := bytes.LastIndex(ogg, []byte("OggS"))
	if head < 0 || head+12 > len(ogg) || last < 0 || last+14 > len(ogg) {
		return 0
	}
	preSkip := int64(binary.LittleEndian.Uint16(ogg[head+10:]))
	granule := int64(binary.LittleEndian.Uint64(ogg[last+6:]))
	samples := granule - preSkip
	if samples <= 0 {
		return 0
	}
	return time.Duration(samples) * time.Second / 48000
}

// Waveform resume PCM s16le mono em n barras (pico de cada trecho), escaladas
// para que a maior seja 100.
func Waveform(pcm []byte, n int) []byte {
	samples := len(pcm) / 2
	out := make([]byte, n)
	if samples == 0 || n <= 0 {
		return out
	}
	peaks := make([]float64, n)
	var top float64
	for i := range n {
		from, to := i*samples/n, (i+1)*samples/n
		for s := from; s < to; s++ {
			v := math.Abs(float64(int16(binary.LittleEndian.Uint16(pcm[2*s:]))))
			peaks[i] = max(peaks[i], v)
		}
		top = max(top, peaks[i])
	}
	if top == 0 {
		return out
	}
	for i, p := range peaks {
		out[i] = byte(math.Round(p / top * 100))
	}
	return out
}
