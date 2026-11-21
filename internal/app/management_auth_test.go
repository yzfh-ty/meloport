package app

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

const adminTestPassword = "fixture-only-password-2026"

func authFixture(t *testing.T, password string) *webFixture {
	t.Helper()
	f := newWebFixture(t)
	f.control.auth = newManagementAuth(password)
	f.handler = f.control.handler(healthHandler(context.Background(), f.store, &f.ready), &f.ready)
	return f
}

func adminLogin(t *testing.T, f *webFixture, password string) *httptest.ResponseRecorder {
	t.Helper()
	return webRequest(t, f, "POST", "/api/session", map[string]string{"username": "admin", "password": password})
}

func withAdminCookie(cookie *http.Cookie) func(*http.Request) {
	return func(r *http.Request) { r.AddCookie(cookie) }
}

func TestManagementLoginProtectsAPIsAndRevokesLogout(t *testing.T) {
	f := authFixture(t, adminTestPassword)
	before, _ := os.ReadFile(f.path)
	for _, path := range []string{"/api/overview", "/api/search?keyword=test", "/api/audio", "/api/cloud115", "/api/not-found"} {
		webStatus(t, webRequest(t, f, "GET", path, nil), 401)
	}
	webStatus(t, webRequest(t, f, "POST", "/api/tasks", webTask("blocked", "netease")), 401)
	after, _ := os.ReadFile(f.path)
	if string(before) != string(after) || f.sources["netease"].searches.Load() != 0 {
		t.Fatal("anonymous request reached management operations")
	}
	for _, path := range []string{"/", "/assets/app.css", "/assets/app.js", "/healthz", "/readyz"} {
		webStatus(t, webRequest(t, f, "GET", path, nil), 200)
	}
	status := webRequest(t, f, "GET", "/api/session", nil)
	if !strings.Contains(status.Body.String(), `"authenticated":false`) || status.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("incorrect public session status")
	}
	bad := adminLogin(t, f, "wrong-password")
	webStatus(t, bad, 401)
	if len(bad.Result().Cookies()) != 0 {
		t.Fatal("failed login created a session")
	}
	login := adminLogin(t, f, adminTestPassword)
	webStatus(t, login, 200)
	cookie := login.Result().Cookies()[0]
	if cookie.Name != managementCookie || len(cookie.Value) != 43 || !cookie.HttpOnly || cookie.Secure || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.MaxAge != 43200 {
		t.Fatalf("unsafe session cookie attributes: %+v", cookie)
	}
	if strings.Contains(login.Body.String(), adminTestPassword) || strings.Contains(string(after), adminTestPassword) {
		t.Fatal("password leaked")
	}
	webStatus(t, webRequest(t, f, "GET", "/api/overview", nil, withAdminCookie(cookie)), 200)
	for _, change := range []func(*http.Request){func(r *http.Request) { r.Header.Del("X-Meloport-Request") }, func(r *http.Request) { r.Header.Set("Origin", "http://foreign.test") }} {
		webStatus(t, webRequest(t, f, "DELETE", "/api/session", nil, withAdminCookie(cookie), change), 403)
	}
	logout := webRequest(t, f, "DELETE", "/api/session", nil, withAdminCookie(cookie))
	webStatus(t, logout, 200)
	if logout.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("logout did not clear browser cookie")
	}
	webStatus(t, webRequest(t, f, "GET", "/api/overview", nil, withAdminCookie(cookie)), 401)
}

func TestManagementLoginExpiryRestartAndConcurrentReads(t *testing.T) {
	f := authFixture(t, adminTestPassword)
	now := time.Now()
	f.control.auth.now = func() time.Time { return now }
	cookie := adminLogin(t, f, adminTestPassword).Result().Cookies()[0]
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := httptest.NewRequest("GET", "http://localhost/api/overview", nil)
			r.AddCookie(cookie)
			if !f.control.auth.authenticated(r) {
				t.Error("valid session refused")
			}
		}()
	}
	wg.Wait()
	now = now.Add(managementSessionTTL)
	webStatus(t, webRequest(t, f, "GET", "/api/overview", nil, withAdminCookie(cookie)), 401)
	newCookie := adminLogin(t, f, adminTestPassword).Result().Cookies()[0]
	f.control.auth = newManagementAuth(adminTestPassword)
	f.handler = f.control.handler(healthHandler(context.Background(), f.store, &f.ready), &f.ready)
	webStatus(t, webRequest(t, f, "GET", "/api/overview", nil, withAdminCookie(newCookie)), 401)
	tlsLogin := webRequest(t, f, "POST", "/api/session", map[string]string{"username": "admin", "password": adminTestPassword}, func(r *http.Request) { r.TLS = &tls.ConnectionState{} })
	if !tlsLogin.Result().Cookies()[0].Secure {
		t.Fatal("TLS session cookie must be Secure")
	}
}

func TestManagementLoginThrottleAndMissingConfiguration(t *testing.T) {
	f := authFixture(t, adminTestPassword)
	now := time.Now()
	f.control.auth.now = func() time.Time { return now }
	for i := 0; i < 5; i++ {
		webStatus(t, adminLogin(t, f, "bad"), 401)
	}
	limited := adminLogin(t, f, adminTestPassword)
	webStatus(t, limited, 429)
	if limited.Header().Get("Retry-After") == "" {
		t.Fatal("missing retry hint")
	}
	now = now.Add(31 * time.Second)
	webStatus(t, adminLogin(t, f, adminTestPassword), 200)
	for _, password := range []string{"", "short", strings.Repeat("a", 1025)} {
		locked := authFixture(t, password)
		webStatus(t, adminLogin(t, locked, password), 503)
		webStatus(t, webRequest(t, locked, "GET", "/api/overview", nil), 401)
		if !strings.Contains(webRequest(t, locked, "GET", "/api/session", nil).Body.String(), `"configured":false`) {
			t.Fatal("unconfigured instance must stay locked")
		}
	}
}

func TestManagementSessionCapacityAndLoginCSRF(t *testing.T) {
	f := authFixture(t, adminTestPassword)
	for _, change := range []func(*http.Request){func(r *http.Request) { r.Header.Del("X-Meloport-Request") }, func(r *http.Request) { r.Header.Set("Origin", "http://foreign.test") }} {
		webStatus(t, webRequest(t, f, "POST", "/api/session", map[string]string{"username": "admin", "password": adminTestPassword}, change), 403)
	}
	for i := 0; i < 20; i++ {
		webStatus(t, adminLogin(t, f, adminTestPassword), 200)
	}
	if len(f.control.auth.sessions) != 16 {
		t.Fatal("session storage is unbounded")
	}
}
