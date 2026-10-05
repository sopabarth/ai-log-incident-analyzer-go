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
	"log"
	"os"
	"os/signal"

	"github.com/joho/godotenv"

	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/config"
	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/store"
)

// commands maps each subcommand to what it does. Looking the command up here,
// before touching the database, means a typo fails fast and there is a single
// place that knows the list.
var commands = map[string]func(context.Context, *store.Store) error{
	"up":     up,
	"down":   down,
	"status": status,
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("migrate: ")
	if err := run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(args []string) error {
	name := "up"
	if len(args) > 0 {
		name = args[0]
	}
	command, ok := commands[name]
	if !ok {
		return fmt.Errorf("unknown command %q (want up, down or status)", name)
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

	return command(ctx, s)
}

func up(ctx context.Context, s *store.Store) error {
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
	return nil
}

func down(ctx context.Context, s *store.Store) error {
	name, err := s.RollbackOne(ctx)
	if err != nil {
		return err
	}
	fmt.Println("reverted", name)
	return nil
}

func status(ctx context.Context, s *store.Store) error {
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
	return nil
}
