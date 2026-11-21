package reminders

import (
	"testing"
	"time"
)

func TestParseWhen(t *testing.T) {
	loc := time.FixedZone("BRT", -3*3600)
	now := time.Date(2026, 11, 21, 14, 30, 0, 0, loc) // sábado
	at := func(mo time.Month, d, h, mi int) time.Time { return time.Date(2026, mo, d, h, mi, 0, 0, loc) }
	cases := []struct {
		in   string
		when time.Time
		rest string
	}{
		{"em 10 minutos tirar o bolo", now.Add(10 * time.Minute), "tirar o bolo"},
		{"daqui a 2 horas reunião", now.Add(2 * time.Hour), "reunião"},
		{"30min café", now.Add(30 * time.Minute), "café"},
		{"2h: dormir", now.Add(2 * time.Hour), "dormir"},
		{"em 3 dias pagar conta", now.AddDate(0, 0, 3), "pagar conta"},
		{"amanhã às 9h academia", at(11, 22, 9, 0), "academia"},
		{"Amanhã 9:30 dentista", at(11, 22, 9, 30), "dentista"},
		{"hoje as 18h jogo", at(11, 21, 18, 0), "jogo"},
		{"depois de amanhã às 7h voo", at(11, 23, 7, 0), "voo"},
		{"às 15h ligar", at(11, 21, 15, 0), "ligar"},
		{"às 8h ligar", at(11, 22, 8, 0), "ligar"}, // já passou hoje
		{"25/12 às 10h natal", at(12, 25, 10, 0), "natal"},
		{"05/01 ano novo", time.Date(2027, 1, 5, 9, 0, 0, 0, loc), "ano novo"},
		{"10/03/2027 14:00 prova", time.Date(2027, 3, 10, 14, 0, 0, 0, loc), "prova"},
		{"segunda às 9h reunião", at(11, 23, 9, 0), "reunião"},
		{"sábado 10h feira", at(11, 28, 10, 0), "feira"},
	}
	for _, tt := range cases {
		got, rest, ok := ParseWhen(tt.in, now)
		if !ok || !got.Equal(tt.when) || rest != tt.rest {
			t.Errorf("%q → %v %q %v; want %v %q", tt.in, got, rest, ok, tt.when, tt.rest)
		}
	}
	for _, bad := range []string{"", "comprar pão", "31/02 festa", "às 25h", "em 0 minutos x"} {
		if _, _, ok := ParseWhen(bad, now); ok {
			t.Errorf("%q deveria falhar", bad)
		}
	}
}
