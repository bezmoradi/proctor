package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"m04-httprouter-mariadb/cmd/server"
	"m04-httprouter-mariadb/configs"
	"m04-httprouter-mariadb/internal/adapters/handlers/health"
	"m04-httprouter-mariadb/internal/adapters/handlers/httprouter"
	"m04-httprouter-mariadb/internal/adapters/loggers/slog"
	"m04-httprouter-mariadb/internal/adapters/repositories/mariadb"
	"m04-httprouter-mariadb/internal/core/shared"
	"m04-httprouter-mariadb/internal/core/user/services"
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

	db, err := mariadb.New()
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	service := services.NewUserService(db)

	serveErr := server.Serve(ctx, httprouter.NewRouter(service, logger), logger, health.Check{Name: "database", Probe: db.Ping})
	return serveErr
}
