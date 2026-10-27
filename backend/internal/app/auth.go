package app

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"
)

const sessionCookieName = "erp_session"
const sessionLifetime = 8 * time.Hour
const maxIdentitySessions = 8

type SessionUser struct {
	IdentityID   string   `json:"identity_id"`
	MerchantID   string   `json:"merchant_id"`
	MerchantName string   `json:"merchant_name"`
	DisplayName  string   `json:"display_name"`
	Role         string   `json:"role"`
	Permissions  []string `json:"permissions"`
}

type SessionResponse struct {
	User      SessionUser `json:"user"`
	CSRFToken string      `json:"csrf_token"`
	ExpiresAt time.Time   `json:"expires_at"`
}

type principalKey struct{}
type principal struct {
	User        SessionUser
	SessionHash string
	CSRFToken   string
	ExpiresAt   time.Time
	Legacy      bool
}

func userForIdentity(id string) (SessionUser, bool) {
	u := SessionUser{IdentityID: id, Permissions: []string{"orders:read", "refunds:read"}}
	switch id {
	case "a-write":
		u.MerchantID, u.MerchantName, u.DisplayName, u.Role = "A", "华东商贸", "财务经办", "operator"
	case "a-read":
		u.MerchantID, u.MerchantName, u.DisplayName, u.Role = "A", "华东商贸", "财务查看", "viewer"
	case "b-write":
		u.MerchantID, u.MerchantName, u.DisplayName, u.Role = "B", "华南商贸", "财务经办", "operator"
	default:
		return SessionUser{}, false
	}
	if u.Role == "operator" {
		u.Permissions = append(u.Permissions, "refunds:create", "refunds:reconcile")
	}
	return u, true
}

type loginBucket struct {
	start time.Time
	count int
}
type authService struct {
	db             *sql.DB
	allowLegacy    bool
	allowDataReset bool
	origins        map[string]struct{}
	secureCookie   bool
	mu             sync.Mutex
	clients        map[string]loginBucket
	global         loginBucket
}

func newAuthService(db *sql.DB, legacy bool, dataReset ...bool) *authService {
	enabled := len(dataReset) > 0 && dataReset[0] && ValidateDataResetDB(db) == nil
	a := &authService{db: db, allowLegacy: legacy, allowDataReset: enabled, clients: map[string]loginBucket{}}
	_ = a.configureHTTP(nil, false)
	return a
}

func (a *authService) sessionUser(identity string) (SessionUser, bool) {
	user, ok := userForIdentity(identity)
	if ok && a.allowDataReset && user.Role == "operator" {
		user.Permissions = append(user.Permissions, "data:reset")
	}
	return user, ok
}

func (a *authService) allowLogin(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	// X-Forwarded-For is intentionally ignored, including behind a reverse proxy.
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	for key, bucket := range a.clients {
		if now.Sub(bucket.start) >= time.Minute {
			delete(a.clients, key)
		}
	}
	if now.Sub(a.global.start) >= time.Minute {
		a.global = loginBucket{start: now}
	}
	bucket, exists := a.clients[host]
	if !exists {
		if len(a.clients) >= 256 {
			return false
		}
		bucket = loginBucket{start: now}
	}
	if bucket.count >= 20 || a.global.count >= 120 {
		return false
	}
	bucket.count++
	a.global.count++
	a.clients[host] = bucket
	return true
}

func validOrigin(r *http.Request) bool {
	a := newAuthService(nil, false)
	return originAllowed(r, a.origins)
}

