package ai

import (
	"context"
	"strings"

	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/llm"
)

// Mensagens do !ocr.
const (
	MsgOCRNoImage = "❌ Por favor, envie uma imagem com o comando ou responda a uma imagem usando !ocr."
	MsgOCREmpty   = "⚠️ Não consegui extrair nenhum texto desta imagem."
	MsgOCRError   = "❌ Ocorreu um erro ao processar o OCR. Tente novamente mais tarde."
)

func (f *Feature) runOCR(ctx context.Context, c *bot.Context) error {
	if f.LLM == nil {
		return bot.Userf(MsgUnavailable)
	}
	src := c.Message.AnyMedia()
	if src == nil || src.Kind != bot.MediaImage {
		return bot.Userf(MsgOCRNoImage)
	}
	img, err := f.image(ctx, src)
	if err != nil {
		return err
	}
	text, err := f.LLM.Chat(ctx, llm.ChatRequest{
		System: "Você é um assistente especializado em OCR. Sua única tarefa é transcrever com precisão o texto da imagem.",
		Messages: []llm.Message{{
			Role:   "user",
			Text:   "Extraia TODO o texto presente nesta imagem. Retorne apenas o texto extraído, preservando as quebras de linha, sem comentários.",
			Images: [][]byte{img},
		}},
		Temperature: 0.1,
		MaxTokens:   1000,
	})
	if err != nil {
		f.log().Error("ocr: LLM falhou", "err", err)
		return bot.Userf(MsgOCRError)
	}
	if text = strings.TrimSpace(text); text == "" {
		return bot.Userf(MsgOCREmpty)
	}
	return c.Reply(ctx, bot.Text("📝 *Texto Extraído:*\n\n"+text))
}
