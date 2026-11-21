package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCloud115ConfigurationKeepsCD2AwayFromWorkFiles(t *testing.T) {
	const cloud = "cloud115:\n  enabled: true\n  target_cid: '1234'\n"
	valid := writeConfig(t, cloud+"  media_base_url: https://media.example.com/music/\ntasks: []\n")
	cfg, err := Load(valid)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Cloud115.StagingDir != filepath.Join(filepath.Dir(valid), "data", "115-staging") ||
		cfg.Cloud115.MediaBaseURL != "https://media.example.com/music" {
		t.Fatalf("unexpected cloud settings: %+v", cfg.Cloud115)
	}
	for _, test := range []struct{ name, settings string }{
		{"whole data tree", "  staging_dir: data\n"},
		{"STRM under staging", "  strm_dir: data/115-staging/strm\n"},
		{"same listener", "  media_listen: '127.0.0.1:8080'\n"},
		{"cleartext public address", "  media_base_url: http://media.example.com\n"},
		{"relative media mount", "  navidrome_url: http://127.0.0.1:4533\n  navidrome_mount_root: music/library\n"},
		{"task under staging", "tasks:\n- id: songs\n  playlist: '123'\n  directory: ../115-staging/songs\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, cloud+test.settings)); err == nil {
				t.Fatalf("accepted invalid configuration: %s", test.name)
			}
		})
	}
	localNavidrome := cloud + "  navidrome_url: http://127.0.0.1:4533\n  navidrome_mount_root: /music/115\n  navidrome_playlist_sync: true\n  navidrome_username: sync\ntasks: []\n"
	if _, err := Load(writeConfig(t, localNavidrome)); err != nil {
		t.Fatalf("loopback HTTP Navidrome should remain supported: %v", err)
	}
	remoteNavidrome := cloud + "  navidrome_url: http://music.example.com:4533\n  navidrome_mount_root: /music/115\n  navidrome_playlist_sync: true\n  navidrome_username: sync\ntasks: []\n"
	if _, err := Load(writeConfig(t, remoteNavidrome)); err == nil {
		t.Fatal("accepted cleartext credentials to a remote Navidrome server")
	}
	const private = "not-a-real-115-token"
	_, err = Load(writeConfig(t, cloud+"  access_token: "+private+"\n"))
	if err == nil || strings.Contains(err.Error(), private) {
		t.Fatalf("unexpected secret handling: %v", err)
	}
}
