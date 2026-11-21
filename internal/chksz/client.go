// Package chksz adapts the subset of ChKSz used by playlist synchronization.
package chksz

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

	"meloport/internal/model"
)

const (
	maxResponseBytes = 8 << 20
	requestLimit     = 20
	requestWindow    = time.Minute
)

type Options struct {
	BaseURL     string
	APIKey      string
	Timeout     time.Duration
	MinInterval time.Duration
	HTTPClient  *http.Client
}

type Client struct {
	base          *url.URL
	key           string
	http          *http.Client
	minInterval   time.Duration
	gate          chan struct{}
	nextRequest   time.Time   // Protected by gate.
	requestStarts []time.Time // Protected by gate.
	// rateLimit and rateWindow are fixed for production and configurable only
	// in package tests so short windows can be verified without a one-minute wait.
	rateLimit  int
	rateWindow time.Duration
}

// APIError preserves the upstream status and message, with credentials removed.
type APIError struct {
	Status     int
	Message    string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("ChKSz API HTTP %d: %s", e.Status, e.Message)
}

// IsAccountError identifies errors for which a batch must stop making API calls.
// A 429 has already exhausted the client's single permitted delayed retry.
// A 503 also stops the batch to avoid repeatedly querying a disabled service.
func IsAccountError(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.Status {
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden,
		http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return true
	default:
		return false
	}
}

// New builds a client without making an API request. MinInterval must be set by
// the caller (the application defaults to 3.1s); zero is useful for local tests.
func New(opts Options) (*Client, error) {
	if opts.BaseURL == "" {
		opts.BaseURL = "https://api.chksz.com"
	}
	base, err := url.Parse(opts.BaseURL)
	if err != nil || !validHTTPURL(base) || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("ChKSz base URL must be an HTTP(S) URL without credentials, query, or fragment")
	}
	if strings.TrimSpace(opts.APIKey) == "" || opts.APIKey != strings.TrimSpace(opts.APIKey) {
		return nil, errors.New("ChKSz API key is missing or contains surrounding whitespace")
	}
	if opts.MinInterval < 0 || opts.Timeout < 0 {
		return nil, errors.New("ChKSz timeout and minimum interval must not be negative")
	}
	client := http.Client{Timeout: time.Minute}
	if opts.HTTPClient != nil {
		client = *opts.HTTPClient
		if client.Timeout == 0 {
			client.Timeout = time.Minute
		}
	}
	if opts.Timeout != 0 {
		client.Timeout = opts.Timeout
	}
	// The key is a URL query parameter: never send it to a redirect destination.
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Client{
		base: base, key: opts.APIKey, http: &client,
		minInterval: opts.MinInterval, gate: make(chan struct{}, 1),
		rateLimit: requestLimit, rateWindow: requestWindow,
	}, nil
}

