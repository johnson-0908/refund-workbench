package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

type ResetResult struct {
	ResetID     string    `json:"reset_id"`
	MerchantID  string    `json:"merchant_id"`
	OrderCount  int       `json:"order_count"`
	ResetAt     time.Time `json:"reset_at"`
	DataVersion int64     `json:"data_version"`
	Message     string    `json:"message"`
}

type localMutationKey struct {
	db       *sql.DB
	merchant string
}

var localMutationLocks sync.Map

func mutationLockName(database, merchant string) string {
	sum := sha256.Sum256([]byte(database))
	return "refund-workbench:" + hex.EncodeToString(sum[:])[:16] + ":" + merchant
}

func resetBusy() error {
	return &APIError{409, "RESET_BUSY", "当前企业有操作正在处理，请稍后重试"}
}

// Queue locally BEFORE reserving a connection: at most one lock connection per
// merchant/process is held, rather than one per waiting HTTP request. GET_LOCK
// then protects the complete channel + state-write interval across processes.
func (s *Service) lockMerchant(ctx context.Context, merchant string) (func(), error) {
	if merchant != "A" && merchant != "B" {
		return nil, notFound()
	}
	key := localMutationKey{s.db, merchant}
	candidate := make(chan struct{}, 1)
	actual, _ := localMutationLocks.LoadOrStore(key, candidate)
	queue := actual.(chan struct{})
	lockCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	select {
	case queue <- struct{}{}:
	case <-lockCtx.Done():
		return nil, resetBusy()
	}
	conn, err := s.db.Conn(lockCtx)
	if err != nil {
		<-queue
		return nil, err
	}
	var database string
	if err = conn.QueryRowContext(lockCtx, "SELECT DATABASE()").Scan(&database); err != nil {
		conn.Close()
		<-queue
		return nil, err
	}
	name := mutationLockName(database, merchant)
	var acquired sql.NullInt64
	err = conn.QueryRowContext(lockCtx, "SELECT GET_LOCK(?, 5)", name).Scan(&acquired)
	if err != nil || !acquired.Valid || acquired.Int64 != 1 {
		if err != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		conn.Close()
		<-queue
		if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			return nil, err
		}
		return nil, resetBusy()
	}
	return func() {
		releaseCtx, done := context.WithTimeout(context.Background(), 2*time.Second)
		defer done()
		var released sql.NullInt64
		err := conn.QueryRowContext(releaseCtx, "SELECT RELEASE_LOCK(?)", name).Scan(&released)
		if err != nil || !released.Valid || released.Int64 != 1 {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		_ = conn.Close()
		<-queue
	}, nil
}

// ActivityWatermark is the durable read-side boundary. Old Kafka records may
// still be delivered; callers must hide them and fail closed on lookup errors.
func (s *Service) ActivityWatermark(ctx context.Context, merchant string) (time.Time, int64, error) {
	var cutoff time.Time
	var version int64
	err := s.db.QueryRowContext(ctx, `SELECT reset_at,data_version FROM merchant_reset_watermarks WHERE merchant_id=?`, merchant).Scan(&cutoff, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, 0, nil
	}
	return cutoff.UTC(), version, err
}

func readDataVersion(ctx context.Context, tx *sql.Tx, merchant string) (int64, error) {
	var version int64
	err := tx.QueryRowContext(ctx, `SELECT data_version FROM merchant_reset_watermarks WHERE merchant_id=?`, merchant).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return version, err
}

func (s *Service) lockMutation(ctx context.Context, merchant string) (func(), error) {
	_, before, err := s.ActivityWatermark(ctx, merchant)
	if err != nil {
		return nil, err
	}
	release, err := s.lockMerchant(ctx, merchant)
	if err != nil {
		return nil, err
	}
	_, after, err := s.ActivityWatermark(ctx, merchant)
	if err != nil {
		release()
		return nil, err
	}
	if before != after {
		release()
		return nil, resetBusy()
	}
	return release, nil
}

