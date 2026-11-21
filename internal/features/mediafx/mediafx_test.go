package mediafx_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/bot/bottest"
	"github.com/joaodobread/ravena-go/internal/features/mediafx"
	"github.com/joaodobread/ravena-go/internal/media"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func requireBin(t *testing.T, bins ...string) {
	t.Helper()
	for _, b := range bins {
		if _, err := exec.LookPath(b); err != nil {
			t.Skipf("%s não instalado", b)
		}
	}
}

// fakeProc registra as chamadas sem rodar nada.
type fakeProc struct {
	calls []string
	args  [][]string
	err   error
}

func (p *fakeProc) FFmpeg(_ context.Context, _ []byte, ext string, args ...string) ([]byte, error) {
	p.calls, p.args = append(p.calls, "ffmpeg"+ext), append(p.args, args)
	return []byte("out"), p.err
}

func (p *fakeProc) Magick(_ context.Context, _ []byte, in, out string, args ...string) ([]byte, error) {
	p.calls, p.args = append(p.calls, "magick"+in+out), append(p.args, args)
	return []byte("out"), p.err
}

func (p *fakeProc) StaticSticker(context.Context, []byte, media.Crop) ([]byte, error) {
	p.calls = append(p.calls, "sticker")
	return fakeWebP(), nil
}

// fakeWebP é um WebP VP8L mínimo, suficiente para SetStickerMetadata.
func fakeWebP() []byte {
	data := binary.LittleEndian.AppendUint32([]byte{0x2f}, uint32(511)|uint32(511)<<14)
	out := []byte("RIFF")
	out = binary.LittleEndian.AppendUint32(out, uint32(4+8+len(data)+1))
	out = append(out, "WEBPVP8L"...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(data)))
	return append(append(out, data...), 0)
}

type env struct {
	r  *bot.Router
	fc *bottest.FakeClient
}

func setup(t *testing.T, p mediafx.Processor, o mediafx.Options) env {
	t.Helper()
	fc := &bottest.FakeClient{}
	o.Log = quiet
	f := mediafx.New(p, o)
	r := bot.NewRouter("!", fc, quiet)
	if err := r.Register(f.Commands()...); err != nil {
		t.Fatal(err)
	}
	return env{r, fc}
}

func msg(text string, m *bot.Media) *bot.Message {
	x := bottest.GroupMsg(text)
	x.Media = m
	return x
}

func pngImage(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), uint8(x + y), 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// wav gera um WAV PCM mono de 8 kHz com um seno.
func wav(seconds float64) []byte {
	const rate = 8000
	n := int(seconds * rate)
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+n*2))
	b.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), uint16(1), uint16(1), uint32(rate), uint32(rate * 2), uint16(2), uint16(16)} {
		_ = binary.Write(&b, binary.LittleEndian, v)
	}
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(n*2))
	for i := range n {
		_ = binary.Write(&b, binary.LittleEndian, int16(8000*math.Sin(2*math.Pi*440*float64(i)/rate)))
	}
	return b.Bytes()
}

func realConv(t *testing.T) *media.Converter {
	requireBin(t, "ffmpeg", "magick")
	return media.NewConverter(media.Options{TempDir: t.TempDir()})
}

// --- efeitos ---

func TestEffectsWithRealMagick(t *testing.T) {
	conv := realConv(t)
	e := setup(t, conv, mediafx.Options{})
	img := pngImage(t, 160, 120)
	for _, cmd := range []string{"distort 60", "morejpeg", "sketch", "oil", "neon", "pixelate"} {
		e.r.Handle(context.Background(), msg("!"+cmd, bottest.StaticMedia(bot.MediaImage, "image/png", img)))
		out := e.fc.Last().Out
		if out.Kind != bot.OutImage || len(out.Data) < 100 || !bytes.HasPrefix(out.Data, []byte{0xff, 0xd8}) {
			t.Fatalf("%s: saída inesperada kind=%v len=%d text=%q", cmd, out.Kind, len(out.Data), out.Text)
		}
	}
	if got := e.fc.Last().Out.Caption; got != "Efeito pixelate aplicado" {
		t.Fatalf("legenda = %q", got)
	}
}

