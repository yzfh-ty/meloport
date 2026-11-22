// Package media serves durable STRM links and optionally proxies a Subsonic server.
package media

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type Lookup interface {
	ByID(context.Context, string) (pickCode string, found bool, err error)
	ByPath(context.Context, string) (pickCode string, found bool, err error)
}

type Resolver interface {
	DownURL(ctx context.Context, pickCode, userAgent string) (string, error)
}

var playbackID = regexp.MustCompile(`^[A-Za-z0-9_-]{22,64}$`)

type handler struct {
	lookup    Lookup
	resolver  Resolver
	proxy     *httputil.ReverseProxy
	songURL   *url.URL
	mountRoot string
	client    *http.Client
}

// New returns a media-only handler. When an upstream is configured, requests
// outside the reserved /media namespace also go to the music server.
func New(lookup Lookup, resolver Resolver, upstreamURL, mountRoot string) http.Handler {
	h := &handler{lookup: lookup, resolver: resolver, mountRoot: mountRoot}
	upstream, err := url.Parse(upstreamURL)
	if err != nil || upstream.Hostname() == "" || upstream.User != nil || upstream.RawQuery != "" || upstream.ForceQuery || upstream.Fragment != "" ||
		(upstream.Scheme != "https" && upstream.Scheme != "http") || !ValidMountRoot(mountRoot) {
		return h
	}
	h.songURL = upstream.JoinPath("rest", "getSong")
	h.client = &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	h.proxy = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(upstream)
			pr.Out.URL.RawQuery = pr.In.URL.RawQuery
			pr.SetXForwarded()
		},
		ErrorLog: log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "music server unavailable", http.StatusBadGateway)
		},
	}
	return h
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/media" || strings.HasPrefix(r.URL.Path, "/media/") {
		h.serveMedia(w, r)
		return
	}
	if h.proxy == nil {
		http.NotFound(w, r)
		return
	}
	if r.URL.RawPath == "" && (r.URL.Path == "/rest/stream" || r.URL.Path == "/rest/stream.view") && h.redirectStream(w, r) {
		return
	}
	h.proxy.ServeHTTP(w, r)
}

func (h *handler) serveMedia(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id := strings.TrimPrefix(r.URL.Path, "/media/")
	if !playbackID.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.lookup == nil || h.resolver == nil {
		http.NotFound(w, r)
		return
	}
	pickCode, found, err := h.lookup.ByID(r.Context(), id)
	if err != nil {
		http.Error(w, "media mapping unavailable", http.StatusServiceUnavailable)
		return
	}
	if !found || pickCode == "" {
		http.NotFound(w, r)
		return
	}
	if !h.redirect(w, r, pickCode) {
		http.Error(w, "media source unavailable", http.StatusBadGateway)
	}
}

func (h *handler) redirect(w http.ResponseWriter, r *http.Request, pickCode string) bool {
	location, err := h.resolver.DownURL(r.Context(), pickCode, r.UserAgent())
	if err != nil {
		return false
	}
	u, err := url.Parse(location)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || strings.ContainsAny(location, "\\\r\n\t ") {
		return false
	}
	w.Header().Set("Location", location)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(http.StatusFound)
	return true
}
