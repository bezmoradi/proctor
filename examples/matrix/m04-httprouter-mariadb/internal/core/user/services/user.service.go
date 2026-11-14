package services

import (
	"context"

	"m04-httprouter-mariadb/internal/core/user/domain"
	"m04-httprouter-mariadb/internal/core/user/ports"
)

// UserService holds the user use cases. It depends only on ports, so any
// repository adapter can be plugged in.
type UserService struct {
	repo ports.UserRepositoryInterface
}

func NewUserService(repo ports.UserRepositoryInterface) *UserService {
	return &UserService{repo: repo}
}

func (us *UserService) Index(ctx context.Context, limit int, cursor string) (domain.Page, error) {
	// Clamp here so the guardrail is one rule for every caller: a missing or
	// non-positive limit falls back to the default, and no caller can ask for
	// more than the cap. The repository always receives a sane, bounded limit.
	if limit <= 0 {
		limit = domain.DefaultPageLimit
	}
	if limit > domain.MaxPageLimit {
		limit = domain.MaxPageLimit
	}

	return us.repo.FetchAll(ctx, limit, cursor)
}

func (us *UserService) Show(ctx context.Context, id string) (domain.User, error) {
	return us.repo.Fetch(ctx, id)
}

func (us *UserService) Create(ctx context.Context, name string) (string, error) {
	// domain.ValidateName owns every name rule; see domain/user.go.
	if err := domain.ValidateName(name); err != nil {
		return "", err
	}

	id, err := us.repo.Insert(ctx, name)
	if err != nil {
		return "", err
	}

	return id, nil
}

func (us *UserService) Update(ctx context.Context, id string, newName string) (domain.User, error) {
	if err := domain.ValidateName(newName); err != nil {
		return domain.User{}, err
	}

	if err := us.repo.Update(ctx, id, newName); err != nil {
		return domain.User{}, err
	}

	user := domain.User{ID: id, Name: newName}

	return user, nil
}

func (us *UserService) Delete(ctx context.Context, id string) error {
	return us.repo.Delete(ctx, id)
}
