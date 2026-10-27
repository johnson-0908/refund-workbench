package app

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"
)

const DefaultDSN = "demo:demo-refund-only@tcp(127.0.0.1:3307)/refund_demo?parseTime=true&loc=UTC"

var resetSafeDatabases sync.Map

func ValidateDataResetDB(db *sql.DB) error {
	if allowed, ok := resetSafeDatabases.Load(db); !ok || allowed != true {
		return fmt.Errorf("data reset requires verified local demo MySQL or explicitly allowed mysql:3306/refund_demo")
	}
	return nil
}

func resetDSNAllowed(dsn string) bool {
	return resetDSNAllowedWithOptions(dsn, DatabaseOptions{})
}

type DatabaseOptions struct {
	// Only the fixed private Compose endpoint/schema can be opted in. This does
	// not enable the HTTP reset feature; ENABLE_DATA_RESET is still independent.
	AllowDockerDemoReset bool
}

func resetDSNAllowedWithOptions(dsn string, options DatabaseOptions) bool {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil || cfg.Net != "tcp" {
		return false
	}
	host, _, err := net.SplitHostPort(cfg.Addr)
	local := err == nil && (host == "127.0.0.1" || host == "localhost" || host == "::1") && (cfg.DBName == "refund_demo" || cfg.DBName == "refund_test" || cfg.DBName == "refund_acceptance")
	docker := options.AllowDockerDemoReset && cfg.Addr == "mysql:3306" && cfg.DBName == "refund_demo"
	return local || docker
}

func OpenDB(dsn string) (*sql.DB, error) {
	return OpenDBWithOptions(dsn, DatabaseOptions{})
}

func OpenDBWithOptions(dsn string, options DatabaseOptions) (*sql.DB, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("MySQL connection configuration is invalid")
	}
	cfg.Logger = safeMySQLLogger{}
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, fmt.Errorf("MySQL connector configuration is invalid")
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(24)
	db.SetMaxIdleConns(8)
	db.SetConnMaxLifetime(5 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("MySQL connection failed (%s)", errorDiagnostic(err))
	}
	resetSafeDatabases.Store(db, resetDSNAllowedWithOptions(dsn, options))
	return db, nil
}

