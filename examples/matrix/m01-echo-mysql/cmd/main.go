package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"m01-echo-mysql/cmd/server"
	"m01-echo-mysql/configs"
	"m01-echo-mysql/internal/adapters/handlers/echo"
	"m01-echo-mysql/internal/adapters/handlers/health"
	"m01-echo-mysql/internal/adapters/loggers/zap"
	"m01-echo-mysql/internal/adapters/repositories/mysql"
	"m01-echo-mysql/internal/core/shared"
	"m01-echo-mysql/internal/core/user/services"
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

	db, err := mysql.New()
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	service := services.NewUserService(db)

	serveErr := server.Serve(ctx, echo.NewRouter(service, logger), logger, health.Check{Name: "database", Probe: db.Ping})
	return serveErr
}
