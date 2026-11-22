package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"meloport/internal/media"
)

type Cloud115 struct {
	Enabled               bool     `yaml:"enabled"`
	AccessTokenEnv        string   `yaml:"access_token_env"`
	ClientID              string   `yaml:"client_id"`
	TargetCID             string   `yaml:"target_cid"`
	StagingDir            string   `yaml:"staging_dir"`
	STRMDir               string   `yaml:"strm_dir"`
	MediaEnabled          bool     `yaml:"media_enabled"`
	MediaListen           string   `yaml:"media_listen"`
	MediaBaseURL          string   `yaml:"media_base_url"`
	NavidromeEnabled      bool     `yaml:"navidrome_enabled"`
	NavidromeURL          string   `yaml:"navidrome_url"`
	NavidromeMountRoot    string   `yaml:"navidrome_mount_root"`
	NavidromeUsername     string   `yaml:"navidrome_username"`
	NavidromePasswordEnv  string   `yaml:"navidrome_password_env"`
	NavidromePlaylistSync bool     `yaml:"navidrome_playlist_sync"`
	PollInterval          Duration `yaml:"poll_interval"`
	Timeout               Duration `yaml:"timeout"`
	stagingDirInput       string
	strmDirInput          string
}

func cloud115Defaults() Cloud115 {
	return Cloud115{
		AccessTokenEnv: "MELOPORT_115_ACCESS_TOKEN", NavidromePasswordEnv: "MELOPORT_NAVIDROME_PASSWORD",
		ClientID:   "100197303",
		StagingDir: "data/115-staging", STRMDir: "data/strm",
		MediaListen: "127.0.0.1:8081", MediaBaseURL: "http://127.0.0.1:8081",
		PollInterval: Duration{time.Minute}, Timeout: Duration{15 * time.Second},
	}
}

