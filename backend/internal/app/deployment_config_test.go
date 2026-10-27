package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

func lookupValues(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) { value, ok := values[name]; return value, ok }
}

func TestDeploymentOriginsRejectUnsafeConfiguration(t *testing.T) {
	for _, origins := range [][]string{nil, DefaultAuthOrigins(), {"https://erp.example.invalid"}, {"http://192.0.2.3:8080", "https://[::1]:8443"}} {
		if err := ValidateAuthOrigins(origins); err != nil {
			t.Fatalf("valid configuration rejected: %v", err)
		}
	}
	for _, origin := range []string{"", "*", "null", "https://*.example.invalid", "https://erp.example.invalid/", "https://erp.example.invalid/a", "https://erp.example.invalid?", "https://erp.example.invalid?a=1", "https://erp.example.invalid#", "https://erp.example.invalid#x", "https://user:secret@erp.example.invalid", "ftp://erp.example.invalid", "https://erp.example.invalid:", "https://erp.example.invalid:0", "https://erp.example.invalid:65536", "https://erp.example.invalid:no", "https://企业.example", " https://erp.example.invalid", "https://erp.example.invalid\n", "https://erp.example.invalid\\evil"} {
		if err := ValidateAuthOrigins([]string{origin}); err == nil {
			t.Fatalf("invalid origin accepted %q", origin)
		}
	}
	if ValidateAuthOrigins([]string{}) == nil {
		t.Fatal("explicit empty whitelist accepted")
	}
	h := NewHandlerWithOptions(NewService(nil, nil), HandlerOptions{AllowedOrigins: []string{"*"}, EnableDemoBearer: true})
	w := authRequest(h, "GET", "/api/orders", nil, "", "", "", map[string]string{"Authorization": "Bearer demo-a-write"})
	requireAuthCode(t, w, 503, "UNAVAILABLE")
}

func TestDeploymentCookieTransportConfiguration(t *testing.T) {
	for _, tc := range []struct {
		origins       []string
		secure, valid bool
	}{
		{nil, false, true}, {nil, true, true}, {[]string{"https://erp.example.invalid"}, true, true},
		{[]string{"https://erp.example.invalid"}, false, false}, {[]string{"http://192.0.2.1:8080"}, false, true},
		{[]string{"http://192.0.2.1:8080"}, true, false}, {[]string{"http://127.0.0.1:8080"}, true, true},
		{[]string{"https://erp.example.invalid", "http://192.0.2.1:8080"}, true, false},
	} {
		if err := ValidateAuthHTTPConfig(tc.origins, tc.secure); (err == nil) != tc.valid {
			t.Fatalf("secure=%v valid=%v err=%v", tc.secure, tc.valid, err)
		}
	}
	h := NewHandlerWithOptions(NewService(nil, nil), HandlerOptions{AllowedOrigins: []string{"https://erp.example.invalid"}})
	w := authRequest(h, "GET", "/api/health", nil, "", "", "", nil)
	requireAuthCode(t, w, 503, "UNAVAILABLE")
}

func TestDeploymentRequestDeadlineBoundsDatabaseWork(t *testing.T) {
	var remaining time.Duration
	h := NewHandlerWithOptions(NewService(nil, nil), HandlerOptions{EnableDemoBearer: true, Wrap: func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			deadline, ok := r.Context().Deadline()
			if !ok {
				t.Error("request has no context deadline")
			}
			remaining = time.Until(deadline)
			writeJSON(w, 200, map[string]string{"status": "ok"})
		})
	}})
	w := authRequest(h, "GET", "/api/activity", nil, "", "", "", map[string]string{"Authorization": "Bearer demo-a-write"})
	requireAuthCode(t, w, 200, "")
	if remaining <= 14*time.Second || remaining > requestTimeout {
		t.Fatalf("request timeout=%s", remaining)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h = withRequestDeadline(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !errors.Is(r.Context().Err(), context.Canceled) {
			t.Error("parent cancellation was detached")
		}
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil).WithContext(ctx))
}

