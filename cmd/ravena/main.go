// Comando ravena: bot de WhatsApp.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	_ "modernc.org/sqlite"

	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/config"
	"github.com/joaodobread/ravena-go/internal/features/sticker"
	"github.com/joaodobread/ravena-go/internal/llm"
	"github.com/joaodobread/ravena-go/internal/media"
	"github.com/joaodobread/ravena-go/internal/wa"
)

func main() {
	if err := run(); err != nil {
		slog.Error("encerrando", "err", err)
		os.Exit(1)
	}
}

func run() error {
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
	dsn := "file:" + filepath.Join(cfg.DataDir, "whatsapp.db") +
		"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	container, err := sqlstore.New(ctx, "sqlite", dsn, waLog.Stdout("db", "WARN", false))
	if err != nil {
		return fmt.Errorf("abrindo sessão: %w", err)
	}
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		return fmt.Errorf("carregando dispositivo: %w", err)
	}
	waClient := whatsmeow.NewClient(device, waLog.Stdout("wa", "WARN", false))
	client := wa.NewClient(waClient)

	router := bot.NewRouter(cfg.Prefix, client, log)
	var focus sticker.FocusFinder
	if cfg.OpenRouterKey != "" {
		l := llm.New(cfg.OpenRouterKey, cfg.LLMModel)
		l.BaseURL = cfg.OpenRouterBase
		focus = l
	} else {
		log.Warn("OPENROUTER_API_KEY vazio: !sqi vai usar corte central")
	}
	stickers := sticker.New(
		media.NewConverter(media.Options{Concurrency: cfg.FFmpegJobs}),
		focus, cfg.StickerAuthor, log.With("feature", "sticker"),
	)
	if err := router.Register(stickers.Commands()...); err != nil {
		return err
	}
	router.Listen(stickers.AutoSticker)

	// whatsmeow chama os handlers em sequência; o trabalho pesado vai para
	// goroutines, limitadas por um semáforo para não estourar memória.
	workers := make(chan struct{}, cfg.Workers)
	waClient.AddEventHandler(func(evt any) {
		switch e := evt.(type) {
		case *events.Message:
			if time.Since(e.Info.Timestamp) > cfg.MaxMessageAge {
				return
			}
			msg := wa.Convert(waClient, e)
			go func() {
				select {
				case workers <- struct{}{}:
				case <-ctx.Done():
					return
				}
				defer func() { <-workers }()
				hctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
				defer cancel()
				router.Handle(hctx, msg)
			}()
		case *events.Connected:
			log.Info("conectado ao WhatsApp", "jid", waClient.Store.ID)
		case *events.LoggedOut:
			log.Error("sessão encerrada pelo WhatsApp; apague o banco e pareie de novo", "reason", e.Reason)
		}
	})

	// O HTTP sobe antes do pareamento para o healthcheck do deploy passar
	// enquanto o QR/código espera alguém no celular.
	if cfg.HTTPAddr != "" {
		srv := healthServer(cfg.HTTPAddr, waClient)
		go func() {
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("servidor HTTP", "err", err)
			}
		}()
		defer srv.Shutdown(context.Background())
	}

	if err := connect(ctx, waClient, cfg, log); err != nil {
		return err
	}
	defer waClient.Disconnect()

	<-ctx.Done()
	log.Info("desligando")
	return nil
}

// connect conecta, pareando por QR ou código se ainda não houver sessão.
func connect(ctx context.Context, c *whatsmeow.Client, cfg config.Config, log *slog.Logger) error {
	if c.Store.ID != nil {
		return c.Connect()
	}

	qr, err := c.GetQRChannel(ctx)
	if err != nil {
		return err
	}
	if err := c.Connect(); err != nil {
		return err
	}
	requested := false
	for item := range qr {
		switch item.Event {
		case whatsmeow.QRChannelEventCode:
			if cfg.PairPhone != "" {
				// O canal manda um QR novo a cada ~20s; o código só pode ser
				// pedido uma vez, senão o anterior é invalidado.
				if requested {
					continue
				}
				requested = true
				code, err := c.PairPhone(ctx, cfg.PairPhone, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
				if err != nil {
					return fmt.Errorf("pareando por código: %w", err)
				}
				log.Info("WhatsApp > Aparelhos conectados > Conectar com número de telefone", "codigo", code)
				continue
			}
			log.Info("escaneie o QR code abaixo em WhatsApp > Aparelhos conectados")
			qrterminal.GenerateHalfBlock(item.Code, qrterminal.L, os.Stdout)
		case whatsmeow.QRChannelSuccess.Event:
			log.Info("pareado com sucesso")
			return nil
		default:
			if item.Error != nil {
				return fmt.Errorf("pareamento: %w", item.Error)
			}
			if item.Event == whatsmeow.QRChannelTimeout.Event {
				return errors.New("pareamento expirou; reinicie para tentar de novo")
			}
		}
	}
	return nil
}

func healthServer(addr string, c *whatsmeow.Client) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		if c.Store.ID == nil {
			_, _ = w.Write([]byte("aguardando pareamento"))
			return
		}
		if !c.IsConnected() {
			http.Error(w, "desconectado", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
	return &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
}
