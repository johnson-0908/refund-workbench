package app

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

type Service struct {
	db      *sql.DB
	gateway Gateway
}

func NewService(db *sql.DB, gateway Gateway) *Service { return &Service{db: db, gateway: gateway} }
func (s *Service) Health(ctx context.Context) error   { return s.db.PingContext(ctx) }

type rowScanner interface{ Scan(...any) error }

const refundColumns = `id,order_id,merchant_id,amount_cents,provider_request_id,reason,status,message,created_at,updated_at`

func scanRefund(row rowScanner) (*Refund, error) {
	var r Refund
	err := row.Scan(&r.RefundID, &r.OrderID, &r.MerchantID, &r.AmountCents, &r.ProviderRequestID, &r.Reason, &r.Status, &r.Message, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, err
	}
	r.CreatedAt, r.UpdatedAt = r.CreatedAt.UTC(), r.UpdatedAt.UTC()
	return &r, nil
}

func (s *Service) GetRefund(ctx context.Context, id Identity, refundID string) (*Refund, error) {
	r, err := scanRefund(s.db.QueryRowContext(ctx, `SELECT `+refundColumns+` FROM refunds WHERE id=? AND merchant_id=?`, refundID, id.MerchantID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound()
	}
	return r, err
}

func (s *Service) GetOrder(ctx context.Context, id Identity, orderID string) (*Order, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var o Order
	err = tx.QueryRowContext(ctx, `SELECT id,merchant_id,amount_cents,status FROM orders WHERE id=? AND merchant_id=?`, orderID, id.MerchantID).
		Scan(&o.OrderID, &o.MerchantID, &o.AmountCents, &o.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound()
	}
	if err != nil {
		return nil, err
	}
	o.Refund, err = scanRefund(tx.QueryRowContext(ctx, `SELECT `+refundColumns+` FROM refunds WHERE order_id=? AND merchant_id=?`, orderID, id.MerchantID))
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	if err != nil {
		return nil, err
	}
	o.DataVersion, err = readDataVersion(ctx, tx, id.MerchantID)
	if err != nil {
		return nil, err
	}
	return &o, tx.Commit()
}

func (s *Service) ListOrders(ctx context.Context, id Identity) ([]Order, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	version, err := readDataVersion(ctx, tx, id.MerchantID)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,merchant_id,amount_cents,status FROM orders WHERE merchant_id=? ORDER BY id`, id.MerchantID)
	if err != nil {
		return nil, err
	}
	orders := make([]Order, 0)
	for rows.Next() {
		var o Order
		if err = rows.Scan(&o.OrderID, &o.MerchantID, &o.AmountCents, &o.Status); err != nil {
			rows.Close()
			return nil, err
		}
		orders = append(orders, o)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range orders {
		orders[i].DataVersion = version
		orders[i].Refund, err = scanRefund(tx.QueryRowContext(ctx, `SELECT `+refundColumns+` FROM refunds WHERE order_id=? AND merchant_id=?`, orders[i].OrderID, id.MerchantID))
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	return orders, tx.Commit()
}

func validateCreate(key, reason string) error {
	if len(key) == 0 || len(key) > 128 {
		return &APIError{400, "VALIDATION_ERROR", "Idempotency-Key 长度必须为 1 到 128"}
	}
	for _, r := range key {
		if r < 33 || r > 126 {
			return &APIError{400, "VALIDATION_ERROR", "Idempotency-Key 只能包含可见 ASCII 字符"}
		}
	}
	if !utf8.ValidString(reason) || strings.TrimSpace(reason) == "" || utf8.RuneCountInString(reason) > 200 {
		return &APIError{400, "VALIDATION_ERROR", "退款原因必须为 1 到 200 个字符，且不能全为空白"}
	}
	return nil
}

// Resolve scoped resource visibility before write permission. A read-only
// merchant must not learn whether another merchant owns the supplied ID.
func (s *Service) authorizeOrderWrite(ctx context.Context, id Identity, orderID string) error {
	var visibleID string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM orders WHERE id=? AND merchant_id=?`, orderID, id.MerchantID).Scan(&visibleID)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound()
	}
	if err != nil {
		return err
	}
	if !id.CanWrite {
		return forbidden()
	}
	return nil
}