func TestDeploymentDiagnosticsNeverEchoSecrets(t *testing.T) {
	var log bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&log, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	secret := "dummy-credential-DO-NOT-LOG"
	w := httptest.NewRecorder()
	writeError(w, &mysql.MySQLError{Number: 1045, Message: secret})
	writeError(w, errors.New(secret))
	safeMySQLLogger{}.Print(secret)
	if strings.Contains(log.String(), secret) || strings.Contains(w.Body.String(), secret) {
		t.Fatal("diagnostics echoed secret")
	}
	if !strings.Contains(log.String(), "mysql_code=1045") {
		t.Fatal("diagnostic lost actionable MySQL code")
	}
	if _, err := OpenDB("invalid-" + secret); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatal("invalid DSN echoed secret or was accepted")
	}
}

func TestDeploymentOriginsReplaceDefaultsAndIgnoreForwardedHeaders(t *testing.T) {
	a := newAuthService(nil, false)
	origins := []string{"https://erp.example.invalid"}
	if err := a.configureHTTP(origins, true); err != nil {
		t.Fatal(err)
	}
	origins[0] = "http://evil.invalid"
	for _, tc := range []struct {
		origin  string
		allowed bool
	}{
		{"https://erp.example.invalid", true}, {testOrigin, false}, {"http://evil.invalid", false},
		{"https://erp.example.invalid/", false}, {"https://erp.example.invalid:443", false}, {"https://erp.example.invalid.evil.invalid", false}, {"", false},
	} {
		r := httptest.NewRequest("POST", "/api/auth/login", nil)
		if tc.origin != "" {
			r.Header.Set("Origin", tc.origin)
		}
		r.Header.Set("X-Forwarded-Host", "erp.example.invalid")
		r.Header.Set("X-Forwarded-Proto", "https")
		if originAllowed(r, a.origins) != tc.allowed {
			t.Fatalf("origin=%q allowed=%v", tc.origin, tc.allowed)
		}
	}
	r := httptest.NewRequest("POST", "/api/auth/login", nil)
	r.Header.Add("Origin", "https://erp.example.invalid")
	r.Header.Add("Origin", "https://erp.example.invalid")
	if originAllowed(r, a.origins) {
		t.Fatal("duplicate Origin accepted")
	}
}

func TestDeploymentDatabaseFieldsAndSecretSafeErrors(t *testing.T) {
	dsn, err := DatabaseDSNFromEnv(lookupValues(nil))
	if err != nil || dsn != DefaultDSN {
		t.Fatal("local defaults changed")
	}
	password := "dummy@:/?#$%&=()+ secret"
	values := map[string]string{"MYSQL_HOST": "mysql", "MYSQL_USER": "refundapp", "MYSQL_PASSWORD": password}
	dsn, err = DatabaseDSNFromEnv(lookupValues(values))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal("generated DSN cannot be parsed")
	}
	if cfg.User != "refundapp" || cfg.Passwd != password || cfg.Addr != "mysql:3306" || cfg.DBName != "refund_demo" || !cfg.ParseTime || cfg.Loc != time.UTC {
		t.Fatal("connection fields failed roundtrip")
	}
	values["MYSQL_HOST"], values["MYSQL_PORT"], values["MYSQL_DATABASE"] = "::1", "3307", "refund_test"
	dsn, err = DatabaseDSNFromEnv(lookupValues(values))
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ = mysql.ParseDSN(dsn)
	if cfg.Addr != "[::1]:3307" || cfg.DBName != "refund_test" {
		t.Fatal("IPv6 fields failed roundtrip")
	}
	values["MYSQL_DSN"] = DefaultDSN
	dsn, err = DatabaseDSNFromEnv(lookupValues(values))
	if err != nil || dsn != DefaultDSN {
		t.Fatal("explicit DSN precedence changed")
	}
	for _, invalid := range []map[string]string{
		{"MYSQL_DSN": ""}, {"MYSQL_DSN": "dummy-secret-invalid"}, {"MYSQL_USER": "refundapp"}, {"MYSQL_PASSWORD": password},
		{"MYSQL_HOST": "mysql:3306", "MYSQL_USER": "refundapp", "MYSQL_PASSWORD": password},
		{"MYSQL_HOST": "[::1]", "MYSQL_USER": "refundapp", "MYSQL_PASSWORD": password},
		{"MYSQL_HOST": "mysql/evil", "MYSQL_USER": "refundapp", "MYSQL_PASSWORD": password},
		{"MYSQL_HOST": "mysql", "MYSQL_USER": "refundapp", "MYSQL_PASSWORD": password, "MYSQL_PORT": "65536"},
		{"MYSQL_HOST": "mysql", "MYSQL_USER": "refundapp", "MYSQL_PASSWORD": password, "MYSQL_PORT": "0"},
		{"MYSQL_HOST": "mysql", "MYSQL_USER": "refundapp", "MYSQL_PASSWORD": password, "MYSQL_PORT": "abc"},
	} {
		_, err := DatabaseDSNFromEnv(lookupValues(invalid))
		if err == nil {
			t.Fatal("invalid database config accepted")
		}
		if strings.Contains(err.Error(), password) || strings.Contains(err.Error(), "dummy-secret") {
			t.Fatal("configuration error leaked secret")
		}
	}
}

