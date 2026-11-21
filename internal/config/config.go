// Package config reads and validates subscription configuration without network I/O.
package config

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/robfig/cron/v3"
	"go.yaml.in/yaml/v3"

	"meloport/internal/model"
	"meloport/internal/platform/kugou"
	"meloport/internal/platform/qq"
)

const DefaultSchedule = "0 3 * * *"

var (
	idPattern   = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
	taskPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	envPattern  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	parser      = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
)

type Duration struct{ time.Duration }

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	if value.Tag != "!!str" {
		return errors.New("duration must be a string with a unit, for example 30s")
	}
	parsed, err := time.ParseDuration(value.Value)
	if err != nil {
		return errors.New("invalid duration; use a value such as 30s or 5m")
	}
	d.Duration = parsed
	return nil
}

type Config struct {
	Timezone   string         `yaml:"timezone"`
	Schedule   string         `yaml:"schedule"`
	Quality    string         `yaml:"quality"`
	RunOnStart bool           `yaml:"run_on_start"`
	API        API            `yaml:"api"`
	Metadata   Metadata       `yaml:"metadata"`
	Storage    Storage        `yaml:"storage"`
	Download   Download       `yaml:"download"`
	Server     Server         `yaml:"server"`
	Cloud115   Cloud115       `yaml:"cloud115"`
	Tasks      []Task         `yaml:"tasks"`
	Location   *time.Location `yaml:"-"`
}

type API struct {
	BaseURL     string   `yaml:"base_url"`
	KeyEnv      string   `yaml:"key_env"`
	Timeout     Duration `yaml:"timeout"`
	MinInterval Duration `yaml:"min_interval"`
}

type Metadata struct {
	Timeout          Duration `yaml:"timeout"`
	MinInterval      Duration `yaml:"min_interval"`
	FallbackToAPI    bool     `yaml:"fallback_to_api"`
	NeteaseCookieEnv string   `yaml:"netease_cookie_env"`
	QQCookieEnv      string   `yaml:"qq_cookie_env"`
	KugouCookieEnv   string   `yaml:"kugou_cookie_env"`
}

type Storage struct {
	Database           string `yaml:"database"`
	MusicDir           string `yaml:"music_dir"`
	MusicDirConfigured string `yaml:"-"`
}

type Download struct {
	Timeout   Duration `yaml:"timeout"`
	MaxSizeMB int64    `yaml:"max_size_mb"`
}

type Server struct {
	Listen string `yaml:"listen"`
}

type Task struct {
	ID         string `yaml:"id"`
	Platform   string `yaml:"platform"`
	Playlist   string `yaml:"playlist"`
	SourceType string `yaml:"source_type"`
	SourceID   string `yaml:"source_id"`
	Schedule   string `yaml:"schedule"`
	Quality    string `yaml:"quality"`
	Directory  string `yaml:"directory"`
	Enabled    *bool  `yaml:"enabled"`
	PlaylistID string `yaml:"-"`
}

func (t Task) IsEnabled() bool { return t.Enabled == nil || *t.Enabled }

func (t Task) Model() model.Task {
	sourceID := t.SourceID
	if sourceID == "" {
		sourceID = t.PlaylistID
	}
	sourceType := t.SourceType
	if sourceType == "" {
		sourceType = "playlist"
	}
	return model.Task{ID: t.ID, SourceType: sourceType, SourceID: sourceID, PlaylistID: t.PlaylistID, Platform: t.Platform, Quality: t.Quality, Directory: t.Directory}
}

func defaults() Config {
	return Config{
		Timezone: "Asia/Shanghai", Schedule: DefaultSchedule, Quality: "lossless",
		API:      API{BaseURL: "https://api.chksz.com", KeyEnv: "CHKSZ_API_KEY", Timeout: Duration{30 * time.Second}, MinInterval: Duration{3100 * time.Millisecond}},
		Metadata: Metadata{Timeout: Duration{30 * time.Second}, MinInterval: Duration{time.Second}, FallbackToAPI: true, NeteaseCookieEnv: "MELOPORT_NETEASE_COOKIE", QQCookieEnv: "MELOPORT_QQ_COOKIE", KugouCookieEnv: "MELOPORT_KUGOU_COOKIE"},
		Storage:  Storage{Database: "data/meloport.db", MusicDir: "data/music"},
		Download: Download{Timeout: Duration{5 * time.Minute}, MaxSizeMB: 512},
		Server:   Server{Listen: "127.0.0.1:8080"},
		Cloud115: cloud115Defaults(),
	}
}

