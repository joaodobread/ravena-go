package download

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/bot/bottest"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type fakeFetcher struct {
	got   []string
	audio bool
	file  File
	err   error
}

func (f *fakeFetcher) Fetch(_ context.Context, target string, audio bool) (File, error) {
	f.got, f.audio = append(f.got, target), audio
	return f.file, f.err
}

type fakeResolver map[string][]string

func (r fakeResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	ips, ok := r[host]
	if !ok {
		return nil, errors.New("no such host")
	}
	var out []net.IPAddr
	for _, ip := range ips {
		out = append(out, net.IPAddr{IP: net.ParseIP(ip)})
	}
	return out, nil
}

func setup(t *testing.T, fetch *fakeFetcher) (*bot.Router, *bottest.FakeClient) {
	t.Helper()
	lrc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Query().Get("q"), "inexistente") {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(`[{"plainLyrics":""},{"plainLyrics":"la la la"}]`))
	}))
	t.Cleanup(lrc.Close)
	f := &Feature{
		fetch: fetch, http: lrc.Client(), lrclib: lrc.URL, log: quiet,
		resolver: fakeResolver{"www.tiktok.com": {"151.101.1.1"}, "evil.example": {"10.0.0.5"}},
	}
	fc := &bottest.FakeClient{}
	r := bot.NewRouter("!", fc, quiet)
	if err := r.Register(f.Commands()...); err != nil {
		t.Fatal(err)
	}
	return r, fc
}

func TestDownloadVideoFromArgsAndQuote(t *testing.T) {
	fetch := &fakeFetcher{file: File{Data: []byte("mp4"), Mime: "video/mp4", Title: "gatinho"}}
	r, fc := setup(t, fetch)
	r.Handle(context.Background(), bottest.GroupMsg("!tiktok https://www.tiktok.com/@a/video/1?x=1."))
	if out := fc.Last().Out; out.Kind != bot.OutVideo || out.Caption != "gatinho" || fetch.audio {
		t.Fatalf("saída: %+v", out)
	}
	m := bottest.GroupMsg("!dl")
	m.Quoted = &bot.Message{Text: "olha isso https://www.tiktok.com/@a/video/2 kkk"}
	r.Handle(context.Background(), m)
	if !slices.Equal(fetch.got, []string{"https://www.tiktok.com/@a/video/1?x=1", "https://www.tiktok.com/@a/video/2"}) {
		t.Fatalf("alvos: %q", fetch.got)
	}
}

func TestDownloadAudioByReaction(t *testing.T) {
	fetch := &fakeFetcher{file: File{Data: []byte("mp3"), Mime: "audio/mpeg"}}
	r, fc := setup(t, fetch)
	target := bottest.GroupMsg("https://www.tiktok.com/@a/video/3")
	r.HandleReaction(context.Background(), target, bot.Reaction{Emoji: "⏬", By: bottest.User})
	if out := fc.Last().Out; out.Kind != bot.OutAudio || !fetch.audio {
		t.Fatalf("saída: %+v", out)
	}
}

func TestRejectsInternalAndInvalidURLs(t *testing.T) {
	fetch := &fakeFetcher{}
	r, fc := setup(t, fetch)
	for _, u := range []string{
		"!dl", "!dl http://localhost:8080/x", "!dl http://127.0.0.1/x", "!dl http://[::1]/x",
		"!dl http://ravena.railway.internal:9000", "!dl https://evil.example/x", "!dl https://naoexiste.example/x",
		"!dl http://169.254.169.254/latest/meta-data",
	} {
		r.Handle(context.Background(), bottest.GroupMsg(u))
		if fc.Last().Out.Text == "" {
			t.Errorf("%q deveria ser recusado com mensagem", u)
		}
	}
	if len(fetch.got) != 0 {
		t.Fatalf("nada deveria ter sido baixado: %q", fetch.got)
	}
}

func TestFetchErrorsBecomeFriendlyMessages(t *testing.T) {
	fetch := &fakeFetcher{err: ErrTooBig}
	r, fc := setup(t, fetch)
	r.Handle(context.Background(), bottest.GroupMsg("!dl https://www.tiktok.com/x"))
	if !strings.Contains(fc.Last().Out.Text, "50 MB") {
		t.Fatalf("too big: %q", fc.Last().Out.Text)
	}
	fetch.err = errors.New("yt-dlp: exit 1: ERROR: Unsupported URL")
	r.Handle(context.Background(), bottest.GroupMsg("!dl https://www.tiktok.com/y"))
	if got := fc.Last().Out.Text; !strings.Contains(got, "Não consegui baixar") || strings.Contains(got, "yt-dlp") {
		t.Fatalf("erro genérico: %q", got)
	}
}

func TestSongRequestSendsAudioAndLyrics(t *testing.T) {
	fetch := &fakeFetcher{file: File{Data: []byte("mp3"), Mime: "audio/mpeg", Title: "Música"}}
	r, fc := setup(t, fetch)
	r.Handle(context.Background(), bottest.GroupMsg("!sr artista - musica"))
	if fetch.got[0] != "ytsearch1:artista - musica" || !fetch.audio {
		t.Fatalf("busca: %q", fetch.got)
	}
	texts := fc.Texts()
	if len(fc.Replies) != 2 || fc.Replies[0].Out.Kind != bot.OutAudio || !strings.Contains(texts[0], "la la la") {
		t.Fatalf("respostas: %+v", fc.Replies)
	}
}

func TestLyricsCommand(t *testing.T) {
	r, fc := setup(t, &fakeFetcher{})
	r.Handle(context.Background(), bottest.GroupMsg("!letra boa musica"))
	if !strings.Contains(fc.Last().Out.Text, "la la la") {
		t.Fatalf("letra: %q", fc.Last().Out.Text)
	}
	r.Handle(context.Background(), bottest.GroupMsg("!letra inexistente"))
	if !strings.Contains(fc.Last().Out.Text, "Não achei") {
		t.Fatalf("sem letra: %q", fc.Last().Out.Text)
	}
	r.Handle(context.Background(), bottest.GroupMsg("!letra"))
	if !strings.Contains(fc.Last().Out.Text, "Diga a música") {
		t.Fatalf("sem argumento: %q", fc.Last().Out.Text)
	}
}

func TestYTDLPArgs(t *testing.T) {
	y := NewYTDLP("", "", 1000)
	video := strings.Join(y.Args("https://x", "/tmp/d", false), " ")
	audio := strings.Join(y.Args("ytsearch1:a", "/tmp/d", true), " ")
	if !strings.Contains(video, "--max-filesize 1000") || !strings.Contains(video, "--merge-output-format mp4") || !strings.HasSuffix(video, "-- https://x") {
		t.Fatalf("args de vídeo: %s", video)
	}
	if !strings.Contains(audio, "--audio-format mp3") || !strings.Contains(audio, "--no-playlist") {
		t.Fatalf("args de áudio: %s", audio)
	}
}
