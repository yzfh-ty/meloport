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
	Enabled            bool     `yaml:"enabled"`
	AccessTokenEnv     string   `yaml:"access_token_env"`
	TargetCID          string   `yaml:"target_cid"`
	StagingDir         string   `yaml:"staging_dir"`
	STRMDir            string   `yaml:"strm_dir"`
	MediaListen        string   `yaml:"media_listen"`
	MediaBaseURL       string   `yaml:"media_base_url"`
	NavidromeURL       string   `yaml:"navidrome_url"`
	NavidromeMountRoot string   `yaml:"navidrome_mount_root"`
	PollInterval       Duration `yaml:"poll_interval"`
	Timeout            Duration `yaml:"timeout"`
}

func cloud115Defaults() Cloud115 {
	return Cloud115{
		AccessTokenEnv: "MELOPORT_115_ACCESS_TOKEN",
		StagingDir:     "data/115-staging", STRMDir: "data/strm",
		MediaListen: "127.0.0.1:8081", MediaBaseURL: "http://127.0.0.1:8081",
		PollInterval: Duration{time.Minute}, Timeout: Duration{15 * time.Second},
	}
}

func (c *Config) validateCloud115(baseDir string) error {
	cloud := &c.Cloud115
	if !cloud.Enabled {
		return nil
	}
	if !envPattern.MatchString(cloud.AccessTokenEnv) || !idPattern.MatchString(cloud.TargetCID) {
		return errors.New("cloud115 requires an access token environment variable name and a non-root numeric target_cid")
	}
	if cloud.Timeout.Duration < time.Second || cloud.Timeout.Duration > time.Minute || cloud.PollInterval.Duration < 15*time.Second || cloud.PollInterval.Duration > 24*time.Hour {
		return errors.New("cloud115.timeout must be between 1s and 1m and poll_interval between 15s and 24h")
	}
	host, port, err := net.SplitHostPort(cloud.MediaListen)
	portNumber, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || portNumber < 1 || portNumber > 65535 || host == "" || cloud.MediaListen == c.Server.Listen {
		return errors.New("cloud115.media_listen must specify its own host and port")
	}
	public, err := parseCloudURL(cloud.MediaBaseURL)
	if err != nil || (public.Scheme == "http" && !strings.EqualFold(public.Hostname(), "localhost") && !net.ParseIP(public.Hostname()).IsLoopback()) {
		return errors.New("cloud115.media_base_url must use HTTPS or loopback HTTP, with no credentials, query or fragment")
	}
	cloud.MediaBaseURL = strings.TrimRight(public.String(), "/")
	if (cloud.NavidromeURL == "") != (cloud.NavidromeMountRoot == "") {
		return errors.New("set both cloud115.navidrome_url and navidrome_mount_root to enable the music proxy")
	}
	if cloud.NavidromeURL != "" {
		if _, err := parseCloudURL(cloud.NavidromeURL); err != nil {
			return errors.New("cloud115.navidrome_url must be an HTTP(S) URL without credentials, query or fragment")
		}
		if !media.ValidMountRoot(cloud.NavidromeMountRoot) {
			return errors.New("cloud115.navidrome_mount_root must be an absolute POSIX or Windows drive path")
		}
	}
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
