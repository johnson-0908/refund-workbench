package events

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/segmentio/kafka-go"
)

func exampleEvent(id, merchant string) Event {
	return Event{EventID: id, MerchantID: merchant, RefundID: "rf_test", OrderID: merchant + "-1001",
		AmountCents: 19900, Status: "UNKNOWN", Message: "渠道结果待确认", OccurredAt: time.Date(2026, 10, 27, 1, 0, 0, 0, time.UTC)}
}

func eventBytes(t *testing.T, event Event) []byte {
	t.Helper()
	payload, err := marshalEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func quiet(s *Service) *Service {
	s.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	return s
}

func TestDecodeEventRejectsInvalidPayloads(t *testing.T) {
	valid := eventBytes(t, exampleEvent("ev_1", "A"))
	for _, bad := range [][]byte{
		[]byte(`{`), []byte(`null`), []byte(`{}`),
		[]byte(strings.Replace(string(valid), `"UNKNOWN"`, `"PENDING"`, 1)),
		[]byte(strings.Replace(string(valid), `19900`, `19.9`, 1)),
		[]byte(strings.Replace(string(valid), `19900`, `-1`, 1)),
		[]byte(strings.Replace(string(valid), `"ev_1"`, `""`, 1)),
	} {
		if _, err := decodeEvent(bad); err == nil {
			t.Errorf("accepted invalid payload %s", bad)
		}
	}
	if _, err := decodeEvent(valid); err != nil {
		t.Fatal(err)
	}
}

func TestRedisAbsentDegradesActivityAndFailsOpenLimiter(t *testing.T) {
	s := quiet(New(nil, nil, nil, "", ""))
	items, available := s.Activity(context.Background(), "A")
	if available || items == nil || len(items) != 0 {
		t.Fatalf("unexpected activity: %#v / available=%v", items, available)
	}
	allowed, available := s.Limiter(context.Background(), "demo-a-write")
	if !allowed || available {
		t.Fatalf("expected fail-open, got allowed=%v available=%v", allowed, available)
	}
}

func TestLimiterKeysDoNotStoreCredentialsAndRespectNamespaces(t *testing.T) {
	s := New(nil, nil, nil, "", "")
	s.SetPrefix("test:one:")
	now := time.Date(2026, 10, 27, 1, 0, 1, 0, time.UTC)
	key := s.limiterKey("secret-demo-token", now)
	if strings.Contains(key, "secret-demo-token") || !strings.HasPrefix(key, "test:one:") {
		t.Fatal("limiter key exposed identity or lost prefix")
	}
	if s.limiterKey("secret-demo-token", now.Add(time.Minute)) == key {
		t.Fatal("next minute must have separate counter")
	}
	s.SetPrefix("test:two:")
	if s.limiterKey("secret-demo-token", now) == key {
		t.Fatal("prefix must isolate counters")
	}
	seen, list := s.activityKeys("merchant:{other}")
	if strings.Contains(seen, "{other}") || strings.Contains(list, "{other}") {
		t.Fatal("merchant must not control Redis hash tag")
	}
}

// A tiny database/sql driver records SQL boundaries without a database/mock
// dependency, so acknowledgement-order tests run even when Docker is absent.
type sqlState struct {
	item       outboxRow
	marks      int
	markError  error
	operations []string
}

type fakeConnector struct{ state *sqlState }
type fakeDriver struct{ state *sqlState }
type fakeConn struct{ state *sqlState }
type fakeRows struct {
	item outboxRow
	done bool
}

func (c fakeConnector) Connect(context.Context) (driver.Conn, error) { return fakeConn{c.state}, nil }
func (c fakeConnector) Driver() driver.Driver                        { return fakeDriver{c.state} }
func (d fakeDriver) Open(string) (driver.Conn, error)                { return fakeConn{d.state}, nil }
func (c fakeConn) Prepare(string) (driver.Stmt, error)               { return nil, errors.New("unexpected prepare") }
func (c fakeConn) Close() error                                      { return nil }
func (c fakeConn) Begin() (driver.Tx, error)                         { return nil, errors.New("unexpected transaction") }
func (c fakeConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if !strings.Contains(query, "published_at IS NULL ORDER BY id ASC") {
		return nil, errors.New("unexpected query")
	}
	return &fakeRows{item: c.state.item}, nil
}
func (c fakeConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if !strings.HasPrefix(query, "UPDATE outbox_events SET published_at=") || len(args) != 1 || args[0].Value != c.state.item.id {
		return nil, errors.New("unexpected update")
	}
	c.state.marks++
	c.state.operations = append(c.state.operations, "mark")
	if c.state.markError != nil {
		return nil, c.state.markError
	}
	return driver.RowsAffected(1), nil
}
func (r *fakeRows) Columns() []string {
	return []string{"id", "event_id", "merchant_id", "refund_id", "payload"}
}
func (r *fakeRows) Close() error { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	dest[0], dest[1], dest[2], dest[3], dest[4] = r.item.id, r.item.eventID, r.item.merchantID, r.item.refundID, r.item.payload
	return nil
}

type writerFunc func(context.Context, ...kafka.Message) error

func (fn writerFunc) WriteMessages(ctx context.Context, msgs ...kafka.Message) error {
	return fn(ctx, msgs...)
}

func outboxService(t *testing.T) (*Service, *sqlState) {
	t.Helper()
	event := exampleEvent("ev_outbox", "A")
	state := &sqlState{item: outboxRow{id: 42, eventID: event.EventID, merchantID: event.MerchantID, refundID: event.RefundID, payload: eventBytes(t, event)}}
	db := sql.OpenDB(fakeConnector{state})
	t.Cleanup(func() { _ = db.Close() })
	return quiet(New(db, nil, []string{"127.0.0.1:19092"}, "", "")), state
}

func TestOutboxMarksOnlyAfterKafkaAcknowledgement(t *testing.T) {
	s, state := outboxService(t)
	writer := writerFunc(func(_ context.Context, msgs ...kafka.Message) error {
		if state.marks != 0 {
			t.Fatal("outbox marked before ack")
		}
		if len(msgs) != 1 || string(msgs[0].Key) != "A" || string(msgs[0].Value) != string(state.item.payload) {
			t.Fatal("lost merchant partition key or payload")
		}
		state.operations = append(state.operations, "ack")
		return nil
	})
	if err := s.publishBatch(context.Background(), writer); err != nil {
		t.Fatal(err)
	}
	if strings.Join(state.operations, ",") != "ack,mark" {
		t.Fatalf("wrong ordering: %v", state.operations)
	}
}

func TestKafkaFailureKeepsOutboxUnpublished(t *testing.T) {
	s, state := outboxService(t)
	err := s.publishBatch(context.Background(), writerFunc(func(context.Context, ...kafka.Message) error {
		return errors.New("broker unavailable")
	}))
	if err == nil || state.marks != 0 {
		t.Fatalf("broker failure must leave pending row: %v marks=%d", err, state.marks)
	}
}

func TestMarkFailureRedeliversSameEventID(t *testing.T) {
	s, state := outboxService(t)
	state.markError = errors.New("SQL unavailable after Kafka ack")
	var delivered []string
	writer := writerFunc(func(_ context.Context, msgs ...kafka.Message) error {
		event, err := decodeEvent(msgs[0].Value)
		if err != nil {
			t.Fatal(err)
		}
		delivered = append(delivered, event.EventID)
		return nil
	})
	if err := s.publishBatch(context.Background(), writer); err == nil {
		t.Fatal("expected mark failure")
	}
	state.markError = nil
	if err := s.publishBatch(context.Background(), writer); err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 2 || delivered[0] != delivered[1] || delivered[0] != state.item.eventID {
		t.Fatalf("must retry same event identity: %v", delivered)
	}
}

func TestOutboxIdentityMismatchIsNotPublished(t *testing.T) {
	s, state := outboxService(t)
	state.item.merchantID = "B"
	called := false
	err := s.publishBatch(context.Background(), writerFunc(func(context.Context, ...kafka.Message) error {
		called = true
		return nil
	}))
	if err == nil || called || state.marks != 0 {
		t.Fatal("mismatched outbox must not be published or marked")
	}
}

type testReader struct {
	message      kafka.Message
	fetches      atomic.Int64
	commits      atomic.Int64
	commitError  bool
	afterSuccess context.CancelFunc
}

func (r *testReader) FetchMessage(ctx context.Context) (kafka.Message, error) {
	if r.fetches.Add(1) == 1 {
		return r.message, nil
	}
	<-ctx.Done()
	return kafka.Message{}, ctx.Err()
}
func (r *testReader) CommitMessages(_ context.Context, _ ...kafka.Message) error {
	count := r.commits.Add(1)
	if r.commitError && count == 1 {
		return errors.New("temporary Kafka offset commit failure")
	}
	if r.afterSuccess != nil {
		r.afterSuccess()
	}
	return nil
}

func TestRedisFailureDoesNotCommitOrFetchNextKafkaMessage(t *testing.T) {
	s := quiet(New(nil, nil, nil, "", ""))
	reader := &testReader{message: kafka.Message{Value: eventBytes(t, exampleEvent("ev_pending", "A"))}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	s.consume(ctx, reader)
	if reader.fetches.Load() != 1 || reader.commits.Load() != 0 {
		t.Fatalf("must hold current message: fetches=%d commits=%d", reader.fetches.Load(), reader.commits.Load())
	}
}
