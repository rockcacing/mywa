// Command mywa is a lightweight WhatsApp HTTP API.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rockcacing/mywa/internal/api"
	"github.com/rockcacing/mywa/internal/config"
	"github.com/rockcacing/mywa/internal/hub"
	"github.com/rockcacing/mywa/internal/store"
	"github.com/rockcacing/mywa/internal/wa"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()

	h := hub.New()
	mgr, err := wa.NewManager(ctx, db, h, log)
	if err != nil {
		return err
	}
	defer mgr.Close()
	mgr.Restore(context.Background())

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           api.New(mgr, h, cfg.APIKey, log).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Addr, "db", cfg.DBPath)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("shutting down")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
