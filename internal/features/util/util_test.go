package util_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
	"rsc.io/qr"

	"github.com/joaodobread/ravena-go/internal/app"
	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/bot/bottest"
	"github.com/joaodobread/ravena-go/internal/features/util"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type env struct {
	router *bot.Router
	client *bottest.FakeClient
	srv    *httptest.Server
	now    time.Time

	mu       sync.Mutex
	handlers map[string]http.HandlerFunc
	queries  map[string]string // path -> última query
}

func (e *env) handle(path string, h http.HandlerFunc) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.handlers[path] = h
}

func (e *env) query(path string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.queries[path]
}

func jsonBody(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	}
}

func status(code int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }
}

func setup(t *testing.T) *env {
	t.Helper()
	e := &env{handlers: map[string]http.HandlerFunc{}, queries: map[string]string{}, now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		h, ok := e.handlers[r.URL.Path]
		e.queries[r.URL.Path] = r.URL.RawQuery
		e.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		h(w, r)
	}))
	t.Cleanup(e.srv.Close)

	e.client = &bottest.FakeClient{Names: map[types.JID]string{}}
	e.router = bot.NewRouter("!", e.client, quiet)
	start := e.now
	clock := func() time.Time {
		e.mu.Lock()
		defer e.mu.Unlock()
		if start.IsZero() {
			return e.now
		}
		s := start
		start = time.Time{}
		return s
	}
	m := util.Module{
		URLs: util.URLs{Wiki: e.srv.URL, Geo: e.srv.URL, Forecast: e.srv.URL, DuckDuckGo: e.srv.URL + "/ddg", GamerPower: e.srv.URL},
		Now:  clock,
	}
	d := &app.Deps{Router: e.router, Client: e.client, HTTP: e.srv.Client(), Log: quiet}
	if err := m.Register(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) send(text string) bottest.Reply {
	e.router.Handle(context.Background(), bottest.GroupMsg(text))
	return e.client.Last()
}

func (e *env) text(t *testing.T, cmd string, want ...string) string {
	t.Helper()
	out := e.send(cmd).Out
	if out.Kind != bot.OutText {
		t.Fatalf("%s: esperava texto, veio %+v", cmd, out)
	}
	for _, w := range want {
		if !strings.Contains(out.Text, w) {
			t.Fatalf("%s: faltou %q em:\n%s", cmd, w, out.Text)
		}
	}
	return out.Text
}

func pngOf(t *testing.T, s string) []byte {
	t.Helper()
	c, err := qr.Encode(s, qr.M)
	if err != nil {
		t.Fatal(err)
	}
	return c.PNG()
}

// ---------- ping ----------

func TestPing(t *testing.T) {
	e := setup(t)
	e.mu.Lock()
	e.now = e.now.Add(3*time.Hour + 5*time.Minute)
	e.mu.Unlock()
	msg := bottest.GroupMsg("!ping")
	msg.Timestamp = e.now.Add(-1500 * time.Millisecond)
	e.router.Handle(context.Background(), msg)
	out := e.client.Last().Out.Text
	if !strings.Contains(out, "Pong! 🏓 _(1.5s)_") || !strings.Contains(out, "Online há 3h 5min") {
		t.Fatalf("ping = %q", out)
	}
	if got := e.client.Reactions; len(got) != 1 || got[0] != "🍭" {
		t.Fatalf("reações = %q", got)
	}
	// Sem timestamp: só pong + uptime.
	if out := e.text(t, "!ping", "Pong!"); strings.Contains(out, "_(") {
		t.Fatalf("latência sem timestamp: %q", out)
	}
}

// ---------- qr ----------

func TestQR(t *testing.T) {
	e := setup(t)
	out := e.send("!qr https://exemplo.com/a b").Out
	if out.Kind != bot.OutImage || out.Mime != "image/png" || !bytes.Equal(out.Data, pngOf(t, "https://exemplo.com/ab")) {
		t.Fatalf("qr de URL inesperado: kind=%v caption=%q", out.Kind, out.Caption)
	}
	if !strings.Contains(out.Caption, "https://exemplo.com/ab") {
		t.Fatalf("legenda = %q", out.Caption)
	}

	msg := bottest.GroupMsg("!qr")
	msg.Quoted = &bot.Message{ID: "q", Text: "texto citado"}
	e.router.Handle(context.Background(), msg)
	if out := e.client.Last().Out; !bytes.Equal(out.Data, pngOf(t, "texto citado")) {
		t.Fatal("qr da mensagem citada errado")
	}

	e.text(t, "!qr", "forneça o texto")
	e.text(t, "!qr "+strings.Repeat("x", 4000), "longo demais")
}

