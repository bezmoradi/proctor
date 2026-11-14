package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"m08-http-dynamodb-kafka/cmd/server"
	"m08-http-dynamodb-kafka/configs"
	"m08-http-dynamodb-kafka/internal/adapters/brokers/kafka"
	"m08-http-dynamodb-kafka/internal/adapters/events"
	"m08-http-dynamodb-kafka/internal/adapters/handlers/health"
	"m08-http-dynamodb-kafka/internal/adapters/handlers/http"
	"m08-http-dynamodb-kafka/internal/adapters/loggers/zap"
	"m08-http-dynamodb-kafka/internal/adapters/repositories/dynamodb"
	"m08-http-dynamodb-kafka/internal/core/shared"
	"m08-http-dynamodb-kafka/internal/core/user/services"
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

// run wires the app and serves until SIGINT/SIGTERM or until the event
// consumer fails. It returns instead of exiting, so the deferred Close calls run.
func run(logger shared.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := dynamodb.New()
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

	// The consumer runs alongside the HTTP server. If it fails, cancelling ctx
	// drains the server and run returns the error, so the process exits 1 and
	// its supervisor restarts it, rather than serving with a dead consumer.
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var wg sync.WaitGroup
	wg.Go(func() {
		if err := broker.Consume(ctx, handleEvent(logger)); err != nil {
			cancel(fmt.Errorf("event consumer: %w", err))
		}
	})

	serveErr := server.Serve(ctx, http.NewRouter(service, logger), logger, health.Check{Name: "database", Probe: db.Ping})

	// Stop the consumer and let it finish the message in hand.
	cancel(nil)
	wg.Wait()
	if cause := context.Cause(ctx); serveErr == nil && !errors.Is(cause, context.Canceled) {
		return cause
	}
	return serveErr
}

// handleEvent logs each event the service consumes: its type, id and size, never
// the payload, which carries a user's name. A message that isn't an event is
// logged and dropped, since retrying can't fix it.
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
