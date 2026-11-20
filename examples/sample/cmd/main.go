package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/bezmoradi/proctor/examples/sample/cmd/server"
	"github.com/bezmoradi/proctor/examples/sample/configs"
	"github.com/bezmoradi/proctor/examples/sample/internal/adapters/brokers/kafka"
	"github.com/bezmoradi/proctor/examples/sample/internal/adapters/events"
	"github.com/bezmoradi/proctor/examples/sample/internal/adapters/handlers/health"
	"github.com/bezmoradi/proctor/examples/sample/internal/adapters/handlers/http"
	"github.com/bezmoradi/proctor/examples/sample/internal/adapters/loggers/slog"
	"github.com/bezmoradi/proctor/examples/sample/internal/adapters/repositories/postgres"
	"github.com/bezmoradi/proctor/examples/sample/internal/core/shared"
	"github.com/bezmoradi/proctor/examples/sample/internal/core/user/services"
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

func run(logger shared.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := postgres.New()
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	broker, err := kafka.New()
	if err != nil {
		return err
	}
	defer func() { _ = broker.Close() }()

	service := services.NewUserService(db).WithPublisher(events.NewPublisher(broker))

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var wg sync.WaitGroup
	wg.Go(func() {
		if err := broker.Consume(ctx, handleEvent(logger)); err != nil {
			cancel(fmt.Errorf("event consumer: %w", err))
		}
	})

	serveErr := server.Serve(ctx, http.NewRouter(service, logger), logger, health.Check{Name: "database", Probe: db.Ping})

	cancel(nil)
	wg.Wait()
	if cause := context.Cause(ctx); serveErr == nil && !errors.Is(cause, context.Canceled) {
		return cause
	}
	return serveErr
}

func handleEvent(logger shared.Logger) func(context.Context, []byte) error {
	return func(_ context.Context, body []byte) error {
		envelope, err := events.Decode(body)
		if err != nil {
			logger.Error("dropping malformed event", "error", err, "bytes", len(body))
			return nil
		}
		logger.Info("event received", "type", envelope.Type, "id", envelope.ID, "bytes", len(body))
		return nil
	}
}