func hashSession(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func validSessionToken(token string) bool {
	if len(token) != 43 {
		return false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(token)
	return err == nil && len(decoded) == 32
}

// The 256-bit random session secret acts as this session's HMAC key. The
// database stores only its SHA-256 hash; restoring a session can reproduce the
// CSRF value from the HttpOnly cookie without storing either secret in MySQL.
func csrfForSession(token string) string {
	mac := hmac.New(sha256.New, []byte(token))
	_, _ = mac.Write([]byte("refund-workbench:csrf:v1"))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func unauthorized() error {
	return &APIError{401, "UNAUTHORIZED", "请先登录或重新登录"}
}
func csrfRejected() error {
	return &APIError{403, "CSRF_REJECTED", "请求来源或安全令牌无效，请刷新后重试"}
}

func (a *authService) authenticate(ctx context.Context, r *http.Request) (*principal, error) {
	// Never silently fall back to a cookie when an Authorization header exists.
	// This makes disabled legacy credentials unambiguously invalid.
	if len(r.Header.Values("Authorization")) > 0 {
		if !a.allowLegacy || len(r.Header.Values("Authorization")) != 1 {
			return nil, unauthorized()
		}
		identity := ""
		switch r.Header.Get("Authorization") {
		case "Bearer demo-a-write":
			identity = "a-write"
		case "Bearer demo-a-read":
			identity = "a-read"
		case "Bearer demo-b-write":
			identity = "b-write"
		default:
			return nil, unauthorized()
		}
		user, _ := a.sessionUser(identity)
		return &principal{User: user, Legacy: true}, nil
	}
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || !validSessionToken(cookie.Value) {
		return nil, unauthorized()
	}
	var identity string
	var expires time.Time
	hash := hashSession(cookie.Value)
	err = a.db.QueryRowContext(ctx, `SELECT identity_id,expires_at FROM auth_sessions WHERE session_hash=? AND revoked_at IS NULL AND expires_at>UTC_TIMESTAMP(6)`, hash).Scan(&identity, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, unauthorized()
	}
	if err != nil {
		return nil, err
	}
	user, ok := a.sessionUser(identity)
	if !ok {
		return nil, unauthorized()
	}
	return &principal{User: user, SessionHash: hash, CSRFToken: csrfForSession(cookie.Value), ExpiresAt: expires.UTC()}, nil
}

func (a *authService) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.URL.Path == "/api/health" && r.Method == http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/api/auth/login" {
			if r.Method != http.MethodPost {
				writeError(w, &APIError{405, "METHOD_NOT_ALLOWED", "请使用 POST 登录"})
				return
			}
			if len(r.Header.Values("Authorization")) > 0 {
				writeError(w, unauthorized())
				return
			}
			if !originAllowed(r, a.origins) {
				writeError(w, csrfRejected())
				return
			}
			if !a.allowLogin(r.RemoteAddr) {
				w.Header().Set("Retry-After", "60")
				writeError(w, &APIError{429, "RATE_LIMITED", "登录请求过于频繁，请稍后重试"})
				return
			}
			a.login(w, r)
			return
		}
		p, err := a.authenticate(r.Context(), r)
		if err != nil {
			writeError(w, err)
			return
		}
		// An old tab may still hold the previous session's UI state after a new
		// tab rotates the shared cookie. Bind opt-in read requests to their known
		// session as well; a bare GET /auth/session can always restore it.
		if !p.Legacy && (r.Method == http.MethodGet || r.Method == http.MethodHead) && len(r.Header.Values("X-CSRF-Token")) > 0 {
			if len(r.Header.Values("X-CSRF-Token")) != 1 || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(p.CSRFToken)) != 1 {
				writeError(w, &APIError{403, "SESSION_CHANGED", "登录身份已在其他页面变更，请重新加载会话"})
				return
			}
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions && !p.Legacy {
			provided := r.Header.Get("X-CSRF-Token")
			if !originAllowed(r, a.origins) || len(r.Header.Values("X-CSRF-Token")) != 1 || subtle.ConstantTimeCompare([]byte(provided), []byte(p.CSRFToken)) != 1 {
				writeError(w, csrfRejected())
				return
			}
		}
		r = r.WithContext(context.WithValue(r.Context(), principalKey{}, p))
		switch r.URL.Path {
		case "/api/auth/session":
			if p.Legacy {
				writeError(w, unauthorized())
				return
			}
			if r.Method != http.MethodGet {
				writeError(w, &APIError{405, "METHOD_NOT_ALLOWED", "请使用 GET 查询会话"})
				return
			}
			writeJSON(w, 200, SessionResponse{p.User, p.CSRFToken, p.ExpiresAt})
		case "/api/auth/logout":
			if p.Legacy {
				writeError(w, unauthorized())
				return
			}
			if r.Method != http.MethodPost {
				writeError(w, &APIError{405, "METHOD_NOT_ALLOWED", "请使用 POST 退出"})
				return
			}
			if _, err = a.db.ExecContext(r.Context(), `UPDATE auth_sessions SET revoked_at=UTC_TIMESTAMP(6) WHERE session_hash=?`, p.SessionHash); err != nil {
				writeError(w, err)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", HttpOnly: true, Secure: a.secureCookie, SameSite: http.SameSiteStrictMode, MaxAge: -1, Expires: time.Unix(1, 0).UTC()})
			writeJSON(w, 200, map[string]string{"status": "logged_out"})
		default:
			next.ServeHTTP(w, r)
		}
	})
}