func TestQRWifi(t *testing.T) {
	e := setup(t)
	out := e.send("!qr-wifi Minha Rede;1\nsenha:secreta\nsim").Out
	want := `WIFI:S:Minha Rede\;1;T:WPA;P:senha\:secreta;H:true;;`
	if out.Kind != bot.OutImage || !bytes.Equal(out.Data, pngOf(t, want)) {
		t.Fatalf("qr-wifi multilinha errado: %q", out.Caption)
	}
	if !strings.Contains(out.Caption, "*SSID:* Minha Rede;1") || !strings.Contains(out.Caption, "*Oculta:* Sim") {
		t.Fatalf("legenda = %q", out.Caption)
	}

	out = e.send("!qr-wifi Casa 12345678").Out
	if !bytes.Equal(out.Data, pngOf(t, "WIFI:S:Casa;T:WPA;P:12345678;H:false;;")) {
		t.Fatal("qr-wifi em uma linha errado")
	}
	e.text(t, "!qr-wifi SoSSID", "Uso correto")
}

func TestQRPix(t *testing.T) {
	e := setup(t)
	msg := bottest.GroupMsg("!qr-pix 123e4567-e12b-12d1-a456-426655440000 Pizza 25,5")
	msg.PushName = "Zé Ninguém"
	e.router.Handle(context.Background(), msg)
	out := e.client.Last().Out
	if out.Kind != bot.OutImage {
		t.Fatalf("esperava imagem: %+v", out)
	}
	for _, w := range []string{"Chave Aleatória", "*Descrição:* Pizza", "R$ 25,50", "540525.50", "5910Ze Ninguem", "0205Pizza"} {
		if !strings.Contains(out.Caption, w) {
			t.Fatalf("faltou %q na legenda:\n%s", w, out.Caption)
		}
	}
	start := strings.Index(out.Caption, "```") + 3
	payload := out.Caption[start : len(out.Caption)-3]
	if !bytes.Equal(out.Data, pngOf(t, payload)) {
		t.Fatal("QR não corresponde ao copia e cola")
	}

	// Só a chave, e só a chave + valor.
	if out := e.send("!qr-pix fulano@email.com").Out; out.Kind != bot.OutImage || strings.Contains(out.Caption, "Valor") {
		t.Fatalf("qr-pix só chave: %q", out.Caption)
	}
	if out := e.send("!qr-pix 11999999999 10").Out; !strings.Contains(out.Caption, "Chave Celular") || !strings.Contains(out.Caption, "R$ 10,00") {
		t.Fatalf("qr-pix celular: %q", out.Caption)
	}

	e.text(t, "!qr-pix", "Uso correto")
	e.text(t, "!qr-pix chave-ruim", "Chave PIX inválida")
}

// ---------- wiki ----------

func TestWiki(t *testing.T) {
	e := setup(t)
	e.handle("/w/api.php", jsonBody(`{"query":{"search":[{"title":"São Paulo"}]}}`))
	e.handle("/api/rest_v1/page/summary/São_Paulo", jsonBody(`{"title":"São Paulo","description":"município brasileiro","extract":"`+strings.Repeat("a", 1200)+`","content_urls":{"desktop":{"page":"https://pt.wikipedia.org/wiki/S%C3%A3o_Paulo"}},"thumbnail":{"source":"`+e.srv.URL+`/thumb.jpg"}}`))
	e.handle("/thumb.jpg", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("jpeg"))
	})
	out := e.send("!wiki sao paulo").Out
	if out.Kind != bot.OutImage || string(out.Data) != "jpeg" {
		t.Fatalf("esperava imagem: %+v", out)
	}
	for _, w := range []string{"📚 *São Paulo*", "*município brasileiro*", "…", "Leia mais:* https://pt.wikipedia.org/wiki/S%C3%A3o_Paulo"} {
		if !strings.Contains(out.Caption, w) {
			t.Fatalf("faltou %q:\n%s", w, out.Caption)
		}
	}
	if q := e.query("/w/api.php"); !strings.Contains(q, "srsearch=sao+paulo") {
		t.Fatalf("query = %q", q)
	}

	// Miniatura quebrada: cai no texto.
	e.handle("/thumb.jpg", status(500))
	e.text(t, "!wiki sao paulo", "📚 *São Paulo*")
}

