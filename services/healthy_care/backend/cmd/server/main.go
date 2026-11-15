package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"healthycare/internal/app"
	"healthycare/internal/config"
	"healthycare/internal/db"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		if err := db.Migrate(ctx, os.Getenv("DATABASE_URL")); err != nil {
			slog.Error("migration failed", "error", err)
			os.Exit(1)
		}
		slog.Info("database migrations complete")
		return
	}
	cfg, err := config.Load()
	if err != nil {
		slog.Error("configuration error", "error", err)
		os.Exit(1)
	}
	a, err := app.New(ctx, cfg)
	if err != nil {
		slog.Error("database connection failed", "error", err)
		os.Exit(1)
	}
	defer a.Close()
	if len(os.Args) > 1 && os.Args[1] == "knowledge-import" {
		if err = a.ImportProducts(ctx, os.Stdin); err != nil {
			slog.Error("knowledge import rejected", "error", err)
			os.Exit(1)
		}
		slog.Info("knowledge bundle imported")
		return
	}
	if len(os.Args) == 4 && os.Args[1] == "knowledge-retire" {
		if err = a.RetireProduct(ctx, os.Args[2], os.Args[3]); err != nil {
			slog.Error("knowledge withdrawal failed", "error", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "worker" {
		if err = a.Work(ctx); err != nil {
			slog.Error("worker stopped", "error", err)
			os.Exit(1)
		}
		return
	}
	if len(cfg.CompanionToken) < 32 {
		slog.Error("COMPANION_TOKEN of at least 32 characters is required for the health API")
		os.Exit(1)
	}
	server := &http.Server{Addr: cfg.Addr, Handler: a.Router(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 125 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	go func() {
		slog.Info("API listening", "address", cfg.Addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("HTTP server failed", "error", err)
			stop()
		}
	}()
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdown)
}
