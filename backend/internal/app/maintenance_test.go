package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestResetDSNProvenanceIsLocalAndAllowlisted(t *testing.T) {
	for _, tc := range []struct {
		dsn     string
		allowed bool
	}{
		{DefaultDSN, true}, {"demo:x@tcp(localhost:3307)/refund_test", true}, {"demo:x@tcp([::1]:3307)/refund_acceptance", true},
		{"demo:x@tcp(127.0.0.1:3307)/business", false}, {"demo:x@tcp(10.0.0.1:3307)/refund_demo", false}, {"demo:x@unix(/tmp/mysql.sock)/refund_demo", false},
	} {
		if resetDSNAllowed(tc.dsn) != tc.allowed {
			t.Fatalf("DSN guard allowed=%v", tc.allowed)
		}
	}
	if ValidateDataResetDB(nil) == nil || ValidateDataResetDB(new(sql.DB)) == nil {
		t.Fatal("unverified DB accepted")
	}
}

func resetHTTP(t *testing.T, h http.Handler, cookie *http.Cookie, csrf string) ResetResult {
	t.Helper()
	w := authRequest(h, "POST", "/api/maintenance/reset", cookie, csrf, testOrigin, `{"confirmation":"恢复初始数据"}`, nil)
	requireAuthCode(t, w, 200, "")
	var result ResetResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.ResetID == "" || result.ResetAt.IsZero() {
		t.Fatal("incomplete reset result")
	}
	return result
}

func TestMySQLResetDefaultOffAndOptInPermissions(t *testing.T) {
	db, s, _ := fixture(t)
	off := NewHandler(s)
	cookie, session := loginSession(t, off, "a-write", nil)
	if strings.Contains(strings.Join(session.User.Permissions, ","), "data:reset") {
		t.Fatal("default session has reset permission")
	}
	w := authRequest(off, "POST", "/api/maintenance/reset", cookie, session.CSRFToken, testOrigin, `{"confirmation":"恢复初始数据"}`, nil)
	requireAuthCode(t, w, 403, "RESET_DISABLED")
	on := NewHandlerWithOptions(s, HandlerOptions{EnableDataReset: true})
	w = authRequest(on, "GET", "/api/auth/session", cookie, "", "", "", nil)
	requireAuthCode(t, w, 200, "")
	if !strings.Contains(w.Body.String(), "data:reset") {
		t.Fatal("existing session did not dynamically gain enabled operator permission")
	}
	reader, readSession := loginSession(t, on, "a-read", nil)
	if strings.Contains(strings.Join(readSession.User.Permissions, ","), "data:reset") {
		t.Fatal("viewer received reset permission")
	}
	w = authRequest(on, "POST", "/api/maintenance/reset", reader, readSession.CSRFToken, testOrigin, `{"confirmation":"恢复初始数据"}`, nil)
	requireAuthCode(t, w, 403, "FORBIDDEN")
	w = authRequest(on, "POST", "/api/maintenance/reset", nil, "", testOrigin, `{"confirmation":"恢复初始数据"}`, nil)
	requireAuthCode(t, w, 401, "UNAUTHORIZED")
	for _, body := range []string{`{}`, `{"confirmation":"恢复"}`, `{"confirmation":"恢复初始数据 "}`, `{"confirmation":"恢复初始数据","merchant_id":"B"}`, `{"confirmation":"恢复初始数据","permissions":["data:reset"]}`} {
		w = authRequest(on, "POST", "/api/maintenance/reset", cookie, session.CSRFToken, testOrigin, body, nil)
		requireAuthCode(t, w, 400, "VALIDATION_ERROR")
	}
	w = authRequest(on, "POST", "/api/maintenance/reset", cookie, "wrong", testOrigin, `{"confirmation":"恢复初始数据"}`, nil)
	requireAuthCode(t, w, 403, "CSRF_REJECTED")
	w = authRequest(on, "POST", "/api/maintenance/reset", cookie, session.CSRFToken, "http://evil.invalid", `{"confirmation":"恢复初始数据"}`, nil)
	requireAuthCode(t, w, 403, "CSRF_REJECTED")
	if n := count(t, db, "SELECT COUNT(*) FROM maintenance_resets"); n != 0 {
		t.Fatalf("rejected reset archived=%d", n)
	}
	resetSafeDatabases.Store(db, false)
	t.Cleanup(func() { resetSafeDatabases.Store(db, true) })
	unsafe := NewHandlerWithOptions(s, HandlerOptions{EnableDataReset: true})
	w = authRequest(unsafe, "GET", "/api/auth/session", cookie, "", "", "", nil)
	if strings.Contains(w.Body.String(), "data:reset") {
		t.Fatal("unsafe DB exposed reset permission")
	}
	w = authRequest(unsafe, "POST", "/api/maintenance/reset", cookie, session.CSRFToken, testOrigin, `{"confirmation":"恢复初始数据"}`, nil)
	requireAuthCode(t, w, 403, "RESET_DISABLED")
}