func (a *authService) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IdentityID string `json:"identity_id"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, &APIError{400, "VALIDATION_ERROR", "登录只接受 identity_id 字符串字段"})
		return
	}
	user, ok := a.sessionUser(body.IdentityID)
	if !ok {
		writeError(w, &APIError{400, "VALIDATION_ERROR", "请选择有效身份"})
		return
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		writeError(w, err)
		return
	}
	token := base64.RawURLEncoding.EncodeToString(secret)
	now := time.Now().UTC().Truncate(time.Microsecond)
	expires := now.Add(sessionLifetime)
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, err)
		return
	}
	defer tx.Rollback()
	var lock int
	if err = tx.QueryRowContext(r.Context(), `SELECT id FROM auth_session_locks WHERE id=1 FOR UPDATE`).Scan(&lock); err != nil {
		writeError(w, err)
		return
	}
	// Login serializes only with other logins, never with financial row locks.
	// Expired/revoked rows are cleaned on every login to keep the demo bounded.
	if _, err = tx.ExecContext(r.Context(), `DELETE FROM auth_sessions WHERE revoked_at IS NOT NULL OR expires_at<=UTC_TIMESTAMP(6)`); err != nil {
		writeError(w, err)
		return
	}
	if old, err := r.Cookie(sessionCookieName); err == nil && validSessionToken(old.Value) {
		if _, err = tx.ExecContext(r.Context(), `UPDATE auth_sessions SET revoked_at=? WHERE session_hash=?`, now, hashSession(old.Value)); err != nil {
			writeError(w, err)
			return
		}
	}
	rows, err := tx.QueryContext(r.Context(), `SELECT session_hash FROM auth_sessions WHERE identity_id=? AND revoked_at IS NULL ORDER BY created_at DESC,session_hash DESC`, user.IdentityID)
	if err != nil {
		writeError(w, err)
		return
	}
	var evict []string
	index := 0
	for rows.Next() {
		var hash string
		if err = rows.Scan(&hash); err != nil {
			rows.Close()
			writeError(w, err)
			return
		}
		if index >= maxIdentitySessions-1 {
			evict = append(evict, hash)
		}
		index++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		writeError(w, err)
		return
	}
	for _, hash := range evict {
		if _, err = tx.ExecContext(r.Context(), `DELETE FROM auth_sessions WHERE session_hash=?`, hash); err != nil {
			writeError(w, err)
			return
		}
	}
	if _, err = tx.ExecContext(r.Context(), `INSERT INTO auth_sessions(session_hash,identity_id,created_at,expires_at) VALUES (?,?,?,?)`, hashSession(token), user.IdentityID, now, expires); err != nil {
		writeError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		writeError(w, err)
		return
	}
	// No cookie is issued until MySQL has durably committed the new session.
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: token, Path: "/", HttpOnly: true, Secure: a.secureCookie, SameSite: http.SameSiteStrictMode, MaxAge: int(sessionLifetime.Seconds()), Expires: expires})
	writeJSON(w, 200, SessionResponse{user, csrfForSession(token), expires})
}
