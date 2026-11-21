package main

import (
	"github.com/joaodobread/ravena-go/internal/app"
	"github.com/joaodobread/ravena-go/internal/features/ai"
	"github.com/joaodobread/ravena-go/internal/features/download"
	"github.com/joaodobread/ravena-go/internal/features/greetings"
	"github.com/joaodobread/ravena-go/internal/features/mediafx"
	"github.com/joaodobread/ravena-go/internal/features/reminders"
	"github.com/joaodobread/ravena-go/internal/features/sticker"
)

// features são as funcionalidades ligadas no bot, na ordem de registro
// (a ordem define a prioridade dos listeners).
func features() []app.Feature {
	return []app.Feature{
		sticker.Module{},
		// ai antes dos comandos personalizados: menção/elogios/interação
		// automática precisam ver a mensagem antes de um gatilho de grupo.
		&ai.Module{},
		greetings.Module{},
		download.Module{},
		reminders.Module{},
		mediafx.Module{},
	}
}