// ResetMerchant is only exposed by the explicitly enabled, authenticated HTTP
// handler. It revalidates DB provenance and role rather than trusting UI state.
func (s *Service) ResetMerchant(ctx context.Context, id Identity) (*ResetResult, error) {
	if err := ValidateDataResetDB(s.db); err != nil {
		return nil, &APIError{403, "RESET_DISABLED", "当前环境未启用数据恢复"}
	}
	if !id.CanWrite {
		return nil, forbidden()
	}
	release, err := s.lockMerchant(ctx, id.MerchantID)
	if err != nil {
		return nil, err
	}
	defer release()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var merchant string
	if err = tx.QueryRowContext(ctx, "SELECT id FROM merchants WHERE id=? FOR UPDATE", id.MerchantID).Scan(&merchant); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, notFound()
		}
		return nil, err
	}
	version, err := readDataVersion(ctx, tx, id.MerchantID)
	if err != nil {
		return nil, err
	}
	var cutoff time.Time
	if err = tx.QueryRowContext(ctx, `SELECT GREATEST(UTC_TIMESTAMP(6),COALESCE(MAX(created_at),UTC_TIMESTAMP(6))) FROM outbox_events WHERE merchant_id=?`, id.MerchantID).Scan(&cutoff); err != nil {
		return nil, err
	}
	result := &ResetResult{ResetID: "rst_" + rand.Text(), MerchantID: id.MerchantID, ResetAt: cutoff.UTC(), DataVersion: version + 1, Message: "当前企业已恢复初始数据，原业务记录已归档"}
	queries := map[string]string{
		"orders":            "SELECT * FROM orders WHERE merchant_id=? ORDER BY id",
		"refunds":           "SELECT * FROM refunds WHERE merchant_id=? ORDER BY id",
		"idempotency_keys":  "SELECT * FROM idempotency_keys WHERE merchant_id=? ORDER BY idempotency_key",
		"channel_scenarios": "SELECT * FROM channel_scenarios WHERE order_id IN (SELECT id FROM orders WHERE merchant_id=?) ORDER BY order_id",
		"channel_requests":  "SELECT * FROM channel_requests WHERE order_id IN (SELECT id FROM orders WHERE merchant_id=?) ORDER BY provider_request_id",
		"outbox_events":     "SELECT * FROM outbox_events WHERE merchant_id=? ORDER BY id",
	}
	backup := make(map[string]any, len(queries))
	for name, query := range queries {
		rows, err := snapshotRows(ctx, tx, query, id.MerchantID)
		if err != nil {
			return nil, err
		}
		backup[name] = rows
	}
	payload, err := json.Marshal(backup)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO maintenance_resets(reset_id,merchant_id,payload,created_at) VALUES (?,?,?,?)`, result.ResetID, id.MerchantID, payload, result.ResetAt); err != nil {
		return nil, err
	}
	// Only table names from this literal allowlist are used. Channel rows are
	// scoped through the current merchant's orders, never by request parameters.
	for _, query := range []string{
		"DELETE FROM outbox_events WHERE merchant_id=?",
		"DELETE FROM idempotency_keys WHERE merchant_id=?",
		"DELETE FROM refunds WHERE merchant_id=?",
		"DELETE FROM channel_requests WHERE order_id IN (SELECT id FROM orders WHERE merchant_id=?)",
		"DELETE FROM channel_scenarios WHERE order_id IN (SELECT id FROM orders WHERE merchant_id=?)",
		"DELETE FROM orders WHERE merchant_id=?",
	} {
		if _, err = tx.ExecContext(ctx, query, id.MerchantID); err != nil {
			return nil, err
		}
	}
	for _, row := range seedOrders {
		if row.merchant != id.MerchantID {
			continue
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO orders(id,merchant_id,amount_cents,status) VALUES(?,?,?,?)`, row.id, row.merchant, row.amount, row.status); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO channel_scenarios(order_id,mode) VALUES(?,?)`, row.id, row.mode); err != nil {
			return nil, err
		}
		result.OrderCount++
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO merchant_reset_watermarks(merchant_id,reset_id,reset_at,data_version) VALUES(?,?,?,?) ON DUPLICATE KEY UPDATE reset_id=?,reset_at=?,data_version=?`, id.MerchantID, result.ResetID, result.ResetAt, result.DataVersion, result.ResetID, result.ResetAt, result.DataVersion); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func snapshotRows(ctx context.Context, tx *sql.Tx, query string, merchant string) ([]map[string]any, error) {
	rows, err := tx.QueryContext(ctx, query, merchant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	result := make([]map[string]any, 0)
	for rows.Next() {
		values := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err = rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(columns))
		for i, value := range values {
			if data, ok := value.([]byte); ok {
				if columns[i] == "payload" {
					if !json.Valid(data) {
						return nil, fmt.Errorf("invalid outbox JSON in backup")
					}
					row[columns[i]] = json.RawMessage(data)
				} else {
					row[columns[i]] = string(data)
				}
			} else {
				row[columns[i]] = value
			}
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
