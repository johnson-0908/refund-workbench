package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/go-sql-driver/mysql"
	"refundworkbench/internal/app"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		return errors.New("usage: admin seed|inspect|reset --confirm")
	}
	command := os.Args[1]
	if command != "seed" && command != "inspect" && command != "reset" {
		return errors.New("unknown admin command")
	}
	dsn, err := app.DatabaseDSNFromEnv(os.LookupEnv)
	if err != nil {
		return err
	}
	if command == "reset" {
		if len(os.Args) != 3 || os.Args[2] != "--confirm" {
			return errors.New("reset requires explicit --confirm; deletes this local demo database's fixture data")
		}
		if err := validateResetDSN(dsn); err != nil {
			return err
		}
	} else if len(os.Args) != 2 {
		return errors.New("unexpected admin arguments")
	}
	db, err := app.OpenDB(dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err = app.Migrate(ctx, db); err != nil {
		return err
	}
	switch command {
	case "seed":
		if err = app.Seed(ctx, db); err != nil {
			return err
		}
		fmt.Println(`{"status":"seeded","preserved_existing_data":true}`)
	case "reset":
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		for _, table := range []string{"merchant_reset_watermarks", "auth_sessions", "outbox_events", "idempotency_keys", "refunds", "channel_requests", "channel_scenarios", "orders", "merchants"} {
			if _, err = tx.ExecContext(ctx, "DELETE FROM "+table); err != nil {
				return err
			}
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		if err = app.Seed(ctx, db); err != nil {
			return err
		}
		fmt.Println(`{"status":"reset_and_seeded"}`)
	case "inspect":
		return inspect(ctx, db)
	}
	return nil
}

func validateResetDSN(dsn string) error {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return err
	}
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil || cfg.Net != "tcp" || (host != "127.0.0.1" && host != "localhost" && host != "::1") {
		return errors.New("reset allowed only on localhost TCP MySQL")
	}
	if cfg.DBName != "refund_demo" && cfg.DBName != "refund_test" && cfg.DBName != "refund_acceptance" {
		return errors.New("reset refused: database is not in local demo allowlist")
	}
	return nil
}

func inspect(ctx context.Context, db *sql.DB) error {
	result := map[string]any{}
	var dbName string
	if err := db.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&dbName); err != nil {
		return err
	}
	result["database"] = dbName
	counts := map[string]int64{}
	for _, table := range []string{"orders", "refunds", "idempotency_keys", "channel_requests", "outbox_events"} {
		var n int64
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
			return err
		}
		counts[table] = n
	}
	result["counts"] = counts
	var pending int64
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM outbox_events WHERE published_at IS NULL").Scan(&pending); err != nil {
		return err
	}
	counts["outbox_pending"] = pending
	for name, query := range map[string]string{
		"orders":           "SELECT id AS order_id,merchant_id,amount_cents,status FROM orders ORDER BY id",
		"refunds":          "SELECT id AS refund_id,order_id,merchant_id,amount_cents,provider_request_id,reason,status,message,created_at,updated_at FROM refunds ORDER BY created_at,id",
		"channel_requests": "SELECT provider_request_id,order_id,amount_cents,status,mode,submit_calls,query_calls,effect_count,created_at,updated_at FROM channel_requests ORDER BY created_at,provider_request_id",
	} {
		rows, err := readRows(ctx, db, query)
		if err != nil {
			return err
		}
		result[name] = rows
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

func readRows(ctx context.Context, db *sql.DB, query string) ([]map[string]any, error) {
	rows, err := db.QueryContext(ctx, query)
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
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := map[string]any{}
		for i, v := range values {
			if b, ok := v.([]byte); ok {
				row[columns[i]] = string(b)
			} else {
				row[columns[i]] = v
			}
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