func TestEffectOnStickerReturnsSticker(t *testing.T) {
	conv := realConv(t)
	webp, err := conv.StaticSticker(context.Background(), pngImage(t, 100, 100), media.Crop{Mode: media.Contain})
	if err != nil {
		t.Fatal(err)
	}
	e := setup(t, conv, mediafx.Options{Publisher: "ravena"})
	m := msg("!neon", nil)
	m.Quoted = &bot.Message{ID: "q", Media: bottest.StaticMedia(bot.MediaSticker, "image/webp", webp)}
	e.r.Handle(context.Background(), m)
	out := e.fc.Last().Out
	if out.Kind != bot.OutSticker {
		t.Fatalf("esperava figurinha, veio %+v", out.Text)
	}
	meta, err := media.ReadStickerMetadata(out.Data)
	if err != nil || meta.PackName != "neon" || meta.Publisher != "ravena" {
		t.Fatalf("metadados = %+v, %v", meta, err)
	}
}

func TestDistortFallsBackAndClampsIntensity(t *testing.T) {
	p := &fakeProc{err: fmt.Errorf("magick: no lqr")}
	e := setup(t, p, mediafx.Options{})
	e.r.Handle(context.Background(), msg("!distort 99", bottest.StaticMedia(bot.MediaImage, "image/jpeg", []byte("x"))))
	if len(p.calls) != 2 || !slices.Contains(p.args[0], "70x70%!") || !slices.Contains(p.args[1], "-implode") {
		t.Fatalf("chamadas = %v %v", p.calls, p.args)
	}
	if p.calls[0] != "magick.jpg.jpg" {
		t.Fatalf("extensões = %v", p.calls[0])
	}
}

func TestEffectErrors(t *testing.T) {
	e := setup(t, &fakeProc{}, mediafx.Options{})
	e.r.Handle(context.Background(), msg("!oil", nil))
	if got := e.fc.Last().Out.Text; got != mediafx.MsgNoImage {
		t.Fatalf("sem mídia: %q", got)
	}
	e.r.Handle(context.Background(), msg("!oil", bottest.StaticMedia(bot.MediaVideo, "video/mp4", []byte("v"))))
	if got := e.fc.Last().Out.Text; got != mediafx.MsgNoImage {
		t.Fatalf("vídeo: %q", got)
	}
	big := make([]byte, mediafx.MaxInput+1)
	e.r.Handle(context.Background(), msg("!oil", bottest.StaticMedia(bot.MediaImage, "image/jpeg", big)))
	if got := e.fc.Last().Out.Text; got != mediafx.MsgTooLarge {
		t.Fatalf("grande: %q", got)
	}
}

// --- emojik ---

func TestEmojis(t *testing.T) {
	cases := map[string][]string{
		"🥹 😗":          {"🥹", "😗"},
		"❤️🔥":          {"❤️", "🔥"},
		"👍🏽 e 🇧🇷 x":    {"👍🏽", "🇧🇷"},
		"👨‍👩‍👧 1️⃣":    {"👨‍👩‍👧", "1️⃣"},
		"texto 12 só😀": {"😀"},
	}
	for in, want := range cases {
		if got := mediafx.Emojis(in, 2); !slices.Equal(got, want) {
			t.Errorf("Emojis(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEmojik(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		if strings.Contains(path, "💩") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("png"))
	}))
	defer srv.Close()
	p := &fakeProc{}
	e := setup(t, p, mediafx.Options{EmojikURL: srv.URL, HTTP: srv.Client()})

	e.r.Handle(context.Background(), msg("!memoji 🥹 😗", nil))
	out := e.fc.Last().Out
	meta, _ := media.ReadStickerMetadata(out.Data)
	if out.Kind != bot.OutSticker || meta.PackName != "Emojik: 🥹+😗" || path != "/s/🥹_😗" {
		t.Fatalf("saída %+v meta %+v path %q", out.Kind, meta, path)
	}

	e.r.Handle(context.Background(), msg("!emojik 💩 😗", nil))
	if got := e.fc.Last().Out.Text; !strings.Contains(got, "pode não estar disponível") {
		t.Fatalf("combinação inexistente: %q", got)
	}

	e.r.Handle(context.Background(), msg("!emojik oi", nil))
	if got := e.fc.Last().Out.Text; got != mediafx.MsgEmojikUsage {
		t.Fatalf("uso: %q", got)
	}

	// emojis da mensagem citada
	m := msg("!emojik", nil)
	m.Quoted = &bot.Message{ID: "q", Text: "olha 😎 🤠"}
	e.r.Handle(context.Background(), m)
	if path != "/s/😎_🤠" {
		t.Fatalf("citada: path %q", path)
	}
}

