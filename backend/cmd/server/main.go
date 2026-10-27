package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"refundworkbench/internal/app"
	"refundworkbench/internal/events"

	"github.com/redis/go-redis/v9"
)

func main() {
	var err error
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		err = healthcheck(envOr("HTTP_ADDR", "127.0.0.1:18080"))
	} else if len(os.Args) > 1 {
		err = fmt.Errorf("usage: server [healthcheck]")
	} else {
		err = run()
	}
	if err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	config, err := loadServerConfig(os.LookupEnv)
	if err != nil {
		return err
	}
	db, err := app.OpenDBWithOptions(config.DSN, app.DatabaseOptions{AllowDockerDemoReset: config.AllowDockerReset})
	if err != nil {
		return err
	}
	defer db.Close()
	if config.EnableDataReset {
		if err = app.ValidateDataResetDB(db); err != nil {
			return err
		}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err = app.Migrate(ctx, db); err != nil {
		return err
	}
	if err = app.Seed(ctx, db); err != nil {
		return err
	}
	service := app.NewService(db, app.NewPersistentGateway(db))
	redisClient := redis.NewClient(&redis.Options{
		Addr:        envOr("REDIS_ADDR", "127.0.0.1:6379"),
		DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second,
		ContextTimeoutEnabled: true, MaxRetries: -1,
	})
	defer redisClient.Close()
	eventService := events.New(db, redisClient, strings.Split(envOr("KAFKA_BROKERS", "127.0.0.1:19092"), ","),
		envOr("KAFKA_TOPIC", events.DefaultTopic), envOr("KAFKA_GROUP", events.DefaultGroup))
	eventService.SetPrefix(envOr("REDIS_PREFIX", events.DefaultPrefix))
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		eventService.Run(ctx)
	}()
	defer func() { cancel(); <-workerDone }()
	addr := config.HTTPAddr
	if config.EnableLegacy {
		slog.Warn("explicit assignment compatibility enabled: static demo Bearer identities")
	}
	handler := app.NewHandlerWithOptions(service, app.HandlerOptions{EnableDemoBearer: config.EnableLegacy, EnableDataReset: config.EnableDataReset, AllowedOrigins: config.Origins, SessionCookieSecure: config.CookieSecure, Wrap: func(next http.Handler) http.Handler { return withEvents(next, eventService, service) }})
	server := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()
		shutdownCtx, done := context.WithTimeout(context.Background(), 15*time.Second)
		defer done()
		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Error("graceful shutdown failed", "error", err)
		}
	}()
	slog.Info("refund workbench listening", "address", addr)
	err = server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		<-stopped
		return nil
	}
	cancel()
	return err
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// Preserve the default local-only validation for callers/tests. Deployment
// requires separate explicit bind and exact-origin configuration.
func validateHTTPAddr(addr string) error {
	return validateHTTPBinding(addr, false, false)
}

// Authentication remains server-side. The projection endpoint receives only
// the authenticated merchant, never a query/body-supplied merchant ID.
func withEvents(next http.Handler, eventService *events.Service, business *app.Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.URL.Path == "/api/health" {
			next.ServeHTTP(w, r)
			return
		}
		identity, err := app.Authenticate(r)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		allowed, available := eventService.Limiter(r.Context(), identity.MerchantID+":"+strconv.FormatBool(identity.CanWrite))
		if !available {
			w.Header().Set("X-RateLimit-Degraded", "true")
		}
		if !allowed {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Retry-After", strconv.FormatInt(60-time.Now().Unix()%60, 10))
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "RATE_LIMITED", "message": "请求过于频繁，请稍后查询原退款"}})
			return
		}
		if r.URL.Path == "/api/activity" && r.Method == http.MethodGet {
			items, available := eventService.Activity(r.Context(), identity.MerchantID)
			cutoff, version, err := business.ActivityWatermark(r.Context(), identity.MerchantID)
			if err != nil {
				items = []events.Event{}
				available = false
			} else {
				filtered := make([]events.Event, 0, len(items))
				for _, item := range items {
					if item.MerchantID == identity.MerchantID && item.OccurredAt.After(cutoff) {
						filtered = append(filtered, item)
					}
				}
				items = filtered
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "available": available, "data_version": version})
			return
		}
		next.ServeHTTP(w, r)
	})
}