// CreateRefund allocates a unique refund in a short database transaction, then
// releases transactional row locks before invoking the channel exactly once.
// A merchant advisory lease spans the channel interval to exclude data reset.
// A replay returns the persisted refund and NEVER resubmits to the channel.
func (s *Service) CreateRefund(ctx context.Context, id Identity, orderID, key, reason string) (*Refund, bool, error) {
	if err := s.authorizeOrderWrite(ctx, id, orderID); err != nil {
		return nil, false, err
	}
	release, err := s.lockMutation(ctx, id.MerchantID)
	if err != nil {
		return nil, false, err
	}
	defer release()
	return s.createRefund(ctx, id, orderID, key, reason)
}

func (s *Service) createRefund(ctx context.Context, id Identity, orderID, key, reason string) (*Refund, bool, error) {
	if err := s.authorizeOrderWrite(ctx, id, orderID); err != nil {
		return nil, false, err
	}
	if err := validateCreate(key, reason); err != nil {
		return nil, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	var merchant string
	if err = tx.QueryRowContext(ctx, `SELECT id FROM merchants WHERE id=? FOR UPDATE`, id.MerchantID).Scan(&merchant); err != nil {
		return nil, false, err
	}
	var order Order
	err = tx.QueryRowContext(ctx, `SELECT id,merchant_id,amount_cents,status FROM orders WHERE id=? AND merchant_id=?`, orderID, id.MerchantID).
		Scan(&order.OrderID, &order.MerchantID, &order.AmountCents, &order.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, notFound()
	}
	if err != nil {
		return nil, false, err
	}
	var oldOrder, oldReason, oldRefund string
	err = tx.QueryRowContext(ctx, `SELECT order_id,reason,refund_id FROM idempotency_keys WHERE merchant_id=? AND idempotency_key=?`, id.MerchantID, key).Scan(&oldOrder, &oldReason, &oldRefund)
	if err == nil {
		if oldOrder != orderID || oldReason != reason {
			return nil, false, &APIError{409, "IDEMPOTENCY_CONFLICT", "该幂等键已用于不同请求，请勿修改原请求参数"}
		}
		r, readErr := scanRefund(tx.QueryRowContext(ctx, `SELECT `+refundColumns+` FROM refunds WHERE id=? AND merchant_id=?`, oldRefund, id.MerchantID))
		if readErr != nil {
			return nil, false, readErr
		}
		return r, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT id FROM refunds WHERE order_id=?`, orderID).Scan(&existing)
	if err == nil {
		return nil, false, &APIError{409, "REFUND_EXISTS", "该订单已有退款，请查看或查证原退款，不可重新退款"}
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	if order.Status != "PAID" {
		return nil, false, &APIError{409, "ORDER_NOT_PAID", "未支付订单不能退款"}
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	r := &Refund{RefundID: "rf_" + rand.Text(), OrderID: orderID, MerchantID: id.MerchantID, AmountCents: order.AmountCents, ProviderRequestID: "pr_" + rand.Text(), Reason: reason, Status: "PROCESSING", Message: "退款请求已登记，正在获取渠道结果", CreatedAt: now, UpdatedAt: now}
	_, err = tx.ExecContext(ctx, `INSERT INTO refunds (`+refundColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?)`, r.RefundID, r.OrderID, r.MerchantID, r.AmountCents, r.ProviderRequestID, r.Reason, r.Status, r.Message, r.CreatedAt, r.UpdatedAt)
	if err != nil {
		return nil, false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO idempotency_keys(merchant_id,idempotency_key,order_id,reason,refund_id) VALUES (?,?,?,?,?)`, id.MerchantID, key, orderID, reason, r.RefundID)
	if err != nil {
		return nil, false, err
	}
	if err = insertOutbox(ctx, tx, r); err != nil {
		return nil, false, err
	}
	if err = tx.Commit(); err != nil {
		return nil, false, err
	}
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	result, channelErr := s.gateway.SubmitRefund(callCtx, ChannelRequest{r.ProviderRequestID, r.OrderID, r.AmountCents})
	cancel()
	// Persist uncertainty even when the HTTP client disappeared. This context is
	// bounded and only finalizes an already-reserved operation; it cannot resubmit.
	writeCtx, writeCancel := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Second)
	defer writeCancel()
	r, err = s.applyResult(writeCtx, r, result, channelErr)
	return r, true, err
}

func (s *Service) Reconcile(ctx context.Context, id Identity, refundID string) (*Refund, error) {
	if _, err := s.GetRefund(ctx, id, refundID); err != nil {
		return nil, err
	}
	if !id.CanWrite {
		return nil, forbidden()
	}
	release, err := s.lockMutation(ctx, id.MerchantID)
	if err != nil {
		return nil, err
	}
	defer release()
	return s.reconcile(ctx, id, refundID)
}

func (s *Service) reconcile(ctx context.Context, id Identity, refundID string) (*Refund, error) {
	r, err := s.GetRefund(ctx, id, refundID)
	if err != nil {
		return nil, err
	}
	if !id.CanWrite {
		return nil, forbidden()
	}
	if isTerminal(r.Status) {
		return r, nil
	}
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	result, channelErr := s.gateway.QueryRefund(callCtx, r.ProviderRequestID)
	cancel()
	writeCtx, writeCancel := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Second)
	defer writeCancel()
	return s.applyResult(writeCtx, r, result, channelErr)
}

func (s *Service) applyResult(ctx context.Context, original *Refund, result ChannelResult, channelErr error) (*Refund, error) {
	status, message := "UNKNOWN", "渠道处理结果待确认，请查询原退款"
	if channelErr == nil && result.ProviderRequestID == original.ProviderRequestID && result.OrderID == original.OrderID && result.AmountCents == original.AmountCents && isTerminal(result.Status) {
		status = result.Status
		if status == "SUCCEEDED" {
			message = "渠道明确退款成功"
		} else {
			message = "渠道明确拒绝退款"
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	current, err := scanRefund(tx.QueryRowContext(ctx, `SELECT `+refundColumns+` FROM refunds WHERE id=? AND merchant_id=? FOR UPDATE`, original.RefundID, original.MerchantID))
	if err != nil {
		return nil, err
	}
	if isTerminal(current.Status) || (current.Status == status && current.Message == message) {
		return current, nil
	}
	current.Status, current.Message, current.UpdatedAt = status, message, time.Now().UTC().Truncate(time.Microsecond)
	_, err = tx.ExecContext(ctx, `UPDATE refunds SET status=?,message=?,updated_at=? WHERE id=? AND status IN ('PROCESSING','UNKNOWN')`, status, message, current.UpdatedAt, current.RefundID)
	if err != nil {
		return nil, err
	}
	if err = insertOutbox(ctx, tx, current); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return current, nil
}

func insertOutbox(ctx context.Context, tx *sql.Tx, r *Refund) error {
	e := Activity{EventID: "ev_" + rand.Text(), MerchantID: r.MerchantID, RefundID: r.RefundID, OrderID: r.OrderID, AmountCents: r.AmountCents, Status: r.Status, Message: r.Message, OccurredAt: r.UpdatedAt}
	payload, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO outbox_events(event_id,merchant_id,refund_id,payload,created_at) VALUES (?,?,?,?,?)`, e.EventID, e.MerchantID, e.RefundID, payload, e.OccurredAt)
	return err
}
