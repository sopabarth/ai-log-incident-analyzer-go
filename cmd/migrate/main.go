// Command migrate applies, reverts or reports database migrations.
//
//	migrate [up]    apply all pending migrations (the default)
//	migrate down    revert the most recently applied migration
//	migrate status  list migrations and whether each is applied
//
// It reads DATABASE_URL from the environment, or from a .env file if present.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/joho/godotenv"

	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/config"
	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/store"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	command := "up"
	if len(args) > 0 {
		command = args[0]
	}
	if command != "up" && command != "down" && command != "status" {
		return fmt.Errorf("unknown command %q (want up, down or status)", command)
	}

	_ = godotenv.Load() // optional; variables already in the environment win
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	s, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer s.Close()

	switch command {
	case "up":
		applied, err := s.Migrate(ctx)
		for _, name := range applied {
			fmt.Println("applied", name)
		}
		if err != nil {
			return err
		}
		if len(applied) == 0 {
			fmt.Println("nothing to apply: database is up to date")
		}
	case "down":
		name, err := s.RollbackOne(ctx)
		if err != nil {
			return err
		}
		fmt.Println("reverted", name)
	case "status":
		statuses, err := s.MigrationStatuses(ctx)
		if err != nil {
			return err
		}
		for _, st := range statuses {
			state := "pending"
			if st.Applied {
				state = "applied"
			}
			fmt.Printf("%-8s %s\n", state, st.Name)
		}
	}
	return nil
}
