package events

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/segmentio/kafka-go"
)

// Run blocks until cancellation, retrying unavailable infrastructure in the
// background. Start it with go svc.Run(ctx). Cancel before closing SQL/Redis.
// A Service has only one active Run to prevent accidental duplicate publishers.
func (s *Service) Run(ctx context.Context) {
	if !s.running.CompareAndSwap(false, true) {
		s.log.Error("events worker already running")
		return
	}
	defer s.running.Store(false)
	if s.db == nil || len(s.brokers) == 0 {
		s.log.Error("events worker disabled: SQL database and Kafka broker required")
		return
	}
	for ctx.Err() == nil {
		if err := s.ensureTopic(ctx); err != nil {
			s.log.Warn("Kafka topic unavailable; outbox stays in MySQL", "error", err)
			if !waitRetry(ctx, 2*time.Second) {
				return
			}
			continue
		}
		break
	}
	if ctx.Err() != nil {
		return
	}
	writer := &kafka.Writer{
		Addr: kafka.TCP(s.brokers...), Topic: s.topic, Balancer: &kafka.Hash{},
		RequiredAcks: kafka.RequireAll, Async: false, BatchSize: 1,
		ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second,
		AllowAutoTopicCreation: true,
	}
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers: s.brokers, Topic: s.topic, GroupID: s.group,
		MinBytes: 1, MaxBytes: 1e6, MaxWait: time.Second,
		CommitInterval: 0, StartOffset: kafka.FirstOffset,
		ReadLagInterval: -1, SessionTimeout: 10 * time.Second,
		Dialer: &kafka.Dialer{Timeout: 5 * time.Second},
	})
	defer writer.Close()
	defer reader.Close()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		s.produce(ctx, writer)
	}()
	go func() {
		defer wg.Done()
		s.consume(ctx, reader)
	}()
	wg.Wait()
}

func (s *Service) ensureTopic(ctx context.Context) error {
	opCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := kafka.DialContext(opCtx, "tcp", s.brokers[0])
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline, _ := opCtx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}
	controller, err := conn.Controller()
	if err != nil {
		return err
	}
	admin, err := kafka.DialContext(opCtx, "tcp", net.JoinHostPort(controller.Host, strconv.Itoa(controller.Port)))
	if err != nil {
		return err
	}
	defer admin.Close()
	if err := admin.SetDeadline(deadline); err != nil {
		return err
	}
	err = admin.CreateTopics(kafka.TopicConfig{
		Topic: s.topic, NumPartitions: 1, ReplicationFactor: 1,
	})
	if errors.Is(err, kafka.TopicAlreadyExists) {
		return nil
	}
	return err
}

type outboxRow struct {
	id         int64
	eventID    string
	merchantID string
	refundID   string
	payload    []byte
}

func (s *Service) pending(ctx context.Context) ([]outboxRow, error) {
	opCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	rows, err := s.db.QueryContext(opCtx, `SELECT id, event_id, merchant_id, refund_id, payload
		FROM outbox_events WHERE published_at IS NULL ORDER BY id ASC LIMIT 50`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]outboxRow, 0)
	for rows.Next() {
		var item outboxRow
		if err := rows.Scan(&item.id, &item.eventID, &item.merchantID, &item.refundID, &item.payload); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Service) publishBatch(ctx context.Context, writer messageWriter) error {
	items, err := s.pending(ctx)
	if err != nil {
		return err
	}
	for _, item := range items {
		event, err := decodeEvent(item.payload)
		if err != nil {
			return fmt.Errorf("outbox %d invalid: %w", item.id, err)
		}
		if event.EventID != item.eventID || event.MerchantID != item.merchantID || event.RefundID != item.refundID {
			return fmt.Errorf("outbox %d identity mismatch", item.id)
		}
		writeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		err = writer.WriteMessages(writeCtx, kafka.Message{
			Key: []byte(item.merchantID), Value: item.payload, Time: event.OccurredAt,
		})
		cancel()
		if err != nil {
			return fmt.Errorf("publish outbox %d: %w", item.id, err)
		}
		// Publish acknowledgement precedes this update. A crash here can cause
		// redelivery, handled atomically by the Redis event_id deduplication.
		markCtx, markCancel := context.WithTimeout(ctx, 3*time.Second)
		_, err = s.db.ExecContext(markCtx, `UPDATE outbox_events SET published_at=UTC_TIMESTAMP(6)
			WHERE id=? AND published_at IS NULL`, item.id)
		markCancel()
		if err != nil {
			return fmt.Errorf("mark outbox %d published (redelivery is safe): %w", item.id, err)
		}
	}
	return nil
}

func (s *Service) produce(ctx context.Context, writer messageWriter) {
	for ctx.Err() == nil {
		if err := s.publishBatch(ctx, writer); err != nil && ctx.Err() == nil {
			s.log.Warn("outbox publishing postponed; business state unaffected", "error", err)
		}
		if !waitRetry(ctx, time.Second) {
			return
		}
	}
}

func (s *Service) consume(ctx context.Context, reader messageReader) {
	for ctx.Err() == nil {
		message, err := reader.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() == nil {
				s.log.Warn("Kafka activity fetch postponed", "error", err)
			}
			if !waitRetry(ctx, time.Second) {
				return
			}
			continue
		}
		// Do not fetch/commit a later message before this message is projected
		// AND committed. Otherwise a later offset could skip a Redis failure.
		// Malformed data blocks/reports instead of silently acknowledging loss.
		for ctx.Err() == nil {
			err = s.project(ctx, message.Value)
			if err == nil {
				commitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				err = reader.CommitMessages(commitCtx, message)
				cancel()
			}
			if err == nil {
				break
			}
			if ctx.Err() == nil {
				s.log.Warn("activity not acknowledged; retrying same event", "partition", message.Partition, "offset", message.Offset, "error", err)
			}
			if !waitRetry(ctx, time.Second) {
				return
			}
		}
	}
}