func TestMySQLResetArchivesExactMerchantAndRestoresFixtures(t *testing.T) {
	db, s, _ := fixture(t)
	ctx := context.Background()
	if _, err := db.Exec("UPDATE orders SET amount_cents=20100 WHERE id='A-1001'"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE orders SET amount_cents=1,status='PAID' WHERE id='A-1004'"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO orders(id,merchant_id,amount_cents,status) VALUES('A-CUSTOM','A',1,'PAID')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO channel_scenarios(order_id,mode) VALUES('A-CUSTOM','SUCCESS')"); err != nil {
		t.Fatal(err)
	}
	var original []*Refund
	for _, order := range []string{"A-1001", "A-1002", "A-1003"} {
		r, _, err := s.CreateRefund(ctx, writerA, order, "original-"+order, "测试恢复前记录")
		if err != nil {
			t.Fatal(err)
		}
		original = append(original, r)
	}
	b, _, err := s.CreateRefund(ctx, writerB, "B-2001", "b-original", "保留B记录")
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandlerWithOptions(s, HandlerOptions{EnableDataReset: true})
	cookie, session := loginSession(t, h, "a-write", nil)
	result := resetHTTP(t, h, cookie, session.CSRFToken)
	if result.MerchantID != "A" || result.OrderCount != 4 || result.DataVersion != 1 {
		t.Fatalf("result=%+v", result)
	}
	var data []byte
	if err := db.QueryRow("SELECT payload FROM maintenance_resets WHERE reset_id=?", result.ResetID).Scan(&data); err != nil {
		t.Fatal(err)
	}
	var backup map[string][]map[string]any
	if err := json.Unmarshal(data, &backup); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]int{"orders": 5, "refunds": 3, "idempotency_keys": 3, "channel_scenarios": 5, "channel_requests": 3, "outbox_events": 6} {
		if len(backup[key]) != want {
			t.Fatalf("backup %s has %d want%d", key, len(backup[key]), want)
		}
	}
	if len(backup) != 6 || strings.Contains(string(data), cookie.Value) || strings.Contains(string(data), session.CSRFToken) || strings.Contains(string(data), "auth_sessions") {
		t.Fatal("backup contains non-business secrets/tables")
	}
	for _, record := range backup["refunds"] {
		if record["merchant_id"] != "A" {
			t.Fatal("cross-merchant backup")
		}
	}
	for _, r := range original {
		_, err := s.GetRefund(ctx, writerA, r.RefundID)
		errorCode(t, err, "NOT_FOUND")
	}
	orders, err := s.ListOrders(ctx, writerA)
	if err != nil || len(orders) != 4 {
		t.Fatalf("restored orders=%+v err=%v", orders, err)
	}
	for i, o := range orders {
		seed := seedOrders[i]
		if o.OrderID != seed.id || o.AmountCents != seed.amount || o.Status != seed.status || o.Refund != nil || o.DataVersion != 1 {
			t.Fatalf("not restored fixture=%+v", o)
		}
	}
	preserved, err := s.GetRefund(ctx, writerB, b.RefundID)
	if err != nil || preserved.Status != b.Status || preserved.ProviderRequestID != b.ProviderRequestID {
		t.Fatalf("B changed=%+v err=%v", preserved, err)
	}
	if n := count(t, db, "SELECT COUNT(*) FROM channel_requests WHERE order_id LIKE 'A-%'"); n != 0 {
		t.Fatalf("A channel rows remain=%d", n)
	}
	if n := count(t, db, "SELECT COUNT(*) FROM channel_requests WHERE order_id='B-2001'"); n != 1 {
		t.Fatalf("B channel rows=%d", n)
	}
	w := authRequest(h, "GET", "/api/auth/session", cookie, "", "", "", nil)
	requireAuthCode(t, w, 200, "")
	cutoff, version, err := s.ActivityWatermark(ctx, "A")
	if err != nil || !cutoff.Equal(result.ResetAt) || version != 1 {
		t.Fatalf("watermark=%v %d %v", cutoff, version, err)
	}
	for _, r := range original {
		if r.UpdatedAt.After(cutoff) {
			t.Fatal("old event later than cutoff")
		}
	}
	newRefund, _, err := s.CreateRefund(ctx, writerA, "A-1001", "original-A-1001", "测试恢复前记录")
	if err != nil || newRefund.Status != "SUCCEEDED" || newRefund.RefundID == original[0].RefundID || !newRefund.UpdatedAt.After(cutoff) {
		t.Fatalf("fresh retest=%+v err=%v", newRefund, err)
	}
	second := resetHTTP(t, h, cookie, session.CSRFToken)
	if second.DataVersion != 2 || second.ResetID == result.ResetID {
		t.Fatal("repeated reset not independently archived/versioned")
	}
	if n := count(t, db, "SELECT COUNT(*) FROM maintenance_resets WHERE merchant_id='A'"); n != 2 {
		t.Fatalf("archives=%d", n)
	}
	bCookie, bSession := loginSession(t, h, "b-write", nil)
	bReset := resetHTTP(t, h, bCookie, bSession.CSRFToken)
	if bReset.OrderCount != 1 || bReset.MerchantID != "B" || bReset.DataVersion != 1 {
		t.Fatalf("B reset=%+v", bReset)
	}
	_, aVersion, err := s.ActivityWatermark(ctx, "A")
	if err != nil || aVersion != 2 {
		t.Fatal("B reset changed A version")
	}
}