func TestDeploymentResetProvenanceExactDockerOptIn(t *testing.T) {
	for _, tc := range []struct {
		address, database string
		allowed           bool
	}{
		{"mysql:3306", "refund_demo", true}, {"mysql:3307", "refund_demo", false}, {"mysql:3306", "refund_test", false},
		{"mysql:3306", "refund_acceptance", false}, {"mysql:3306", "business", false}, {"MYSQL:3306", "refund_demo", false},
		{"mysql.:3306", "refund_demo", false}, {"mysql.evil:3306", "refund_demo", false}, {"192.0.2.1:3306", "refund_demo", false},
	} {
		dsn := "refundapp:dummy@tcp(" + tc.address + ")/" + tc.database
		if resetDSNAllowed(dsn) {
			t.Fatal("Docker reset enabled without opt-in")
		}
		if got := resetDSNAllowedWithOptions(dsn, DatabaseOptions{AllowDockerDemoReset: true}); got != tc.allowed {
			t.Fatalf("address=%s database=%s allowed=%v", tc.address, tc.database, got)
		}
	}
	if resetDSNAllowedWithOptions("refundapp:dummy@unix(mysql:3306)/refund_demo", DatabaseOptions{AllowDockerDemoReset: true}) {
		t.Fatal("non-tcp endpoint accepted")
	}
	if !resetDSNAllowedWithOptions(DefaultDSN, DatabaseOptions{AllowDockerDemoReset: true}) {
		t.Fatal("local provenance changed")
	}
}

func TestMySQLDeploymentSecureCookieAndCustomOrigin(t *testing.T) {
	_, service, _ := fixture(t)
	const origin = "https://erp.example.invalid"
	h := NewHandlerWithOptions(service, HandlerOptions{AllowedOrigins: []string{origin}, SessionCookieSecure: true})
	for _, rejected := range []string{testOrigin, "http://erp.example.invalid", "https://erp.example.invalid.evil.invalid", ""} {
		w := authRequest(h, "POST", "/api/auth/login", nil, "", rejected, `{"identity_id":"a-write"}`, map[string]string{"X-Forwarded-Host": "erp.example.invalid", "X-Forwarded-Proto": "https"})
		requireAuthCode(t, w, 403, "CSRF_REJECTED")
	}
	w := authRequest(h, "POST", "/api/auth/login", nil, "", origin, `{"identity_id":"a-write"}`, nil)
	requireAuthCode(t, w, 200, "")
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("secure login cookie flags incorrect")
	}
	var session SessionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(session.User.Permissions, ","), "data:reset") {
		t.Fatal("deployment options enabled reset implicitly")
	}
	cookie := cookies[0]
	w = authRequest(h, "POST", "/api/orders/A-1001/refunds", cookie, session.CSRFToken, testOrigin, `{"reason":"取消"}`, map[string]string{"Idempotency-Key": "secure-origin"})
	requireAuthCode(t, w, 403, "CSRF_REJECTED")
	w = authRequest(h, "POST", "/api/orders/A-1001/refunds", cookie, session.CSRFToken, origin, `{"reason":"取消"}`, map[string]string{"Idempotency-Key": "secure-origin"})
	requireAuthCode(t, w, 201, "")
	w = authRequest(h, "POST", "/api/auth/logout", cookie, session.CSRFToken, origin, "", nil)
	requireAuthCode(t, w, 200, "")
	cookies = w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || cookies[0].MaxAge != -1 {
		t.Fatal("secure logout cookie incorrect")
	}
}
