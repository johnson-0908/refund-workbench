package events

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/segmentio/kafka-go"
)

func uniqueTestID(t *testing.T) string {
	t.Helper()
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(random[:])
}

// Integration tests never FLUSHDB, scan/delete unrelated keys, stop containers,
// or use the business database. Every test tracks its exact random-prefix keys.
func redisService(t *testing.T) (*Service, *redis.Client) {
	t.Helper()
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("real Redis integration not run: set TEST_REDIS_ADDR explicitly")
	}
	client := redis.NewClient(&redis.Options{
		Addr: addr, Password: os.Getenv("TEST_REDIS_PASSWORD"),
		DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second,
		ContextTimeoutEnabled: true, MaxRetries: -1,
	})
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("TEST_REDIS_ADDR provided but unavailable: %v", err)
	}
	s := quiet(New(nil, client, nil, "", ""))
	s.SetPrefix("test:refundworkbench:" + uniqueTestID(t) + ":")
	for _, merchant := range []string{"A", "B"} {
		seen, list := s.activityKeys(merchant)
		t.Cleanup(func() {
			if err := client.Del(context.Background(), seen, list).Err(); err != nil {
				t.Errorf("remove own test keys: %v", err)
			}
		})
	}
	return s, client
}

func TestRedisProjectionIsDeduplicatedBoundedAndMerchantScoped(t *testing.T) {
	s, client := redisService(t)
	ctx := context.Background()
	for i := 0; i < 105; i++ {
		if err := s.project(ctx, eventBytes(t, exampleEvent(fmt.Sprintf("ev_%d", i), "A"))); err != nil {
			t.Fatal(err)
		}
	}
	// Both a retained and an already-evicted payload must remain deduplicated.
	for _, id := range []string{"ev_104", "ev_0"} {
		if err := s.project(ctx, eventBytes(t, exampleEvent(id, "A"))); err != nil {
			t.Fatal(err)
		}
	}
	items, available := s.Activity(ctx, "A")
	if !available || len(items) != ActivityLimit || items[0].EventID != "ev_104" || items[99].EventID != "ev_5" {
		t.Fatalf("unexpected bounded projection: available=%v count=%d", available, len(items))
	}
	if err := s.project(ctx, eventBytes(t, exampleEvent("ev_104", "B"))); err != nil {
		t.Fatal(err)
	}
	other, available := s.Activity(ctx, "B")
	if !available || len(other) != 1 || other[0].MerchantID != "B" {
		t.Fatal("cross-merchant dedup/list isolation failed")
	}
	// Refuse corrupted/mixed-merchant projection rather than leak another tenant.
	_, list := s.activityKeys("A")
	if err := client.LPush(ctx, list, eventBytes(t, exampleEvent("ev_other", "B"))).Err(); err != nil {
		t.Fatal(err)
	}
	if corrupted, available := s.Activity(ctx, "A"); available || len(corrupted) != 0 {
		t.Fatal("mixed merchant activity must not be returned")
	}
}

func TestRedisProjectionTypeErrorCanRecoverWithoutLosingEvent(t *testing.T) {
	s, client := redisService(t)
	ctx := context.Background()
	seen, list := s.activityKeys("A")
	if err := client.Set(ctx, list, "injected wrong type", 0).Err(); err != nil {
		t.Fatal(err)
	}
	payload := eventBytes(t, exampleEvent("ev_retry", "A"))
	if err := s.project(ctx, payload); err == nil {
		t.Fatal("expected incompatible-type failure")
	}
	if count, err := client.SCard(ctx, seen).Result(); err != nil || count != 0 {
		t.Fatal("failed projection must not mark event as seen")
	}
	if err := client.Del(ctx, list).Err(); err != nil {
		t.Fatal(err)
	}
	if err := s.project(ctx, payload); err != nil {
		t.Fatal(err)
	}
	items, available := s.Activity(ctx, "A")
	if !available || len(items) != 1 || items[0].EventID != "ev_retry" {
		t.Fatal("recovered projection lost event")
	}
}

func TestRedisLimiter300PerMinuteAndIdentityIsolation(t *testing.T) {
	s, client := redisService(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Minute)
	s.now = func() time.Time { return now }
	keys := []string{s.limiterKey("writer-A", now), s.limiterKey("writer-B", now), s.limiterKey("writer-A", now.Add(time.Minute))}
	t.Cleanup(func() { _ = client.Del(context.Background(), keys...).Err() })
	for i := 1; i <= RateLimit+1; i++ {
		allowed, available := s.Limiter(ctx, "writer-A")
		if !available || allowed != (i <= RateLimit) {
			t.Fatalf("request %d allowed=%v available=%v", i, allowed, available)
		}
	}
	if allowed, available := s.Limiter(ctx, "writer-B"); !allowed || !available {
		t.Fatal("identity B must have independent limit")
	}
	ttl, err := client.PTTL(ctx, keys[0]).Result()
	if err != nil || ttl <= 0 || ttl > 65*time.Second {
		t.Fatalf("counter must expire: ttl=%v err=%v", ttl, err)
	}
	now = now.Add(time.Minute)
	if allowed, available := s.Limiter(ctx, "writer-A"); !allowed || !available {
		t.Fatal("new minute must reset limiter")
	}
}