func TestMySQLResetBackupAndDeleteFailuresRollBackAtomically(t *testing.T) {
	for _, stage := range []string{"backup", "delete"} {
		t.Run(stage, func(t *testing.T) {
			db, s, _ := fixture(t)
			ctx := context.Background()
			r, _, err := s.CreateRefund(ctx, writerA, "A-1001", "rollback", "不可丢失")
			if err != nil {
				t.Fatal(err)
			}
			// Inject genuine database constraint failures without SUPER privileges,
			// triggers, or changing any global MySQL settings.
			if stage == "backup" {
				if _, err := db.Exec("ALTER TABLE maintenance_resets ADD CONSTRAINT test_reset_backup_failure CHECK (merchant_id <> 'A')"); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _, _ = db.Exec("ALTER TABLE maintenance_resets DROP CHECK test_reset_backup_failure") })
			} else {
				if _, err := db.Exec("CREATE TABLE test_reset_delete_guard (refund_id VARCHAR(40) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY, FOREIGN KEY(refund_id) REFERENCES refunds(id)) ENGINE=InnoDB"); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _, _ = db.Exec("DROP TABLE IF EXISTS test_reset_delete_guard") })
				if _, err := db.Exec("INSERT INTO test_reset_delete_guard(refund_id) VALUES(?)", r.RefundID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.ResetMerchant(ctx, writerA); err == nil {
				t.Fatal("injected reset failure was ignored")
			}
			got, err := s.GetRefund(ctx, writerA, r.RefundID)
			if err != nil || got.Status != "SUCCEEDED" {
				t.Fatalf("rollback lost original=%+v err=%v", got, err)
			}
			for table, want := range map[string]int64{"maintenance_resets": 0, "merchant_reset_watermarks": 0, "orders": 5, "refunds": 1, "idempotency_keys": 1, "channel_requests": 1, "outbox_events": 2} {
				if n := count(t, db, "SELECT COUNT(*) FROM "+table); n != want {
					t.Fatalf("partial commit %s=%d want%d", table, n, want)
				}
			}
		})
	}
}