// --- áudio ---

func TestExtractAudioAndVoiceWithFFmpeg(t *testing.T) {
	conv := realConv(t)
	e := setup(t, conv, mediafx.Options{})
	audio := bottest.StaticMedia(bot.MediaAudio, "audio/wav", wav(1))

	m := msg("!extractaudio", nil)
	m.Quoted = &bot.Message{ID: "q", Media: audio}
	e.r.Handle(context.Background(), m)
	out := e.fc.Last().Out
	if out.Kind != bot.OutAudio || out.PTT || out.Mime != "audio/mpeg" || len(out.Data) < 100 {
		t.Fatalf("mp3 inesperado: %+v %q", out.Mime, out.Text)
	}

	e.r.Handle(context.Background(), msg("!extractvoice", audio))
	out = e.fc.Last().Out
	if out.Kind != bot.OutAudio || !out.PTT || !bytes.HasPrefix(out.Data, []byte("OggS")) {
		t.Fatalf("voz inesperada: ptt=%v %q", out.PTT, out.Text)
	}

	// vídeo sem áudio
	png := pngImage(t, 32, 32)
	silent, err := conv.FFmpeg(context.Background(), png, ".mp4", "-f", "lavfi", "-i", "color=c=blue:s=32x32:d=1",
		"-map", "1:v", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-f", "mp4")
	if err != nil {
		t.Fatal(err)
	}
	e.r.Handle(context.Background(), msg("!extractaudio", bottest.StaticMedia(bot.MediaVideo, "video/mp4", silent)))
	if got := e.fc.Last().Out.Text; got != "Essa mídia não tem áudio." {
		t.Fatalf("vídeo mudo: %q", got)
	}
}

func TestVolumeWithFFmpeg(t *testing.T) {
	conv := realConv(t)
	e := setup(t, conv, mediafx.Options{})
	ctx := context.Background()
	voice, err := conv.FFmpeg(ctx, wav(1), ".ogg", "-c:a", "libopus", "-f", "ogg")
	if err != nil {
		t.Fatal(err)
	}
	e.r.Handle(ctx, msg("!volume 300", bottest.StaticMedia(bot.MediaAudio, "audio/ogg; codecs=opus", voice)))
	if out := e.fc.Last().Out; out.Kind != bot.OutAudio || !out.PTT {
		t.Fatalf("voz: %+v %q", out.Kind, out.Text)
	}

	video, err := conv.FFmpeg(ctx, wav(1), ".mp4", "-f", "lavfi", "-i", "color=c=red:s=64x64:d=1",
		"-shortest", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-f", "mp4")
	if err != nil {
		t.Fatal(err)
	}
	m := msg("!volume 50", nil)
	m.Quoted = &bot.Message{ID: "q", Media: bottest.StaticMedia(bot.MediaVideo, "video/mp4", video)}
	e.r.Handle(ctx, m)
	if out := e.fc.Last().Out; out.Kind != bot.OutVideo || out.Caption != "🔊 Volume ajustado para 50%" {
		t.Fatalf("vídeo: %+v %q", out.Kind, out.Text)
	}
}

func TestVolumeArgsAndErrors(t *testing.T) {
	p := &fakeProc{}
	e := setup(t, p, mediafx.Options{})
	audio := bottest.StaticMedia(bot.MediaAudio, "audio/mpeg", []byte("a"))
	for text, want := range map[string]string{
		"!volume":      mediafx.MsgVolumeUsage,
		"!volume 2000": "Nível de volume inválido. Use um valor entre 0 e 1000.",
		"!volume abc":  "Nível de volume inválido. Use um valor entre 0 e 1000.",
	} {
		e.r.Handle(context.Background(), msg(text, audio))
		if got := e.fc.Last().Out.Text; got != want {
			t.Errorf("%s: %q", text, got)
		}
	}
	e.r.Handle(context.Background(), msg("!volume 150", nil))
	if got := e.fc.Last().Out.Text; got != mediafx.MsgNoAV {
		t.Fatalf("sem mídia: %q", got)
	}
	e.r.Handle(context.Background(), msg("!volume 150", audio))
	if !slices.Contains(p.args[0], "volume=1.50") || p.calls[0] != "ffmpeg.mp3" {
		t.Fatalf("args = %v %v", p.calls, p.args)
	}
	e.r.Handle(context.Background(), msg("!extractaudio", bottest.StaticMedia(bot.MediaImage, "image/jpeg", []byte("i"))))
	if got := e.fc.Last().Out.Text; got != mediafx.MsgNoAV {
		t.Fatalf("imagem: %q", got)
	}
}

// --- myinstants ---

const instantsPage = `<div class="instants result-page">
<div class="instant">
<div class="circle small-button-background" style="background-color:#ffff33;"></div>
<button class="small-button" onclick="play('/media/sounds/movie_1.mp3', 'loader-23010', 'bruh')" title="Play BRUH sound" type="button"></button>
<a href="/en/instant/bruh/" class="instant-link link-secondary">BRUH</a>
</div>
<div class="instant">
<button class="small-button" onclick="play('/media/sounds/b.mp3', 'loader-2', 'b')"></button>
<a class="instant-link link-secondary" href="/en/instant/b/">Bruh &amp; <b>Co</b></a>
</div>
<div class="instant"><a class="instant-link" href="/x/">sem botão</a></div>
</div>`

func TestParseInstants(t *testing.T) {
	got := mediafx.ParseInstants(instantsPage, "https://www.myinstants.com/")
	want := []mediafx.Instant{
		{Title: "BRUH", MP3: "https://www.myinstants.com/media/sounds/movie_1.mp3"},
		{Title: "Bruh & Co", MP3: "https://www.myinstants.com/media/sounds/b.mp3"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v", got)
	}
}

func TestInstantsCommand(t *testing.T) {
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/search/":
			query = r.URL.Query().Get("name")
			if query == "nada" {
				_, _ = w.Write([]byte("<html></html>"))
				return
			}
			_, _ = w.Write([]byte(instantsPage))
		case strings.HasPrefix(r.URL.Path, "/media/sounds/"):
			_, _ = w.Write([]byte("mp3"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	p := &fakeProc{}
	e := setup(t, p, mediafx.Options{MyInstantsURL: srv.URL, HTTP: srv.Client()})
	ctx := context.Background()

	e.r.Handle(ctx, msg("!audio", nil))
	if got := e.fc.Last().Out.Text; got != mediafx.MsgInstantsUsage {
		t.Fatalf("uso: %q", got)
	}

	e.r.Handle(ctx, msg("!audio bruh moment", nil))
	if got := e.fc.Last().Out.Text; query != "bruh moment" || !strings.Contains(got, "- 2. Bruh & Co") {
		t.Fatalf("lista: %q (query %q)", got, query)
	}

	e.r.Handle(ctx, msg("!áudio bruh 9", nil))
	if got := e.fc.Last().Out.Text; !strings.Contains(got, "entre 1 e 2") {
		t.Fatalf("índice inválido: %q", got)
	}

	e.r.Handle(ctx, msg("!audio nada", nil))
	if got := e.fc.Last().Out.Text; !strings.Contains(got, "Nenhum áudio") {
		t.Fatalf("vazio: %q", got)
	}

	n := len(e.fc.Replies)
	e.r.Handle(ctx, msg("!audio bruh 1", nil))
	if len(e.fc.Replies) != n+2 {
		t.Fatalf("esperava título + áudio, veio %d", len(e.fc.Replies)-n)
	}
	if got := e.fc.Replies[n].Out.Text; got != "▶️ _BRUH_" {
		t.Fatalf("título: %q", got)
	}
	if out := e.fc.Last().Out; out.Kind != bot.OutAudio || !out.PTT || query != "bruh" {
		t.Fatalf("áudio: %+v", out)
	}
	if !slices.Contains(p.args[0], "60") {
		t.Fatalf("deveria cortar em 60s: %v", p.args[0])
	}
}

// --- anime ---

func TestAnime(t *testing.T) {
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v4/anime":
			if r.URL.Query().Get("q") == "xyz" {
				_, _ = w.Write([]byte(`{"data":[]}`))
				return
			}
			fmt.Fprintf(w, `{"data":[{"title":"Naruto","title_japanese":"ナルト","type":"TV","source":"Manga",
"episodes":220,"status":"Finished Airing","duration":"23 min per ep","score":8.01,"rank":660,"popularity":8,
"synopsis":"Ninja.","aired":{"string":"Oct 3, 2002 to Feb 8, 2007"},"genres":[{"name":"Action"},{"name":"Adventure"}],
"studios":[{"name":"Pierrot"}],"images":{"jpg":{"large_image_url":"%s/cover.jpg"}}}]}`, srvURL)
		case "/cover.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("jpg"))
		}
	}))
	defer srv.Close()
	srvURL = srv.URL
	e := setup(t, &fakeProc{}, mediafx.Options{JikanURL: srv.URL, HTTP: srv.Client()})
	ctx := context.Background()

	e.r.Handle(ctx, msg("!anime naruto", nil))
	out := e.fc.Last().Out
	for _, s := range []string{"*Naruto* (ナルト)", "Oct 3, 2002 (Finished Airing @ TV)", "Action, Adventure", "220 (_23 min per ep_)", "8.01, #660", "Ninja."} {
		if !strings.Contains(out.Caption, s) {
			t.Errorf("legenda sem %q:\n%s", s, out.Caption)
		}
	}
	if out.Kind != bot.OutImage || out.Mime != "image/jpeg" {
		t.Fatalf("esperava imagem: %+v", out.Kind)
	}

	e.r.Handle(ctx, msg("!anime xyz", nil))
	if got := e.fc.Last().Out.Text; !strings.Contains(got, "Não foi possível encontrar") {
		t.Fatalf("não achou: %q", got)
	}
	e.r.Handle(ctx, msg("!anime", nil))
	if got := e.fc.Last().Out.Text; got != mediafx.MsgAnimeUsage {
		t.Fatalf("uso: %q", got)
	}
}

// --- apod ---

func TestParseAPODDate(t *testing.T) {
	now := time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)
	cases := map[string]string{"": "", "hoje": "", "ontem": "2024-02-29", "2023-05-06": "2023-05-06", "06/05/2023": "2023-05-06"}
	for in, want := range cases {
		if got, ok := mediafx.ParseAPODDate(in, now); !ok || got != want {
			t.Errorf("%q → %q %v", in, got, ok)
		}
	}
	if _, ok := mediafx.ParseAPODDate("semana passada", now); ok {
		t.Error("deveria recusar")
	}
}

