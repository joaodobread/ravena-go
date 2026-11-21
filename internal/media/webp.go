package media

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
)

// Erros de parsing de WebP.
var (
	ErrNotWebP       = errors.New("webp: arquivo não é um RIFF/WEBP")
	ErrTruncatedWebP = errors.New("webp: arquivo truncado")
)

// Flags do chunk VP8X (https://developers.google.com/speed/webp/docs/riff_container).
const (
	vp8xFlagAnimation = 0x02
	vp8xFlagEXIF      = 0x08
	vp8xFlagAlpha     = 0x10
)

// WebPInfo descreve o que interessa de um WebP para decidir como tratá-lo.
type WebPInfo struct {
	Width, Height int
	Animated      bool
	Alpha         bool
}

// StickerMetadata é o JSON que o WhatsApp lê do EXIF de uma figurinha.
type StickerMetadata struct {
	PackID    string   `json:"sticker-pack-id"`
	PackName  string   `json:"sticker-pack-name"`
	Publisher string   `json:"sticker-pack-publisher"`
	Emojis    []string `json:"emojis,omitempty"`
}

type riffChunk struct {
	id   string
	data []byte
}

func parseWebP(b []byte) ([]riffChunk, error) {
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WEBP" {
		return nil, ErrNotWebP
	}
	end := 8 + int(binary.LittleEndian.Uint32(b[4:8]))
	if end > len(b) {
		return nil, ErrTruncatedWebP
	}
	var chunks []riffChunk
	for p := 12; p+8 <= end; {
		id := string(b[p : p+4])
		size := int(binary.LittleEndian.Uint32(b[p+4 : p+8]))
		p += 8
		if size < 0 || p+size > end {
			return nil, ErrTruncatedWebP
		}
		chunks = append(chunks, riffChunk{id: id, data: b[p : p+size]})
		p += size + size&1 // chunks são alinhados em 2 bytes
	}
	if len(chunks) == 0 {
		return nil, ErrTruncatedWebP
	}
	return chunks, nil
}

func encodeWebP(chunks []riffChunk) []byte {
	payload := 4 // "WEBP"
	for _, c := range chunks {
		payload += 8 + len(c.data) + len(c.data)&1
	}
	out := make([]byte, 0, 8+payload)
	out = append(out, "RIFF"...)
	out = binary.LittleEndian.AppendUint32(out, uint32(payload))
	out = append(out, "WEBP"...)
	for _, c := range chunks {
		out = append(out, c.id...)
		out = binary.LittleEndian.AppendUint32(out, uint32(len(c.data)))
		out = append(out, c.data...)
		if len(c.data)&1 == 1 {
			out = append(out, 0)
		}
	}
	return out
}

// InspectWebP lê dimensões, animação e transparência sem decodificar a imagem.
func InspectWebP(b []byte) (WebPInfo, error) {
	chunks, err := parseWebP(b)
	if err != nil {
		return WebPInfo{}, err
	}
	return inspectChunks(chunks)
}

func inspectChunks(chunks []riffChunk) (WebPInfo, error) {
	first := chunks[0]
	switch first.id {
	case "VP8X":
		if len(first.data) < 10 {
			return WebPInfo{}, ErrTruncatedWebP
		}
		flags := first.data[0]
		return WebPInfo{
			Width:    int(le24(first.data[4:7])) + 1,
			Height:   int(le24(first.data[7:10])) + 1,
			Animated: flags&vp8xFlagAnimation != 0,
			Alpha:    flags&vp8xFlagAlpha != 0,
		}, nil
	case "VP8 ":
		// frame tag (3 bytes) + start code 9d 01 2a + largura/altura em 14 bits.
		d := first.data
		if len(d) < 10 || d[3] != 0x9d || d[4] != 0x01 || d[5] != 0x2a {
			return WebPInfo{}, fmt.Errorf("webp: cabeçalho VP8 inválido")
		}
		return WebPInfo{
			Width:  int(binary.LittleEndian.Uint16(d[6:8]) & 0x3fff),
			Height: int(binary.LittleEndian.Uint16(d[8:10]) & 0x3fff),
		}, nil
	case "VP8L":
		d := first.data
		if len(d) < 5 || d[0] != 0x2f {
			return WebPInfo{}, fmt.Errorf("webp: cabeçalho VP8L inválido")
		}
		bits := binary.LittleEndian.Uint32(d[1:5])
		return WebPInfo{
			Width:  int(bits&0x3fff) + 1,
			Height: int((bits>>14)&0x3fff) + 1,
			Alpha:  (bits>>28)&1 == 1,
		}, nil
	default:
		return WebPInfo{}, fmt.Errorf("webp: primeiro chunk inesperado %q", first.id)
	}
}

