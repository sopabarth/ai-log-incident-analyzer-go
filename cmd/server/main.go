// Command server runs the incident analyzer's HTTP API.
//
// It is configured from the environment (see .env.example), or from a .env file
// if present, and expects the database schema to be current - run
// `go run ./cmd/migrate up` first.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/api"
	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/config"
	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/incident"
	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/llm"
	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/store"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("server: ")
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	_ = godotenv.Load() // optional; variables already in the environment win
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// SIGINT/SIGTERM start a graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := requireCurrentSchema(ctx, db); err != nil {
		return err
	}

	groq, err := llm.NewGroq(cfg.GroqAPIKey, cfg.GroqModel)
	if err != nil {
		return err
	}
	analyzer, err := llm.NewAnalyzer(groq, llm.Options{
		MaxAttempts: cfg.LLMMaxAttempts,
		Backoff:     cfg.LLMRetryBackoff,
		Decompose:   cfg.TaskDecomposition,
		Logger:      logger,
	})
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           api.NewHandler(incident.NewService(db, analyzer, cfg.DedupWindow), logger),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      3 * time.Minute, // longer than one analysis may take, retries included
		IdleTimeout:       2 * time.Minute,
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.HTTPAddr, "model", cfg.GroqModel,
			"task_decomposition", cfg.TaskDecomposition, "dedup_window", cfg.DedupWindow)
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		return err // the server could not start, or stopped by itself
	case <-ctx.Done():
	}

	logger.Info("shutting down: finishing requests in flight")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// requireCurrentSchema fails fast, with an instruction, when the database has
// migrations that have not been applied, instead of letting the first request
// die on a missing table.
func requireCurrentSchema(ctx context.Context, db *store.Store) error {
	statuses, err := db.MigrationStatuses(ctx)
	if err != nil {
		return err
	}
	pending := 0
	for _, st := range statuses {
		if !st.Applied {
			pending++
		}
	}
	if pending > 0 {
		return fmt.Errorf("database schema is out of date: %d pending migration(s); run `go run ./cmd/migrate up`", pending)
	}
	return nil
}
