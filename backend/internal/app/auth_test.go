package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testOrigin = "http://127.0.0.1:5173"

func authRequest(h http.Handler, method, path string, cookie *http.Cookie, csrf, origin, body string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	r.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		r.Header.Set(key, value)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func loginSession(t *testing.T, h http.Handler, id string, previous *http.Cookie) (*http.Cookie, SessionResponse) {
	t.Helper()
	w := authRequest(h, "POST", "/api/auth/login", previous, "", testOrigin, `{"identity_id":"`+id+`"}`, nil)
	if w.Code != 200 {
		t.Fatalf("login status=%d body=%s", w.Code, w.Body.String())
	}
	var result SessionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookieName {
		t.Fatalf("wrong login cookie count=%d", len(cookies))
	}
	return cookies[0], result
}

func requireAuthCode(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("want %d got %d body=%s", status, w.Code, w.Body.String())
	}
	if code != "" && !strings.Contains(w.Body.String(), `"code":"`+code+`"`) {
		t.Fatalf("expected code %s got %s", code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("auth responses missing security cache headers")
	}
}

func TestAuthSecureDefaultsAndTokenEncoding(t *testing.T) {
	h := NewHandler(NewService(nil, nil))
	for _, path := range []string{"/api/orders", "/api/activity", "/api/refunds/a", "/api/auth/session"} {
		w := authRequest(h, "GET", path, nil, "", "", "", nil)
		requireAuthCode(t, w, 401, "UNAUTHORIZED")
		w = authRequest(h, "GET", path, nil, "", "", "", map[string]string{"Authorization": "Bearer demo-a-write"})
		requireAuthCode(t, w, 401, "UNAUTHORIZED")
	}
	token := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	if !validSessionToken(token) || len(hashSession(token)) != 64 || len(csrfForSession(token)) != 43 {
		t.Fatal("token encoding or derivation incorrect")
	}
	if csrfForSession(token) != csrfForSession(token) || csrfForSession(token) == token {
		t.Fatal("CSRF not deterministic and separate")
	}
	for _, bad := range []string{"", token + "=", token[:42], strings.Repeat("x", 64), strings.Repeat("+", 43)} {
		if validSessionToken(bad) {
			t.Fatalf("accepted malformed token %q", bad)
		}
	}
}

func TestAuthOriginsAreExactAndRequired(t *testing.T) {
	h := NewHandler(NewService(nil, nil))
	for _, origin := range []string{"", "null", "http://evil.invalid", "http://127.0.0.1:5173.evil.invalid", "http://127.0.0.1:5173/", "https://127.0.0.1:5173", "http://localhost:18080", "http://127.0.0.1:5173, http://evil.invalid"} {
		w := authRequest(h, "POST", "/api/auth/login", nil, "", origin, `{"identity_id":"a-write"}`, nil)
		requireAuthCode(t, w, 403, "CSRF_REJECTED")
	}
	r := httptest.NewRequest("POST", "/api/auth/login", nil)
	r.Header.Add("Origin", testOrigin)
	r.Header.Add("Origin", testOrigin)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	requireAuthCode(t, w, 403, "CSRF_REJECTED")
	for _, origin := range []string{testOrigin, "http://localhost:5173", "http://127.0.0.1:18080"} {
		r := httptest.NewRequest("POST", "/", nil)
		r.Header.Set("Origin", origin)
		if !validOrigin(r) {
			t.Fatal("valid origin rejected")
		}
	}
}

func TestMySQLAuthLoginSessionPersistsOnlyHashAndRestores(t *testing.T) {
	db, s, dsn := fixture(t)
	h := NewHandler(s)
	cookie, login := loginSession(t, h, "a-write", nil)
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Secure || cookie.Path != "/" || cookie.MaxAge != 28800 || !validSessionToken(cookie.Value) {
		t.Fatalf("cookie security flags=%+v", *cookie)
	}
	if login.User.IdentityID != "a-write" || login.User.MerchantID != "A" || login.User.MerchantName != "华东商贸" || login.User.Role != "operator" || len(login.User.Permissions) != 4 {
		t.Fatalf("server user=%+v", login.User)
	}
	if delta := time.Until(login.ExpiresAt); delta < sessionLifetime-time.Minute || delta > sessionLifetime {
		t.Fatalf("expiry interval=%s", delta)
	}
	var storedHash, identity string
	if err := db.QueryRow("SELECT session_hash,identity_id FROM auth_sessions").Scan(&storedHash, &identity); err != nil {
		t.Fatal(err)
	}
	if storedHash != hashSession(cookie.Value) || storedHash == cookie.Value || identity != "a-write" {
		t.Fatal("incorrect hashed persistence")
	}
	w := authRequest(h, "GET", "/api/auth/session", cookie, "", "", "", nil)
	requireAuthCode(t, w, 200, "")
	var restored SessionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &restored); err != nil {
		t.Fatal(err)
	}
	if restored.CSRFToken != login.CSRFToken || !restored.ExpiresAt.Equal(login.ExpiresAt) {
		t.Fatal("session restore changed secrets or expiry")
	}
	// Close and reopen DB + all auth objects; no process memory is required.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenDB(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := Migrate(context.Background(), reopened); err != nil {
		t.Fatal(err)
	}
	if err := Seed(context.Background(), reopened); err != nil {
		t.Fatal(err)
	}
	h = NewHandler(NewService(reopened, NewPersistentGateway(reopened)))
	w = authRequest(h, "GET", "/api/auth/session", cookie, "", "", "", nil)
	requireAuthCode(t, w, 200, "")
	if !strings.Contains(w.Body.String(), login.CSRFToken) {
		t.Fatal("session not recovered after reopen")
	}
}