func TestMySQLResetWaitsForChannelAndArchivesFinalOutcome(t *testing.T) {
	db, _, _ := fixture(t)
	ctx := context.Background()
	persistent := NewPersistentGateway(db)
	accepted := make(chan struct{})
	release := make(chan struct{})
	g := &testGateway{query: persistent.QueryRefund}
	g.submit = func(ctx context.Context, req ChannelRequest) (ChannelResult, error) {
		result, err := persistent.SubmitRefund(ctx, req)
		if err != nil {
			return result, err
		}
		close(accepted)
		select {
		case <-release:
			return result, nil
		case <-ctx.Done():
			return ChannelResult{}, ctx.Err()
		}
	}
	s := NewService(db, g)
	type createResult struct {
		refund *Refund
		err    error
	}
	createDone := make(chan createResult, 1)
	go func() {
		r, _, err := s.CreateRefund(ctx, writerA, "A-1001", "inflight", "在途退款")
		createDone <- createResult{r, err}
	}()
	select {
	case <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("channel not reached")
	}
	type resetResult struct {
		reset *ResetResult
		err   error
	}
	resetDone := make(chan resetResult, 1)
	go func() { r, err := s.ResetMerchant(ctx, writerA); resetDone <- resetResult{r, err} }()
	select {
	case <-resetDone:
		close(release)
		t.Fatal("reset raced an accepted in-flight refund")
	case <-time.After(50 * time.Millisecond):
	}
	// Different merchant progresses while A's channel still holds its lease.
	bService := NewService(db, persistent)
	b, _, err := bService.CreateRefund(ctx, writerB, "B-2001", "independent-b", "B操作")
	if err != nil || b.Status != "SUCCEEDED" {
		close(release)
		t.Fatalf("B blocked=%+v %v", b, err)
	}
	close(release)
	created := <-createDone
	reset := <-resetDone
	if created.err != nil || created.refund.Status != "SUCCEEDED" || reset.err != nil {
		t.Fatalf("create=%+v reset=%+v", created, reset)
	}
	var payload []byte
	if err := db.QueryRow("SELECT payload FROM maintenance_resets WHERE reset_id=?", reset.reset.ResetID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var backup map[string][]map[string]any
	if err := json.Unmarshal(payload, &backup); err != nil {
		t.Fatal(err)
	}
	if len(backup["refunds"]) != 1 || backup["refunds"][0]["status"] != "SUCCEEDED" || len(backup["channel_requests"]) != 1 {
		t.Fatal("reset did not archive completed channel and business states")
	}
	if created.refund.UpdatedAt.After(reset.reset.ResetAt) {
		t.Fatal("cutoff was generated before inflight operation finished")
	}
	if n := count(t, db, "SELECT COUNT(*) FROM refunds WHERE merchant_id='A'"); n != 0 {
		t.Fatalf("orphan A refund=%d", n)
	}
}

func TestMySQLResetAdvisoryLockIsCrossConnectionAndBusyIsExplicit(t *testing.T) {
	db, s, _ := fixture(t)
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var got int
	name := mutationLockName("refund_test", "A")
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?,0)", name).Scan(&got); err != nil || got != 1 {
		t.Fatalf("external lock=%d %v", got, err)
	}
	defer conn.ExecContext(ctx, "DO RELEASE_LOCK(?)", name)
	short, cancel := context.WithTimeout(ctx, 80*time.Millisecond)
	defer cancel()
	_, err = s.ResetMerchant(short, writerA)
	errorCode(t, err, "RESET_BUSY")
	if n := count(t, db, "SELECT COUNT(*) FROM maintenance_resets"); n != 0 {
		t.Fatal("busy reset changed data")
	}
	if _, err = conn.ExecContext(ctx, "DO RELEASE_LOCK(?)", name); err != nil {
		t.Fatal(err)
	}
	reset, err := s.ResetMerchant(ctx, writerA)
	if err != nil || reset.DataVersion != 1 {
		t.Fatalf("lock did not release=%+v %v", reset, err)
	}
}

