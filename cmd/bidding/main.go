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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/licit/licit-go/internal/bidding"
	"github.com/licit/licit-go/internal/config"
	"github.com/licit/licit-go/internal/messaging"
)

func main() {
	slog.Info("Starting Bidding Engine...")

	configPath := "config.dev.yaml"
	if p := os.Getenv("CONFIG_PATH"); p != "" {
		configPath = p
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Database
	pool, err := pgxpool.New(ctx, cfg.DB.DSN())
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	// NATS
	nc, err := messaging.NewClient(cfg.NATS.URL)
	if err != nil {
		slog.Error("failed to connect to NATS", "error", err)
		os.Exit(1)
	}
	defer nc.Close()

	// Repository & Service
	repo := bidding.NewRepository(pool)
	if err := repo.Migrate(ctx); err != nil {
		slog.Error("failed to migrate database", "error", err)
		os.Exit(1)
	}

	svc := bidding.NewService(repo, nc)

	// Start listening for .NET auction creation events
	svc.ListenAuctionCreated()

	// Start auction scheduler (activates/ends auctions by time)
	svc.StartAuctionScheduler(ctx)

	// HTTP Router
	handler := bidding.NewHandler(svc)
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Heartbeat("/health"))
	r.Mount("/api/v1", handler.Routes())

	addr := fmt.Sprintf(":%d", cfg.Server.BiddingPort)
	srv := &http.Server{Addr: addr, Handler: r}

	// Graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		slog.Info("Shutting down Bidding Engine...")
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Error("server shutdown error", "error", err)
		}
	}()

	slog.Info("Bidding Engine started", "port", cfg.Server.BiddingPort)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}
