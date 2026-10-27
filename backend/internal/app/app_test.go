package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

var writerA = Identity{MerchantID: "A", CanWrite: true}
var writerB = Identity{MerchantID: "B", CanWrite: true}

func legacyHandler(service *Service) http.Handler {
	return NewHandlerWithOptions(service, HandlerOptions{EnableDemoBearer: true})
}

func TestAuthenticate(t *testing.T) {
	for _, tc := range []struct {
		token, merchant string
		write           bool
		valid           bool
	}{
		{"demo-a-write", "A", true, true}, {"demo-a-read", "A", false, true}, {"demo-b-write", "B", true, true},
		{"", "", false, false}, {"forged", "", false, false}, {"demo-b-read", "", false, false},
	} {
		t.Run(tc.token, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/orders", nil)
			r.Header.Set("Authorization", "Bearer "+tc.token)
			id, err := Authenticate(r)
			if err == nil {
				t.Fatal("bare header bypassed authentication middleware")
			}
			p, err := newAuthService(nil, true).authenticate(r.Context(), r)
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
				r = r.WithContext(context.WithValue(r.Context(), principalKey{}, p))
				id, err = Authenticate(r)
				if err != nil || id.MerchantID != tc.merchant || id.CanWrite != tc.write {
					t.Fatalf("identity=%+v err=%v", id, err)
				}
			} else if err == nil {
				t.Fatal("invalid token accepted")
			}
		})
	}
}

