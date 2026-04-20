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

	"github.com/licit/licit-go/internal/config"
	"github.com/licit/licit-go/internal/gateway"
)

func main() {
	slog.Info("Starting API Gateway...")

	configPath := "config.dev.yaml"
	if p := os.Getenv("CONFIG_PATH"); p != "" {
		configPath = p
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	gatewayService, err := gateway.New(cfg.Gateway, cfg.Redis)
	if err != nil {
		slog.Error("failed to initialize gateway", "error", err)
		os.Exit(1)
	}
	defer gatewayService.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	gatewayService.Start(ctx)

	addr := fmt.Sprintf(":%d", cfg.Gateway.ListenPort())
	srv := &http.Server{
		Addr:    addr,
		Handler: gatewayService.Handler(),
	}

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh

		slog.Info("Shutting down API Gateway...")
		cancel()

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()

		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Error("server shutdown error", "error", err)
		}
	}()

	slog.Info("API Gateway started", "port", cfg.Gateway.ListenPort())
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}
