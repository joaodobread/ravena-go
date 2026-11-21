// Package gamekit tem utilidades comuns aos jogos por rodada e às dinâmicas
// de grupo: agendador injetável (timers de rodada), identidade canônica de
// usuário (número em vez de LID), normalização de texto e medalhas.
package gamekit

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"go.mau.fi/whatsmeow/types"

	"github.com/joaodobread/ravena-go/internal/bot"
)

// Scheduler agenda f para daqui a d e devolve a função que cancela o agendamento.
type Scheduler func(d time.Duration, f func()) (stop func() bool)

// RealScheduler usa time.AfterFunc.
func RealScheduler(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }

// FakeScheduler guarda os agendamentos para os testes dispararem à mão.
type FakeScheduler struct {
	mu   sync.Mutex
	jobs []*fakeJob
}

type fakeJob struct {
	d       time.Duration
	f       func()
	stopped bool
	fired   bool
}

// Schedule implementa Scheduler.
func (s *FakeScheduler) Schedule(d time.Duration, f func()) func() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := &fakeJob{d: d, f: f}
	s.jobs = append(s.jobs, j)
	return func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		was := !j.stopped && !j.fired
		j.stopped = true
		return was
	}
}

// Pending conta os agendamentos ainda ativos.
func (s *FakeScheduler) Pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, j := range s.jobs {
		if !j.stopped && !j.fired {
			n++
		}
	}
	return n
}

// FireNext roda o agendamento ativo mais antigo (de forma síncrona) e devolve
// sua duração; ok=false se não houver nenhum.
func (s *FakeScheduler) FireNext() (time.Duration, bool) {
	s.mu.Lock()
	var job *fakeJob
	for _, j := range s.jobs {
		if !j.stopped && !j.fired {
			job = j
			break
		}
	}
	if job != nil {
		job.fired = true
	}
	s.mu.Unlock()
	if job == nil {
		return 0, false
	}
	job.f()
	return job.d, true
}

// UserID devolve a identidade preferida do autor: o número (s.whatsapp.net)
// quando conhecido, senão o JID recebido (LID). Sem device.
func UserID(msg *bot.Message) types.JID {
	return Prefer(msg.Sender, msg.SenderAlt)
}

// Prefer escolhe entre duas identidades do mesmo usuário, preferindo o número.
func Prefer(a, alt types.JID) types.JID {
	if a.Server != types.DefaultUserServer && alt.Server == types.DefaultUserServer && alt.User != "" {
		return alt.ToNonAD()
	}
	return a.ToNonAD()
}

// Canonical converte jid (talvez um LID marcado) para o número usando os
// participantes do grupo; sem informação, devolve jid sem device.
func Canonical(g *bot.GroupInfo, jid types.JID) types.JID {
	if g != nil {
		if p, ok := g.Member(jid); ok {
			for _, id := range []types.JID{p.Phone, p.JID, p.LID} {
				if id.User != "" && id.Server == types.DefaultUserServer {
					return id.ToNonAD()
				}
			}
		}
	}
	return jid.ToNonAD()
}

// Author devolve a identidade canônica do autor da mensagem, resolvendo LID
// pelo grupo quando a mensagem não trouxe o número.
func Author(g *bot.GroupInfo, msg *bot.Message) types.JID {
	id := UserID(msg)
	if id.Server != types.DefaultUserServer {
		return Canonical(g, id)
	}
	return id
}

// Group busca os dados do grupo; erro vira nil (as funções aqui toleram nil).
func Group(ctx context.Context, c bot.Client, chat types.JID) *bot.GroupInfo {
	g, err := c.GroupInfo(ctx, chat)
	if err != nil {
		return nil
	}
	return g
}

// Name devolve o nome de exibição do autor (ou fallback).
func Name(msg *bot.Message, fallback string) string {
	if n := strings.TrimSpace(msg.PushName); n != "" {
		return n
	}
	return fallback
}

var accents = map[rune]rune{
	'á': 'a', 'à': 'a', 'â': 'a', 'ã': 'a', 'ä': 'a',
	'é': 'e', 'è': 'e', 'ê': 'e', 'ë': 'e',
	'í': 'i', 'ì': 'i', 'î': 'i', 'ï': 'i',
	'ó': 'o', 'ò': 'o', 'ô': 'o', 'õ': 'o', 'ö': 'o',
	'ú': 'u', 'ù': 'u', 'û': 'u', 'ü': 'u',
	'ç': 'c', 'ñ': 'n',
}

// Fold deixa s minúsculo, sem acentos e sem espaços nas pontas.
func Fold(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.Map(func(r rune) rune {
		if a, ok := accents[r]; ok {
			return a
		}
		if unicode.Is(unicode.Mn, r) { // acento combinante solto
			return -1
		}
		return r
	}, s)
}

// Medal devolve 🥇🥈🥉 para os três primeiros e "N." para o resto.
func Medal(i int) string {
	switch i {
	case 0:
		return "🥇"
	case 1:
		return "🥈"
	case 2:
		return "🥉"
	}
	return strconv.Itoa(i+1) + "."
}

// SendCtx cria um contexto curto, derivado do ctx de Register, para envios
// feitos por timers (fora de um comando).
func SendCtx(base context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(base, 45*time.Second)
}
