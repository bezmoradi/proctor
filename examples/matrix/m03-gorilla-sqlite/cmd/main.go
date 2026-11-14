package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"m03-gorilla-sqlite/cmd/server"
	"m03-gorilla-sqlite/configs"
	"m03-gorilla-sqlite/internal/adapters/handlers/gorilla"
	"m03-gorilla-sqlite/internal/adapters/handlers/health"
	"m03-gorilla-sqlite/internal/adapters/loggers/log"
	"m03-gorilla-sqlite/internal/adapters/repositories/sqlite"
	"m03-gorilla-sqlite/internal/core/shared"
	"m03-gorilla-sqlite/internal/core/user/services"
)

func init() {
	configs.LoadEnv()
}

func main() {
	logger := log.New()
	if err := run(logger); err != nil {
		logger.Fatal(err)
	}
}

// run wires the app and serves until SIGINT/SIGTERM. It returns instead of exiting, so the deferred Close calls run.
func run(logger shared.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := sqlite.New()
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	service := services.NewUserService(db)

	serveErr := server.Serve(ctx, gorilla.NewRouter(service, logger), logger, health.Check{Name: "database", Probe: db.Ping})
	return serveErr
}