// Load resolves every relative filesystem path against the configuration's directory.
func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open configuration: %w", err)
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || info.Size() > 1<<20 {
		return nil, errors.New("configuration must be a readable file of at most 1 MiB")
	}
	cfg := defaults()
	dec := yaml.NewDecoder(io.LimitReader(f, 1<<20))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		// YAML parser errors can echo secret values from an incorrectly named key.
		return nil, errors.New("invalid configuration YAML: check field names, value types and indentation against config.example.yaml")
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, errors.New("configuration must contain exactly one YAML document")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, errors.New("cannot resolve configuration path")
	}
	if err := cfg.validate(filepath.Dir(abs)); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) validate(baseDir string) error {
	var err error
	c.Location, err = time.LoadLocation(c.Timezone)
	if err != nil {
		return errors.New("invalid timezone; use an IANA name such as Asia/Shanghai")
	}
	if _, err = ParseSchedule(c.Schedule); err != nil {
		return fmt.Errorf("global schedule: %w", err)
	}
	if !validGlobalQuality(c.Quality) {
		return errors.New("invalid global quality")
	}
	u, err := url.Parse(c.API.BaseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return errors.New("api.base_url must be an HTTP(S) URL without credentials, query or fragment")
	}
	if u.Scheme == "http" && u.Hostname() != "localhost" && !net.ParseIP(u.Hostname()).IsLoopback() {
		return errors.New("api.base_url must use HTTPS except on loopback during development")
	}
	if !envPattern.MatchString(c.API.KeyEnv) {
		return errors.New("api.key_env must name an environment variable")
	}
	if c.API.Timeout.Duration <= 0 || c.API.Timeout.Duration > 10*time.Minute {
		return errors.New("api.timeout must be greater than zero and at most 10m")
	}
	if c.API.MinInterval.Duration < 3100*time.Millisecond {
		return errors.New("api.min_interval must be at least 3100ms to respect the 20 requests/minute limit")
	}
	if c.Metadata.Timeout.Duration <= 0 || c.Metadata.Timeout.Duration > 10*time.Minute {
		return errors.New("metadata.timeout must be greater than zero and at most 10m")
	}
	if c.Metadata.MinInterval.Duration < time.Second || c.Metadata.MinInterval.Duration > time.Hour {
		return errors.New("metadata.min_interval must be between 1s and 1h")
	}
	for _, name := range []string{c.Metadata.NeteaseCookieEnv, c.Metadata.QQCookieEnv, c.Metadata.KugouCookieEnv} {
		if name != "" && !envPattern.MatchString(name) {
			return errors.New("metadata cookie settings must name environment variables")
		}
	}
	if c.Download.Timeout.Duration <= 0 || c.Download.Timeout.Duration > time.Hour {
		return errors.New("download.timeout must be greater than zero and at most 1h")
	}
	if c.Download.MaxSizeMB < 1 || c.Download.MaxSizeMB > 10240 {
		return errors.New("download.max_size_mb must be between 1 and 10240")
	}
	if c.Storage.Database == "" || c.Storage.Database == ":memory:" || c.Storage.MusicDir == "" {
		return errors.New("storage.database and storage.music_dir must be nonempty filesystem paths")
	}
	c.Storage.Database = resolvePath(baseDir, c.Storage.Database)
	c.Storage.MusicDirConfigured = c.Storage.MusicDir
	c.Storage.MusicDir = resolvePath(baseDir, c.Storage.MusicDir)
	if c.Server.Listen != "" {
		host, _, err := net.SplitHostPort(c.Server.Listen)
		if err != nil {
			return errors.New("server.listen must be host:port, or an empty string to disable HTTP health checks")
		}
		if host != "localhost" && !net.ParseIP(host).IsLoopback() {
			return errors.New("server.listen must bind a loopback address for local development")
		}
	}
	if len(c.Tasks) > 1000 {
		return errors.New("configure at most 1000 subscription tasks")
	}
	ids, dirs := map[string]bool{}, map[string]bool{}
	for i := range c.Tasks {
		t := &c.Tasks[i]
		if !taskPattern.MatchString(t.ID) || windowsReserved(t.ID) {
			return fmt.Errorf("tasks[%d].id must be a portable directory name of 1-64 letters, digits, underscores or hyphens", i)
		}
		if ids[strings.ToLower(t.ID)] {
			return fmt.Errorf("tasks[%d] has a duplicate task id", i)
		}
		ids[strings.ToLower(t.ID)] = true
		if t.Platform == "" {
			t.Platform = "netease"
		}
		if t.SourceType == "" {
			t.SourceType = "playlist"
		}
		switch t.SourceType {
		case "playlist":
			if t.Playlist != "" && t.SourceID != "" {
				return fmt.Errorf("task %s: provide either playlist or source_id for a playlist task", t.ID)
			}
			inputID := t.Playlist
			if inputID == "" {
				inputID = t.SourceID
			}
			t.PlaylistID, err = ParsePlaylistFor(t.Platform, inputID)
			if err != nil {
				return fmt.Errorf("task %s: %w", t.ID, err)
			}
			t.SourceID = t.PlaylistID
		case "ranking":
			if t.Playlist != "" {
				return fmt.Errorf("task %s: ranking tasks must use source_id, not playlist", t.ID)
			}
			t.SourceID, err = ParseRankingFor(t.Platform, t.SourceID)
			if err != nil {
				return fmt.Errorf("task %s: %w", t.ID, err)
			}
		default:
			return fmt.Errorf("task %s: source_type must be playlist or ranking", t.ID)
		}
		if t.Schedule == "" {
			t.Schedule = c.Schedule
		}
		if _, err := ParseSchedule(t.Schedule); err != nil {
			return fmt.Errorf("task %s schedule: %w", t.ID, err)
		}
		if t.Quality == "" {
			t.Quality = effectiveQuality(t.Platform, c.Quality)
		}
		if !validTaskQuality(t.Platform, t.Quality) {
			return fmt.Errorf("task %s: unsupported native platform quality", t.ID)
		}
		if t.Directory == "" {
			t.Directory = t.ID
		}
		t.Directory = resolvePath(c.Storage.MusicDir, t.Directory)
		key := strings.ToLower(t.Directory)
		if dirs[key] {
			return fmt.Errorf("task %s: each task must have its own output directory", t.ID)
		}
		dirs[key] = true
	}
	return c.validateCloud115(baseDir)
}

