package ports

import (
	"context"

	"m07-echo-postgres-redis/internal/core/user/domain"
)

// EventPublisher is the outbound port a service uses to emit domain events. The
// events adapter implements it on top of the message broker (Kafka, RabbitMQ,
// or Redis).
type EventPublisher interface {
	Publish(ctx context.Context, event domain.Event) error
}