func TestWikiErrors(t *testing.T) {
	e := setup(t)
	e.text(t, "!wiki", "forneça um termo")

	e.handle("/w/api.php", jsonBody(`{"query":{"search":[]}}`))
	e.text(t, "!wiki xyzzy", "Não foi possível encontrar")

	e.handle("/w/api.php", jsonBody(`{"query":{"search":[{"title":"X"}]}}`))
	e.text(t, "!wiki x", "Não foi possível encontrar uma página") // summary 404

	e.handle("/w/api.php", status(429))
	e.text(t, "!wiki x", "Muitas solicitações")

	e.handle("/w/api.php", status(500))
	e.text(t, "!wiki x", "Erro ao buscar informações da Wikipedia")
}

// ---------- clima ----------

const forecastJSON = `{"current":{"temperature_2m":23.4,"relative_humidity_2m":80,"apparent_temperature":25.1,"weather_code":2,"pressure_msl":1012.3,"wind_speed_10m":10.5,"wind_direction_10m":180,"wind_gusts_10m":20},
"daily":{"time":["2026-01-01","2026-01-02","2026-01-03","2026-01-04"],"weather_code":[2,61,0,95],"temperature_2m_max":[30,28.6,31,27],"temperature_2m_min":[20,19.4,21,18],
"sunrise":["2026-01-01T05:40","x","x","x"],"sunset":["2026-01-01T19:30","x","x","x"],"uv_index_max":[9.5,1,1,1],"precipitation_probability_max":[10,80,0,90]}}`

func TestClima(t *testing.T) {
	e := setup(t)
	e.handle("/v1/search", jsonBody(`{"results":[
		{"name":"Santa Maria","latitude":1,"longitude":2,"country_code":"US","country":"EUA","admin1":"California"},
		{"name":"Santa Maria","latitude":-29.68,"longitude":-53.8,"country_code":"BR","country":"Brasil","admin1":"Rio Grande do Sul"}]}`))
	e.handle("/v1/forecast", jsonBody(forecastJSON))

	out := e.text(t, "!clima Santa Maria, RS",
		"*🌍 Santa Maria, Rio Grande do Sul - Brasil*", "⛅ *23.4°C* (Parcialmente nublado)", "Sensação: 25.1°C | 💧 Umidade: 80%",
		"🌧️ *Sexta-feira:* 19°C a 29°C - 80% ☔", "⛈️ *Domingo:*", "⬇️ Vento: 10.5 km/h (S)", "UV Máx: 9.5", "Nascer: 05:40 | 🌇 Pôr do sol: 19:30")
	_ = out
	if q := e.query("/v1/search"); !strings.Contains(q, "name=Santa+Maria") {
		t.Fatalf("geocoding query = %q", q)
	}
	if q := e.query("/v1/forecast"); !strings.Contains(q, "latitude=-29.68") || !strings.Contains(q, "longitude=-53.8") {
		t.Fatalf("forecast query = %q", q)
	}
}

func TestClimaErrors(t *testing.T) {
	e := setup(t)
	e.text(t, "!clima", "digite uma cidade")

	e.handle("/v1/search", jsonBody(`{}`))
	e.text(t, "!clima Atlantida", "Cidade não encontrada")

	e.handle("/v1/search", status(503))
	e.text(t, "!clima Lisboa", "Não foi possível obter o clima")

	e.handle("/v1/search", jsonBody(`{"results":[{"name":"Lisboa","country_code":"PT","country":"Portugal"}]}`))
	e.handle("/v1/forecast", jsonBody(`não é json`))
	e.text(t, "!clima Lisboa", "Não foi possível obter o clima")
}

