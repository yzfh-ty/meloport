package app

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"
	"sync"
	"time"
)

const managementCookie = "meloport_session"
const managementSessionTTL = 12 * time.Hour

// One administrator, no user database. Credentials and sessions live only in
// memory; restarting the service invalidates all browser sessions.
type managementAuth struct {
	mu           sync.Mutex
	password     [32]byte
	configured   bool
	sessions     map[[32]byte]time.Time
	failures     int
	blockedUntil time.Time
	now          func() time.Time
}

func newManagementAuth(password string) *managementAuth {
	return &managementAuth{password: sha256.Sum256([]byte(password)), configured: len(password) >= 12 && len(password) <= 1024,
		sessions: make(map[[32]byte]time.Time), now: time.Now}
}

func (a *managementAuth) authenticated(r *http.Request) bool {
	cookie, err := r.Cookie(managementCookie)
	if err != nil || len(cookie.Value) != 43 {
		return false
	}
	key := sha256.Sum256([]byte(cookie.Value))
	a.mu.Lock()
	defer a.mu.Unlock()
	expires, ok := a.sessions[key]
	if !ok || !a.now().Before(expires) {
		delete(a.sessions, key)
		return false
	}
	return true
}

func (a *managementAuth) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/api/session" && !a.authenticated(r) {
			webError(w, http.StatusUnauthorized, "请先登录管理控制台。")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *managementAuth) status(w http.ResponseWriter, r *http.Request) {
	webJSON(w, http.StatusOK, map[string]any{"authenticated": a.authenticated(r), "configured": a.configured, "username": "admin"})
}

func (a *managementAuth) login(w http.ResponseWriter, r *http.Request) {
	if !a.configured {
		webError(w, http.StatusServiceUnavailable, "请在服务环境中设置 MELOPORT_ADMIN_PASSWORD（12–1024 字节）并重启 Meloport。")
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readWebJSON(w, r, &input) {
		return
	}
	user, pass := sha256.Sum256([]byte(input.Username)), sha256.Sum256([]byte(input.Password))
	admin := sha256.Sum256([]byte("admin"))
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	if now.Before(a.blockedUntil) {
		w.Header().Set("Retry-After", "30")
		webError(w, http.StatusTooManyRequests, "尝试次数过多，请稍后重试。")
		return
	}
	if subtle.ConstantTimeCompare(user[:], admin[:])&subtle.ConstantTimeCompare(pass[:], a.password[:]) != 1 {
		a.failures++
		if a.failures >= 5 {
			a.blockedUntil = now.Add(30 * time.Second)
			a.failures = 0
		}
		webError(w, http.StatusUnauthorized, "账号或密码不正确。")
		return
	}
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		webError(w, 500, "无法创建登录会话，请重试。")
		return
	}
	value := base64.RawURLEncoding.EncodeToString(token[:])
	for key, expires := range a.sessions {
		if !now.Before(expires) {
			delete(a.sessions, key)
		}
	}
	if len(a.sessions) >= 16 {
		var oldest [32]byte
		var earliest time.Time
		for key, expires := range a.sessions {
			if earliest.IsZero() || expires.Before(earliest) {
				oldest, earliest = key, expires
			}
		}
		delete(a.sessions, oldest)
	}
	expires := now.Add(managementSessionTTL)
	a.sessions[sha256.Sum256([]byte(value))] = expires
	a.failures = 0
	http.SetCookie(w, &http.Cookie{Name: managementCookie, Value: value, Path: "/", HttpOnly: true, Secure: r.TLS != nil,
		SameSite: http.SameSiteStrictMode, MaxAge: int(managementSessionTTL.Seconds()), Expires: expires})
	webJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
}

func (a *managementAuth) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(managementCookie); err == nil {
		a.mu.Lock()
		delete(a.sessions, sha256.Sum256([]byte(cookie.Value)))
		a.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: managementCookie, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	webJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
}
