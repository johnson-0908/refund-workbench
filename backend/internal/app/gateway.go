package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// PersistentGateway is a deterministic local channel simulator. Its durable
// receipt is committed BEFORE LOST_RESPONSE returns a transport error.
// Business code must not inspect these tables directly.
type PersistentGateway struct{ db *sql.DB }

func NewPersistentGateway(db *sql.DB) *PersistentGateway { return &PersistentGateway{db: db} }

func (g *PersistentGateway) SubmitRefund(ctx context.Context, req ChannelRequest) (ChannelResult, error) {
	if req.ProviderRequestID == "" || req.OrderID == "" || req.AmountCents <= 0 {
		return ChannelResult{}, ErrChannelConflict
	}
	tx, err := g.db.BeginTx(ctx, nil)
	if err != nil {
		return ChannelResult{}, err
	}
	defer tx.Rollback()
	var mode, existing string
	now := time.Now().UTC()
	// Existing receipts are authoritative even if a scenario is later changed
	// or removed. This read takes no gap/shared lock; the following UPDATE or
	// upsert takes the exclusive lock before the final current read.
	err = tx.QueryRowContext(ctx, `SELECT provider_request_id FROM channel_requests WHERE provider_request_id=?`, req.ProviderRequestID).Scan(&existing)
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE channel_requests SET submit_calls=submit_calls+1,updated_at=? WHERE provider_request_id=?`, now, req.ProviderRequestID)
	} else if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `SELECT mode FROM channel_scenarios WHERE order_id=?`, req.OrderID).Scan(&mode)
		if err != nil {
			return ChannelResult{}, fmt.Errorf("channel scenario: %w", err)
		}
		status, message, effectCount := "SUCCEEDED", "渠道明确退款成功", 1
		if mode == "REJECT" {
			status, message, effectCount = "FAILED", "渠道明确拒绝退款", 0
		}
		if mode != "SUCCESS" && mode != "LOST_RESPONSE" && mode != "REJECT" {
			return ChannelResult{}, ErrChannelUncertain
		}
		// The PK serializes duplicate provider request IDs across processes. An
		// explicit duplicate-key UPDATE obtains an exclusive lock immediately;
		// INSERT IGNORE followed by FOR UPDATE would upgrade shared locks and can
		// deadlock under concurrent identical requests. Never overwrite parameters.
		_, err = tx.ExecContext(ctx, `INSERT INTO channel_requests
		(provider_request_id,order_id,amount_cents,status,message,mode,effect_count,submit_calls,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,1,?,?)
		ON DUPLICATE KEY UPDATE submit_calls=submit_calls+1,updated_at=?`, req.ProviderRequestID, req.OrderID, req.AmountCents, status, message, mode, effectCount, now, now, now)
	}
	if err != nil {
		return ChannelResult{}, err
	}
	var result ChannelResult
	var submitCalls int64
	err = tx.QueryRowContext(ctx, `SELECT provider_request_id,order_id,amount_cents,status,message,mode,submit_calls FROM channel_requests WHERE provider_request_id=? FOR UPDATE`, req.ProviderRequestID).
		Scan(&result.ProviderRequestID, &result.OrderID, &result.AmountCents, &result.Status, &result.Message, &mode, &submitCalls)
	if err != nil {
		return ChannelResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return ChannelResult{}, err
	}
	if result.OrderID != req.OrderID || result.AmountCents != req.AmountCents {
		return ChannelResult{}, ErrChannelConflict
	}
	if mode == "LOST_RESPONSE" && submitCalls == 1 {
		return ChannelResult{}, ErrChannelUncertain
	}
	return result, nil
}

func (g *PersistentGateway) QueryRefund(ctx context.Context, requestID string) (ChannelResult, error) {
	tx, err := g.db.BeginTx(ctx, nil)
	if err != nil {
		return ChannelResult{}, err
	}
	defer tx.Rollback()
	var result ChannelResult
	err = tx.QueryRowContext(ctx, `SELECT provider_request_id,order_id,amount_cents,status,message FROM channel_requests WHERE provider_request_id=? FOR UPDATE`, requestID).
		Scan(&result.ProviderRequestID, &result.OrderID, &result.AmountCents, &result.Status, &result.Message)
	if errors.Is(err, sql.ErrNoRows) {
		return ChannelResult{}, ErrChannelNotFound
	}
	if err != nil {
		return ChannelResult{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE channel_requests SET query_calls=query_calls+1,updated_at=? WHERE provider_request_id=?`, time.Now().UTC(), requestID); err != nil {
		return ChannelResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return ChannelResult{}, err
	}
	return result, nil
}