func (c *Client) Playlist(ctx context.Context, id string) (model.Playlist, error) {
	if !numericID(id) {
		return model.Playlist{}, errors.New("playlist ID must contain only digits")
	}
	root, err := c.get(ctx, "/api/163_playlist", url.Values{"id": {id}})
	if err != nil {
		return model.Playlist{}, err
	}
	obj, envelopes, err := c.unwrapPath(root, "playlist")
	if err != nil {
		return model.Playlist{}, err
	}
	rawTracks, ok := first(obj, "tracks", "songs", "list", "songList")
	if !ok {
		return model.Playlist{}, errors.New("ChKSz playlist response has no recognized track list")
	}
	items, ok := rawTracks.([]any)
	if !ok {
		return model.Playlist{}, errors.New("ChKSz playlist track list must be a JSON array")
	}
	playlist := model.Playlist{
		ID: id, Name: textField(obj, "name", "playlistName", "playlist_name", "title"),
		Tracks: make([]model.Track, 0, len(items)), ReportedCount: len(items),
	}
	if remoteID := textField(obj, "id", "playlistId", "playlist_id"); remoteID != "" {
		if remoteID != id {
			return model.Playlist{}, errors.New("ChKSz returned a different playlist ID")
		}
	}
	// Count metadata can live outside data/playlist. Inspect only the selected
	// wrapper path, but require every recognized count alias on it to agree.
	for _, envelope := range envelopes {
		for _, field := range []string{"trackCount", "track_count", "songCount", "song_count", "total", "count"} {
			count, present := envelope[field]
			if !present {
				continue
			}
			n, valid := integer(count)
			if !valid || n < 0 || int64(int(n)) != n {
				return model.Playlist{}, errors.New("ChKSz playlist contains an invalid track count")
			}
			if n != int64(len(items)) {
				return model.Playlist{}, fmt.Errorf("ChKSz playlist is incomplete or inconsistent: reported %d tracks, received %d", n, len(items))
			}
		}
	}
	for i, raw := range items {
		trackObj, ok := raw.(map[string]any)
		if !ok {
			return model.Playlist{}, fmt.Errorf("ChKSz playlist track %d is not an object", i+1)
		}
		trackID := textField(trackObj, "id", "songId", "song_id")
		if !numericID(trackID) {
			return model.Playlist{}, fmt.Errorf("ChKSz playlist track %d has no valid song ID", i+1)
		}
		track := model.Track{ID: trackID, Name: textField(trackObj, "name", "songName", "song_name", "title")}
		if value, found := first(trackObj, "artists", "ar", "artist", "singer"); found {
			track.Artists = artists(value)
		}
		if value, found := first(trackObj, "album", "al"); found {
			switch album := value.(type) {
			case string:
				track.Album = album
			case map[string]any:
				track.Album = textField(album, "name", "title")
				track.CoverURL = c.coverURL(textField(album, "picUrl"))
			}
		}
		if track.CoverURL == "" {
			track.CoverURL = c.coverURL(textField(trackObj, "cover"))
		}
		playlist.Tracks = append(playlist.Tracks, track)
	}
	for _, envelope := range envelopes {
		if err := validateAuthoritativeIDs(envelope, playlist.Tracks); err != nil {
			return model.Playlist{}, err
		}
	}
	return playlist, nil
}

// Native NetEase responses can include all member IDs but only the first batch
// of embedded song details. Count equality alone also misses wrong-ID batches.
// Duplicates are permitted: the engine synchronizes distinct song identities.
func validateAuthoritativeIDs(obj map[string]any, tracks []model.Track) error {
	actual := make(map[string]struct{}, len(tracks))
	for _, track := range tracks {
		actual[track.ID] = struct{}{}
	}
	for _, field := range []string{"trackIds", "track_ids"} {
		rawIDs, present := obj[field]
		if !present {
			continue
		}
		items, valid := rawIDs.([]any)
		if !valid {
			return errors.New("ChKSz authoritative playlist IDs must be a JSON array")
		}
		expected := make(map[string]struct{}, len(items))
		for i, raw := range items {
			var id string
			switch value := raw.(type) {
			case map[string]any:
				id = textField(value, "id")
			case string:
				id = value
			case json.Number:
				id = value.String()
			}
			if !numericID(id) {
				return fmt.Errorf("ChKSz authoritative playlist ID %d is invalid", i+1)
			}
			expected[id] = struct{}{}
		}
		if len(expected) != len(actual) {
			return fmt.Errorf("ChKSz playlist is incomplete or inconsistent: authoritative IDs contain %d distinct songs, received %d", len(expected), len(actual))
		}
		for id := range expected {
			if _, found := actual[id]; !found {
				return errors.New("ChKSz playlist song details do not match the authoritative playlist IDs")
			}
		}
	}
	return nil
}

func (c *Client) Resolve(ctx context.Context, id, quality string) (model.ResolvedTrack, error) {
	if !numericID(id) {
		return model.ResolvedTrack{}, errors.New("song ID must contain only digits")
	}
	if quality == "" {
		quality = "lossless"
	}
	switch quality {
	case "standard", "exhigh", "lossless", "hires", "jyeffect", "sky", "jymaster":
	default:
		return model.ResolvedTrack{}, errors.New("unsupported NetEase quality")
	}
	root, err := c.get(ctx, "/api/163_music", url.Values{"id": {id}, "level": {quality}, "type": {"json"}})
	if err != nil {
		return model.ResolvedTrack{}, err
	}
	obj, err := c.unwrap(root, "song")
	if err != nil {
		return model.ResolvedTrack{}, err
	}
	return c.decodeAudio(obj)
}

