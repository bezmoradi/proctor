package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/bezmoradi/proctor/examples/sample/internal/core/user/domain"

	"github.com/google/uuid"
)

const Version = 1

type Envelope struct {
	ID         string    `json:"id"`
	Type       string    `json:"type"`
	Version    int       `json:"version"`
	OccurredAt time.Time `json:"occurred_at"`
	Data       User      `json:"data"`
}

type User struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

func Encode(event domain.Event) ([]byte, error) {
	return json.Marshal(Envelope{
		ID:         uuid.NewString(),
		Type:       string(event.Kind),
		Version:    Version,
		OccurredAt: time.Now().UTC(),
		Data:       User{ID: event.User.ID, Name: event.User.Name},
	})
}

var ErrMalformed = errors.New("malformed event")

func Decode(body []byte) (Envelope, error) {
	var envelope Envelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return Envelope{}, fmt.Errorf("%w: %w", ErrMalformed, err)
	}
	if envelope.Version != Version {
		return Envelope{}, fmt.Errorf("%w: unsupported version %d", ErrMalformed, envelope.Version)
	}
	if _, err := uuid.Parse(envelope.ID); err != nil {
		return Envelope{}, fmt.Errorf("%w: id is not a UUID", ErrMalformed)
	}
	if envelope.Type == "" {
		return Envelope{}, fmt.Errorf("%w: no type", ErrMalformed)
	}
	return envelope, nil
}

type Producer interface {
	Produce(ctx context.Context, body []byte) error
}

type Publisher struct {
	producer Producer
}

func NewPublisher(producer Producer) *Publisher {
	return &Publisher{producer: producer}
}

func (p *Publisher) Publish(ctx context.Context, event domain.Event) error {
	body, err := Encode(event)
	if err != nil {
		return err
	}
	return p.producer.Produce(ctx, body)
}
