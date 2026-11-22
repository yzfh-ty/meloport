package chksz

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"sync"

	"meloport/internal/model"
)

var keyPattern = regexp.MustCompile(`^chksz_[A-Za-z0-9._~-]+$`)

func KeyConfigured(environment string, configured ...string) bool {
	return keyPattern.MatchString(resolveKey(environment, configured...))
}

func resolveKey(environment string, configured ...string) string {
	if key := os.Getenv(environment); keyPattern.MatchString(key) {
		return key
	}
	if len(configured) > 0 {
		return configured[0]
	}
	return ""
}

// Lazy reads a key only when resolution or a supported metadata fallback is used.
type Lazy struct {
	mu      sync.Mutex
	options Options
	keyEnv  string
	client  *Client
}

func NewLazy(options Options, keyEnv string) *Lazy { return &Lazy{options: options, keyEnv: keyEnv} }

func (l *Lazy) get() (*Client, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.client != nil {
		return l.client, nil
	}
	options := l.options
	options.APIKey = resolveKey(l.keyEnv, options.APIKey)
	if !keyPattern.MatchString(options.APIKey) {
		return nil, &APIError{Status: 401, Message: fmt.Sprintf("set api.key in your local configuration or environment variable %s to your personal chksz_ key for audio resolution or metadata fallback", l.keyEnv)}
	}
	client, err := New(options)
	if err != nil {
		return nil, err
	}
	l.client = client
	return client, nil
}

func (l *Lazy) Playlist(ctx context.Context, id string) (model.Playlist, error) {
	c, err := l.get()
	if err != nil {
		return model.Playlist{}, err
	}
	return c.Playlist(ctx, id)
}
func (l *Lazy) Search(ctx context.Context, source, keyword string, limit, offset int) ([]model.Track, error) {
	c, err := l.get()
	if err != nil {
		return nil, err
	}
	return c.Search(ctx, source, keyword, limit, offset)
}
func (l *Lazy) ResolvePlatform(ctx context.Context, source, id, quality string) (model.ResolvedTrack, error) {
	c, err := l.get()
	if err != nil {
		return model.ResolvedTrack{}, err
	}
	return c.ResolvePlatform(ctx, source, id, quality)
}