func TestMySQLAuthRejectsForgedElevatedAndDisabledLegacyCredentials(t *testing.T) {
	db, s, _ := fixture(t)
	h := NewHandler(s)
	for _, body := range []string{`{"identity_id":"a-write","merchant_id":"B"}`, `{"identity_id":"a-write","role":"operator"}`, `{"identity_id":"a-read","permissions":["refunds:create"]}`, `{"identity_id":"root"}`, `{"identity_id":1}`, `null`, `{"identity_id":"a-read"}{}`} {
		w := authRequest(h, "POST", "/api/auth/login", nil, "", testOrigin, body, nil)
		requireAuthCode(t, w, 400, "VALIDATION_ERROR")
		if len(w.Result().Cookies()) != 0 {
			t.Fatal("invalid login issued cookie")
		}
	}
	if n := count(t, db, "SELECT COUNT(*) FROM auth_sessions"); n != 0 {
		t.Fatalf("invalid requests created sessions=%d", n)
	}
	cookie, login := loginSession(t, h, "a-read", nil)
	if login.User.Role != "viewer" || len(login.User.Permissions) != 2 || strings.Contains(strings.Join(login.User.Permissions, ","), "create") {
		t.Fatalf("viewer permissions=%+v", login.User)
	}
	for _, token := range []string{"Bearer demo-a-write", "Bearer forged", "Basic invalid", ""} {
		w := authRequest(h, "GET", "/api/orders", cookie, login.CSRFToken, "", "", map[string]string{"Authorization": token})
		requireAuthCode(t, w, 401, "UNAUTHORIZED")
	}
	for _, value := range []string{"forged", hashSession(cookie.Value), base64.RawURLEncoding.EncodeToString(make([]byte, 32))} {
		w := authRequest(h, "GET", "/api/auth/session", &http.Cookie{Name: sessionCookieName, Value: value}, "", "", "", nil)
		requireAuthCode(t, w, 401, "UNAUTHORIZED")
	}
	legacy := NewHandlerWithOptions(s, HandlerOptions{EnableDemoBearer: true})
	w := authRequest(legacy, "GET", "/api/orders", nil, "", "", "", map[string]string{"Authorization": "Bearer demo-a-write"})
	requireAuthCode(t, w, 200, "")
	w = authRequest(legacy, "POST", "/api/orders/A-1001/refunds", nil, "", "", `{"reason":"兼容评测"}`, map[string]string{"Authorization": "Bearer demo-a-write", "Idempotency-Key": "legacy-auth-1"})
	requireAuthCode(t, w, 201, "")
}

