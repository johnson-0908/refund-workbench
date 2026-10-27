package app

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

// DatabaseDSNFromEnv keeps credentials out of shell-generated DSNs. MYSQL_DSN
// has explicit precedence; otherwise all individual connection fields are
// assembled by the driver's formatter. Errors never echo passwords or DSNs.
func DatabaseDSNFromEnv(lookup func(string) (string, bool)) (string, error) {
	if value, exists := lookup("MYSQL_DSN"); exists {
		if value == "" {
			return "", fmt.Errorf("MYSQL_DSN must not be empty")
		}
		if _, err := mysql.ParseDSN(value); err != nil {
			return "", fmt.Errorf("MYSQL_DSN is not a valid MySQL DSN")
		}
		return value, nil
	}
	values := map[string]string{}
	configured := false
	for _, name := range []string{"MYSQL_HOST", "MYSQL_PORT", "MYSQL_DATABASE", "MYSQL_USER", "MYSQL_PASSWORD"} {
		if value, exists := lookup(name); exists {
			values[name] = value
			configured = true
		}
	}
	if !configured {
		return DefaultDSN, nil
	}
	host, user, password := values["MYSQL_HOST"], values["MYSQL_USER"], values["MYSQL_PASSWORD"]
	if host == "" || user == "" || password == "" {
		return "", fmt.Errorf("MYSQL_HOST, MYSQL_USER and MYSQL_PASSWORD are all required when using individual database fields")
	}
	if strings.ContainsAny(host, " /\\\t\r\n[]?#@") || (strings.Contains(host, ":") && net.ParseIP(host) == nil) {
		return "", fmt.Errorf("MYSQL_HOST must be a bare hostname or IP address")
	}
	port := values["MYSQL_PORT"]
	if port == "" {
		port = "3306"
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return "", fmt.Errorf("MYSQL_PORT must be a numeric port between 1 and 65535")
	}
	database := values["MYSQL_DATABASE"]
	if database == "" {
		database = "refund_demo"
	}
	cfg := mysql.NewConfig()
	cfg.User, cfg.Passwd, cfg.Net, cfg.Addr, cfg.DBName = user, password, "tcp", net.JoinHostPort(host, port), database
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	return cfg.FormatDSN(), nil
}
