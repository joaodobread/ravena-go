// Comando ravena: bot de WhatsApp.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"go.mau.fi/whatsmeow"
	wastore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"

	"github.com/joaodobread/ravena-go/internal/app"
	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/config"
	"github.com/joaodobread/ravena-go/internal/llm"
	"github.com/joaodobread/ravena-go/internal/media"
	"github.com/joaodobread/ravena-go/internal/policy"
	"github.com/joaodobread/ravena-go/internal/store"
	"github.com/joaodobread/ravena-go/internal/wa"
	"github.com/joaodobread/ravena-go/internal/web"
)

func main() {
	if err := run(); err != nil {
		slog.Error("encerrando", "err", err)
		os.Exit(1)
	}
}

func run() error {
	started := time.Now()
	cfg, err := config.Load(config.Getenv)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return err
	}

	// Banco da aplicação.
	st, err := store.Open(ctx, filepath.Join(cfg.DataDir, "app.db"))
	if err != nil {
		return fmt.Errorf("abrindo banco: %w", err)
	}
	defer st.Close()

	// Sessão do WhatsApp (arquivo separado, gerenciado pelo whatsmeow).
	dsn := "file:" + filepath.Join(cfg.DataDir, "whatsapp.db") +
		"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	container, err := sqlstore.New(ctx, "sqlite", dsn, waLog.Stdout("db", "WARN", false))
	if err != nil {
		return fmt.Errorf("abrindo sessão: %w", err)
	}
	// O bot não usa o histórico de conversas: pede o mínimo no pareamento
	// (vale para pareamentos novos) e não baixa o que vier depois.
	hs := wastore.DeviceProps.HistorySyncConfig
	hs.FullSyncDaysLimit, hs.RecentSyncDaysLimit = new(uint32(1)), new(uint32(1))
	hs.FullSyncSizeMbLimit, hs.StorageQuotaMb = new(uint32(1)), new(uint32(1))
	hs.InitialSyncMaxMessagesPerChat, hs.ThumbnailSyncDaysLimit = new(uint32(1)), new(uint32(0))
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		return fmt.Errorf("carregando dispositivo: %w", err)
	}
	waClient := whatsmeow.NewClient(device, waLog.Stdout("wa", "WARN", false))
	waClient.ManualHistorySyncDownload = true // ainda confirma o recebimento
	client := wa.NewClient(waClient)
	session := wa.NewSession(waClient, cfg.PairPhone, log.With("mod", "sessao"))

	router := bot.NewRouter(cfg.Prefix, client, log)
	pol, err := policy.New(ctx, st, client, cfg.SuperAdmins, policy.DefaultOptions, log.With("mod", "policy"))
	if err != nil {
		return err
	}
	router.SetPolicy(pol)
	go pol.RunUsageWriter(ctx)

	password, err := adminPassword(ctx, cfg, st, log)
	if err != nil {
		return err
	}
	panel, err := web.New(ctx, web.Deps{
		Store: st, Policy: pol, Router: router, Session: session, Groups: client,
		Password: password, Log: log.With("mod", "painel"), Started: started,
	})
	if err != nil {
		return err
	}

	var llmClient *llm.Client
	if cfg.OpenRouterKey != "" {
		llmClient = llm.New(cfg.OpenRouterKey, cfg.LLMModel)
		llmClient.BaseURL = cfg.OpenRouterBase
	} else {
		log.Warn("OPENROUTER_API_KEY vazio: funções de IA ficam indisponíveis")
	}
	deps := &app.Deps{
		Router: router, Client: client, Store: st, Policy: pol, LLM: llmClient,
		Media: media.NewConverter(media.Options{Concurrency: cfg.FFmpegJobs}),
		Web:   panel, HTTP: app.NewHTTPClient(), Log: log, DataDir: cfg.DataDir,
	}
	for _, f := range features() {
		d := *deps
		d.Log = log.With("feature", f.Name())
		if err := f.Register(ctx, &d); err != nil {
			return fmt.Errorf("registrando %s: %w", f.Name(), err)
		}
	}
	log.Info("funcionalidades carregadas", "comandos", len(router.Commands()))

	wireEvents(ctx, cfg, waClient, client, router, log)

	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: panel.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("servidor HTTP", "err", err)
		}
	}()
	log.Info("painel no ar", "addr", cfg.HTTPAddr)

	session.Run(ctx) // bloqueia até o desligamento
	log.Info("desligando")
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}

