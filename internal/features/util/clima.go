package util

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/joaodobread/ravena-go/internal/bot"
)

var brStates = map[string]string{
	"AC": "Acre", "AL": "Alagoas", "AP": "Amapá", "AM": "Amazonas", "BA": "Bahia", "CE": "Ceará",
	"DF": "Distrito Federal", "ES": "Espírito Santo", "GO": "Goiás", "MA": "Maranhão",
	"MT": "Mato Grosso", "MS": "Mato Grosso do Sul", "MG": "Minas Gerais", "PA": "Pará",
	"PB": "Paraíba", "PR": "Paraná", "PE": "Pernambuco", "PI": "Piauí", "RJ": "Rio de Janeiro",
	"RN": "Rio Grande do Norte", "RS": "Rio Grande do Sul", "RO": "Rondônia", "RR": "Roraima",
	"SC": "Santa Catarina", "SP": "São Paulo", "SE": "Sergipe", "TO": "Tocantins",
}

type wmo struct{ desc, emoji string }

var wmoCodes = map[int]wmo{
	0: {"Céu limpo", "☀️"}, 1: {"Predominantemente limpo", "🌤️"}, 2: {"Parcialmente nublado", "⛅"},
	3: {"Nublado", "☁️"}, 45: {"Nevoeiro", "🌫️"}, 48: {"Nevoeiro com rima", "🌫️"},
	51: {"Garoa leve", "🌦️"}, 53: {"Garoa moderada", "🌦️"}, 55: {"Garoa densa", "🌦️"},
	56: {"Garoa congelante leve", "❄️🌦️"}, 57: {"Garoa congelante densa", "❄️🌦️"},
	61: {"Chuva fraca", "🌧️"}, 63: {"Chuva moderada", "🌧️"}, 65: {"Chuva forte", "🌧️"},
	66: {"Chuva congelante leve", "❄️🌧️"}, 67: {"Chuva congelante forte", "❄️🌧️"},
	71: {"Neve fraca", "❄️"}, 73: {"Neve moderada", "❄️"}, 75: {"Neve forte", "❄️"}, 77: {"Grãos de neve", "❄️"},
	80: {"Pancadas de chuva leve", "🌦️"}, 81: {"Pancadas de chuva moderada", "🌦️"}, 82: {"Pancadas de chuva violenta", "🌦️"},
	85: {"Pancadas de neve leve", "❄️"}, 86: {"Pancadas de neve forte", "❄️"},
	95: {"Trovoada", "⛈️"}, 96: {"Trovoada com granizo leve", "⛈️🌨️"}, 99: {"Trovoada com granizo forte", "⛈️🌨️"},
}

func wmoInfo(code int) wmo {
	if w, ok := wmoCodes[code]; ok {
		return w
	}
	return wmo{"Desconhecido", "🌡️"}
}

var windNames = []string{"N", "NNE", "NE", "ENE", "E", "ESE", "SE", "SSE", "S", "SSW", "SW", "WSW", "W", "WNW", "NW", "NNW"}
var windEmojis = []string{"⬆️", "↗️", "↗️", "↗️", "👉", "↘️", "↘️", "↘️", "⬇️", "↙️", "↙️", "↙️", "👈", "↖️", "↖️", "↖️"}

func windDir(deg float64) (string, string) {
	d := math.Mod(math.Mod(deg, 360)+360, 360)
	i := int(math.Floor((d+11.25)/22.5)) % 16
	return windNames[i], windEmojis[i]
}

var weekdays = [...]string{"Domingo", "Segunda-feira", "Terça-feira", "Quarta-feira", "Quinta-feira", "Sexta-feira", "Sábado"}

var ufRe = regexp.MustCompile(`^(.*?)[\s,\-]+([a-zA-Z]{2})$`)

// parseCity separa "Santa Maria, RS" / "Santa Maria Rio Grande do Sul" em
// cidade e estado (nome completo, "" se não reconhecido).
func parseCity(q string) (city, state string) {
	q = strings.TrimSpace(q)
	if m := ufRe.FindStringSubmatch(q); m != nil {
		if s, ok := brStates[strings.ToUpper(m[2])]; ok && strings.TrimSpace(m[1]) != "" {
			return strings.TrimSpace(m[1]), s
		}
	}
	r := []rune(q)
	for _, st := range brStates {
		n := len([]rune(st))
		if len(r) <= n || fold(string(r[len(r)-n:])) != fold(st) || !strings.ContainsRune(" ,-", r[len(r)-n-1]) {
			continue
		}
		if rest := strings.TrimRight(string(r[:len(r)-n]), " ,-"); rest != "" {
			return rest, st
		}
	}
	return q, ""
}

var foldReplacer = strings.NewReplacer("á", "a", "à", "a", "â", "a", "ã", "a", "é", "e", "ê", "e",
	"í", "i", "ó", "o", "ô", "o", "õ", "o", "ú", "u", "ç", "c")

func fold(s string) string { return foldReplacer.Replace(strings.ToLower(s)) }