func ParsePlaylistFor(source, value string) (string, error) {
	if err := rejectPlaylistCredentials(value); err != nil {
		return "", err
	}
	switch source {
	case "netease":
		return ParsePlaylist(value)
	case "qq":
		return qq.NormalizePlaylist(value)
	case "kugou":
		return kugou.NormalizePlaylist(value)
	default:
		return "", errors.New("platform must be netease, qq or kugou")
	}
}

func ParseRankingFor(source, value string) (string, error) {
	if strings.TrimSpace(value) != value || !idPattern.MatchString(value) {
		return "", errors.New("ranking source ID must be a positive integer")
	}
	switch source {
	case "netease", "qq", "kugou":
		return value, nil
	default:
		return "", errors.New("platform must be netease, qq or kugou")
	}
}

// ParseSchedule accepts standard five-field cron; timezone is configured separately.
func ParseSchedule(value string) (cron.Schedule, error) {
	if strings.HasPrefix(value, "TZ=") || strings.HasPrefix(value, "CRON_TZ=") {
		return nil, errors.New("set timezone with the global timezone field")
	}
	schedule, err := parser.Parse(value)
	if err != nil {
		return nil, errors.New("expected a five-field cron expression (minute hour day month weekday)")
	}
	if schedule.Next(time.Now()).IsZero() {
		return nil, errors.New("cron expression has no upcoming execution")
	}
	return schedule, nil
}