func TestMySQLAuthCSRFOriginPermissionsAndMerchantIsolation(t *testing.T) {
	db, s, _ := fixture(t)
	h := NewHandler(s)
	a, aSession := loginSession(t, h, "a-write", nil)
	reader, rSession := loginSession(t, h, "a-read", nil)
	b, bSession := loginSession(t, h, "b-write", nil)
	for _, tc := range []struct{ csrf, origin string }{
		{"", testOrigin}, {"wrong", testOrigin}, {aSession.CSRFToken, ""}, {aSession.CSRFToken, "http://evil.invalid"}, {aSession.CSRFToken, "null"},
	} {
		w := authRequest(h, "POST", "/api/orders/A-1001/refunds", a, tc.csrf, tc.origin, `{"reason":"取消"}`, map[string]string{"Idempotency-Key": "csrf-test"})
		requireAuthCode(t, w, 403, "CSRF_REJECTED")
	}
	if n := count(t, db, "SELECT COUNT(*) FROM refunds"); n != 0 {
		t.Fatalf("CSRF wrote refunds=%d", n)
	}
	w := authRequest(h, "POST", "/api/orders/A-1001/refunds", a, aSession.CSRFToken, testOrigin, `{"reason":"取消"}`, map[string]string{"Idempotency-Key": "cookie-create"})
	requireAuthCode(t, w, 201, "")
	aRefund := decodeRefund(t, w.Body.Bytes())
	w = authRequest(h, "POST", "/api/orders/B-2001/refunds", b, bSession.CSRFToken, testOrigin, `{"reason":"取消"}`, map[string]string{"Idempotency-Key": "cookie-create"})
	requireAuthCode(t, w, 201, "")
	bRefund := decodeRefund(t, w.Body.Bytes())
	for _, tc := range []struct {
		method, path, body string
		status             int
		code               string
	}{
		{"POST", "/api/orders/A-1002/refunds", `{"reason":"取消"}`, 403, "FORBIDDEN"},
		{"POST", "/api/orders/A-1002/refunds", `{"invalid":true}`, 403, "FORBIDDEN"},
		{"POST", "/api/orders/B-2001/refunds", `{"invalid":true}`, 404, "NOT_FOUND"},
		{"POST", "/api/refunds/" + aRefund.RefundID + "/reconcile", "", 403, "FORBIDDEN"},
		{"POST", "/api/refunds/" + bRefund.RefundID + "/reconcile", "", 404, "NOT_FOUND"},
		{"GET", "/api/refunds/" + bRefund.RefundID, "", 404, "NOT_FOUND"},
		{"GET", "/api/orders/B-2001", "", 404, "NOT_FOUND"},
		{"GET", "/api/refunds/" + aRefund.RefundID, "", 200, ""},
	} {
		w := authRequest(h, tc.method, tc.path, reader, rSession.CSRFToken, testOrigin, tc.body, map[string]string{"Idempotency-Key": "reader-key"})
		requireAuthCode(t, w, tc.status, tc.code)
	}
	for _, tc := range []struct {
		cookie         *http.Cookie
		csrf, merchant string
		size           int
	}{{a, aSession.CSRFToken, "A", 4}, {reader, rSession.CSRFToken, "A", 4}, {b, bSession.CSRFToken, "B", 1}} {
		w := authRequest(h, "GET", "/api/orders?merchant_id=forged", tc.cookie, tc.csrf, "", "", nil)
		requireAuthCode(t, w, 200, "")
		var payload struct {
			Items []Order `json:"items"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Items) != tc.size {
			t.Fatal("wrong merchant list size")
		}
		for _, o := range payload.Items {
			if o.MerchantID != tc.merchant {
				t.Fatal("merchant leaked")
			}
		}
	}
}

func TestMySQLAuthRotationLogoutExpiryAndCrossTabBinding(t *testing.T) {
	db, s, _ := fixture(t)
	h := NewHandler(s)
	old, oldSession := loginSession(t, h, "a-write", nil)
	next, nextSession := loginSession(t, h, "b-write", old)
	if next.Value == old.Value || nextSession.CSRFToken == oldSession.CSRFToken {
		t.Fatal("login did not rotate secrets")
	}
	w := authRequest(h, "GET", "/api/auth/session", old, "", "", "", nil)
	requireAuthCode(t, w, 401, "UNAUTHORIZED")
	w = authRequest(h, "GET", "/api/orders", next, oldSession.CSRFToken, "", "", nil)
	requireAuthCode(t, w, 403, "SESSION_CHANGED")
	w = authRequest(h, "GET", "/api/orders", next, nextSession.CSRFToken, "", "", nil)
	requireAuthCode(t, w, 200, "")
	w = authRequest(h, "POST", "/api/auth/logout", next, oldSession.CSRFToken, testOrigin, "", nil)
	requireAuthCode(t, w, 403, "CSRF_REJECTED")
	w = authRequest(h, "POST", "/api/auth/logout", next, nextSession.CSRFToken, testOrigin, "", nil)
	requireAuthCode(t, w, 200, "")
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].MaxAge != -1 || cookies[0].Value != "" {
		t.Fatal("logout did not expire cookie")
	}
	w = authRequest(h, "GET", "/api/auth/session", next, "", "", "", nil)
	requireAuthCode(t, w, 401, "UNAUTHORIZED")
	expiring, _ := loginSession(t, h, "a-write", nil)
	if _, err := db.Exec("UPDATE auth_sessions SET expires_at=DATE_SUB(UTC_TIMESTAMP(6),INTERVAL 1 SECOND) WHERE session_hash=?", hashSession(expiring.Value)); err != nil {
		t.Fatal(err)
	}
	w = authRequest(h, "GET", "/api/auth/session", expiring, "", "", "", nil)
	requireAuthCode(t, w, 401, "UNAUTHORIZED")
	newCookie, _ := loginSession(t, h, "a-read", nil)
	if n := count(t, db, "SELECT COUNT(*) FROM auth_sessions WHERE revoked_at IS NOT NULL OR expires_at<=UTC_TIMESTAMP(6)"); n != 0 {
		t.Fatalf("expired rows not cleaned=%d", n)
	}
	if _, err := db.Exec("UPDATE auth_sessions SET identity_id='forged-admin' WHERE session_hash=?", hashSession(newCookie.Value)); err != nil {
		t.Fatal(err)
	}
	w = authRequest(h, "GET", "/api/auth/session", newCookie, "", "", "", nil)
	requireAuthCode(t, w, 401, "UNAUTHORIZED")
}

func TestMySQLAuthLoginRateLimitIgnoresForwardedFor(t *testing.T) {
	db, s, _ := fixture(t)
	h := NewHandler(s)
	for i := range 21 {
		w := authRequest(h, "POST", "/api/auth/login", nil, "", testOrigin, `{"identity_id":"invalid"}`, map[string]string{"X-Forwarded-For": fmt.Sprintf("192.0.2.%d", i)})
		if i < 20 {
			requireAuthCode(t, w, 400, "VALIDATION_ERROR")
		} else {
			requireAuthCode(t, w, 429, "RATE_LIMITED")
			if w.Header().Get("Retry-After") != "60" {
				t.Fatal("missing retry hint")
			}
		}
	}
	if n := count(t, db, "SELECT COUNT(*) FROM auth_sessions"); n != 0 {
		t.Fatalf("rate-limit test created sessions=%d", n)
	}
}

func TestMySQLAuthConcurrentLoginSessionsRemainBounded(t *testing.T) {
	db, s, _ := fixture(t)
	h := NewHandler(s)
	const workers = 16
	results := make(chan *httptest.ResponseRecorder, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			results <- authRequest(h, "POST", "/api/auth/login", nil, "", testOrigin, `{"identity_id":"a-write"}`, nil)
		})
	}
	wg.Wait()
	close(results)
	active, inactive := 0, 0
	for result := range results {
		requireAuthCode(t, result, 200, "")
		cookies := result.Result().Cookies()
		if len(cookies) != 1 {
			t.Fatal("missing session cookie")
		}
		w := authRequest(h, "GET", "/api/auth/session", cookies[0], "", "", "", nil)
		if w.Code == 200 {
			active++
		} else if w.Code == 401 {
			inactive++
		} else {
			t.Fatalf("unexpected session status=%d", w.Code)
		}
	}
	if active != 8 || inactive != 8 {
		t.Fatalf("active=%d inactive=%d", active, inactive)
	}
	if n := count(t, db, "SELECT COUNT(*) FROM auth_sessions"); n != 8 {
		t.Fatalf("session storage not bounded=%d", n)
	}
}

func TestMySQLAuthOuterMiddlewareProtectsProjectionAndCommitFailure(t *testing.T) {
	db, s, _ := fixture(t)
	var reached atomic.Int64
	h := NewHandlerWithOptions(s, HandlerOptions{Wrap: func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/activity" {
				reached.Add(1)
				id, err := Authenticate(r)
				if err != nil {
					t.Error("projection reached without principal")
				}
				writeJSON(w, 200, map[string]string{"merchant_id": id.MerchantID})
				return
			}
			next.ServeHTTP(w, r)
		})
	}})
	w := authRequest(h, "GET", "/api/activity", nil, "", "", "", nil)
	requireAuthCode(t, w, 401, "UNAUTHORIZED")
	if reached.Load() != 0 {
		t.Fatal("unauthorized request reached projection")
	}
	cookie, session := loginSession(t, h, "b-write", nil)
	w = authRequest(h, "GET", "/api/activity?merchant_id=A", cookie, session.CSRFToken, "", "", nil)
	requireAuthCode(t, w, 200, "")
	if !strings.Contains(w.Body.String(), `"merchant_id":"B"`) || reached.Load() != 1 {
		t.Fatal("projection scope not derived from context")
	}
	if _, err := db.Exec("DELETE FROM auth_session_locks WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec("INSERT IGNORE INTO auth_session_locks(id) VALUES(1)") })
	w = authRequest(h, "POST", "/api/auth/login", nil, "", testOrigin, `{"identity_id":"a-write"}`, nil)
	requireAuthCode(t, w, 503, "UNAVAILABLE")
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("failed transaction issued cookie")
	}
	if n := count(t, db, "SELECT COUNT(*) FROM auth_sessions"); n != 1 {
		t.Fatal("failed transaction changed sessions")
	}
}
