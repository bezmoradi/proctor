package ports

import (
	"context"

	"github.com/bezmoradi/proctor/examples/sample/internal/core/user/domain"
)

type UserRepositoryInterface interface {
	Delete(ctx context.Context, id string) error
	Fetch(ctx context.Context, id string) (domain.User, error)
	FetchAll(ctx context.Context, limit int, cursor string) (domain.Page, error)
	Insert(ctx context.Context, name string) (string, error)
	Update(ctx context.Context, id string, value string) error
}