func TestValidateCreate(t *testing.T) {
	for _, tc := range []struct {
		name, key, reason string
		valid             bool
	}{
		{"normal", "key:1", "取消", true}, {"preserved spaces", "k", "  取消  ", true},
		{"200 unicode", "k", strings.Repeat("😀", 200), true}, {"201 unicode", "k", strings.Repeat("😀", 201), false},
		{"empty key", "", "理由", false}, {"long key", strings.Repeat("x", 129), "理由", false}, {"key limit", strings.Repeat("x", 128), "理由", true},
		{"unicode key", "中文", "理由", false}, {"space key", "bad key", "理由", false}, {"empty reason", "k", "", false},
		{"unicode whitespace", "k", " \n\t\u3000", false}, {"invalid utf8", "k", string([]byte{0xff}), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCreate(tc.key, tc.reason)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

func TestHTTPInvalidIdentityWithoutDatabase(t *testing.T) {
	handler := legacyHandler(NewService(nil, nil))
	for _, token := range []string{"", "invalid"} {
		code, data := request(t, handler, http.MethodPost, "/api/orders/A-1001/refunds", token, "k", `{"reason":"x"}`)
		if code != 401 {
			t.Fatalf("invalid identity: %d %s", code, data)
		}
	}
}

func TestMySQLHTTPInvalidInputs(t *testing.T) {
	_, service, _ := fixture(t)
	handler := legacyHandler(service)
	for _, tc := range []struct {
		name, token, body, key string
		status                 int
	}{
		{"missing identity", "", `{"reason":"x"}`, "k", 401},
		{"forged identity", "invalid", `{"reason":"x"}`, "k", 401},
		{"reader", "demo-a-read", `{"reason":"x"}`, "k", 403},
		{"reader invalid body", "demo-a-read", `{"invalid":true}`, "k", 403},
		{"amount field", "demo-a-write", `{"reason":"x","amount_cents":1}`, "k", 400},
		{"merchant field", "demo-a-write", `{"reason":"x","merchant_id":"B"}`, "k", 400},
		{"unknown field", "demo-a-write", `{"reason":"x","other":true}`, "k", 400},
		{"trailing object", "demo-a-write", `{"reason":"x"}{}`, "k", 400},
		{"non string", "demo-a-write", `{"reason":42}`, "k", 400},
		{"array", "demo-a-write", `[]`, "k", 400},
		{"null", "demo-a-write", `null`, "k", 400},
		{"empty reason", "demo-a-write", `{}`, "k", 400},
		{"missing key", "demo-a-write", `{"reason":"x"}`, "", 400},
		{"bad utf8", "demo-a-write", string([]byte{0xff}), "k", 400},
		{"oversized body", "demo-a-write", `{"reason":"` + strings.Repeat("x", 5000) + `"}`, "k", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, data := request(t, handler, http.MethodPost, "/api/orders/A-1001/refunds", tc.token, tc.key, tc.body)
			if code != tc.status {
				t.Fatalf("status=%d body=%s", code, data)
			}
			var result struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(data, &result); err != nil || result.Error.Code == "" {
				t.Fatalf("error envelope=%s err=%v", data, err)
			}
		})
	}
}

// These tests never silently substitute an in-memory database. They skip only
// when TEST_MYSQL_DSN is absent, and fail closed for nonlocal/non-test schemas.
func fixture(t *testing.T) (*sql.DB, *Service, string) {
	t.Helper()
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("integration test requires isolated local TEST_MYSQL_DSN (database refund_test)")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	host, _, hostErr := net.SplitHostPort(cfg.Addr)
	if cfg.DBName != "refund_test" || cfg.Net != "tcp" || hostErr != nil || (host != "127.0.0.1" && host != "localhost" && host != "::1") {
		t.Fatal("REFUSING test cleanup outside localhost/refund_test")
	}
	db, err := OpenDB(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if err = Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	// Exact, ordered allowlist; no DROP DATABASE, TRUNCATE or unrelated objects.
	for _, table := range []string{"maintenance_resets", "merchant_reset_watermarks", "auth_sessions", "outbox_events", "idempotency_keys", "refunds", "channel_requests", "channel_scenarios", "orders", "merchants"} {
		if _, err = db.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			t.Fatal(err)
		}
	}
	if err = Seed(ctx, db); err != nil {
		t.Fatal(err)
	}
	return db, NewService(db, NewPersistentGateway(db)), dsn
}

func request(t *testing.T, h http.Handler, method, path, token, key, body string) (int, []byte) {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code, w.Body.Bytes()
}

func decodeRefund(t *testing.T, data []byte) Refund {
	t.Helper()
	var r Refund
	if err := json.Unmarshal(data, &r); err != nil || r.RefundID == "" {
		t.Fatalf("not refund: %s err=%v", data, err)
	}
	return r
}

func errorCode(t *testing.T, err error, want string) {
	t.Helper()
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != want {
		t.Fatalf("want %s, got %v", want, err)
	}
}

func count(t *testing.T, db *sql.DB, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMySQLNormalIdempotencyAndExactParameters(t *testing.T) {
	db, service, _ := fixture(t)
	ctx := context.Background()
	reason := "  用户取消服务\n"
	r, created, err := service.CreateRefund(ctx, writerA, "A-1001", "idempotent-1", reason)
	if err != nil || !created || r.Status != "SUCCEEDED" || r.AmountCents != 19900 || r.Reason != reason {
		t.Fatalf("refund=%+v created=%v err=%v", r, created, err)
	}
	replay, created, err := service.CreateRefund(ctx, writerA, "A-1001", "idempotent-1", reason)
	if err != nil || created || replay.RefundID != r.RefundID || replay.ProviderRequestID != r.ProviderRequestID || replay.Status != "SUCCEEDED" {
		t.Fatalf("replay=%+v created=%v err=%v", replay, created, err)
	}
	_, _, err = service.CreateRefund(ctx, writerA, "A-1001", "idempotent-1", strings.TrimSpace(reason))
	errorCode(t, err, "IDEMPOTENCY_CONFLICT")
	_, _, err = service.CreateRefund(ctx, writerA, "A-1002", "idempotent-1", reason)
	errorCode(t, err, "IDEMPOTENCY_CONFLICT")
	_, _, err = service.CreateRefund(ctx, writerA, "A-1001", "another-key", reason)
	errorCode(t, err, "REFUND_EXISTS")
	b, created, err := service.CreateRefund(ctx, writerB, "B-2001", "idempotent-1", reason)
	if err != nil || !created || b.RefundID == r.RefundID || b.Status != "SUCCEEDED" {
		t.Fatalf("merchant scoped key: %+v %v", b, err)
	}
	if n := count(t, db, "SELECT submit_calls FROM channel_requests WHERE provider_request_id=?", r.ProviderRequestID); n != 1 {
		t.Fatalf("submit_calls=%d", n)
	}
	if n := count(t, db, "SELECT COUNT(*) FROM refunds WHERE order_id='A-1001'"); n != 1 {
		t.Fatalf("refund rows=%d", n)
	}
	if n := count(t, db, "SELECT COUNT(*) FROM outbox_events WHERE refund_id=?", r.RefundID); n != 2 {
		t.Fatalf("state events=%d", n)
	}
	o, err := service.GetOrder(ctx, writerA, "A-1001")
	if err != nil || o.Refund == nil || o.Refund.RefundID != r.RefundID {
		t.Fatalf("order detail=%+v err=%v", o, err)
	}
}

func TestMySQLFailureAndUnpaidAreNotRetried(t *testing.T) {
	db, s, _ := fixture(t)
	r, created, err := s.CreateRefund(context.Background(), writerA, "A-1003", "reject-1", "退款")
	if err != nil || !created || r.Status != "FAILED" {
		t.Fatalf("refund=%+v err=%v", r, err)
	}
	_, _, err = s.CreateRefund(context.Background(), writerA, "A-1003", "reject-2", "退款")
	errorCode(t, err, "REFUND_EXISTS")
	queried, err := s.Reconcile(context.Background(), writerA, r.RefundID)
	if err != nil || queried.Status != "FAILED" {
		t.Fatalf("reconcile=%+v %v", queried, err)
	}
	if n := count(t, db, "SELECT query_calls FROM channel_requests WHERE provider_request_id=?", r.ProviderRequestID); n != 0 {
		t.Fatalf("terminal queried: %d", n)
	}
	if n := count(t, db, "SELECT effect_count FROM channel_requests WHERE provider_request_id=?", r.ProviderRequestID); n != 0 {
		t.Fatalf("rejection monetary effects=%d", n)
	}
	_, _, err = s.CreateRefund(context.Background(), writerA, "A-1004", "unpaid-1", "退款")
	errorCode(t, err, "ORDER_NOT_PAID")
	if n := count(t, db, "SELECT COUNT(*) FROM channel_requests WHERE order_id='A-1004'"); n != 0 {
		t.Fatalf("unpaid submitted=%d", n)
	}
}

func TestMySQLUnknownSurvivesReopenAndSeed(t *testing.T) {
	db, s, dsn := fixture(t)
	ctx := context.Background()
	r, created, err := s.CreateRefund(ctx, writerA, "A-1002", "lost-1", "取消服务")
	if err != nil || !created || r.Status != "UNKNOWN" {
		t.Fatalf("initial=%+v err=%v", r, err)
	}
	if n := count(t, db, "SELECT effect_count FROM channel_requests WHERE provider_request_id=?", r.ProviderRequestID); n != 1 {
		t.Fatalf("channel accepted effects=%d", n)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenDB(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err = Migrate(ctx, reopened); err != nil {
		t.Fatal(err)
	}
	if err = Seed(ctx, reopened); err != nil {
		t.Fatal(err)
	}
	if err = Seed(ctx, reopened); err != nil {
		t.Fatal(err)
	}
	s = NewService(reopened, NewPersistentGateway(reopened))
	persisted, err := s.GetRefund(ctx, writerA, r.RefundID)
	if err != nil || persisted.Status != "UNKNOWN" || persisted.ProviderRequestID != r.ProviderRequestID {
		t.Fatalf("persisted=%+v err=%v", persisted, err)
	}
	resolved, err := s.Reconcile(ctx, writerA, r.RefundID)
	if err != nil || resolved.Status != "SUCCEEDED" || resolved.RefundID != r.RefundID || resolved.ProviderRequestID != r.ProviderRequestID {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}
	replay, created, err := s.CreateRefund(ctx, writerA, "A-1002", "lost-1", "取消服务")
	if err != nil || created || replay.Status != "SUCCEEDED" {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	if n := count(t, reopened, "SELECT submit_calls FROM channel_requests WHERE provider_request_id=?", r.ProviderRequestID); n != 1 {
		t.Fatalf("reconcile resubmitted: %d", n)
	}
	if n := count(t, reopened, "SELECT query_calls FROM channel_requests WHERE provider_request_id=?", r.ProviderRequestID); n != 1 {
		t.Fatalf("query_calls=%d", n)
	}
	if n := count(t, reopened, "SELECT COUNT(*) FROM refunds"); n != 1 {
		t.Fatalf("seed changed refunds count=%d", n)
	}
	if n := count(t, reopened, "SELECT COUNT(*) FROM idempotency_keys"); n != 1 {
		t.Fatalf("seed changed keys count=%d", n)
	}
	if n := count(t, reopened, "SELECT COUNT(*) FROM outbox_events"); n != 3 {
		t.Fatalf("outbox count=%d", n)
	}
	rows, err := reopened.Query("SELECT payload FROM outbox_events ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := []string{"PROCESSING", "UNKNOWN", "SUCCEEDED"}
	i := 0
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			t.Fatal(err)
		}
		var event Activity
		if err := json.Unmarshal(data, &event); err != nil {
			t.Fatal(err)
		}
		if i >= len(want) || event.Status != want[i] || event.RefundID != r.RefundID || event.MerchantID != "A" {
			t.Fatalf("event=%+v", event)
		}
		i++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestMySQLHTTPAuthorizationAndIsolation(t *testing.T) {
	db, s, _ := fixture(t)
	h := legacyHandler(s)
	code, data := request(t, h, "POST", "/api/orders/A-1001/refunds", "demo-a-write", "auth-1", `{"reason":"取消"}`)
	if code != 201 {
		t.Fatalf("create=%d %s", code, data)
	}
	r := decodeRefund(t, data)
	for _, tc := range []struct {
		method, path, token, key, body string
		status                         int
	}{
		{"GET", "/api/orders", "", "", "", 401},
		{"GET", "/api/orders/A-1001", "demo-b-write", "", "", 404},
		{"POST", "/api/orders/A-1001/refunds", "demo-b-write", "x", `{"reason":"取消"}`, 404},
		{"GET", "/api/refunds/" + r.RefundID, "demo-b-write", "", "", 404},
		{"POST", "/api/refunds/" + r.RefundID + "/reconcile", "demo-b-write", "", "", 404},
		{"POST", "/api/refunds/" + r.RefundID + "/reconcile", "demo-a-read", "", "", 403},
		{"GET", "/api/refunds/" + r.RefundID, "demo-a-read", "", "", 200},
		{"GET", "/api/orders/A-1001", "demo-a-read", "", "", 200},
		{"POST", "/api/orders/A-1002/refunds", "demo-a-read", "x", `{"reason":"取消"}`, 403},
		{"GET", "/api/refunds/nonexistent", "demo-a-write", "", "", 404},
		{"POST", "/api/orders/A-1002/refunds", "demo-a-write", "x", `{"reason":"取消","amount_cents":1}`, 400},
		{"POST", "/api/orders/A-1002/refunds", "demo-a-write", "x", `{"reason":"取消","merchant_id":"B"}`, 400},
	} {
		t.Run(tc.method+tc.path+tc.token, func(t *testing.T) {
			code, data := request(t, h, tc.method, tc.path, tc.token, tc.key, tc.body)
			if code != tc.status {
				t.Fatalf("got %d %s", code, data)
			}
		})
	}
	for _, tc := range []struct {
		token, merchant string
		n               int
	}{{"demo-a-read", "A", 4}, {"demo-b-write", "B", 1}} {
		code, data := request(t, h, "GET", "/api/orders", tc.token, "", "")
		var payload struct {
			Items []Order `json:"items"`
		}
		if err := json.Unmarshal(data, &payload); err != nil || code != 200 || len(payload.Items) != tc.n {
			t.Fatalf("orders=%d %s", code, data)
		}
		for _, order := range payload.Items {
			if order.MerchantID != tc.merchant {
				t.Fatalf("cross merchant order: %+v", order)
			}
		}
	}
	if n := count(t, db, "SELECT COUNT(*) FROM refunds"); n != 1 {
		t.Fatalf("unauthorized write count=%d", n)
	}
}

func TestMySQLReadOnlyCrossMerchantWritesReturn404(t *testing.T) {
	db, s, _ := fixture(t)
	ctx := context.Background()
	b, _, err := s.CreateRefund(ctx, writerB, "B-2001", "b-refund", "退款")
	if err != nil {
		t.Fatal(err)
	}
	a, _, err := s.CreateRefund(ctx, writerA, "A-1001", "a-refund", "退款")
	if err != nil {
		t.Fatal(err)
	}
	reader := Identity{MerchantID: "A", CanWrite: false}
	_, _, err = s.CreateRefund(ctx, reader, "B-2001", "", " ")
	errorCode(t, err, "NOT_FOUND")
	_, err = s.Reconcile(ctx, reader, b.RefundID)
	errorCode(t, err, "NOT_FOUND")
	_, _, err = s.CreateRefund(ctx, reader, "A-1002", "", " ")
	errorCode(t, err, "FORBIDDEN")
	_, err = s.Reconcile(ctx, reader, a.RefundID)
	errorCode(t, err, "FORBIDDEN")
	h := legacyHandler(s)
	for _, tc := range []struct {
		path, body string
		status     int
	}{
		{"/api/orders/B-2001/refunds", `{"reason":"退款"}`, 404},
		{"/api/orders/B-2001/refunds", `{"invalid":true}`, 404},
		{"/api/refunds/" + b.RefundID + "/reconcile", "", 404},
		{"/api/orders/A-1002/refunds", `{"invalid":true}`, 403},
		{"/api/refunds/" + a.RefundID + "/reconcile", "", 403},
		{"/api/orders/MISSING/refunds", `{}`, 404},
		{"/api/refunds/MISSING/reconcile", "", 404},
	} {
		code, data := request(t, h, "POST", tc.path, "demo-a-read", "k", tc.body)
		if code != tc.status {
			t.Fatalf("%s expected %d got %d %s", tc.path, tc.status, code, data)
		}
	}
	if n := count(t, db, "SELECT COUNT(*) FROM refunds"); n != 2 {
		t.Fatalf("read-only writes changed rows: %d", n)
	}
}

func TestMySQLHTTPConcurrentSameKey(t *testing.T)       { testConcurrent(t, true) }
func TestMySQLHTTPConcurrentDifferentKeys(t *testing.T) { testConcurrent(t, false) }

func testConcurrent(t *testing.T, sameKey bool) {
	db, s, _ := fixture(t)
	server := httptest.NewServer(legacyHandler(s))
	defer server.Close()
	const workers = 16
	type response struct {
		code int
		body []byte
		err  error
	}
	results := make(chan response, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			key := "concurrent"
			if !sameKey {
				key = fmt.Sprintf("concurrent-%d", index)
			}
			req, err := http.NewRequest("POST", server.URL+"/api/orders/A-1001/refunds", bytes.NewBufferString(`{"reason":"并发取消"}`))
			if err != nil {
				results <- response{err: err}
				return
			}
			req.Header.Set("Authorization", "Bearer demo-a-write")
			req.Header.Set("Idempotency-Key", key)
			res, err := server.Client().Do(req)
			if err != nil {
				results <- response{err: err}
				return
			}
			defer res.Body.Close()
			data, err := io.ReadAll(res.Body)
			results <- response{res.StatusCode, data, err}
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	created, success, conflicts := 0, 0, 0
	refundID, providerID := "", ""
	for res := range results {
		if res.err != nil {
			t.Fatal(res.err)
		}
		switch res.code {
		case 201, 200:
			if res.code == 201 {
				created++
			}
			success++
			r := decodeRefund(t, res.body)
			if refundID == "" {
				refundID, providerID = r.RefundID, r.ProviderRequestID
			} else if refundID != r.RefundID || providerID != r.ProviderRequestID {
				t.Fatalf("multiple refund identities: %s", res.body)
			}
		case 409:
			conflicts++
			if !bytes.Contains(res.body, []byte("REFUND_EXISTS")) {
				t.Fatalf("wrong conflict: %s", res.body)
			}
		default:
			t.Fatalf("unexpected %d %s", res.code, res.body)
		}
	}
	if created != 1 || (sameKey && (success != workers || conflicts != 0)) || (!sameKey && (success != 1 || conflicts != workers-1)) {
		t.Fatalf("created=%d success=%d conflicts=%d", created, success, conflicts)
	}
	if n := count(t, db, "SELECT COUNT(*) FROM refunds"); n != 1 {
		t.Fatalf("refunds=%d", n)
	}
	if n := count(t, db, "SELECT submit_calls FROM channel_requests WHERE provider_request_id=?", providerID); n != 1 {
		t.Fatalf("submit calls=%d", n)
	}
	if n := count(t, db, "SELECT effect_count FROM channel_requests WHERE provider_request_id=?", providerID); n != 1 {
		t.Fatalf("effects=%d", n)
	}
}

type testGateway struct {
	submit  func(context.Context, ChannelRequest) (ChannelResult, error)
	query   func(context.Context, string) (ChannelResult, error)
	submits atomic.Int64
	queries atomic.Int64
}

func (g *testGateway) SubmitRefund(ctx context.Context, r ChannelRequest) (ChannelResult, error) {
	g.submits.Add(1)
	if g.submit != nil {
		return g.submit(ctx, r)
	}
	return ChannelResult{}, ErrChannelUncertain
}
func (g *testGateway) QueryRefund(ctx context.Context, id string) (ChannelResult, error) {
	g.queries.Add(1)
	if g.query != nil {
		return g.query(ctx, id)
	}
	return ChannelResult{}, ErrChannelNotFound
}

func TestMySQLQueryFailuresAndMalformedResultsStayUnknown(t *testing.T) {
	for _, kind := range []string{"not_found", "network_error", "parse_error", "wrong_request", "wrong_order", "wrong_amount", "invalid_status"} {
		t.Run(kind, func(t *testing.T) {
			db, _, _ := fixture(t)
			g := &testGateway{}
			s := NewService(db, g)
			r, _, err := s.CreateRefund(context.Background(), writerA, "A-1001", "unknown-1", "取消")
			if err != nil || r.Status != "UNKNOWN" {
				t.Fatalf("refund=%+v err=%v", r, err)
			}
			g.query = func(_ context.Context, id string) (ChannelResult, error) {
				result := ChannelResult{id, r.OrderID, r.AmountCents, "SUCCEEDED", "ok"}
				switch kind {
				case "not_found":
					return ChannelResult{}, ErrChannelNotFound
				case "network_error":
					return ChannelResult{}, io.ErrUnexpectedEOF
				case "parse_error":
					return ChannelResult{}, &json.SyntaxError{}
				case "wrong_request":
					result.ProviderRequestID = "other"
				case "wrong_order":
					result.OrderID = "other"
				case "wrong_amount":
					result.AmountCents++
				case "invalid_status":
					result.Status = "PENDING"
				}
				return result, nil
			}
			for range 2 {
				result, err := s.Reconcile(context.Background(), writerA, r.RefundID)
				if err != nil || result.Status != "UNKNOWN" || result.RefundID != r.RefundID {
					t.Fatalf("result=%+v err=%v", result, err)
				}
			}
			if g.submits.Load() != 1 || g.queries.Load() != 2 {
				t.Fatalf("submits=%d queries=%d", g.submits.Load(), g.queries.Load())
			}
			if n := count(t, db, "SELECT COUNT(*) FROM refunds"); n != 1 {
				t.Fatalf("refunds=%d", n)
			}
			if n := count(t, db, "SELECT COUNT(*) FROM outbox_events"); n != 2 {
				t.Fatalf("duplicate UNKNOWN events=%d", n)
			}
		})
	}
}

func TestMySQLTerminalCannotRegress(t *testing.T) {
	for _, orderID := range []string{"A-1001", "A-1003"} {
		t.Run(orderID, func(t *testing.T) {
			db, s, _ := fixture(t)
			r, _, err := s.CreateRefund(context.Background(), writerA, orderID, "terminal-1", "取消")
			if err != nil {
				t.Fatal(err)
			}
			original := *r
			for _, status := range []string{"UNKNOWN", "SUCCEEDED", "FAILED", "PROCESSING"} {
				result, err := s.applyResult(context.Background(), r, ChannelResult{r.ProviderRequestID, r.OrderID, r.AmountCents, status, "late response"}, nil)
				if err != nil || result.Status != original.Status || !result.UpdatedAt.Equal(original.UpdatedAt) {
					t.Fatalf("terminal regression: %+v err=%v", result, err)
				}
			}
			if n := count(t, db, "SELECT COUNT(*) FROM outbox_events"); n != 2 {
				t.Fatalf("terminal emitted events=%d", n)
			}
		})
	}
}

func TestMySQLReconcileSerializesWithInFlightSubmit(t *testing.T) {
	db, _, _ := fixture(t)
	persistent := NewPersistentGateway(db)
	accepted := make(chan struct{})
	release := make(chan struct{})
	g := &testGateway{query: persistent.QueryRefund}
	g.submit = func(ctx context.Context, r ChannelRequest) (ChannelResult, error) {
		if _, err := persistent.SubmitRefund(ctx, r); err != nil {
			return ChannelResult{}, err
		}
		close(accepted)
		select {
		case <-release:
			return ChannelResult{}, ErrChannelUncertain
		case <-ctx.Done():
			return ChannelResult{}, ctx.Err()
		}
	}
	s := NewService(db, g)
	type outcome struct {
		refund *Refund
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		r, _, err := s.CreateRefund(context.Background(), writerA, "A-1001", "race-1", "取消")
		done <- outcome{r, err}
	}()
	select {
	case <-accepted:
	case <-time.After(10 * time.Second):
		t.Fatal("channel did not accept")
	}
	o, err := s.GetOrder(context.Background(), writerA, "A-1001")
	if err != nil || o.Refund == nil || o.Refund.Status != "PROCESSING" {
		close(release)
		t.Fatalf("in flight order=%+v err=%v", o, err)
	}
	queryDone := make(chan outcome, 1)
	go func() {
		r, err := s.Reconcile(context.Background(), writerA, o.Refund.RefundID)
		queryDone <- outcome{r, err}
	}()
	select {
	case <-queryDone:
		close(release)
		t.Fatal("query overlapped in-flight submit lease")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	queried := <-queryDone
	resolved, err := queried.refund, queried.err
	if err != nil || resolved.Status != "SUCCEEDED" {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}
	result := <-done
	if result.err != nil || result.refund.Status != "UNKNOWN" {
		t.Fatalf("lost submit response was not persisted before query=%+v err=%v", result.refund, result.err)
	}
	if g.submits.Load() != 1 || g.queries.Load() != 1 {
		t.Fatalf("submits=%d queries=%d", g.submits.Load(), g.queries.Load())
	}
}

func TestMySQLChannelIdempotencyAndDurableLostResponse(t *testing.T) {
	db, _, _ := fixture(t)
	g := NewPersistentGateway(db)
	ctx := context.Background()
	req := ChannelRequest{"pr_direct_1", "A-1001", 19900}
	first, err := g.SubmitRefund(ctx, req)
	if err != nil || first.Status != "SUCCEEDED" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	replay, err := g.SubmitRefund(ctx, req)
	if err != nil || replay != first {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	wrong := req
	wrong.AmountCents++
	if _, err := g.SubmitRefund(ctx, wrong); !errors.Is(err, ErrChannelConflict) {
		t.Fatalf("different amount accepted: %v", err)
	}
	wrong = req
	wrong.OrderID = "A-1002"
	if _, err := g.SubmitRefund(ctx, wrong); !errors.Is(err, ErrChannelConflict) {
		t.Fatalf("different order accepted: %v", err)
	}
	if n := count(t, db, "SELECT effect_count FROM channel_requests WHERE provider_request_id=?", req.ProviderRequestID); n != 1 {
		t.Fatalf("effect count=%d", n)
	}
	if n := count(t, db, "SELECT submit_calls FROM channel_requests WHERE provider_request_id=?", req.ProviderRequestID); n != 4 {
		t.Fatalf("submit calls=%d", n)
	}
	wrong = req
	wrong.OrderID = "NONEXISTENT-ORDER"
	if _, err := g.SubmitRefund(ctx, wrong); !errors.Is(err, ErrChannelConflict) {
		t.Fatalf("unknown different order accepted: %v", err)
	}
	if _, err := db.Exec("DELETE FROM channel_scenarios WHERE order_id='A-1001'"); err != nil {
		t.Fatal(err)
	}
	replay, err = g.SubmitRefund(ctx, req)
	if err != nil || replay != first {
		t.Fatalf("existing receipt depends on mutable scenario: %+v %v", replay, err)
	}
	if _, err := g.QueryRefund(ctx, "pr_missing"); !errors.Is(err, ErrChannelNotFound) {
		t.Fatalf("missing lookup=%v", err)
	}
	lost := ChannelRequest{"pr_direct_lost", "A-1002", 8800}
	if _, err := g.SubmitRefund(ctx, lost); !errors.Is(err, ErrChannelUncertain) {
		t.Fatalf("lost response=%v", err)
	}
	query, err := g.QueryRefund(ctx, lost.ProviderRequestID)
	if err != nil || query.Status != "SUCCEEDED" {
		t.Fatalf("durable receipt=%+v err=%v", query, err)
	}
	replay, err = g.SubmitRefund(ctx, lost)
	if err != nil || replay != query {
		t.Fatalf("lost-response replay=%+v err=%v", replay, err)
	}
	if n := count(t, db, "SELECT effect_count FROM channel_requests WHERE provider_request_id=?", lost.ProviderRequestID); n != 1 {
		t.Fatalf("lost effects=%d", n)
	}
}

func TestMySQLChannelConcurrentSameRequest(t *testing.T) {
	db, _, _ := fixture(t)
	g := NewPersistentGateway(db)
	const workers = 16
	errorsCh := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			r, err := g.SubmitRefund(context.Background(), ChannelRequest{"pr_concurrent", "A-1001", 19900})
			if err == nil && r.Status != "SUCCEEDED" {
				err = fmt.Errorf("unexpected status %s", r.Status)
			}
			errorsCh <- err
		})
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := count(t, db, "SELECT COUNT(*) FROM channel_requests"); n != 1 {
		t.Fatalf("channel receipts=%d", n)
	}
	if n := count(t, db, "SELECT effect_count FROM channel_requests WHERE provider_request_id='pr_concurrent'"); n != 1 {
		t.Fatalf("monetary effects=%d", n)
	}
	if n := count(t, db, "SELECT submit_calls FROM channel_requests WHERE provider_request_id='pr_concurrent'"); n != workers {
		t.Fatalf("calls=%d", n)
	}
}

func TestMySQLSeedPreservesExistingBusinessAndScenarioValues(t *testing.T) {
	db, s, _ := fixture(t)
	ctx := context.Background()
	if _, err := db.Exec("UPDATE orders SET amount_cents=20000 WHERE id='A-1001'"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE channel_scenarios SET mode='REJECT' WHERE order_id='A-1001'"); err != nil {
		t.Fatal(err)
	}
	r, _, err := s.CreateRefund(ctx, writerA, "A-1001", "preserve-1", "自定义原因")
	if err != nil || r.Status != "FAILED" || r.AmountCents != 20000 {
		t.Fatalf("refund=%+v err=%v", r, err)
	}
	for range 3 {
		if err := Migrate(ctx, db); err != nil {
			t.Fatal(err)
		}
		if err := Seed(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	if n := count(t, db, "SELECT amount_cents FROM orders WHERE id='A-1001'"); n != 20000 {
		t.Fatalf("seed overwrote amount: %d", n)
	}
	var mode string
	if err := db.QueryRow("SELECT mode FROM channel_scenarios WHERE order_id='A-1001'").Scan(&mode); err != nil || mode != "REJECT" {
		t.Fatalf("seed overwrote mode=%s err=%v", mode, err)
	}
	got, err := s.GetRefund(ctx, writerA, r.RefundID)
	if err != nil || got.Reason != r.Reason || got.Status != "FAILED" {
		t.Fatalf("seed changed refund=%+v err=%v", got, err)
	}
	if n := count(t, db, "SELECT COUNT(*) FROM channel_requests"); n != 1 {
		t.Fatalf("seed changed receipt count=%d", n)
	}
}
