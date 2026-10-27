// Package events implements a best-effort activity view. MySQL remains the
// refund source of truth; neither Kafka nor Redis decides a refund's status.
package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/segmentio/kafka-go"
)

const (
	DefaultTopic  = "refund.events"
	DefaultGroup  = "refund-workbench-activity"
	DefaultPrefix = "refundworkbench:v1:"
	ActivityLimit = 100
	RateLimit     = 300
)

// Event is the public activity/Kafka payload. Amounts are integer cents.
type Event struct {
	EventID     string    `json:"event_id"`
	MerchantID  string    `json:"merchant_id"`
	RefundID    string    `json:"refund_id"`
	OrderID     string    `json:"order_id"`
	AmountCents int64     `json:"amount_cents"`
	Status      string    `json:"status"`
	Message     string    `json:"message"`
	OccurredAt  time.Time `json:"occurred_at"`
}

type messageWriter interface {
	WriteMessages(context.Context, ...kafka.Message) error
}

type messageReader interface {
	FetchMessage(context.Context) (kafka.Message, error)
	CommitMessages(context.Context, ...kafka.Message) error
}

// Service owns no SQL/Redis connection lifecycle. Run owns its Kafka clients.
// Deploy one outbox publisher per database for this local demonstration.
type Service struct {
	db      *sql.DB
	redis   *redis.Client
	brokers []string
	topic   string
	group   string
	prefix  atomic.Value
	log     *slog.Logger
	running atomic.Bool
	now     func() time.Time
}

func New(db *sql.DB, redisClient *redis.Client, brokers []string, topic, group string) *Service {
	if topic == "" {
		topic = DefaultTopic
	}
	if group == "" {
		group = DefaultGroup
	}
	s := &Service{
		db: db, redis: redisClient, brokers: append([]string(nil), brokers...),
		topic: topic, group: group, log: slog.Default(), now: time.Now,
	}
	s.prefix.Store(DefaultPrefix)
	return s
}

// SetPrefix isolates projection and limiter keys for a database/test instance.
// Call before Run/HTTP serving; it is race-safe, but changing it live switches
// the activity namespace rather than migrating an existing projection.
func (s *Service) SetPrefix(prefix string) {
	if prefix != "" {
		s.prefix.Store(prefix)
	}
}

func (s *Service) redisPrefix() string { return s.prefix.Load().(string) }

func decodeEvent(payload []byte) (Event, error) {
	var event Event
	if err := json.Unmarshal(payload, &event); err != nil {
		return event, fmt.Errorf("decode activity: %w", err)
	}
	if strings.TrimSpace(event.EventID) == "" || strings.TrimSpace(event.MerchantID) == "" ||
		strings.TrimSpace(event.RefundID) == "" || strings.TrimSpace(event.OrderID) == "" ||
		event.AmountCents <= 0 || event.OccurredAt.IsZero() {
		return event, errors.New("invalid activity identity, amount or timestamp")
	}
	switch event.Status {
	case "PROCESSING", "UNKNOWN", "SUCCEEDED", "FAILED":
	default:
		return event, errors.New("invalid activity status")
	}
	return event, nil
}

func waitRetry(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