func (c *Client) get(ctx context.Context, endpoint string, params url.Values) (map[string]any, error) {
	select {
	case c.gate <- struct{}{}:
		defer func() { <-c.gate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	u := *c.base
	u.Path = strings.TrimRight(u.Path, "/") + endpoint
	u.RawPath = ""
	params.Set("apikey", c.key)
	u.RawQuery = params.Encode()
	var retryErr error
	for attempt := 0; attempt < 2; attempt++ {
		if err := c.waitForRequestSlot(ctx); err != nil {
			return nil, errors.Join(retryErr, err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, errors.New("cannot construct ChKSz API request")
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "meloport/0.1")
		resp, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			// url.Error includes the credential-bearing URL. Never wrap it.
			return nil, errors.New("ChKSz API request failed (connection, timeout, or TLS error)")
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
		resp.Body.Close()
		if readErr != nil {
			return nil, errors.New("cannot read ChKSz API response")
		}
		if len(body) > maxResponseBytes {
			return nil, errors.New("ChKSz API response exceeds 8 MiB limit")
		}
		root, decodeErr := decodeObject(body)
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			message := http.StatusText(resp.StatusCode)
			if root != nil {
				if msg := responseMessage(root); msg != "" {
					message = c.redact(msg)
				}
			}
			if resp.StatusCode >= 300 && resp.StatusCode < 400 {
				message = "API redirects are disabled to protect credentials"
			}
			delay, validDelay := retryAfter(resp.Header.Get("Retry-After"), time.Now())
			apiErr := &APIError{Status: resp.StatusCode, Message: message, RetryAfter: delay}
			if resp.StatusCode == http.StatusTooManyRequests && attempt == 0 && validDelay {
				if err := sleep(ctx, delay); err != nil {
					return nil, errors.Join(apiErr, err)
				}
				retryErr = apiErr
				continue
			}
			return nil, apiErr
		}
		if decodeErr != nil {
			return nil, errors.New("ChKSz API returned invalid JSON or a non-object response")
		}
		if err := c.businessError(root); err != nil {
			return nil, err
		}
		return root, nil
	}
	panic("unreachable")
}

// waitForRequestSlot blocks until this client may start another HTTP request.
// The caller must hold c.gate. A request is counted only once it is ready to
// start, so canceled waits do not consume the rolling-window allowance.
func (c *Client) waitForRequestSlot(ctx context.Context) error {
	for {
		now := time.Now()
		cutoff := now.Add(-c.rateWindow)
		first := 0
		for first < len(c.requestStarts) && !c.requestStarts[first].After(cutoff) {
			first++
		}
		if first > 0 {
			c.requestStarts = append(c.requestStarts[:0], c.requestStarts[first:]...)
		}

		delay := time.Until(c.nextRequest)
		if c.rateLimit > 0 && len(c.requestStarts) >= c.rateLimit {
			windowDelay := time.Until(c.requestStarts[0].Add(c.rateWindow))
			if windowDelay > delay {
				delay = windowDelay
			}
		}
		if err := sleep(ctx, delay); err != nil {
			return err
		}
		now = time.Now()
		// Re-check after sleeping because another caller cannot enter while the
		// gate is held, but the clock may have advanced only partially.
		cutoff = now.Add(-c.rateWindow)
		first = 0
		for first < len(c.requestStarts) && !c.requestStarts[first].After(cutoff) {
			first++
		}
		if first > 0 {
			c.requestStarts = append(c.requestStarts[:0], c.requestStarts[first:]...)
		}
		if (c.rateLimit <= 0 || len(c.requestStarts) < c.rateLimit) && !now.Before(c.nextRequest) {
			c.requestStarts = append(c.requestStarts, now)
			c.nextRequest = now.Add(c.minInterval)
			return nil
		}
	}
}

func (c *Client) unwrap(root map[string]any, named string) (map[string]any, error) {
	obj, _, err := c.unwrapPath(root, named)
	return obj, err
}

func (c *Client) unwrapPath(root map[string]any, named string) (map[string]any, []map[string]any, error) {
	obj := root
	var envelopes []map[string]any
	for depth := 0; depth < 4; depth++ {
		envelopes = append(envelopes, obj)
		if err := c.businessError(obj); err != nil {
			return nil, nil, err
		}
		if named == "playlist" {
			if _, found := first(obj, "tracks", "songs", "list", "songList"); found {
				return obj, envelopes, nil
			}
		} else if textField(obj, "url", "musicUrl", "music_url", "playUrl", "play_url") != "" {
			return obj, envelopes, nil
		}
		if next, ok := obj[named].(map[string]any); ok {
			obj = next
			continue
		}
		if next, ok := obj["data"].(map[string]any); ok {
			obj = next
			continue
		}
		if named == "song" {
			if values, ok := obj["data"].([]any); ok {
				if len(values) != 1 {
					return nil, nil, errors.New("ChKSz audio response must contain exactly one song")
				}
				if next, ok := values[0].(map[string]any); ok {
					obj = next
					continue
				}
			}
		}
		return obj, envelopes, nil
	}
	return nil, nil, errors.New("ChKSz API response contains too many nested wrappers")
}

func (c *Client) businessError(obj map[string]any) error {
	if value, present := obj["code"]; present {
		code, valid := integer(value)
		if !valid {
			return errors.New("ChKSz API returned an invalid business status code")
		}
		if code != 0 && code != 200 {
			message := responseMessage(obj)
			if message == "" {
				message = "upstream business request failed"
			}
			return &APIError{Status: int(code), Message: c.redact(message)}
		}
	}
	if success, present := obj["success"].(bool); present && !success {
		message := responseMessage(obj)
		if message == "" {
			message = "upstream reported an unsuccessful request"
		}
		return &APIError{Status: 200, Message: c.redact(message)}
	}
	return nil
}

func (c *Client) redact(value string) string {
	value = strings.ReplaceAll(value, c.key, "[REDACTED]")
	value = strings.ReplaceAll(value, url.QueryEscape(c.key), "[REDACTED]")
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > 1024 {
		value = string([]rune(value)[:min(256, len([]rune(value)))]) + "..."
	}
	return value
}

func (c *Client) containsKey(value string) bool {
	return strings.Contains(value, c.key) || strings.Contains(value, url.QueryEscape(c.key))
}

func decodeObject(body []byte) (map[string]any, error) {
	var obj map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&obj); err != nil || obj == nil {
		return nil, errors.New("invalid JSON object")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("unexpected trailing JSON")
	}
	return obj, nil
}

func retryAfter(value string, now time.Time) (time.Duration, bool) {
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseInt(value, 10, 32); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second, true
	}
	if date, err := http.ParseTime(value); err == nil {
		return max(0, date.Sub(now)), true
	}
	return 0, false
}

