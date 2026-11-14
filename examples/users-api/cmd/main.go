package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"users-api/cmd/server"
	"users-api/configs"
	"users-api/internal/adapters/handlers/http"
	"users-api/internal/adapters/loggers/slog"
	"users-api/internal/adapters/repositories/memory"
	"users-api/internal/core/shared"
	"users-api/internal/core/user/services"
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

	// No -d: an in-memory store, lost on restart.
	db := memory.New()

	service := services.NewUserService(db)

	serveErr := server.Serve(ctx, http.NewRouter(service, logger), logger)
	return serveErr
}
