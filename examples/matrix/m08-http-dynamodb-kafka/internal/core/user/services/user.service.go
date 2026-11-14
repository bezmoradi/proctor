package services

import (
	"context"
	"fmt"

	"m08-http-dynamodb-kafka/internal/core/user/domain"
	"m08-http-dynamodb-kafka/internal/core/user/ports"
)

// UserService holds the user use cases. It depends only on ports, so any
// repository and publisher adapter can be plugged in.
type UserService struct {
	repo      ports.UserRepositoryInterface
	publisher ports.EventPublisher
}

func NewUserService(repo ports.UserRepositoryInterface) *UserService {
	return &UserService{repo: repo}
}

// WithPublisher attaches an event publisher so that writes emit domain events.
// It is optional: a service constructed without one simply skips publishing.
func (us *UserService) WithPublisher(publisher ports.EventPublisher) *UserService {
	us.publisher = publisher
	return us
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

// publish emits an event for a write that has already committed. On failure the
// write is deliberately not undone: a compensating write can fail or be
// interrupted too, so it trades one inconsistency for another. The error is
// tagged instead, so the caller can report which half failed. Making both halves
// atomic needs an outbox table written in the write's own transaction.
func (us *UserService) publish(ctx context.Context, kind domain.EventKind, user domain.User) error {
	if us.publisher == nil {
		return nil
	}
	if err := us.publisher.Publish(ctx, domain.Event{Kind: kind, User: user}); err != nil {
		return fmt.Errorf("%w: %w", domain.ErrEventPublishFailed, err)
	}
	return nil
}
