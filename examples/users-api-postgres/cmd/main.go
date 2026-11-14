package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"users-api-postgres/cmd/server"
	"users-api-postgres/configs"
	"users-api-postgres/internal/adapters/handlers/chi"
	"users-api-postgres/internal/adapters/handlers/health"
	"users-api-postgres/internal/adapters/loggers/slog"
	"users-api-postgres/internal/adapters/repositories/postgres"
	"users-api-postgres/internal/core/shared"
	"users-api-postgres/internal/core/user/services"
)

func init() {
	configs.LoadEnv()
}

func main() {
	logger := slog.New()
	if err := run(logger); err != nil {
		logger.Fatal(err)
	}
}

// run wires the app and serves until SIGINT/SIGTERM. It returns instead of exiting, so the deferred Close calls run.
func run(logger shared.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := postgres.New()
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	service := services.NewUserService(db)

	serveErr := server.Serve(ctx, chi.NewRouter(service, logger), logger, health.Check{Name: "database", Probe: db.Ping})
	return serveErr
}