func sleep(ctx context.Context, duration time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if duration <= 0 {
		return nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func first(obj map[string]any, fields ...string) (any, bool) {
	for _, field := range fields {
		if value, ok := obj[field]; ok {
			return value, true
		}
	}
	return nil, false
}

func responseMessage(obj map[string]any) string {
	if message := textField(obj, "msg", "message", "error"); message != "" {
		return message
	}
	if nested, ok := obj["error"].(map[string]any); ok {
		return textField(nested, "msg", "message")
	}
	return ""
}

func textField(obj map[string]any, fields ...string) string {
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

func integer(value any) (int64, bool) {
	var text string
	switch v := value.(type) {
	case json.Number:
		text = string(v)
	case string:
		text = v
	default:
		return 0, false
	}
	n, err := strconv.ParseInt(text, 10, 64)
	return n, err == nil
}

func artists(value any) []string {
	var result []string
	appendArtist := func(value any) {
		var name string
		switch artist := value.(type) {
		case string:
			name = artist
		case map[string]any:
			name = textField(artist, "name", "title")
		}
		if name != "" {
			result = append(result, name)
		}
	}
	if values, ok := value.([]any); ok {
		for _, value := range values {
			appendArtist(value)
		}
	} else {
		appendArtist(value)
	}
	return result
}

func numericID(id string) bool {
	if id == "" || len(id) > 32 || strings.Trim(id, "0") == "" {
		return false
	}
	for _, char := range id {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func validHTTPURL(u *url.URL) bool {
	return u != nil && (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != "" && u.User == nil && u.Opaque == ""
}

func validFormat(value string) bool {
	switch value {
	case "mp3", "flac", "m4a", "aac", "ogg", "opus", "wav", "aiff", "ape", "alac", "wma":
		return true
	default:
		return false
	}
}
