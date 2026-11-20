package services

import (
	"context"
	"fmt"

	"github.com/bezmoradi/proctor/examples/sample/internal/core/user/domain"
	"github.com/bezmoradi/proctor/examples/sample/internal/core/user/ports"
)

type UserService struct {
	repo      ports.UserRepositoryInterface
	publisher ports.EventPublisher
}

func NewUserService(repo ports.UserRepositoryInterface) *UserService {
	return &UserService{repo: repo}
}

func (us *UserService) WithPublisher(publisher ports.EventPublisher) *UserService {
	us.publisher = publisher
	return us
}

func (us *UserService) Index(ctx context.Context, limit int, cursor string) (domain.Page, error) {
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
	if err := domain.ValidateName(name); err != nil {
		return "", err
	}

	id, err := us.repo.Insert(ctx, name)
	if err != nil {
		return "", err
	}

	if err := us.publish(ctx, domain.UserCreated, domain.User{ID: id, Name: name}); err != nil {
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
	if err := us.publish(ctx, domain.UserUpdated, user); err != nil {
		return domain.User{}, err
	}

	return user, nil
}

func (us *UserService) Delete(ctx context.Context, id string) error {
	if err := us.repo.Delete(ctx, id); err != nil {
		return err
	}

	return us.publish(ctx, domain.UserDeleted, domain.User{ID: id})
}

func (us *UserService) publish(ctx context.Context, kind domain.EventKind, user domain.User) error {
	if us.publisher == nil {
		return nil
	}
	if err := us.publisher.Publish(ctx, domain.Event{Kind: kind, User: user}); err != nil {
		return fmt.Errorf("%w: %w", domain.ErrEventPublishFailed, err)
	}
	return nil
}