// Migrate only creates absent tables. It never deletes, truncates, or reseeds data.
func Migrate(ctx context.Context, db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS maintenance_resets (
			reset_id VARCHAR(40) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
			merchant_id VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
			payload JSON NOT NULL,
			created_at DATETIME(6) NOT NULL,
			INDEX idx_maintenance_merchant (merchant_id,created_at)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin`,
		`CREATE TABLE IF NOT EXISTS merchant_reset_watermarks (
			merchant_id VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
			reset_id VARCHAR(40) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
			reset_at DATETIME(6) NOT NULL,
			data_version BIGINT NOT NULL
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin`,
		`CREATE TABLE IF NOT EXISTS auth_sessions (
			session_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
			identity_id VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
			created_at DATETIME(6) NOT NULL,
			expires_at DATETIME(6) NOT NULL,
			revoked_at DATETIME(6) NULL,
			INDEX idx_auth_identity (identity_id,created_at),
			INDEX idx_auth_expiry (expires_at)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin`,
		`CREATE TABLE IF NOT EXISTS auth_session_locks (id TINYINT PRIMARY KEY) ENGINE=InnoDB`,
		`INSERT IGNORE INTO auth_session_locks(id) VALUES (1)`,
		`CREATE TABLE IF NOT EXISTS merchants (
			id VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
			name VARCHAR(100) NOT NULL
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin`,
		`CREATE TABLE IF NOT EXISTS orders (
			id VARCHAR(40) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
			merchant_id VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
			amount_cents BIGINT NOT NULL CHECK (amount_cents > 0),
			status VARCHAR(16) NOT NULL,
			FOREIGN KEY (merchant_id) REFERENCES merchants(id),
			INDEX idx_order_merchant (merchant_id, id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin`,
		`CREATE TABLE IF NOT EXISTS refunds (
			id VARCHAR(40) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
			order_id VARCHAR(40) CHARACTER SET ascii COLLATE ascii_bin NOT NULL UNIQUE,
			merchant_id VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
			amount_cents BIGINT NOT NULL CHECK (amount_cents > 0),
			provider_request_id VARCHAR(40) CHARACTER SET ascii COLLATE ascii_bin NOT NULL UNIQUE,
			reason VARCHAR(800) NOT NULL,
			status VARCHAR(16) NOT NULL,
			message VARCHAR(500) NOT NULL,
			created_at DATETIME(6) NOT NULL,
			updated_at DATETIME(6) NOT NULL,
			FOREIGN KEY (order_id) REFERENCES orders(id),
			FOREIGN KEY (merchant_id) REFERENCES merchants(id),
			INDEX idx_refund_merchant (merchant_id, created_at)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin`,
		`CREATE TABLE IF NOT EXISTS idempotency_keys (
			merchant_id VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
			idempotency_key VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
			order_id VARCHAR(40) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
			reason VARCHAR(800) NOT NULL,
			refund_id VARCHAR(40) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
			PRIMARY KEY (merchant_id, idempotency_key),
			FOREIGN KEY (refund_id) REFERENCES refunds(id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin`,
		`CREATE TABLE IF NOT EXISTS channel_scenarios (
			order_id VARCHAR(40) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
			mode VARCHAR(32) NOT NULL
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin`,
		`CREATE TABLE IF NOT EXISTS channel_requests (
			provider_request_id VARCHAR(40) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
			order_id VARCHAR(40) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
			amount_cents BIGINT NOT NULL,
			status VARCHAR(16) NOT NULL,
			message VARCHAR(500) NOT NULL,
			mode VARCHAR(32) NOT NULL,
			submit_calls BIGINT NOT NULL DEFAULT 0,
			query_calls BIGINT NOT NULL DEFAULT 0,
			effect_count BIGINT NOT NULL DEFAULT 0,
			created_at DATETIME(6) NOT NULL,
			updated_at DATETIME(6) NOT NULL
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin`,
		`CREATE TABLE IF NOT EXISTS outbox_events (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			event_id VARCHAR(40) NOT NULL UNIQUE,
			merchant_id VARCHAR(16) NOT NULL,
			refund_id VARCHAR(40) NOT NULL,
			payload JSON NOT NULL,
			created_at DATETIME(6) NOT NULL,
			published_at DATETIME(6) NULL,
			INDEX idx_outbox_pending (published_at, id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin`,
	}
	for i, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
	}
	return nil
}

type seedOrder struct {
	id, merchant string
	amount       int64
	status, mode string
}

var seedOrders = []seedOrder{
	{"A-1001", "A", 19900, "PAID", "SUCCESS"},
	{"A-1002", "A", 8800, "PAID", "LOST_RESPONSE"},
	{"A-1003", "A", 5600, "PAID", "REJECT"},
	{"A-1004", "A", 12900, "UNPAID", "SUCCESS"},
	{"B-2001", "B", 25600, "PAID", "SUCCESS"},
}

// Seed is idempotent and deliberately does not overwrite existing rows.
func Seed(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT IGNORE INTO merchants (id,name) VALUES ('A','商户 A'),('B','商户 B')`); err != nil {
		return err
	}
	for _, row := range seedOrders {
		if _, err = tx.ExecContext(ctx, `INSERT IGNORE INTO orders(id,merchant_id,amount_cents,status) VALUES (?,?,?,?)`, row.id, row.merchant, row.amount, row.status); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT IGNORE INTO channel_scenarios(order_id,mode) VALUES (?,?)`, row.id, row.mode); err != nil {
			return err
		}
	}
	return tx.Commit()
}
