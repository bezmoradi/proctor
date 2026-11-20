package kafka

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/segmentio/kafka-go"
)

const (
	connectTimeout = 5 * time.Second

	publishTimeout = 2 * time.Second

	handlerTimeout = 30 * time.Second

	minBackoff = 500 * time.Millisecond
	maxBackoff = 30 * time.Second
)

type Broker struct {
	writer *kafka.Writer
	reader *kafka.Reader
}

func New() (*Broker, error) {
	broker := os.Getenv("KAFKA_BROKER")
	topic := os.Getenv("KAFKA_TOPIC")
	group := os.Getenv("KAFKA_GROUP_ID")
	if broker == "" || topic == "" || group == "" {
		return nil, errors.New("KAFKA_BROKER, KAFKA_TOPIC and KAFKA_GROUP_ID must all be set")
	}

	if err := ensureTopic(broker, topic); err != nil {
		return nil, fmt.Errorf("cannot reach Kafka: %w", err)
	}

	writer := &kafka.Writer{
		Addr:     kafka.TCP(broker),
		Topic:    topic,
		Balancer: &kafka.Hash{},

		RequiredAcks: kafka.RequireAll,

		BatchTimeout: 10 * time.Millisecond,
	}

	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers: []string{broker},
		Topic:   topic,

		GroupID: group,

		MinBytes: 1,
		MaxBytes: 10e6,
	})

	return &Broker{writer: writer, reader: reader}, nil
}

func ensureTopic(broker string, topic string) error {
	dialer := &kafka.Dialer{Timeout: connectTimeout}
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()

	conn, err := dialer.DialContext(ctx, "tcp", broker)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	controller, err := conn.Controller()
	if err != nil {
		return err
	}

	controllerConn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(controller.Host, strconv.Itoa(controller.Port)))
	if err != nil {
		return err
	}
	defer func() { _ = controllerConn.Close() }()

	err = controllerConn.CreateTopics(kafka.TopicConfig{
		Topic:             topic,
		NumPartitions:     1,
		ReplicationFactor: 1,
	})
	if err != nil && !errors.Is(err, kafka.TopicAlreadyExists) {
		return err
	}

	return nil
}

func (b *Broker) Produce(ctx context.Context, body []byte) error {
	ctx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	return b.writer.WriteMessages(ctx, kafka.Message{Value: body})
}

func (b *Broker) Consume(ctx context.Context, handler func(context.Context, []byte) error) error {
	for {
		message, err := b.reader.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}

		if !process(ctx, message.Value, handler) {
			return nil
		}

		commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), connectTimeout)
		err = b.reader.CommitMessages(commitCtx, message)
		cancel()
		if err != nil {
			return fmt.Errorf("commit offset: %w", err)
		}
	}
}

func process(ctx context.Context, body []byte, handler func(context.Context, []byte) error) bool {
	backoff := minBackoff
	for {
		attemptCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), handlerTimeout)
		err := handler(attemptCtx, body)
		cancel()
		if err == nil {
			return true
		}

		select {
		case <-ctx.Done():
			return false
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, maxBackoff)
	}
}

func (b *Broker) Close() error {
	return errors.Join(b.reader.Close(), b.writer.Close())
}