func ParsePlaylist(value string) (string, error) {
	if err := rejectPlaylistCredentials(value); err != nil {
		return "", err
	}
	value = strings.TrimSpace(value)
	if idPattern.MatchString(value) {
		return value, nil
	}
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Port() != "" {
		return "", playlistError()
	}
	host := strings.ToLower(u.Hostname())
	if host != "music.163.com" && !strings.HasSuffix(host, ".music.163.com") {
		return "", playlistError()
	}
	path, query := u.Path, u.Query()
	if u.Fragment != "" {
		fragment, err := url.Parse(u.Fragment)
		if err != nil {
			return "", playlistError()
		}
		path, query = fragment.Path, fragment.Query()
	}
	if path != "/playlist" && path != "/m/playlist" {
		return "", playlistError()
	}
	id := query.Get("id")
	if !idPattern.MatchString(id) {
		return "", playlistError()
	}
	return id, nil
}

func playlistError() error {
	return errors.New("playlist must be a numeric NetEase playlist ID or a full music.163.com playlist URL; expand share short links first")
}

func rejectPlaylistCredentials(value string) error {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return nil
	}
	queries := []url.Values{u.Query()}
	if fragment, err := url.Parse(u.Fragment); err == nil {
		queries = append(queries, fragment.Query())
	}
	for _, query := range queries {
		for name, values := range query {
			lower := strings.ToLower(name)
			if strings.HasPrefix(lower, "pass") {
				return errors.New("playlist links must not contain credentials")
			}
			switch lower {
			case "apikey", "api_key", "key", "token", "access_token", "cookie", "authorization", "auth", "session", "sessionid", "credential", "music_u":
				return errors.New("playlist links must not contain credentials; use the public playlist ID and optional cookie environment variables")
			}
			for _, item := range values {
				if strings.Contains(item, "chksz_") {
					return errors.New("playlist links must not contain an API key")
				}
			}
		}
	}
	return nil
}

func resolvePath(base, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(base, path))
}

// ResolveMusicDirectory validates the local download root without creating it.
// Relative paths are resolved beside the configuration file. Missing child
// folders are allowed; an existing file anywhere in their ancestry is not.
func ResolveMusicDirectory(baseDir, value string) (string, error) {
	if strings.TrimSpace(value) == "" || len(value) > 32767 || strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("storage.music_dir must be a nonempty valid folder path")
	}
	if filepath.VolumeName(value) != "" && !filepath.IsAbs(value) {
		return "", errors.New("storage.music_dir must use a full drive path or a relative folder path")
	}
	resolved, err := filepath.Abs(resolvePath(baseDir, value))
	if err != nil {
		return "", errors.New("storage.music_dir cannot be resolved as a folder path")
	}
	for current := resolved; ; current = filepath.Dir(current) {
		info, err := os.Stat(current)
		if err == nil {
			if !info.IsDir() {
				return "", errors.New("storage.music_dir must be a folder; the path or a parent is an existing file")
			}
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", errors.New("storage.music_dir is invalid or inaccessible; choose an accessible folder")
		}
		if parent := filepath.Dir(current); parent == current {
			return "", errors.New("storage.music_dir has no available filesystem root")
		}
	}
}

func validQuality(value string) bool {
	switch value {
	case "standard", "exhigh", "lossless", "hires", "jyeffect", "sky", "jymaster":
		return true
	}
	return false
}

func validGlobalQuality(value string) bool {
	return validQuality(value) || value == "128k" || value == "320k" || value == "flac" || value == "master"
}

func validTaskQuality(source, value string) bool {
	if source == "netease" {
		return validQuality(value)
	}
	switch value {
	case "128k", "320k", "flac", "hires", "master":
		return true
	}
	return false
}

func effectiveQuality(source, value string) string {
	if source == "netease" {
		switch value {
		case "128k":
			return "standard"
		case "320k":
			return "exhigh"
		case "flac":
			return "lossless"
		case "master":
			return "jymaster"
		}
	} else {
		switch value {
		case "standard":
			return "128k"
		case "exhigh":
			return "320k"
		case "lossless":
			return "flac"
		case "jymaster":
			return "master"
		}
	}
	return value
}

func windowsReserved(value string) bool {
	v := strings.ToUpper(value)
	if v == "CON" || v == "PRN" || v == "AUX" || v == "NUL" {
		return true
	}
	return len(v) == 4 && (strings.HasPrefix(v, "COM") || strings.HasPrefix(v, "LPT")) && v[3] >= '1' && v[3] <= '9'
}
