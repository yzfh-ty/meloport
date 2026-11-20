package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmbeddedAssets(t *testing.T) {
	handler := Handler()
	for _, item := range []struct{ path, contentType, contains string }{
		{"/", "text/html", "任务概览"},
		{"/assets/app.css", "text/css", "prefers-reduced-motion"},
		{"/assets/app.js", "text/javascript", "X-Meloport-Request"},
	} {
		t.Run(item.path, func(t *testing.T) {
			request := httptest.NewRequest("GET", item.path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d", response.Code)
			}
			if !strings.Contains(response.Header().Get("Content-Type"), item.contentType) {
				t.Fatal("wrong content type")
			}
			if !strings.Contains(response.Body.String(), item.contains) {
				t.Fatal("missing embedded content")
			}
			if !strings.Contains(response.Header().Get("Content-Security-Policy"), "script-src 'self'") {
				t.Fatal("missing same-origin policy")
			}
			head := httptest.NewRecorder()
			handler.ServeHTTP(head, httptest.NewRequest("HEAD", item.path, nil))
			if head.Code != http.StatusOK || head.Body.Len() != 0 {
				t.Fatal("HEAD returned content or wrong status")
			}
		})
	}
}

func TestStaticRoutesFailClosed(t *testing.T) {
	handler := Handler()
	for _, path := range []string{"/unknown", "/api/overview", "/assets/../index.html", "/assets/index.html", "/assets/missing.js"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d", path, response.Code)
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("POST", "/", strings.NewReader("{}")))
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != "GET, HEAD" {
		t.Fatal("static route accepted mutation")
	}
}