func TestRedisCommitFailureRetriesSameEventWithoutDuplicateActivity(t *testing.T) {
	s, _ := redisService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	reader := &testReader{
		message:     kafka.Message{Value: eventBytes(t, exampleEvent("ev_commit_retry", "A"))},
		commitError: true, afterSuccess: cancel,
	}
	s.consume(ctx, reader)
	if reader.fetches.Load() != 1 || reader.commits.Load() != 2 {
		t.Fatalf("offset retry fetched later message: fetches=%d commits=%d", reader.fetches.Load(), reader.commits.Load())
	}
	items, available := s.Activity(context.Background(), "A")
	if !available || len(items) != 1 {
		t.Fatal("offset retry must not duplicate projection")
	}
}

func TestRedisFailureRecoveryDoesNotSkipKafkaMessage(t *testing.T) {
	s, client := redisService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, list := s.activityKeys("A")
	if err := client.Set(ctx, list, "temporary projection failure", 0).Err(); err != nil {
		t.Fatal(err)
	}
	reader := &testReader{message: kafka.Message{Value: eventBytes(t, exampleEvent("ev_recovered", "A"))}, afterSuccess: cancel}
	done := make(chan struct{})
	go func() { defer close(done); s.consume(ctx, reader) }()
	time.Sleep(100 * time.Millisecond)
	if reader.fetches.Load() != 1 || reader.commits.Load() != 0 {
		t.Error("failed Redis write must leave Kafka uncommitted")
	}
	if err := client.Del(ctx, list).Err(); err != nil {
		t.Error(err)
	}
	<-done
	if reader.fetches.Load() != 1 || reader.commits.Load() != 1 {
		t.Errorf("expected same message recovery, fetch=%d commit=%d", reader.fetches.Load(), reader.commits.Load())
	}
	items, available := s.Activity(context.Background(), "A")
	if !available || len(items) != 1 || items[0].EventID != "ev_recovered" {
		t.Fatal("recovery must retain original event")
	}
}

type countedKafkaReader struct {
	*kafka.Reader
	commits atomic.Int64
	done    chan struct{}
}

func (r *countedKafkaReader) CommitMessages(ctx context.Context, messages ...kafka.Message) error {
	if err := r.Reader.CommitMessages(ctx, messages...); err != nil {
		return err
	}
	if r.commits.Add(1) == 2 {
		close(r.done)
	}
	return nil
}

func TestKafkaRedisRealDuplicateDelivery(t *testing.T) {
	brokersEnv := os.Getenv("TEST_KAFKA_BROKERS")
	if brokersEnv == "" {
		t.Skip("real Kafka integration not run: set TEST_KAFKA_BROKERS and TEST_REDIS_ADDR explicitly")
	}
	s, _ := redisService(t)
	s.brokers = strings.Split(brokersEnv, ",")
	s.topic = "test.refundworkbench." + uniqueTestID(t)
	s.group = "test.refundworkbench." + uniqueTestID(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.ensureTopic(ctx); err != nil {
		t.Fatalf("create isolated Kafka topic: %v", err)
	}
	t.Cleanup(func() { deleteOwnTopic(t, s.brokers[0], s.topic) })
	reader := &countedKafkaReader{
		Reader: kafka.NewReader(kafka.ReaderConfig{
			Brokers: s.brokers, Topic: s.topic, GroupID: s.group, StartOffset: kafka.FirstOffset,
			MinBytes: 1, MaxBytes: 1e6, MaxWait: time.Second, CommitInterval: 0, ReadLagInterval: -1,
		}),
		done: make(chan struct{}),
	}
	defer reader.Close()
	writer := &kafka.Writer{Addr: kafka.TCP(s.brokers...), Topic: s.topic, RequiredAcks: kafka.RequireAll, BatchSize: 1}
	defer writer.Close()
	consumeDone := make(chan struct{})
	go func() { defer close(consumeDone); s.consume(ctx, reader) }()
	defer func() { cancel(); <-consumeDone }()
	payload := eventBytes(t, exampleEvent("ev_real_duplicate", "A"))
	for i := 0; i < 2; i++ {
		if err := writer.WriteMessages(ctx, kafka.Message{Key: []byte("A"), Value: payload}); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-ctx.Done():
		t.Fatal("real Kafka/Redis projection timed out")
	case <-reader.done:
	}
	items, available := s.Activity(ctx, "A")
	if !available || len(items) != 1 || items[0].EventID != "ev_real_duplicate" || reader.commits.Load() != 2 {
		t.Fatal("two acknowledged Kafka deliveries must project one activity")
	}
}

func deleteOwnTopic(t *testing.T, broker, topic string) {
	t.Helper()
	if !strings.HasPrefix(topic, "test.refundworkbench.") {
		t.Error("refusing cleanup of non-test topic")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := kafka.DialContext(ctx, "tcp", broker)
	if err != nil {
		t.Error(err)
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	controller, err := conn.Controller()
	if err != nil {
		t.Error(err)
		return
	}
	admin, err := kafka.DialContext(ctx, "tcp", net.JoinHostPort(controller.Host, strconv.Itoa(controller.Port)))
	if err != nil {
		t.Error(err)
		return
	}
	defer admin.Close()
	_ = admin.SetDeadline(time.Now().Add(5 * time.Second))
	if err := admin.DeleteTopics(topic); err != nil {
		t.Errorf("delete exact test topic %s: %v", topic, err)
	}
}
