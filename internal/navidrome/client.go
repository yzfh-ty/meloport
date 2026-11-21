// Package navidrome publishes synchronized collections as Navidrome playlists.
package navidrome

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode"
)

const maxResponseBytes = 4 << 20

type Options struct {
	BaseURL    string
	Username   string
	Password   string
	MountRoot  string
	Timeout    time.Duration
	HTTPClient *http.Client
}

// Candidate describes a local audio file that Navidrome should already have
// indexed. RelativePath is relative to MountRoot, using slash separators.
type Candidate struct {
	Title        string
	Artists      []string
	Album        string
	RelativePath string
}

type Client struct {
	base     *url.URL
	username string
	password string
	root     string
	http     *http.Client
}

type song struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Artist string `json:"artist"`
	Album  string `json:"album"`
	Path   string `json:"path"`
}

type playlist struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// New validates connection data without making a request. Passwords are only
// held in memory and are sent as Subsonic token authentication in POST bodies.
func New(opts Options) (*Client, error) {
	base, err := url.Parse(opts.BaseURL)
	if err != nil || base.Hostname() == "" || (base.Scheme != "https" && base.Scheme != "http") || base.User != nil || base.RawQuery != "" || base.ForceQuery || base.Fragment != "" {
		return nil, errors.New("Navidrome URL must be an HTTP(S) URL without credentials, query or fragment")
	}
	if strings.TrimSpace(opts.Username) == "" || strings.TrimSpace(opts.Username) != opts.Username || strings.ContainsAny(opts.Username, "\x00\r\n") {
		return nil, errors.New("Navidrome username is invalid")
	}
	if opts.Password == "" || strings.ContainsAny(opts.Password, "\x00\r\n") {
		return nil, errors.New("Navidrome password is missing or invalid")
	}
	root, ok := cleanAbsolute(opts.MountRoot)
	if !ok {
		return nil, errors.New("Navidrome mount root must be an absolute filesystem path")
	}
	client := http.Client{Timeout: 15 * time.Second}
	if opts.Timeout > 0 {
		client.Timeout = opts.Timeout
	}
	if opts.HTTPClient != nil {
		client = *opts.HTTPClient
		if client.Timeout == 0 {
			client.Timeout = 15 * time.Second
		}
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	base.Path = strings.TrimRight(base.Path, "/")
	base.RawPath = ""
	return &Client{base: base, username: opts.Username, password: opts.Password, root: root, http: &client}, nil
}

// Publish resolves every requested track against its exact mounted path and
// metadata before it creates or changes a playlist. Missing or ambiguous
// library entries leave the existing playlist untouched.
func (c *Client) Publish(ctx context.Context, playlistName string, tracks []Candidate) error {
	if strings.TrimSpace(playlistName) == "" || len([]rune(playlistName)) > 300 || strings.ContainsAny(playlistName, "\x00\r\n") {
		return errors.New("Navidrome playlist name is invalid")
	}
	if tracks == nil {
		tracks = []Candidate{}
	}
	ids := make([]string, 0, len(tracks))
	seen := make(map[string]bool, len(tracks))
	for i, candidate := range tracks {
		if err := ctx.Err(); err != nil {
			return err
		}
		id, err := c.resolve(ctx, candidate)
		if err != nil {
			return fmt.Errorf("resolve Navidrome track %d: %w", i+1, err)
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}

	playlists, err := c.playlists(ctx)
	if err != nil {
		return err
	}
	var found *playlist
	for i := range playlists {
		if playlists[i].Name == playlistName {
			if found != nil {
				return errors.New("Navidrome contains multiple playlists with the requested name")
			}
			found = &playlists[i]
		}
	}
	if found == nil {
		form := url.Values{"name": {playlistName}}
		for _, id := range ids {
			form.Add("songId", id)
		}
		_, err := c.call(ctx, "createPlaylist", form)
		if err != nil {
			return fmt.Errorf("create Navidrome playlist: %w", err)
		}
		return nil
	}
	if found.ID == "" {
		return errors.New("Navidrome returned a playlist without an ID")
	}
	current, err := c.getPlaylist(ctx, found.ID)
	if err != nil {
		return err
	}
	form := url.Values{"playlistId": {found.ID}}
	for i := range current {
		form.Add("songIndexToRemove", fmt.Sprint(i))
	}
	for _, id := range ids {
		form.Add("songIdToAdd", id)
	}
	_, err = c.call(ctx, "updatePlaylist", form)
	if err != nil {
		return fmt.Errorf("update Navidrome playlist: %w", err)
	}
	return nil
}

func (c *Client) resolve(ctx context.Context, candidate Candidate) (string, error) {
	if strings.TrimSpace(candidate.Title) == "" || len(candidate.Artists) == 0 || !validRelative(candidate.RelativePath) {
		return "", errors.New("candidate metadata or mounted relative path is invalid")
	}
	query := strings.TrimSpace(candidate.Title + " " + strings.Join(candidate.Artists, " "))
	result, err := c.call(ctx, "search3", url.Values{"query": {query}, "songCount": {"50"}, "songOffset": {"0"}, "albumCount": {"0"}, "artistCount": {"0"}})
	if err != nil {
		return "", fmt.Errorf("search Navidrome library: %w", err)
	}
	search, ok := objectAt(result, "searchResult3")
	if !ok {
		return "", errors.New("Navidrome search response is incomplete")
	}
	results, err := songsAt(search, "song")
	if err != nil {
		return "", errors.New("Navidrome search response is invalid")
	}
	expected := joinAbsolute(c.root, candidate.RelativePath)
	var match string
	for _, found := range results {
		if found.ID == "" || !samePath(found.Path, expected) || !sameText(found.Title, candidate.Title) || !sameArtists(found.Artist, candidate.Artists) {
			continue
		}
		if candidate.Album != "" && !sameText(found.Album, candidate.Album) {
			continue
		}
		detail, err := c.getSong(ctx, found.ID)
		if err != nil {
			return "", err
		}
		if detail.ID != found.ID || !samePath(detail.Path, expected) || !sameText(detail.Title, candidate.Title) || !sameArtists(detail.Artist, candidate.Artists) || candidate.Album != "" && !sameText(detail.Album, candidate.Album) {
			continue
		}
		if match != "" && match != detail.ID {
			return "", errors.New("multiple Navidrome songs match the same file and metadata")
		}
		match = detail.ID
	}
	if match == "" {
		return "", errors.New("song is not indexed at the expected mounted path with matching metadata")
	}
	return match, nil
}

func (c *Client) getSong(ctx context.Context, id string) (song, error) {
	result, err := c.call(ctx, "getSong", url.Values{"id": {id}})
	if err != nil {
		return song{}, fmt.Errorf("verify Navidrome song: %w", err)
	}
	value, ok := objectAt(result, "song")
	if !ok {
		return song{}, errors.New("Navidrome song response is incomplete")
	}
	data, err := json.Marshal(value)
	var item song
	if err != nil || json.Unmarshal(data, &item) != nil {
		return song{}, errors.New("Navidrome song response is invalid")
	}
	return item, nil
}

func (c *Client) playlists(ctx context.Context) ([]playlist, error) {
	result, err := c.call(ctx, "getPlaylists", nil)
	if err != nil {
		return nil, fmt.Errorf("list Navidrome playlists: %w", err)
	}
	data, ok := objectAt(result, "playlists")
	if !ok {
		return []playlist{}, nil
	}
	values, err := arrayOrObject(data["playlist"])
	if err != nil {
		return nil, errors.New("Navidrome playlist list is invalid")
	}
	out := make([]playlist, 0, len(values))
	for _, value := range values {
		var item playlist
		encoded, err := json.Marshal(value)
		if err != nil || json.Unmarshal(encoded, &item) != nil {
			return nil, errors.New("Navidrome playlist entry is invalid")
		}
		out = append(out, item)
	}
	return out, nil
}

func (c *Client) getPlaylist(ctx context.Context, id string) ([]song, error) {
	result, err := c.call(ctx, "getPlaylist", url.Values{"id": {id}})
	if err != nil {
		return nil, fmt.Errorf("read Navidrome playlist: %w", err)
	}
	data, ok := objectAt(result, "playlist")
	if !ok {
		return nil, errors.New("Navidrome playlist response is incomplete")
	}
	if _, present := data["entry"]; !present {
		return nil, errors.New("Navidrome playlist response is missing its song entries")
	}
	tracks, err := songsAt(data, "entry")
	if err != nil {
		return nil, errors.New("Navidrome playlist entries are invalid")
	}
	return tracks, nil
}

func (c *Client) call(ctx context.Context, endpoint string, values url.Values) (map[string]json.RawMessage, error) {
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, errors.New("cannot prepare Navidrome authentication")
	}
	salt := hex.EncodeToString(random[:])
	digest := md5.Sum([]byte(c.password + salt))
	form := url.Values{}
	for key, items := range values {
		for _, item := range items {
			form.Add(key, item)
		}
	}
	form.Set("u", c.username)
	form.Set("t", hex.EncodeToString(digest[:]))
	form.Set("s", salt)
	form.Set("v", "1.16.1")
	form.Set("c", "meloport")
	form.Set("f", "json")
	endpointURL := *c.base
	endpointURL.Path = path.Join(c.base.Path, "rest", endpoint+".view")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, errors.New("cannot construct Navidrome request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Meloport/1.0")
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("Navidrome request failed")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return nil, errors.New("Navidrome response could not be read safely")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Navidrome returned HTTP %d", resp.StatusCode)
	}
	var envelope struct {
		Response map[string]json.RawMessage `json:"subsonic-response"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&envelope); err != nil || envelope.Response == nil {
		return nil, errors.New("Navidrome returned an invalid Subsonic response")
	}
	var status string
	if err := json.Unmarshal(envelope.Response["status"], &status); err != nil {
		return nil, errors.New("Navidrome returned an invalid Subsonic status")
	}
	if status != "ok" {
		return nil, errors.New("Navidrome rejected the Subsonic request")
	}
	return envelope.Response, nil
}

func objectAt(data map[string]json.RawMessage, key string) (map[string]json.RawMessage, bool) {
	var value map[string]json.RawMessage
	if json.Unmarshal(data[key], &value) != nil || value == nil {
		return nil, false
	}
	return value, true
}

func songsAt(data map[string]json.RawMessage, key string) ([]song, error) {
	values, err := arrayOrObject(data[key])
	if err != nil {
		return nil, err
	}
	items := make([]song, 0, len(values))
	for _, value := range values {
		encoded, err := json.Marshal(value)
		var item song
		if err != nil || json.Unmarshal(encoded, &item) != nil {
			return nil, errors.New("invalid song object")
		}
		items = append(items, item)
	}
	return items, nil
}

func arrayOrObject(raw json.RawMessage) ([]json.RawMessage, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return []json.RawMessage{}, nil
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		return []json.RawMessage{append(json.RawMessage(nil), trimmed...)}, nil
	}
	var values []json.RawMessage
	if err := json.Unmarshal(trimmed, &values); err != nil {
		return nil, err
	}
	return values, nil
}

func cleanAbsolute(value string) (string, bool) {
	value = strings.ReplaceAll(value, "\\", "/")
	if value == "" || strings.ContainsAny(value, "\x00\r\n") {
		return "", false
	}
	drive := len(value) >= 3 && value[1] == ':' && value[2] == '/'
	if !strings.HasPrefix(value, "/") && !drive {
		return "", false
	}
	parts := value
	if drive {
		parts = value[3:]
	} else {
		parts = strings.TrimPrefix(value, "/")
	}
	components := strings.Split(parts, "/")
	for i, part := range components {
		if part == "" && i == len(components)-1 {
			continue
		}
		if part == "" || part == "." || part == ".." || strings.IndexFunc(part, unicode.IsControl) >= 0 {
			return "", false
		}
	}
	clean := path.Clean(value)
	if drive && clean == value[:2] {
		clean += "/"
	}
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	return clean, true
}

func validRelative(value string) bool {
	value = strings.ReplaceAll(value, "\\", "/")
	if value == "" || strings.HasPrefix(value, "/") || strings.ContainsAny(value, "\x00\r\n") {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, `:*?"<>|`) || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return false
		}
		if strings.IndexFunc(part, unicode.IsControl) >= 0 {
			return false
		}
	}
	return true
}

func joinAbsolute(root, relative string) string {
	return strings.TrimRight(root, "/") + "/" + strings.ReplaceAll(relative, "\\", "/")
}

func samePath(actual, expected string) bool {
	actual, ok := cleanAbsolute(actual)
	if !ok {
		return false
	}
	expected, ok = cleanAbsolute(expected)
	if !ok {
		return false
	}
	if len(expected) >= 3 && expected[1] == ':' {
		return strings.EqualFold(actual, expected)
	}
	return actual == expected
}

func sameText(a, b string) bool { return normalize(a) == normalize(b) }

func sameArtists(actual string, expected []string) bool {
	parts := make([]string, 0, len(expected))
	for _, value := range expected {
		if strings.TrimSpace(value) != "" {
			parts = append(parts, normalize(value))
		}
	}
	return len(parts) > 0 && normalize(actual) == strings.Join(parts, "")
}

func normalize(value string) string {
	var out strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		if unicode.IsSpace(r) || strings.ContainsRune("&、,，;；/", r) {
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}
