package main

import (
	"github.com/joaodobread/ravena-go/internal/app"
	"github.com/joaodobread/ravena-go/internal/features/ai"
	"github.com/joaodobread/ravena-go/internal/features/customcmd"
	"github.com/joaodobread/ravena-go/internal/features/download"
	"github.com/joaodobread/ravena-go/internal/features/fishing"
	"github.com/joaodobread/ravena-go/internal/features/fun"
	"github.com/joaodobread/ravena-go/internal/features/greetings"
	"github.com/joaodobread/ravena-go/internal/features/groupadmin"
	"github.com/joaodobread/ravena-go/internal/features/groupcmds"
	"github.com/joaodobread/ravena-go/internal/features/mediafx"
	"github.com/joaodobread/ravena-go/internal/features/menu"
	"github.com/joaodobread/ravena-go/internal/features/ranking"
	"github.com/joaodobread/ravena-go/internal/features/reminders"
	"github.com/joaodobread/ravena-go/internal/features/slots"
	"github.com/joaodobread/ravena-go/internal/features/sticker"
	"github.com/joaodobread/ravena-go/internal/features/translate"
	"github.com/joaodobread/ravena-go/internal/features/util"
)

// features são as funcionalidades ligadas no bot, na ordem de registro
// (a ordem define a prioridade dos listeners).
func features() []app.Feature {
	// Pesca e caça-níquel trocam moedas/iscas entre si.
	slotsGame := &slots.Module{}
	fishingGame := &fishing.Module{Wallet: slotsGame}
	slotsGame.Tackle = fishingGame
	// Comandos personalizados alimentam a interação automática e o menu.
	custom := &customcmd.Module{}

	return []app.Feature{
		sticker.Module{},
		// ai antes dos comandos personalizados: menção/elogios/interação
		// automática precisam ver a mensagem antes de um gatilho de grupo.
		&ai.Module{Custom: custom},
		custom,
		&groupadmin.Module{},
		greetings.Module{},
		download.Module{},
		reminders.Module{},
		mediafx.Module{},
		util.Module{},
		translate.Module{},
		menu.Module{Extra: custom},
		slotsGame,
		fishingGame,
		fun.Module{},
		groupcmds.Module{},
		ranking.Module{},
	}
}
