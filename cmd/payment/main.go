package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/licit/licit-go/internal/config"
	"github.com/licit/licit-go/internal/messaging"
	"github.com/licit/licit-go/internal/payment"
)

func main() {
	slog.Info("Starting Payment Validator...")

	configPath := "config.dev.yaml"
	if p := os.Getenv("CONFIG_PATH"); p != "" {
		configPath = p
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	// NATS
	nc, err := messaging.NewClient(cfg.NATS.URL)
	if err != nil {
		slog.Error("failed to connect to NATS", "error", err)
		os.Exit(1)
	}
	defer nc.Close()

	// Validator — listens on NATS for payment requests
	validator := payment.NewValidator(nc, &cfg.DotNet)
	validator.Start()

	// Minimal HTTP server for health checks
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.Heartbeat("/health"))

	addr := fmt.Sprintf(":%d", cfg.Server.PaymentPort)
	srv := &http.Server{Addr: addr, Handler: r}

	// Graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		slog.Info("Shutting down Payment Validator...")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Error("server shutdown error", "error", err)
		}
	}()

	slog.Info("Payment Validator started", "port", cfg.Server.PaymentPort)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}
