package util

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"rsc.io/qr"

	"github.com/joaodobread/ravena-go/internal/bot"
)

// qrPNG gera o PNG de um QR Code (nível M, 8 px por módulo).
func qrPNG(text string) ([]byte, error) {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return nil, bot.Userf("❌ Texto longo demais para um QR Code.")
	}
	return code.PNG(), nil
}

func image(png []byte, caption string) bot.Outgoing {
	return bot.Outgoing{Kind: bot.OutImage, Data: png, Mime: "image/png", Caption: caption}
}

func (f *Feature) qr(ctx context.Context, c *bot.Context) error {
	text := c.RawArgs()
	if q := c.Message.Quoted; q != nil && strings.TrimSpace(q.Text) != "" {
		text = strings.TrimSpace(text + " " + q.Text)
	}
	if text == "" {
		return bot.Userf("❌ Por favor, forneça o texto para o QR Code ou responda a uma mensagem.")
	}
	if strings.HasPrefix(text, "http") {
		text = strings.Join(strings.Fields(text), "")
	}
	png, err := qrPNG(text)
	if err != nil {
		return err
	}
	return c.Reply(ctx, image(png, "✅ QR Code gerado para: _"+truncate(text, 100)+"_"))
}

// wifiEscape escapa os caracteres especiais do formato WIFI:.
var wifiEscape = strings.NewReplacer(`\`, `\\`, `;`, `\;`, `,`, `\,`, `:`, `\:`, `"`, `\"`)

func (f *Feature) qrWifi(ctx context.Context, c *bot.Context) error {
	usage := fmt.Sprintf("❌ Uso correto: %[1]sqr-wifi [SSID]\n[Senha]\n[Oculta(opcional)]\n\nOu: %[1]sqr-wifi SSID Senha", c.Prefix)
	var lines []string
	for l := range strings.SplitSeq(c.RawArgs(), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) < 2 {
		lines = c.Args // tudo numa linha: SSID Senha [oculta]
	}
	if len(lines) < 2 {
		return bot.Userf("%s", usage)
	}
	ssid, pass, hidden := lines[0], lines[1], false
	if len(lines) >= 3 {
		h := strings.ToLower(lines[2])
		hidden = h == "true" || h == "1" || h == "sim"
	}
	payload := fmt.Sprintf("WIFI:S:%s;T:WPA;P:%s;H:%t;;", wifiEscape.Replace(ssid), wifiEscape.Replace(pass), hidden)
	png, err := qrPNG(payload)
	if err != nil {
		return err
	}
	oculta := "Não"
	if hidden {
		oculta = "Sim"
	}
	return c.Reply(ctx, image(png, fmt.Sprintf("📶 *WiFi QR Code*\n\n*SSID:* %s\n*Senha:* %s\n*Oculta:* %s", ssid, pass, oculta)))
}

var priceRe = regexp.MustCompile(`^\d+([.,]\d{1,2})?$`)

func (f *Feature) qrPix(ctx context.Context, c *bot.Context) error {
	if len(c.Args) == 0 {
		return bot.Userf("❌ Uso correto: %sqr-pix [Chave] [Descrição] [Valor]\n\nApenas a chave é obrigatória.", c.Prefix)
	}
	key, ok := parsePixKey(c.Args[0])
	if !ok {
		return bot.Userf("❌ Chave PIX inválida.\n\nA chave informada deve ser de um dos seguintes tipos:\n" +
			"• *CPF*: 11 dígitos (ex: 000.836.590-30)\n" +
			"• *CNPJ*: 14 dígitos (ex: 00.000.000/0001-00)\n" +
			"• *Celular*: DDD + número (ex: 11999999999)\n" +
			"• *E-mail*: endereço válido (ex: nome@email.com)\n" +
			"• *Chave Aleatória*: Formato UUID (ex: 123e4567-e89b-12d3-a456-426614174000)")
	}

	rest := c.Args[1:]
	var desc, amount string
	if n := len(rest); n > 0 && priceRe.MatchString(rest[n-1]) {
		v, err := strconv.ParseFloat(strings.Replace(rest[n-1], ",", ".", 1), 64)
		if err == nil && v > 0 && v < 1e9 {
			amount = strconv.FormatFloat(v, 'f', 2, 64)
		}
		rest = rest[:n-1]
	}
	desc = strings.Join(rest, " ")

	name := pixText(c.Message.PushName, 25)
	if name == "" {
		name = "Pessoa"
	}
	payload := pixPayload(pix{Key: key.Normalized, Description: desc, Amount: amount, Name: name, City: "Sao Paulo"})
	png, err := qrPNG(payload)
	if err != nil {
		return err
	}
	caption := fmt.Sprintf("💠 *PIX Gerado*\n\n*%s:* `%s`", key.Label(), key.Formatted)
	if desc != "" {
		caption += "\n*Descrição:* " + desc
	}
	if amount != "" {
		caption += "\n*Valor:* R$ " + strings.Replace(amount, ".", ",", 1)
	}
	caption += "\n\n*Payload (Copia e Cola):*\n```" + payload + "```"
	return c.Reply(ctx, image(png, caption))
}