// SetStickerMetadata grava (ou substitui) o EXIF com os metadados do pacote.
// WebPs no formato simples (VP8/VP8L) são promovidos para o formato estendido (VP8X).
func SetStickerMetadata(webp []byte, meta StickerMetadata) ([]byte, error) {
	chunks, err := parseWebP(webp)
	if err != nil {
		return nil, err
	}
	info, err := inspectChunks(chunks)
	if err != nil {
		return nil, err
	}
	js, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}

	out := make([]riffChunk, 0, len(chunks)+2)
	if chunks[0].id == "VP8X" {
		vp8x := append([]byte(nil), chunks[0].data...)
		vp8x[0] |= vp8xFlagEXIF
		out = append(out, riffChunk{id: "VP8X", data: vp8x})
		chunks = chunks[1:]
	} else {
		flags := byte(vp8xFlagEXIF)
		if info.Alpha {
			flags |= vp8xFlagAlpha
		}
		out = append(out, riffChunk{id: "VP8X", data: vp8xData(flags, info.Width, info.Height)})
	}
	for _, c := range chunks {
		if c.id != "EXIF" {
			out = append(out, c)
		}
	}
	out = append(out, riffChunk{id: "EXIF", data: stickerEXIF(js)})
	return encodeWebP(out), nil
}

// ReadStickerMetadata extrai os metadados gravados por SetStickerMetadata.
func ReadStickerMetadata(webp []byte) (StickerMetadata, error) {
	var meta StickerMetadata
	chunks, err := parseWebP(webp)
	if err != nil {
		return meta, err
	}
	for _, c := range chunks {
		if c.id != "EXIF" {
			continue
		}
		if len(c.data) < len(exifHeader)+8 {
			return meta, ErrTruncatedWebP
		}
		n := int(binary.LittleEndian.Uint32(c.data[len(exifHeader):]))
		start := len(exifHeader) + 8
		if start+n > len(c.data) {
			return meta, ErrTruncatedWebP
		}
		err = json.Unmarshal(c.data[start:start+n], &meta)
		return meta, err
	}
	return meta, errors.New("webp: sem EXIF")
}

// TIFF little-endian com um único IFD contendo a tag 0x5741 (tipo UNDEFINED),
// que é onde o WhatsApp procura o JSON da figurinha.
var exifHeader = []byte{
	0x49, 0x49, 0x2a, 0x00, // "II*\0"
	0x08, 0x00, 0x00, 0x00, // offset do primeiro IFD
	0x01, 0x00, // 1 entrada
	0x41, 0x57, // tag 0x5741
	0x07, 0x00, // tipo UNDEFINED
}

func stickerEXIF(js []byte) []byte {
	out := make([]byte, 0, len(exifHeader)+8+len(js))
	out = append(out, exifHeader...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(js)))
	out = binary.LittleEndian.AppendUint32(out, 0x16) // dados logo após o IFD
	return append(out, js...)
}

func vp8xData(flags byte, w, h int) []byte {
	d := make([]byte, 10)
	d[0] = flags
	putLE24(d[4:7], uint32(w-1))
	putLE24(d[7:10], uint32(h-1))
	return d
}

func le24(b []byte) uint32 { return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 }

func putLE24(b []byte, v uint32) {
	b[0], b[1], b[2] = byte(v), byte(v>>8), byte(v>>16)
}
