package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"
	"unicode/utf8"
)

func NewHandler(service *Service) http.Handler {
	return NewHandlerWithOptions(service, HandlerOptions{})
}

type HandlerOptions struct {
	// Compatibility is opt-in for the original assignment's CLI evaluation.
	EnableDemoBearer    bool
	EnableDataReset     bool
	AllowedOrigins      []string
	SessionCookieSecure bool
	// Wrap places projection/limiting middleware INSIDE the authentication boundary.
	Wrap func(http.Handler) http.Handler
}

func NewHandlerWithOptions(service *Service, options HandlerOptions) http.Handler {
	auth := newAuthService(service.db, options.EnableDemoBearer, options.EnableDataReset)
	if err := auth.configureHTTP(options.AllowedOrigins, options.SessionCookieSecure); err != nil {
		slog.Error("invalid authentication origin configuration")
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			writeError(w, unavailable())
		})
	}
	resetEnabled := options.EnableDataReset && ValidateDataResetDB(service.db) == nil
	next := newBusinessHandler(service, resetEnabled)
	if options.Wrap != nil {
		next = options.Wrap(next)
	}
	return withRequestDeadline(auth.middleware(next))
}

const requestTimeout = 15 * time.Second

// WriteTimeout only bounds network writes; it does not cancel database work.
// The service deliberately detaches only its already-accepted finalization,
// which has its own eight-second bound and retains the merchant lock.
func withRequestDeadline(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func newBusinessHandler(service *Service, resetEnabled bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		if err := service.Health(r.Context()); err != nil {
			writeError(w, unavailable())
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ok"})
	})
	withIdentity := func(fn func(http.ResponseWriter, *http.Request, Identity)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			id, err := Authenticate(r)
			if err != nil {
				writeError(w, err)
				return
			}
			fn(w, r, id)
		}
	}
	mux.HandleFunc("POST /api/maintenance/reset", withIdentity(func(w http.ResponseWriter, r *http.Request, id Identity) {
		if !resetEnabled {
			writeError(w, &APIError{403, "RESET_DISABLED", "当前环境未启用数据恢复"})
			return
		}
		if !id.CanWrite {
			writeError(w, forbidden())
			return
		}
		var body struct {
			Confirmation string `json:"confirmation"`
		}
		if err := decodeJSON(w, r, &body); err != nil || body.Confirmation != "恢复初始数据" {
			writeError(w, &APIError{400, "VALIDATION_ERROR", "请准确输入确认文字：恢复初始数据，且不要提交额外字段"})
			return
		}
		result, err := service.ResetMerchant(r.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, result)
	}))
	mux.HandleFunc("GET /api/orders", withIdentity(func(w http.ResponseWriter, r *http.Request, id Identity) {
		orders, err := service.ListOrders(r.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"items": orders})
	}))
	mux.HandleFunc("GET /api/orders/{id}", withIdentity(func(w http.ResponseWriter, r *http.Request, id Identity) {
		order, err := service.GetOrder(r.Context(), id, r.PathValue("id"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, order)
	}))
	mux.HandleFunc("POST /api/orders/{id}/refunds", withIdentity(func(w http.ResponseWriter, r *http.Request, id Identity) {
		if err := service.authorizeOrderWrite(r.Context(), id, r.PathValue("id")); err != nil {
			writeError(w, err)
			return
		}
		var body struct {
			Reason string `json:"reason"`
		}
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, err)
			return
		}
		refund, created, err := service.CreateRefund(r.Context(), id, r.PathValue("id"), r.Header.Get("Idempotency-Key"), body.Reason)
		if err != nil {
			writeError(w, err)
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		writeJSON(w, status, refund)
	}))
	mux.HandleFunc("GET /api/refunds/{id}", withIdentity(func(w http.ResponseWriter, r *http.Request, id Identity) {
		refund, err := service.GetRefund(r.Context(), id, r.PathValue("id"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, refund)
	}))
	mux.HandleFunc("POST /api/refunds/{id}/reconcile", withIdentity(func(w http.ResponseWriter, r *http.Request, id Identity) {
		refund, err := service.Reconcile(r.Context(), id, r.PathValue("id"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, refund)
	}))
	// The optional events middleware intercepts this endpoint. A degraded view
	// is intentionally not a refund failure and is safe without Redis/Kafka.
	mux.HandleFunc("GET /api/activity", withIdentity(func(w http.ResponseWriter, r *http.Request, id Identity) {
		writeJSON(w, 200, map[string]any{"items": []Activity{}, "available": false})
	}))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if _, err := Authenticate(r); err != nil {
			writeError(w, err)
			return
		}
		writeError(w, notFound())
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		mux.ServeHTTP(w, r)
	})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	data, err := io.ReadAll(r.Body)
	if err != nil || !utf8.Valid(data) {
		return &APIError{400, "VALIDATION_ERROR", "请求体必须为不超过 4KB 的 UTF-8 JSON"}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(dst); err != nil {
		return &APIError{400, "VALIDATION_ERROR", "请求体只允许 reason 字符串字段"}
	}
	if err = decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return &APIError{400, "VALIDATION_ERROR", "请求体必须为单个 JSON 对象"}
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		slog.Warn("response encoding failed", "error_kind", errorDiagnostic(err))
	}
}

func writeError(w http.ResponseWriter, err error) {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		slog.Error("request failed", "error_kind", errorDiagnostic(err))
		apiErr = unavailable().(*APIError)
	}
	writeJSON(w, apiErr.Status, map[string]any{"error": map[string]string{"code": apiErr.Code, "message": apiErr.Message}})
}
