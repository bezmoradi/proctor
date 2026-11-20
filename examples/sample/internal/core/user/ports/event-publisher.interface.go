package ports

import (
	"context"

	"github.com/bezmoradi/proctor/examples/sample/internal/core/user/domain"
)

type EventPublisher interface {
	Publish(ctx context.Context, event domain.Event) error
}
