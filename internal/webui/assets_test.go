package webui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddedAssets(t *testing.T) {
	handler := Handler()
	for _, item := range []struct{ path, contentType, contains string }{
		{"/", "text/html", "排行榜"},
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

func TestRankingsControlsAreEmbeddedWithExistingThemeTokens(t *testing.T) {
	cases := []struct {
		path     string
		required []string
	}{
		{"/", []string{`data-view="rankings"`, `data-nav="rankings"`, `id="rankings-platform"`, `id="task-source-type"`, `id="task-source-id"`}},
		{"/assets/app.js", []string{`/api/rankings?`, `source_type:`, `source_id:`, `subscribeRanking`}},
		{"/assets/app.css", []string{`var(--accent)`, `.segmented-control`, `.ranking-table`, `@container (max-width: 720px)`}},
	}
	handler := Handler()
	for _, item := range cases {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, item.path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d", item.path, response.Code)
		}
		for _, required := range item.required {
			if !strings.Contains(response.Body.String(), required) {
				t.Errorf("%s does not contain %q", item.path, required)
			}
		}
	}
}

func TestLiveAssetDirectoryOverridesEmbeddedFiles(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "index.html"), []byte("live index"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "app.js"), []byte("live asset"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "config.yaml"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	handler := HandlerFromDirectory(directory)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))
	if response.Code != http.StatusOK || response.Body.String() != "live asset" {
		t.Fatalf("live asset was not served: HTTP%d %q", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("live asset response is cacheable")
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/config.yaml", nil))
	if response.Code != http.StatusNotFound {
		t.Fatal("live asset handler exposed a non-whitelisted file")
	}
	if err := os.WriteFile(filepath.Join(directory, "app.js"), []byte("updated live asset"), 0600); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))
	if response.Code != http.StatusOK || response.Body.String() != "updated live asset" {
		t.Fatalf("asset edit was not visible after refresh: HTTP%d %q", response.Code, response.Body.String())
	}
}

func TestLiveAssetDirectoryValidation(t *testing.T) {
	if err := ValidateDirectory(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing live asset directory was accepted")
	}
	file := filepath.Join(t.TempDir(), "asset")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateDirectory(file); err == nil {
		t.Fatal("file was accepted as live asset directory")
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
