package util

import "testing"

func TestCRC16CheckValue(t *testing.T) {
	if got := crc16("123456789"); got != 0x29B1 {
		t.Fatalf("crc16 = %04X, want 29B1", got)
	}
}

// Exemplo oficial do Manual do BR Code (Banco Central).
const bcbExample = "00020126580014br.gov.bcb.pix0136123e4567-e12b-12d1-a456-4266554400005204000053039865802BR5913Fulano de Tal6008BRASILIA62070503***63041D3D"

func TestPixPayloadMatchesBCBExample(t *testing.T) {
	got := pixPayload(pix{Key: "123e4567-e12b-12d1-a456-426655440000", Name: "Fulano de Tal", City: "BRASILIA"})
	if got != bcbExample {
		t.Fatalf("payload:\n got %s\nwant %s", got, bcbExample)
	}
}

func TestPixPayloadWithAmountAndDescription(t *testing.T) {
	got := pixPayload(pix{Key: "fulano@email.com", Description: "Pizza de sábado!", Amount: "10.50", Name: "João da Silva Sauro Junior Neto", City: "São Paulo"})
	want := "00020126" + "57" + "0014br.gov.bcb.pix" + "0116fulano@email.com" + "0215Pizza de sabado" +
		"52040000" + "5303986" + "540510.50" + "5802BR" + "5925Joao da Silva Sauro Junio" + "6009Sao Paulo" + "62070503***" + "6304"
	if got[:len(got)-4] != want {
		t.Fatalf("payload:\n got %s\nwant %s????", got, want)
	}
	if crc := got[len(got)-4:]; crc != hex4(crc16(want)) {
		t.Fatalf("crc = %s", crc)
	}
}

func TestPixDescriptionFitsInAccountField(t *testing.T) {
	long := "descricao muito longa que nao cabe inteira no campo vinte e seis do payload pix de jeito nenhum"
	got := pixPayload(pix{Key: "123e4567-e12b-12d1-a456-426655440000", Description: long, Name: "A", City: "B"})
	if got[8:10] != "99" {
		t.Fatalf("campo 26 com tamanho %s: %s", got[8:10], got)
	}
}

func hex4(v uint16) string {
	const h = "0123456789ABCDEF"
	return string([]byte{h[v>>12], h[v>>8&15], h[v>>4&15], h[v&15]})
}

func TestParsePixKey(t *testing.T) {
	cases := []struct {
		in, kind, norm, formatted string
	}{
		{"fulano@email.com", "email", "fulano@email.com", "fulano@email.com"},
		{"000.836.590-30", "cpf", "00083659030", "000.836.590-30"},
		{"11.222.333/0001-81", "cnpj", "11222333000181", "11.222.333/0001-81"},
		{"11999999999", "phone", "+5511999999999", "(11) 99999-9999"},
		{"(11) 3333-4444", "phone", "+551133334444", "(11) 3333-4444"},
		{"+5511999999999", "phone", "+5511999999999", "(11) 99999-9999"},
		{"123E4567-E89B-12D3-A456-426614174000", "evp", "123e4567-e89b-12d3-a456-426614174000", "123e4567-e89b-12d3-a456-426614174000"},
		{"123e4567e89b12d3a456426614174000", "evp", "123e4567-e89b-12d3-a456-426614174000", "123e4567-e89b-12d3-a456-426614174000"},
	}
	for _, c := range cases {
		k, ok := parsePixKey(c.in)
		if !ok || k.Kind != c.kind || k.Normalized != c.norm || k.Formatted != c.formatted {
			t.Errorf("%s: %+v ok=%v", c.in, k, ok)
		}
	}
	for _, bad := range []string{"", "abc", "123", "+12", "abc11999999999", "11.222.333/0001-82"} {
		if k, ok := parsePixKey(bad); ok {
			t.Errorf("%q deveria ser inválida, virou %+v", bad, k)
		}
	}
}

func TestParseCity(t *testing.T) {
	cases := map[string][2]string{
		"Santa Maria, RS":               {"Santa Maria", "Rio Grande do Sul"},
		"Santa Maria - rs":              {"Santa Maria", "Rio Grande do Sul"},
		"Campinas SP":                   {"Campinas", "São Paulo"},
		"Santa Maria Rio Grande do Sul": {"Santa Maria", "Rio Grande do Sul"},
		"Belém, Para":                   {"Belém", "Pará"},
		"Lisboa":                        {"Lisboa", ""},
		"Paris XX":                      {"Paris XX", ""},
		"Bahia":                         {"Bahia", ""},
	}
	for in, want := range cases {
		city, state := parseCity(in)
		if city != want[0] || state != want[1] {
			t.Errorf("%q → (%q, %q), want %q", in, city, state, want)
		}
	}
}

func TestWindDir(t *testing.T) {
	for deg, want := range map[float64]string{0: "N", 359: "N", 11.3: "NNE", 90: "E", 180: "S", 225: "SW", 270: "W", -90: "W"} {
		if got, _ := windDir(deg); got != want {
			t.Errorf("%v° → %s, want %s", deg, got, want)
		}
	}
}

func TestChampionSlug(t *testing.T) {
	for in, want := range map[string]string{"Kai'Sa": "kaisa", "lee sin": "lee-sin", "Dr. Mundo": "dr-mundo", "???": ""} {
		if got := championSlug(splitFields(in)); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}

func splitFields(s string) []string {
	var out []string
	cur := ""
	for _, r := range s + " " {
		if r == ' ' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		cur += string(r)
	}
	return out
}