// ---------- buscar ----------

func TestBuscar(t *testing.T) {
	e := setup(t)
	e.handle("/ddg/", jsonBody(`{"AbstractText":"Go &amp; cia","AbstractSource":"Wikipedia","AbstractURL":"https://w/go","Infobox":"",
		"RelatedTopics":[{"Text":"Primeiro","FirstURL":"https://1"},{"Name":"Grupo","Topics":[{"Text":"Aninhado","FirstURL":"https://2"}]}]}`))
	e.text(t, "!google golang", `Resultados para "golang"`, "*Wikipedia:*\nGo & cia", "1. Primeiro", "🔗 https://1", "2. Aninhado")
	if q := e.query("/ddg/"); !strings.Contains(q, "q=golang") || !strings.Contains(q, "format=json") {
		t.Fatalf("query = %q", q)
	}

	e.handle("/ddg/", jsonBody(`{"Infobox":{"content":[{"label":"Nascimento","value":"1990"},{"label":"x","value":{"obj":1}}]},"RelatedTopics":[]}`))
	if out := e.text(t, "!buscar fulano", "Nascimento: 1990"); strings.Contains(out, "obj") {
		t.Fatalf("infobox com objeto: %q", out)
	}

	e.handle("/ddg/", jsonBody(`{"RelatedTopics":[]}`))
	e.text(t, "!buscar nada disso", "Não foram encontrados", "duckduckgo.com/?q=nada+disso")
}

func TestBuscarErrors(t *testing.T) {
	e := setup(t)
	e.text(t, "!buscar", "forneça uma consulta")
	e.handle("/ddg/", status(500))
	e.text(t, "!buscar algo", "Erro ao realizar busca")
}

// ---------- 0800 ----------

func TestGiveaways(t *testing.T) {
	e := setup(t)
	e.handle("/api/giveaways", jsonBody(`[{"title":"Jogo A","type":"Game","platforms":"PC, Steam","open_giveaway_url":"https://a"},
		{"title":"DLC B","type":"DLC","platforms":"PC","open_giveaway_url":"https://b"}]`))
	e.handle("/api/worth", jsonBody(`{"active_giveaways_number":2,"worth_estimation_usd":"$12.34"}`))

	out := e.text(t, "!0800 steam", "📍 Filtro: *STEAM*", "🕹️ *JOGOS:*", "*Jogo A*\n👾 💻 PC, Steam\n🔗 https://a", "DLCs, CUPONS", "*DLC B*", "US$12.34")
	if strings.Index(out, "Jogo A") > strings.Index(out, "DLC B") {
		t.Fatal("jogos deveriam vir antes dos DLCs")
	}
	if q := e.query("/api/giveaways"); !strings.Contains(q, "platform=steam") || !strings.Contains(q, "sort-by=popularity") {
		t.Fatalf("query = %q", q)
	}

	// Valor indisponível não impede a lista.
	e.handle("/api/worth", status(500))
	if out := e.text(t, "!giveaways", "Jogo A"); strings.Contains(out, "ECONOMIA") {
		t.Fatalf("economia sem dados: %q", out)
	}
}

func TestGiveawaysErrors(t *testing.T) {
	e := setup(t)
	e.handle("/api/worth", jsonBody(`{}`))
	e.handle("/api/giveaways", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"status":0,"status_message":"No active giveaways available at the moment."}`)
	})
	e.text(t, "!0800 ps5", "Nenhum brinde encontrado para a plataforma *ps5*")
	e.text(t, "!0800 <script>", "Plataforma inválida")

	e.handle("/api/giveaways", status(502))
	e.text(t, "!0800", "Erro ao buscar brindes")
}

// ---------- builds ----------

func TestBuilds(t *testing.T) {
	e := setup(t)
	e.text(t, "!lol-build Lee Sin", "League of Legends", "*Campeão:* Lee Sin", "https://op.gg/lol/champions/lee-sin/build")
	e.text(t, "!wr-build kai'sa", "Wild Rift", "https://www.wildriftfire.com/guide/kaisa")
	e.text(t, "!lol-build", "informe o nome do campeão", "!lol-build aatrox")
	e.text(t, "!wr-build ???", "informe o nome do campeão")
}