// wireEvents liga os eventos do WhatsApp ao roteador.
func wireEvents(ctx context.Context, cfg config.Config, waClient *whatsmeow.Client, client *wa.Client, router *bot.Router, log *slog.Logger) {
	// whatsmeow chama os handlers em sequência; o trabalho vai para goroutines
	// limitadas por um semáforo, para não estourar memória em rajadas.
	workers := make(chan struct{}, cfg.Workers)
	dispatch := func(fn func(context.Context)) {
		go func() {
			select {
			case workers <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-workers }()
			hctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			fn(hctx)
		}()
	}
	// Mensagens recentes (inclusive as do bot), para reações como 🖼 → figurinha.
	recent := wa.NewRecent(3000, 6*time.Hour)
	client.RecordSent(recent)
	self := func() []types.JID {
		var out []types.JID
		if id := waClient.Store.ID; id != nil {
			out = append(out, id.ToNonAD())
		}
		if lid := waClient.Store.LID; !lid.IsEmpty() {
			out = append(out, lid.ToNonAD())
		}
		return out
	}

	waClient.AddEventHandler(func(evt any) {
		switch e := evt.(type) {
		case *events.Message:
			if time.Since(e.Info.Timestamp) > cfg.MaxMessageAge || e.Info.Chat.Server == "broadcast" {
				return
			}
			if r, ok := wa.ReactionOf(e); ok {
				// Ignora as próprias reações (o bot reage 🖼 ao terminar).
				if e.Info.IsFromMe || !router.HasTrigger(r.Emoji) {
					return
				}
				target, found := recent.Get(r.Chat, r.TargetID)
				if !found {
					log.Debug("reação a mensagem fora do cache", "chat", r.Chat, "msg", r.TargetID)
					return
				}
				msg := wa.Convert(waClient, target)
				re := bot.Reaction{Emoji: r.Emoji, By: r.From, ByAlt: r.FromAlt}
				dispatch(func(ctx context.Context) { router.HandleReaction(ctx, msg, re) })
				return
			}
			recent.Add(e)
			msg := wa.Convert(waClient, e)
			wa.MarkSelf(msg, self()...)
			dispatch(func(ctx context.Context) { router.Handle(ctx, msg) })
		case *events.GroupInfo:
			client.Invalidate(e.JID) // admins/nome mudaram
			if len(e.Join)+len(e.Leave)+len(e.Promote)+len(e.Demote) == 0 {
				return
			}
			ev := bot.GroupEvent{Chat: e.JID, Joined: e.Join, Left: e.Leave, Promoted: e.Promote, Demoted: e.Demote}
			if e.Sender != nil {
				ev.By = *e.Sender
			}
			dispatch(func(ctx context.Context) { router.HandleGroupEvent(ctx, ev) })
		case *events.Connected:
			log.Info("conectado ao WhatsApp", "jid", waClient.Store.ID)
		}
	})
}

// adminPassword devolve a senha do painel: a da variável ou uma gerada e salva.
func adminPassword(ctx context.Context, cfg config.Config, st *store.Store, log *slog.Logger) (string, error) {
	if cfg.AdminPassword != "" {
		return cfg.AdminPassword, nil
	}
	var pass string
	ok, err := st.GetJSON(ctx, "admin_password", &pass)
	if err != nil {
		return "", err
	}
	if !ok {
		b := make([]byte, 9)
		_, _ = rand.Read(b)
		pass = hex.EncodeToString(b)
		if err := st.SetJSON(ctx, "admin_password", pass); err != nil {
			return "", err
		}
	}
	log.Warn("ADMIN_PASSWORD não definida; usando senha gerada para o painel", "senha", pass)
	return pass, nil
}
