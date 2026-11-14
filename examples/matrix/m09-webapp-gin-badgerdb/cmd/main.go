package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"m09-webapp-gin-badgerdb/cmd/server"
	"m09-webapp-gin-badgerdb/configs"
	"m09-webapp-gin-badgerdb/internal/adapters/handlers/gin"
	"m09-webapp-gin-badgerdb/internal/adapters/handlers/health"
	"m09-webapp-gin-badgerdb/internal/adapters/loggers/log"
	"m09-webapp-gin-badgerdb/internal/adapters/repositories/badgerdb"
	"m09-webapp-gin-badgerdb/internal/core/shared"
	"m09-webapp-gin-badgerdb/internal/core/user/services"
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

	db, err := badgerdb.New()
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	service := services.NewUserService(db)

	serveErr := server.Serve(ctx, gin.NewRouter(service, logger), logger, health.Check{Name: "database", Probe: db.Ping})
	return serveErr
}
