// Command api serves the loan API. Subcommands: none (serve), migrate, healthcheck.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/the-vishal-kumar/go-postgres-boilerplate/internal/config"
	"github.com/the-vishal-kumar/go-postgres-boilerplate/internal/httpapi"
	"github.com/the-vishal-kumar/go-postgres-boilerplate/internal/migrate"
	"github.com/the-vishal-kumar/go-postgres-boilerplate/internal/store"
	"github.com/the-vishal-kumar/go-postgres-boilerplate/migrations"
)

func main() {
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "":
		os.Exit(serve())
	case "migrate":
		os.Exit(runMigrate())
	case "healthcheck":
		os.Exit(healthcheck())
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q (use no argument, migrate or healthcheck)\n", cmd)
		os.Exit(2)
	}
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l}))
}

func serve() int {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	log := newLogger(cfg.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Error("database configuration rejected", "error", err.Error())
		return 1
	}
	defer pool.Close()
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = pool.Ping(pingCtx)
	cancel()
	if err != nil {
		log.Error("database unreachable", "error", err.Error())
		return 1
	}

	app, err := httpapi.New(cfg, store.New(pool), log)
	if err != nil {
		log.Error("start-up failed", "error", err.Error())
		return 1
	}
	app.RunSweepers(ctx)

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Port),
		Handler: app.Handler(),
		// A load balancer idles out at 60 s; stay above it so it, not this
		// server, closes idle keep-alive connections.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       65 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()
	log.Info("HTTP server listening", "port", cfg.Port, "nodeEnv", cfg.NodeEnv)

	select {
	case err := <-serveErr:
		log.Error("server stopped", "error", err.Error())
		return 1
	case <-ctx.Done():
	}

	// Fail readiness first, then stop accepting connections and drain.
	app.BeginShutdown()
	log.Info("shutdown started")
	shutCtx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.ShutdownTimeoutMs)*time.Millisecond)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Error("shutdown failed", "error", err.Error())
		return 1
	}
	pool.Close()
	log.Info("shutdown complete")
	return 0
}

func runMigrate() int {
	log := newLogger("info")
	url := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if !strings.HasPrefix(url, "postgres://") && !strings.HasPrefix(url, "postgresql://") {
		log.Error("DATABASE_URL must be set to a postgres:// or postgresql:// URL")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		log.Error("database configuration rejected", "error", err.Error())
		return 1
	}
	defer pool.Close()
	if err := migrate.Run(ctx, pool, migrations.FS, log); err != nil {
		log.Error("migration failed", "error", err.Error())
		return 1
	}
	log.Info("migrations up to date")
	return 0
}

// healthcheck backs the container HEALTHCHECK: a distroless image has no curl.
func healthcheck() int {
	port := strings.TrimSpace(os.Getenv("PORT"))
	if port == "" {
		port = "3000"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/health/live")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, errors.New("liveness returned "+resp.Status))
		return 1
	}
	return 0
}