func TestAPOD(t *testing.T) {
	var srvURL, gotKey, gotDate string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/apod":
			gotKey, gotDate = r.URL.Query().Get("api_key"), r.URL.Query().Get("date")
			if gotDate == "1990-01-01" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code":400,"msg":"Date must be between Jun 16, 1995 and today."}`))
				return
			}
			fmt.Fprintf(w, `{"date":"2024-01-15","title":"Nebula","explanation":"%s","media_type":"image",
"url":"%s/small.jpg","hdurl":"https://hd.example/big.jpg"}`, strings.Repeat("a", 900), srvURL)
		case "/small.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("jpg"))
		}
	}))
	defer srv.Close()
	srvURL = srv.URL
	e := setup(t, &fakeProc{}, mediafx.Options{APODURL: srv.URL + "/apod", HTTP: srv.Client()})
	ctx := context.Background()

	e.r.Handle(ctx, msg("!apod 15/01/2024", nil))
	out := e.fc.Last().Out
	if out.Kind != bot.OutImage || gotKey != "DEMO_KEY" || gotDate != "2024-01-15" {
		t.Fatalf("saída %v key %q date %q", out.Kind, gotKey, gotDate)
	}
	for _, s := range []string{"📅 *Data:* 15/01/2024", "*Título:* Nebula", "aaa...", "Alta Resolução:* https://hd.example/big.jpg"} {
		if !strings.Contains(out.Caption, s) {
			t.Errorf("legenda sem %q", s)
		}
	}

	e.r.Handle(ctx, msg("!apod 1990-01-01", nil))
	if got := e.fc.Last().Out.Text; got != "❌ Erro da NASA: Date must be between Jun 16, 1995 and today." {
		t.Fatalf("erro: %q", got)
	}
	e.r.Handle(ctx, msg("!apod amanhã talvez", nil))
	if got := e.fc.Last().Out.Text; !strings.HasPrefix(got, "Data inválida") {
		t.Fatalf("data inválida: %q", got)
	}
}
