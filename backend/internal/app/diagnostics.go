package app

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/go-sql-driver/mysql"
)

// Driver messages may contain SQL values, server-supplied text, or malformed
// connection parameters. Retain the actionable error class/number, not values.
func errorDiagnostic(err error) string {
	var mysqlError *mysql.MySQLError
	if errors.As(err, &mysqlError) {
		return fmt.Sprintf("mysql_code=%d", mysqlError.Number)
	}
	return fmt.Sprintf("%T", err)
}

type safeMySQLLogger struct{}

func (safeMySQLLogger) Print(_ ...any) {
	slog.Warn("MySQL driver connection warning")
}
