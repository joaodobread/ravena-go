package util

import (
	"fmt"
	"regexp"
	"strings"
)

// pixKey é uma chave PIX validada.
type pixKey struct {
	Kind       string // cpf, cnpj, phone, email, evp
	Normalized string // como vai no payload
	Formatted  string // para mostrar
}

// Label é o nome do tipo de chave mostrado ao usuário.
func (k pixKey) Label() string {
	switch k.Kind {
	case "cpf":
		return "Chave CPF"
	case "cnpj":
		return "Chave CNPJ"
	case "phone":
		return "Chave Celular"
	case "email":
		return "Chave E-mail"
	}
	return "Chave Aleatória"
}

var (
	emailRe   = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
	uuidRe    = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	uuidRawRe = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)
	phoneRe   = regexp.MustCompile(`^[0-9+\-() .]+$`)
)

func digits(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}

// parsePixKey identifica a chave na ordem do Ravena: e-mail, CPF, CNPJ,
// celular e chave aleatória (UUID).
func parsePixKey(raw string) (pixKey, bool) {
	s := strings.TrimSpace(raw)
	if emailRe.MatchString(s) {
		return pixKey{"email", s, s}, true
	}
	d := digits(s)
	if len(d) == 11 && validCPF(d) {
		return pixKey{"cpf", d, d[:3] + "." + d[3:6] + "." + d[6:9] + "-" + d[9:]}, true
	}
	if len(d) == 14 && validCNPJ(d) {
		return pixKey{"cnpj", d, d[:2] + "." + d[2:5] + "." + d[5:8] + "/" + d[8:12] + "-" + d[12:]}, true
	}
	if !uuidRawRe.MatchString(s) && !uuidRe.MatchString(s) {
		phone := ""
		switch {
		case strings.HasPrefix(s, "+") && len(d) >= 12 && len(d) <= 14:
			phone = "+" + d
		case strings.HasPrefix(s, "+"):
		case len(d) == 10 || len(d) == 11:
			phone = "+55" + d
		case strings.HasPrefix(d, "55") && (len(d) == 12 || len(d) == 13):
			phone = "+" + d
		}
		if phone != "" && phoneRe.MatchString(s) {
			ddd, num := phone[3:5], phone[5:]
			switch len(num) {
			case 9:
				num = num[:5] + "-" + num[5:]
			case 8:
				num = num[:4] + "-" + num[4:]
			}
			return pixKey{"phone", phone, "(" + ddd + ") " + num}, true
		}
	}
	if uuidRe.MatchString(s) {
		n := strings.ToLower(s)
		return pixKey{"evp", n, n}, true
	}
	if uuidRawRe.MatchString(s) {
		n := strings.ToLower(s)
		n = n[:8] + "-" + n[8:12] + "-" + n[12:16] + "-" + n[16:20] + "-" + n[20:]
		return pixKey{"evp", n, n}, true
	}
	return pixKey{}, false
}

func allSame(d string) bool { return strings.Count(d, d[:1]) == len(d) }

func checkDigit(s string, weights []int) byte {
	sum := 0
	for i := range len(s) {
		sum += int(s[i]-'0') * weights[i]
	}
	r := sum % 11
	if r < 2 {
		return '0'
	}
	return byte('0' + 11 - r)
}

func validCPF(d string) bool {
	if len(d) != 11 || allSame(d) {
		return false
	}
	w := []int{11, 10, 9, 8, 7, 6, 5, 4, 3, 2}
	d1 := checkDigit(d[:9], w[1:])
	d2 := checkDigit(d[:9]+string(d1), w)
	return d[9] == d1 && d[10] == d2
}

func validCNPJ(d string) bool {
	if len(d) != 14 || allSame(d) {
		return false
	}
	w := []int{6, 5, 4, 3, 2, 9, 8, 7, 6, 5, 4, 3, 2}
	d1 := checkDigit(d[:12], w[1:])
	d2 := checkDigit(d[:12]+string(d1), w)
	return d[12] == d1 && d[13] == d2
}

// pix são os dados de um BR Code estático.
type pix struct {
	Key         string
	Description string // infoAdicional (campo 26.02), opcional
	Amount      string // "10.50"; vazio = valor livre
	Name        string // até 25 caracteres
	City        string // até 15 caracteres
	TxID        string // vazio = "***"
}

func emv(id, value string) string { return fmt.Sprintf("%s%02d%s", id, len(value), value) }

// pixPayload monta o BR Code (padrão EMV-MPM do Banco Central) com CRC16.
func pixPayload(p pix) string {
	gui, key := emv("00", "br.gov.bcb.pix"), emv("01", p.Key)
	account := gui + key
	if desc := pixText(p.Description, 99-len(account)-4); desc != "" {
		account += emv("02", desc)
	}
	txid := pixTxID(p.TxID)

	var b strings.Builder
	b.WriteString(emv("00", "01"))
	b.WriteString(emv("26", account))
	b.WriteString(emv("52", "0000"))
	b.WriteString(emv("53", "986"))
	if p.Amount != "" {
		b.WriteString(emv("54", p.Amount))
	}
	b.WriteString(emv("58", "BR"))
	b.WriteString(emv("59", pixText(p.Name, 25)))
	b.WriteString(emv("60", pixText(p.City, 15)))
	b.WriteString(emv("62", emv("05", txid)))
	b.WriteString("6304")
	return b.String() + fmt.Sprintf("%04X", crc16(b.String()))
}

// crc16 é o CRC-16/CCITT-FALSE (polinômio 0x1021, início 0xFFFF).
func crc16(s string) uint16 {
	crc := uint16(0xFFFF)
	for i := range len(s) {
		crc ^= uint16(s[i]) << 8
		for range 8 {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

var pixAccents = strings.NewReplacer(
	"á", "a", "à", "a", "â", "a", "ã", "a", "ä", "a", "é", "e", "ê", "e", "è", "e", "ë", "e",
	"í", "i", "ì", "i", "î", "i", "ï", "i", "ó", "o", "ò", "o", "ô", "o", "õ", "o", "ö", "o",
	"ú", "u", "ù", "u", "û", "u", "ü", "u", "ç", "c", "ñ", "n",
	"Á", "A", "À", "A", "Â", "A", "Ã", "A", "Ä", "A", "É", "E", "Ê", "E", "È", "E", "Ë", "E",
	"Í", "I", "Ì", "I", "Î", "I", "Ï", "I", "Ó", "O", "Ò", "O", "Ô", "O", "Õ", "O", "Ö", "O",
	"Ú", "U", "Ù", "U", "Û", "U", "Ü", "U", "Ç", "C", "Ñ", "N",
)

// pixText tira acentos e tudo o que não for ASCII alfanumérico/espaço (os
// tamanhos do EMV contam bytes) e corta em max bytes.
func pixText(s string, max int) string {
	s = pixAccents.Replace(s)
	s = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == ' ' {
			return r
		}
		return -1
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if max < 0 {
		max = 0
	}
	if len(s) > max {
		s = strings.TrimSpace(s[:max])
	}
	return s
}

func pixTxID(s string) string {
	s = strings.ReplaceAll(pixText(s, 25), " ", "")
	if s == "" {
		return "***"
	}
	return s
}
