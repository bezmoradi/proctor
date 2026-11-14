package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"m02-gin-mongodb/cmd/server"
	"m02-gin-mongodb/configs"
	"m02-gin-mongodb/internal/adapters/handlers/gin"
	"m02-gin-mongodb/internal/adapters/handlers/health"
	"m02-gin-mongodb/internal/adapters/loggers/logrus"
	"m02-gin-mongodb/internal/adapters/repositories/mongodb"
	"m02-gin-mongodb/internal/core/shared"
	"m02-gin-mongodb/internal/core/user/services"
)

func init() {
	configs.LoadEnv()
}

func main() {
	logger := logrus.New()
	if err := run(logger); err != nil {
		logger.Fatal(err)
	}
}

// run wires the app and serves until SIGINT/SIGTERM. It returns instead of exiting, so the deferred Close calls run.
func run(logger shared.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := mongodb.New()
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	service := services.NewUserService(db)

	serveErr := server.Serve(ctx, gin.NewRouter(service, logger), logger, health.Check{Name: "database", Probe: db.Ping})
	return serveErr
}
