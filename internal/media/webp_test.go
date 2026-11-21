package media

import (
	"encoding/binary"
	"errors"
	"testing"
)

// simpleVP8L monta um WebP sem perdas mínimo (só o cabeçalho é lido).
func simpleVP8L(w, h int, alpha bool) []byte {
	bits := uint32(w-1) | uint32(h-1)<<14
	if alpha {
		bits |= 1 << 28
	}
	data := []byte{0x2f}
	data = binary.LittleEndian.AppendUint32(data, bits)
	data = append(data, 0) // tamanho ímpar: força o padding
	return encodeWebP([]riffChunk{{id: "VP8L", data: data}})
}

func simpleVP8(w, h int) []byte {
	data := []byte{0, 0, 0, 0x9d, 0x01, 0x2a}
	data = binary.LittleEndian.AppendUint16(data, uint16(w))
	data = binary.LittleEndian.AppendUint16(data, uint16(h))
	return encodeWebP([]riffChunk{{id: "VP8 ", data: data}})
}

func TestInspectWebP(t *testing.T) {
	anim := encodeWebP([]riffChunk{
		{id: "VP8X", data: vp8xData(vp8xFlagAnimation|vp8xFlagAlpha, 512, 512)},
		{id: "ANIM", data: make([]byte, 6)},
	})
	tests := []struct {
		name string
		in   []byte
		want WebPInfo
	}{
		{"vp8", simpleVP8(320, 240), WebPInfo{Width: 320, Height: 240}},
		{"vp8l alpha", simpleVP8L(512, 300, true), WebPInfo{Width: 512, Height: 300, Alpha: true}},
		{"vp8x animado", anim, WebPInfo{Width: 512, Height: 512, Animated: true, Alpha: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := InspectWebP(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestInspectWebPRejectsGarbage(t *testing.T) {
	if _, err := InspectWebP([]byte("not a webp at all")); !errors.Is(err, ErrNotWebP) {
		t.Fatalf("want ErrNotWebP, got %v", err)
	}
	b := simpleVP8(10, 10)
	if _, err := InspectWebP(b[:len(b)-3]); !errors.Is(err, ErrTruncatedWebP) {
		t.Fatalf("want ErrTruncatedWebP, got %v", err)
	}
}

func TestSetStickerMetadataPromotesSimpleFormat(t *testing.T) {
	meta := StickerMetadata{PackID: "id", PackName: "Grupo dos Amigos", Publisher: "ravena"}
	out, err := SetStickerMetadata(simpleVP8L(512, 512, true), meta)
	if err != nil {
		t.Fatal(err)
	}
	chunks, err := parseWebP(out)
	if err != nil {
		t.Fatal(err)
	}
	if chunks[0].id != "VP8X" || chunks[0].data[0]&vp8xFlagEXIF == 0 {
		t.Fatalf("primeiro chunk deveria ser VP8X com flag EXIF: %q %08b", chunks[0].id, chunks[0].data[0])
	}
	info, _ := InspectWebP(out)
	if info != (WebPInfo{Width: 512, Height: 512, Alpha: true}) {
		t.Fatalf("info inesperada: %+v", info)
	}
	got, err := ReadStickerMetadata(out)
	if err != nil {
		t.Fatal(err)
	}
	if got.PackName != meta.PackName || got.Publisher != meta.Publisher || got.PackID != meta.PackID {
		t.Fatalf("got %+v, want %+v", got, meta)
	}
}

func TestSetStickerMetadataReplacesExisting(t *testing.T) {
	in := encodeWebP([]riffChunk{
		{id: "VP8X", data: vp8xData(vp8xFlagAnimation, 512, 512)},
		{id: "ANIM", data: make([]byte, 6)},
	})
	first, err := SetStickerMetadata(in, StickerMetadata{PackName: "a"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := SetStickerMetadata(first, StickerMetadata{PackName: "b"})
	if err != nil {
		t.Fatal(err)
	}
	chunks, _ := parseWebP(second)
	exifs := 0
	for _, c := range chunks {
		if c.id == "EXIF" {
			exifs++
		}
	}
	if exifs != 1 {
		t.Fatalf("esperava 1 chunk EXIF, achei %d", exifs)
	}
	if info, _ := InspectWebP(second); !info.Animated {
		t.Fatal("flag de animação se perdeu")
	}
	if got, _ := ReadStickerMetadata(second); got.PackName != "b" {
		t.Fatalf("PackName = %q, want b", got.PackName)
	}
}
