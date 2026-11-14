package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"m05-graphql-postgres/cmd/server"
	"m05-graphql-postgres/configs"
	"m05-graphql-postgres/internal/adapters/handlers/graphql"
	"m05-graphql-postgres/internal/adapters/handlers/health"
	"m05-graphql-postgres/internal/adapters/loggers/zap"
	"m05-graphql-postgres/internal/adapters/repositories/postgres"
	"m05-graphql-postgres/internal/core/shared"
	"m05-graphql-postgres/internal/core/user/services"
)

func init() {
	configs.LoadEnv()
}

func main() {
	logger := zap.New()
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

	serveErr := server.Serve(ctx, graphql.NewHandler(service, logger), logger, health.Check{Name: "database", Probe: db.Ping})
	return serveErr
}
