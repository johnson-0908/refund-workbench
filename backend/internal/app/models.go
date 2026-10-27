package app

import (
	"context"
	"errors"
	"net/http"
	"time"
)

type Identity struct {
	MerchantID string
	CanWrite   bool
}

func Authenticate(r *http.Request) (Identity, error) {
	if principal, ok := r.Context().Value(principalKey{}).(*principal); ok && principal != nil {
		return Identity{MerchantID: principal.User.MerchantID, CanWrite: principal.User.Role == "operator"}, nil
	}
	return Identity{}, &APIError{http.StatusUnauthorized, "UNAUTHORIZED", "请先登录或重新登录"}
}

type Refund struct {
	RefundID          string    `json:"refund_id"`
	OrderID           string    `json:"order_id"`
	AmountCents       int64     `json:"amount_cents"`
	ProviderRequestID string    `json:"provider_request_id"`
	Reason            string    `json:"reason"`
	Status            string    `json:"status"`
	Message           string    `json:"message"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	MerchantID        string    `json:"-"`
}

type Order struct {
	OrderID     string  `json:"order_id"`
	MerchantID  string  `json:"merchant_id"`
	AmountCents int64   `json:"amount_cents"`
	Status      string  `json:"status"`
	Refund      *Refund `json:"refund"`
	DataVersion int64   `json:"data_version"`
}

type Activity struct {
	EventID     string    `json:"event_id"`
	MerchantID  string    `json:"merchant_id"`
	RefundID    string    `json:"refund_id"`
	OrderID     string    `json:"order_id"`
	AmountCents int64     `json:"amount_cents"`
	Status      string    `json:"status"`
	Message     string    `json:"message"`
	OccurredAt  time.Time `json:"occurred_at"`
}

type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string { return e.Code + ": " + e.Message }

var (
	ErrChannelUncertain = errors.New("channel result uncertain")
	ErrChannelNotFound  = errors.New("channel refund not found")
	ErrChannelConflict  = errors.New("channel request parameters conflict")
)

// Gateway is the sole boundary through which business code reads channel results.
// AmountCents and OrderID must always originate from the persisted order.
type Gateway interface {
	SubmitRefund(context.Context, ChannelRequest) (ChannelResult, error)
	QueryRefund(context.Context, string) (ChannelResult, error)
}

type ChannelRequest struct {
	ProviderRequestID string
	OrderID           string
	AmountCents       int64
}

type ChannelResult struct {
	ProviderRequestID string
	OrderID           string
	AmountCents       int64
	Status            string
	Message           string
}

func isTerminal(status string) bool { return status == "SUCCEEDED" || status == "FAILED" }

func forbidden() error { return &APIError{403, "FORBIDDEN", "只读身份不能执行退款操作"} }
func notFound() error  { return &APIError{404, "NOT_FOUND", "对象不存在"} }
func unavailable() error {
	return &APIError{503, "UNAVAILABLE", "服务暂不可用，请稍后重试"}
}