func (c *Config) validateCloud115(baseDir string) error {
	cloud := &c.Cloud115
	cloud.stagingDirInput, cloud.strmDirInput = cloud.StagingDir, cloud.STRMDir
	if cloud.NavidromePasswordEnv == "" {
		cloud.NavidromePasswordEnv = "MELOPORT_NAVIDROME_PASSWORD"
	}
	for _, address := range []string{cloud.MediaBaseURL, cloud.NavidromeURL} {
		parsed, err := url.Parse(address)
		if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || strings.Contains(address, "#") {
			return errors.New("media and Navidrome URLs must not contain credentials, query parameters or fragments")
		}
	}
	if cloud.Enabled {
		if !idPattern.MatchString(cloud.ClientID) {
			return errors.New("cloud115.client_id must be a numeric 115 application ID")
		}
		if !envPattern.MatchString(cloud.AccessTokenEnv) || !idPattern.MatchString(cloud.TargetCID) {
			return errors.New("cloud115 requires an access token environment variable name and a non-root numeric target_cid")
		}
		if cloud.Timeout.Duration < time.Second || cloud.Timeout.Duration > time.Minute || cloud.PollInterval.Duration < 15*time.Second || cloud.PollInterval.Duration > 24*time.Hour {
			return errors.New("cloud115.timeout must be between 1s and 1m and poll_interval between 15s and 24h")
		}
	}
	if cloud.MediaEnabled {
		host, port, err := net.SplitHostPort(cloud.MediaListen)
		portNumber, portErr := strconv.Atoi(port)
		if err != nil || portErr != nil || portNumber < 1 || portNumber > 65535 || host == "" || cloud.MediaListen == c.Server.Listen {
			return errors.New("cloud115.media_listen must specify its own host and port")
		}
	}
	if cloud.Enabled || cloud.MediaEnabled {
		public, err := parseCloudURL(cloud.MediaBaseURL)
		if err != nil || (public.Scheme == "http" && !strings.EqualFold(public.Hostname(), "localhost") && !net.ParseIP(public.Hostname()).IsLoopback()) {
			return errors.New("cloud115.media_base_url must use HTTPS or loopback HTTP, with no credentials, query or fragment")
		}
		cloud.MediaBaseURL = strings.TrimRight(public.String(), "/")
	}
	var navidromeURL *url.URL
	if cloud.NavidromeEnabled {
		if !cloud.MediaEnabled {
			return errors.New("Navidrome integration requires cloud115.media_enabled")
		}
		var err error
		if navidromeURL, err = parseCloudURL(cloud.NavidromeURL); err != nil {
			return errors.New("cloud115.navidrome_url must be an HTTP(S) URL without credentials, query or fragment")
		}
		if !media.ValidMountRoot(cloud.NavidromeMountRoot) {
			return errors.New("cloud115.navidrome_mount_root must be an absolute POSIX or Windows drive path")
		}
	}
	if cloud.NavidromeEnabled && cloud.NavidromePlaylistSync {
		if !cloud.Enabled {
			return errors.New("Navidrome playlist sync requires enabled 115 storage")
		}
		if navidromeURL.Scheme == "http" && !strings.EqualFold(navidromeURL.Hostname(), "localhost") && !net.ParseIP(navidromeURL.Hostname()).IsLoopback() {
			return errors.New("Navidrome playlist sync requires HTTPS for non-loopback URLs")
		}
		if strings.TrimSpace(cloud.NavidromeUsername) == "" || strings.ContainsAny(cloud.NavidromeUsername, "\x00\r\n") {
			return errors.New("cloud115.navidrome_username is required for playlist sync")
		}
		if !envPattern.MatchString(cloud.NavidromePasswordEnv) {
			return errors.New("cloud115.navidrome_password_env must name an environment variable")
		}
	}
	if !cloud.Enabled {
		return nil
	}
	var err error
	if cloud.StagingDir, err = ResolveMusicDirectory(baseDir, cloud.StagingDir); err != nil {
		return errors.New("cloud115.staging_dir must be a valid directory")
	}
	if cloud.STRMDir, err = ResolveMusicDirectory(baseDir, cloud.STRMDir); err != nil {
		return errors.New("cloud115.strm_dir must be a valid directory")
	}
	stage, err := physicalPath(cloud.StagingDir)
	if err != nil {
		return errors.New("cannot resolve cloud115.staging_dir")
	}
	strm, err := physicalPath(cloud.STRMDir)
	if err != nil {
		return errors.New("cannot resolve cloud115.strm_dir")
	}
	if overlaps(stage, strm) {
		return errors.New("cloud115.staging_dir and strm_dir must be separate trees")
	}
	check := func(path string) error {
		physical, err := physicalPath(path)
		if err != nil || overlaps(stage, physical) || overlaps(strm, physical) {
			return errors.New("cloud115 staging and STRM directories must be separate from work files and state")
		}
		return nil
	}
	for _, path := range []string{c.Storage.MusicDir, c.Storage.Database} {
		if err := check(path); err != nil {
			return err
		}
	}
	for _, task := range c.Tasks {
		if err := check(task.Directory); err != nil {
			return fmt.Errorf("task %s: %w", task.ID, err)
		}
	}
	return nil
}

func parseCloudURL(value string) (*url.URL, error) {
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.ForceQuery || u.RawQuery != "" || strings.Contains(value, "#") || strings.ContainsAny(value, "\\\r\n") {
		return nil, errors.New("invalid cloud URL")
	}
	return u, nil
}

// Resolve symlinks in existing ancestors, including junctions on Windows.
func physicalPath(name string) (string, error) {
	resolved, err := filepath.EvalSymlinks(name)
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		return resolved, err
	}
	if _, err := os.Lstat(name); !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("path contains an unresolved link")
	}
	parent := filepath.Dir(name)
	if parent == name {
		return "", err
	}
	resolved, err = physicalPath(parent)
	return filepath.Join(resolved, filepath.Base(name)), err
}

func overlaps(a, b string) bool {
	within := func(root, child string) bool {
		relative, err := filepath.Rel(root, child)
		return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
	}
	return within(a, b) || within(b, a)
}
