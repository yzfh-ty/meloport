// Package platform provides bounded, credential-isolated native metadata HTTP.
package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const MaxResponseBytes = 16 << 20

type HTTP struct {
	client   *http.Client
	interval time.Duration
	gate     chan struct{}
	next     time.Time
}

func NewHTTP(client *http.Client, timeout, minInterval time.Duration) *HTTP {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	c := http.Client{Timeout: timeout}
	if client != nil {
		c = *client
		c.Timeout = timeout
	}
	c.Jar = nil
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &HTTP{client: &c, interval: minInterval, gate: make(chan struct{}, 1)}
}

func (h *HTTP) request(ctx context.Context, method, rawURL string, payload []byte, headers http.Header) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, errors.New("invalid platform metadata URL")
	}
	select {
	case h.gate <- struct{}{}:
		defer func() { <-h.gate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if wait := time.Until(h.next); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, bytes.NewReader(payload))
	if err != nil {
		return nil, errors.New("cannot construct platform metadata request")
	}
	for key, values := range headers {
		req.Header[key] = append([]string(nil), values...)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "Mozilla/5.0 Meloport/0.1")
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	h.next = time.Now().Add(h.interval)
	response, err := h.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("platform metadata connection, TLS or timeout failure")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("platform metadata HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, MaxResponseBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("cannot read platform metadata response")
	}
	if len(body) > MaxResponseBytes {
		return nil, errors.New("platform metadata response exceeds 16 MiB")
	}
	return body, nil
}

func (h *HTTP) JSON(ctx context.Context, method, rawURL string, payload []byte, headers http.Header) (map[string]any, error) {
	return h.json(ctx, method, rawURL, payload, headers, nil)
}

// JSONAllowDuplicateKeys permits only the named duplicate object keys for a
// caller that consumes a known upstream response with a documented duplicate.
// All response size, syntax, depth, top-level and trailing-content checks stay
// identical to JSON; ordinary callers should use JSON for strict validation.
func (h *HTTP) JSONAllowDuplicateKeys(ctx context.Context, method, rawURL string, payload []byte, headers http.Header, allowedKeys ...string) (map[string]any, error) {
	allowed := make(map[string]struct{}, len(allowedKeys))
	for _, key := range allowedKeys {
		if key != "" {
			allowed[key] = struct{}{}
		}
	}
	return h.json(ctx, method, rawURL, payload, headers, allowed)
}

func (h *HTTP) json(ctx context.Context, method, rawURL string, payload []byte, headers http.Header, allowedDuplicateKeys map[string]struct{}) (map[string]any, error) {
	body, err := h.request(ctx, method, rawURL, payload, headers)
	if err != nil {
		return nil, err
	}
	if err := validateJSONKeys(body, allowedDuplicateKeys); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var result map[string]any
	if err := decoder.Decode(&result); err != nil || result == nil {
		return nil, errors.New("platform metadata response is not a JSON object")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("platform metadata response has trailing content")
	}
	return result, nil
}

func validateJSONKeys(body []byte, allowedDuplicateKeys map[string]struct{}) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 128 {
			return errors.New("platform metadata JSON nesting exceeds limit")
		}
		token, err := decoder.Token()
		if err != nil {
			return errors.New("platform metadata response contains invalid JSON")
		}
		if delimiter, ok := token.(json.Delim); ok {
			switch delimiter {
			case '{':
				seen := map[string]bool{}
				for decoder.More() {
					keyToken, err := decoder.Token()
					if err != nil {
						return errors.New("invalid platform JSON object")
					}
					key, ok := keyToken.(string)
					if !ok {
						return errors.New("invalid platform JSON field")
					}
					if seen[key] {
						if _, allowed := allowedDuplicateKeys[key]; allowed {
							if err := walk(depth + 1); err != nil {
								return err
							}
							continue
						}
						return errors.New("duplicate platform JSON object field")
					}
					seen[key] = true
					if err := walk(depth + 1); err != nil {
						return err
					}
				}
				end, err := decoder.Token()
				if err != nil || end != json.Delim('}') {
					return errors.New("invalid platform JSON object")
				}
			case '[':
				for decoder.More() {
					if err := walk(depth + 1); err != nil {
						return err
					}
				}
				end, err := decoder.Token()
				if err != nil || end != json.Delim(']') {
					return errors.New("invalid platform JSON array")
				}
			default:
				return errors.New("invalid platform JSON delimiter")
			}
		}
		return nil
	}
	if err := walk(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("platform metadata response has trailing JSON")
	}
	return nil
}

// TextResponse supports platform-owned HTML fallbacks without executing scripts.
func (h *HTTP) TextResponse(ctx context.Context, method, rawURL string, payload []byte, headers http.Header) (string, error) {
	body, err := h.request(ctx, method, rawURL, payload, headers)
	return string(body), err
}

func (h *HTTP) Bytes(ctx context.Context, method, rawURL string, payload []byte, headers http.Header) ([]byte, error) {
	return h.request(ctx, method, rawURL, payload, headers)
}

func Text(obj map[string]any, fields ...string) string {
	for _, field := range fields {
		switch value := obj[field].(type) {
		case string:
			if value != "" {
				return value
			}
		case json.Number:
			return value.String()
		}
	}
	return ""
}

func Integer(value any) (int64, bool) {
	var str string
	switch v := value.(type) {
	case json.Number:
		str = v.String()
	case string:
		str = v
	case int:
		return int64(v), true
	case int64:
		return v, true
	default:
		return 0, false
	}
	n, err := strconv.ParseInt(str, 10, 64)
	return n, err == nil
}

func Object(value any) map[string]any { result, _ := value.(map[string]any); return result }
func Slice(value any) []any           { result, _ := value.([]any); return result }

func Artists(value any) []string {
	var artists []string
	appendArtist := func(raw any) {
		if obj := Object(raw); obj != nil {
			if name := Text(obj, "name", "author_name", "singername"); name != "" {
				artists = append(artists, name)
			}
			return
		}
		if name, ok := raw.(string); ok && strings.TrimSpace(name) != "" {
			artists = append(artists, strings.TrimSpace(name))
		}
	}
	if list, ok := value.([]any); ok {
		for _, item := range list {
			appendArtist(item)
		}
	} else {
		appendArtist(value)
	}
	return artists
}
