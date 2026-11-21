package reminders

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ParseWhen interpreta o início de text como uma data/hora em português e
// devolve o momento e o resto do texto. Formatos:
//
//	em 10 minutos | daqui a 2 horas | em 3 dias | 30min | 2h
//	hoje às 18h | amanhã 9:30 | depois de amanhã às 7h
//	às 15h (hoje, ou amanhã se já passou)
//	25/12 às 10h | 25/12/2026 10:00
//	segunda às 9h (próxima segunda)
func ParseWhen(text string, now time.Time) (time.Time, string, bool) {
	s := fold(strings.TrimSpace(text))
	orig := strings.TrimSpace(text)
	rest := func(consumed int) string {
		// fold só troca letras acentuadas por 1 rune; contar runes mantém o alinhamento.
		r := []rune(orig)
		if consumed > len(r) {
			consumed = len(r)
		}
		return strings.TrimSpace(strings.TrimLeft(string(r[consumed:]), " ,:-"))
	}

	if m := relRE.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		d := unitDuration(m[2])
		if n > 0 && d > 0 && n <= 10000 {
			return now.Add(time.Duration(n) * d), rest(runeLen(m[0])), true
		}
	}

	day, consumed, hasDay := parseDay(s, now)
	clockStr := s[byteIndex(s, consumed):]
	if m := clockRE.FindStringSubmatch(clockStr); m != nil {
		h, _ := strconv.Atoi(m[1])
		mi, _ := strconv.Atoi(m[2])
		if h > 23 || mi > 59 {
			return time.Time{}, "", false
		}
		base := now
		if hasDay {
			base = day
		}
		at := time.Date(base.Year(), base.Month(), base.Day(), h, mi, 0, 0, now.Location())
		if !hasDay && !at.After(now) {
			at = at.AddDate(0, 0, 1) // "às 8h" já passou hoje → amanhã
		}
		return at, rest(consumed + runeLen(m[0])), true
	}
	if hasDay {
		// Só o dia: 9h da manhã.
		at := time.Date(day.Year(), day.Month(), day.Day(), 9, 0, 0, 0, now.Location())
		return at, rest(consumed), true
	}
	return time.Time{}, "", false
}

var (
	relRE   = regexp.MustCompile(`^(?:em|daqui a|daqui|dentro de)?\s*(\d+)\s*(segundos?|seg|s|minutos?|mins?|m|horas?|hrs?|h|dias?|d|semanas?)\b`)
	clockRE = regexp.MustCompile(`^\s*(?:as|a|@)?\s*(\d{1,2})(?:(?:h|:)(\d{2})?(?:min)?|\s*horas?)\b`)
	dateRE  = regexp.MustCompile(`^(?:dia\s+)?(\d{1,2})/(\d{1,2})(?:/(\d{2,4}))?`)
)

var weekdays = map[string]time.Weekday{
	"domingo": time.Sunday, "segunda": time.Monday, "segunda-feira": time.Monday,
	"terca": time.Tuesday, "terca-feira": time.Tuesday, "quarta": time.Wednesday, "quarta-feira": time.Wednesday,
	"quinta": time.Thursday, "quinta-feira": time.Thursday, "sexta": time.Friday, "sexta-feira": time.Friday,
	"sabado": time.Saturday,
}

// parseDay reconhece o dia no começo de s e quantos runes consumiu.
func parseDay(s string, now time.Time) (time.Time, int, bool) {
	for _, p := range []struct {
		word string
		days int
	}{{"depois de amanha", 2}, {"amanha", 1}, {"hoje", 0}} {
		if strings.HasPrefix(s, p.word) {
			return now.AddDate(0, 0, p.days), runeLen(p.word), true
		}
	}
	if m := dateRE.FindStringSubmatch(s); m != nil {
		d, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		y := now.Year()
		if m[3] != "" {
			y, _ = strconv.Atoi(m[3])
			if y < 100 {
				y += 2000
			}
		}
		t := time.Date(y, time.Month(mo), d, 0, 0, 0, 0, now.Location())
		if t.Day() != d || int(t.Month()) != mo { // 31/02 etc.
			return time.Time{}, 0, false
		}
		if m[3] == "" && t.Before(time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())) {
			t = t.AddDate(1, 0, 0) // 05/01 em dezembro → ano que vem
		}
		return t, runeLen(m[0]), true
	}
	word, _, _ := strings.Cut(s, " ")
	if wd, ok := weekdays[word]; ok {
		delta := (int(wd) - int(now.Weekday()) + 7) % 7
		if delta == 0 {
			delta = 7
		}
		return now.AddDate(0, 0, delta), runeLen(word), true
	}
	return time.Time{}, 0, false
}

func unitDuration(u string) time.Duration {
	switch {
	case strings.HasPrefix(u, "sem"):
		return 7 * 24 * time.Hour
	case strings.HasPrefix(u, "seg") || u == "s":
		return time.Second
	case strings.HasPrefix(u, "min") || u == "m":
		return time.Minute
	case strings.HasPrefix(u, "h"):
		return time.Hour
	case strings.HasPrefix(u, "d"):
		return 24 * time.Hour
	}
	return 0
}

var accents = strings.NewReplacer(
	"á", "a", "à", "a", "â", "a", "ã", "a", "é", "e", "ê", "e", "í", "i",
	"ó", "o", "ô", "o", "õ", "o", "ú", "u", "ü", "u", "ç", "c",
)

// fold deixa minúsculo e sem acentos, preservando a contagem de runes.
func fold(s string) string { return accents.Replace(strings.ToLower(s)) }

func runeLen(s string) int { return len([]rune(s)) }

// byteIndex converte um índice em runes para bytes em s.
func byteIndex(s string, runes int) int {
	i := 0
	for pos := range s {
		if i == runes {
			return pos
		}
		i++
	}
	return len(s)
}
