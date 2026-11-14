package main

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"

	"m10-worker-rabbitmq-postgres/configs"
	"m10-worker-rabbitmq-postgres/internal/adapters/brokers/rabbitmq"
	"m10-worker-rabbitmq-postgres/internal/adapters/events"
	"m10-worker-rabbitmq-postgres/internal/adapters/loggers/slog"
	"m10-worker-rabbitmq-postgres/internal/adapters/repositories/postgres"
	"m10-worker-rabbitmq-postgres/internal/core/shared"
	"m10-worker-rabbitmq-postgres/internal/core/user/domain"
	"m10-worker-rabbitmq-postgres/internal/core/user/services"
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

// run consumes until SIGINT/SIGTERM or a broker failure. It returns instead of
// exiting, so the deferred Close calls run.
func run(logger shared.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	repo, err := postgres.New()
	if err != nil {
		return err
	}
	defer func() { _ = repo.Close() }()
	userService := services.NewUserService(repo)

	broker, err := rabbitmq.New()
	if err != nil {
		return err
	}
	defer func() { _ = broker.Close() }()

	// Producer side: publish a sample user.created event, as an upstream service
	// would. The events adapter wraps it in the versioned envelope.
	sample := domain.Event{Kind: domain.UserCreated, User: domain.User{Name: "Hello World"}}
	if err := events.NewPublisher(broker).Publish(ctx, sample); err != nil {
		return fmt.Errorf("publish the sample event: %w", err)
	}

	// Consumer side. Consume blocks until ctx ends (nil) or the broker fails (the
	// error). A handler error means "retry this message"; a message that can never
	// succeed is logged and acknowledged by returning nil.
	logger.Info("worker is running; waiting for messages. Press Ctrl+C to stop.")
	err = broker.Consume(ctx, func(ctx context.Context, body []byte) error {
		envelope, err := events.Decode(body)
		if err != nil {
			logger.Error("dropping malformed message", "error", err, "bytes", len(body))
			return nil
		}
		if envelope.Type != string(domain.UserCreated) {
			logger.Info("event ignored", "type", envelope.Type, "id", envelope.ID)
			return nil
		}

		// CreateOnce keys the user by the event id, so a redelivered event
		// doesn't create a second user.
		err = userService.CreateOnce(ctx, envelope.ID, envelope.Data.Name)
		if domain.IsValidationError(err) {
			logger.Error("dropping invalid event", "id", envelope.ID, "error", err)
			return nil
		}
		if err != nil {
			logger.Error("persisting event failed; will retry", "id", envelope.ID, "error", err)
			return err
		}
		logger.Info("event persisted", "type", envelope.Type, "id", envelope.ID)
		return nil
	})
	logger.Info("worker shutting down")
	return err
}