func TestMySQLResetReadsHaveConsistentVersionAndPersistWatermark(t *testing.T) {
	db, s, dsn := fixture(t)
	ctx := context.Background()
	r, _, err := s.CreateRefund(ctx, writerA, "A-1001", "snapshot", "快照")
	if err != nil {
		t.Fatal(err)
	}
	resetDone := make(chan error, 1)
	go func() { _, err := s.ResetMerchant(ctx, writerA); resetDone <- err }()
	for range 20 {
		orders, err := s.ListOrders(ctx, writerA)
		if err != nil || len(orders) != 4 {
			t.Fatalf("read saw partial fixtures=%+v %v", orders, err)
		}
		version := orders[0].DataVersion
		for _, o := range orders {
			if o.DataVersion != version {
				t.Fatal("mixed reset epochs in one response")
			}
		}
		if version == 0 {
			if orders[0].Refund == nil || orders[0].Refund.RefundID != r.RefundID {
				t.Fatal("old snapshot mixed with new refund data")
			}
		} else if version == 1 {
			if orders[0].Refund != nil {
				t.Fatal("new snapshot resurrected old refund")
			}
		} else {
			t.Fatalf("unexpected version%d", version)
		}
	}
	if err := <-resetDone; err != nil {
		t.Fatal(err)
	}
	cutoff, version, err := s.ActivityWatermark(ctx, "A")
	if err != nil || version != 1 {
		t.Fatal("missing watermark")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenDB(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := Migrate(ctx, reopened); err != nil {
		t.Fatal(err)
	}
	if err := Seed(ctx, reopened); err != nil {
		t.Fatal(err)
	}
	restarted := NewService(reopened, NewPersistentGateway(reopened))
	after, afterVersion, err := restarted.ActivityWatermark(ctx, "A")
	if err != nil || !after.Equal(cutoff) || afterVersion != version {
		t.Fatal("restart lost durable reset boundary")
	}
	o, err := restarted.GetOrder(ctx, writerA, "A-1001")
	if err != nil || o.DataVersion != 1 || o.Refund != nil {
		t.Fatalf("restarted snapshot=%+v %v", o, err)
	}
}

func TestMySQLResetDoesNotStarvePoolWithManyWaitingMutations(t *testing.T) {
	db, s, _ := fixture(t)
	ctx := context.Background()
	release, err := s.lockMerchant(ctx, "A")
	if err != nil {
		t.Fatal(err)
	}
	const waiting = 32
	done := make(chan error, waiting)
	for i := range waiting {
		go func(index int) {
			_, _, err := s.CreateRefund(ctx, writerA, "A-1001", fmt.Sprintf("queue-%d", index), "排队")
			done <- err
		}(i)
	}
	time.Sleep(50 * time.Millisecond)
	// Waiting requests queue in Go, not each on a reserved GET_LOCK connection.
	if inUse := db.Stats().InUse; inUse > 5 {
		release()
		t.Fatalf("waiting mutations exhausted pool: %d", inUse)
	}
	release()
	success, conflicts := 0, 0
	for range waiting {
		err := <-done
		if err == nil {
			success++
		} else {
			var ae *APIError
			if !errors.As(err, &ae) || ae.Code != "REFUND_EXISTS" {
				t.Fatalf("unexpected queued result=%v", err)
			}
			conflicts++
		}
	}
	if success != 1 || conflicts != waiting-1 {
		t.Fatalf("success%d conflict%d", success, conflicts)
	}
}