type geoResult struct {
	Name        string  `json:"name"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	CountryCode string  `json:"country_code"`
	Country     string  `json:"country"`
	Admin1      string  `json:"admin1"`
	Admin2      string  `json:"admin2"`
}

type forecast struct {
	Current struct {
		Temp      float64 `json:"temperature_2m"`
		Humidity  float64 `json:"relative_humidity_2m"`
		Apparent  float64 `json:"apparent_temperature"`
		Code      int     `json:"weather_code"`
		Pressure  float64 `json:"pressure_msl"`
		WindSpeed float64 `json:"wind_speed_10m"`
		WindDir   float64 `json:"wind_direction_10m"`
		Gusts     float64 `json:"wind_gusts_10m"`
	} `json:"current"`
	Daily struct {
		Time    []string  `json:"time"`
		Code    []int     `json:"weather_code"`
		Max     []float64 `json:"temperature_2m_max"`
		Min     []float64 `json:"temperature_2m_min"`
		Sunrise []string  `json:"sunrise"`
		Sunset  []string  `json:"sunset"`
		UV      []float64 `json:"uv_index_max"`
		Rain    []float64 `json:"precipitation_probability_max"`
	} `json:"daily"`
}

func num(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func (f *Feature) clima(ctx context.Context, c *bot.Context) error {
	query := c.ArgText()
	if query == "" {
		return bot.Userf("❌ Por favor, digite uma cidade.\nEx: `%sclima Santa Maria, RS`", c.Prefix)
	}
	city, state := parseCity(query)

	q := url.Values{"name": {city}, "count": {"10"}, "language": {"pt"}, "format": {"json"}}
	var geo struct {
		Results []geoResult `json:"results"`
	}
	if err := f.getJSON(ctx, f.urls.Geo+"/v1/search?"+q.Encode(), &geo); err != nil {
		return f.upstream(err, "open-meteo geocoding", "❌ Não foi possível obter o clima agora. Tente novamente mais tarde.")
	}
	if len(geo.Results) == 0 {
		return bot.Userf("❌ Cidade não encontrada: %s", query)
	}
	loc := pickCity(geo.Results, state)

	fq := url.Values{
		"latitude":      {num(loc.Latitude)},
		"longitude":     {num(loc.Longitude)},
		"current":       {"temperature_2m,relative_humidity_2m,apparent_temperature,is_day,weather_code,pressure_msl,wind_speed_10m,wind_direction_10m,wind_gusts_10m"},
		"daily":         {"weather_code,temperature_2m_max,temperature_2m_min,sunrise,sunset,uv_index_max,precipitation_probability_max"},
		"timezone":      {"auto"},
		"forecast_days": {"4"},
	}
	var fc forecast
	if err := f.getJSON(ctx, f.urls.Forecast+"/v1/forecast?"+fq.Encode(), &fc); err != nil {
		return f.upstream(err, "open-meteo forecast", "❌ Não foi possível obter o clima agora. Tente novamente mais tarde.")
	}
	return c.ReplyText(ctx, "%s", formatWeather(loc, fc))
}

func pickCity(results []geoResult, state string) geoResult {
	for _, r := range results {
		if state != "" && r.CountryCode == "BR" && (r.Admin1 == state || r.Admin2 == state) {
			return r
		}
	}
	if results[0].CountryCode != "BR" {
		for _, r := range results {
			if r.CountryCode == "BR" {
				return r
			}
		}
	}
	return results[0]
}

func at[T any](s []T, i int) T {
	var zero T
	if i < len(s) {
		return s[i]
	}
	return zero
}

func clock(iso string) string {
	if _, t, ok := strings.Cut(iso, "T"); ok {
		return t
	}
	return iso
}

func formatWeather(loc geoResult, w forecast) string {
	var b strings.Builder
	cur := w.Current
	info := wmoInfo(cur.Code)
	place := loc.Name
	if loc.Admin1 != "" {
		place += ", " + loc.Admin1
	}
	if loc.Country != "" {
		place += " - " + loc.Country
	}
	fmt.Fprintf(&b, "*🌍 %s*\n", place)
	fmt.Fprintf(&b, "%s *%s°C* (%s)\n", info.emoji, num(cur.Temp), info.desc)
	fmt.Fprintf(&b, "🔥 Sensação: %s°C | 💧 Umidade: %s%%\n", num(cur.Apparent), num(cur.Humidity))

	d := w.Daily
	if len(d.Time) > 1 {
		b.WriteString("\n*📆 Previsão Próximos Dias:*\n")
		for i := 1; i <= 3 && i < len(d.Time); i++ {
			day := d.Time[i]
			if t, err := time.Parse("2006-01-02", day); err == nil {
				day = weekdays[t.Weekday()]
			}
			fmt.Fprintf(&b, "%s *%s:* %.0f°C a %.0f°C - %s%% ☔\n", wmoInfo(at(d.Code, i)).emoji, day,
				math.Round(at(d.Min, i)), math.Round(at(d.Max, i)), num(at(d.Rain, i)))
		}
	}

	name, emoji := windDir(cur.WindDir)
	b.WriteString("\n*📊 Detalhes Adicionais:*\n")
	fmt.Fprintf(&b, "%s Vento: %s km/h (%s) | Rajadas: %s km/h\n", emoji, num(cur.WindSpeed), name, num(cur.Gusts))
	fmt.Fprintf(&b, "📈 Pressão: %s hPa | ☀️ UV Máx: %s\n", num(cur.Pressure), num(at(d.UV, 0)))
	if len(d.Sunrise) > 0 && len(d.Sunset) > 0 {
		fmt.Fprintf(&b, "🌅 Nascer: %s | 🌇 Pôr do sol: %s\n", clock(d.Sunrise[0]), clock(d.Sunset[0]))
	}
	return strings.TrimRight(b.String(), "\n")
}
