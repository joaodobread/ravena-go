package wa

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types/events"
)

// Session mantém o bot conectado e cuida do pareamento (QR ou código),
// expondo o estado para o painel.
type Session struct {
	cli       *whatsmeow.Client
	pairPhone string
	log       *slog.Logger

	mu        sync.RWMutex
	qr        string
	code      string
	lastError string

	loggedOut chan struct{}
}

// SessionStatus é o estado da conexão para o painel.
type SessionStatus struct {
	Connected bool
	LoggedIn  bool
	JID       string
	PushName  string
	QR        string // conteúdo do QR atual (vazio se não estiver pareando)
	PairCode  string // código de pareamento por número
	LastError string
}

// NewSession cria a sessão. pairPhone (só dígitos) ativa o pareamento por código.
func NewSession(cli *whatsmeow.Client, pairPhone string, log *slog.Logger) *Session {
	s := &Session{cli: cli, pairPhone: pairPhone, log: log, loggedOut: make(chan struct{}, 1)}
	cli.AddEventHandler(func(evt any) {
		if _, ok := evt.(*events.LoggedOut); ok {
			select {
			case s.loggedOut <- struct{}{}:
			default:
			}
		}
	})
	return s
}

// Status devolve o estado atual.
func (s *Session) Status() SessionStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st := SessionStatus{
		Connected: s.cli.IsConnected(),
		LoggedIn:  s.cli.Store.ID != nil,
		QR:        s.qr,
		PairCode:  s.code,
		LastError: s.lastError,
	}
	if s.cli.Store.ID != nil {
		st.JID = s.cli.Store.ID.ToNonAD().String()
		st.PushName = s.cli.Store.PushName
	}
	return st
}

// Run conecta e mantém a sessão até ctx acabar. Se o WhatsApp encerrar a
// sessão, volta a parear sem derrubar o processo.
func (s *Session) Run(ctx context.Context) {
	for ctx.Err() == nil {
		err := s.connect(ctx)
		if err != nil {
			s.setError(err)
			s.log.Error("sessão do WhatsApp", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Second):
			}
			continue
		}
		s.setError(nil)
		select {
		case <-ctx.Done():
			s.cli.Disconnect()
			return
		case <-s.loggedOut:
			s.log.Error("sessão encerrada pelo WhatsApp; pareie de novo pelo painel")
			s.cli.Disconnect()
		}
	}
}

func (s *Session) connect(ctx context.Context) error {
	if s.cli.Store.ID != nil {
		return s.cli.Connect()
	}
	qr, err := s.cli.GetQRChannel(ctx)
	if err != nil {
		return err
	}
	if err := s.cli.Connect(); err != nil {
		return err
	}
	defer s.setPairing("", "")

	requested := false
	for item := range qr {
		switch {
		case item.Event == whatsmeow.QRChannelEventCode:
			code := ""
			if s.pairPhone != "" && !requested {
				// O código só pode ser pedido uma vez por tentativa;
				// pedir de novo invalida o anterior.
				requested = true
				code, err = s.cli.PairPhone(ctx, s.pairPhone, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
				if err != nil {
					s.log.Warn("pareamento por código falhou; use o QR", "err", err)
				} else {
					s.log.Info("código de pareamento", "codigo", code)
				}
			}
			s.mu.Lock()
			s.qr = item.Code
			if code != "" {
				s.code = code
			}
			s.mu.Unlock()
			if code == "" && !requested {
				s.log.Info("aguardando pareamento: abra o painel ou use PAIR_PHONE")
			}
		case item == whatsmeow.QRChannelSuccess:
			s.log.Info("pareado com sucesso")
			return nil
		case item.Error != nil:
			s.cli.Disconnect()
			return item.Error
		case item == whatsmeow.QRChannelTimeout:
			s.cli.Disconnect()
			return errors.New("pareamento expirou; gerando novo QR")
		}
	}
	return nil
}

func (s *Session) setPairing(qr, code string) {
	s.mu.Lock()
	s.qr, s.code = qr, code
	s.mu.Unlock()
}

func (s *Session) setError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		s.lastError = ""
		return
	}
	s.lastError = err.Error()
}
