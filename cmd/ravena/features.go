package main

import (
	"github.com/joaodobread/ravena-go/internal/app"
	"github.com/joaodobread/ravena-go/internal/features/sticker"
)

// features são as funcionalidades ligadas no bot, na ordem de registro
// (a ordem define a prioridade dos listeners).
func features() []app.Feature {
	return []app.Feature{
		sticker.Module{},
	}
}
