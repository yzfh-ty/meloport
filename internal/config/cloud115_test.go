package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
		{"same listener", "  media_enabled: true\n  media_listen: '127.0.0.1:8080'\n"},
		{"cleartext public address", "  media_base_url: http://media.example.com\n"},
		{"relative media mount", "  media_enabled: true\n  navidrome_enabled: true\n  navidrome_url: http://127.0.0.1:4533\n  navidrome_mount_root: music/library\n"},
		{"task under staging", "tasks:\n- id: songs\n  playlist: '123'\n  directory: ../115-staging/songs\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, cloud+test.settings)); err == nil {
				t.Fatalf("accepted invalid configuration: %s", test.name)
			}
		})
	}
	localNavidrome := cloud + "  media_enabled: true\n  navidrome_enabled: true\n  navidrome_url: http://127.0.0.1:4533\n  navidrome_mount_root: /music/115\n  navidrome_playlist_sync: true\n  navidrome_username: sync\ntasks: []\n"
	if _, err := Load(writeConfig(t, localNavidrome)); err != nil {
		t.Fatalf("loopback HTTP Navidrome should remain supported: %v", err)
	}
	remoteNavidrome := cloud + "  media_enabled: true\n  navidrome_enabled: true\n  navidrome_url: http://music.example.com:4533\n  navidrome_mount_root: /music/115\n  navidrome_playlist_sync: true\n  navidrome_username: sync\ntasks: []\n"
	if _, err := Load(writeConfig(t, remoteNavidrome)); err == nil {
		t.Fatal("accepted cleartext credentials to a remote Navidrome server")
	}
	const private = "not-a-real-115-token"
	_, err = Load(writeConfig(t, cloud+"  access_token: "+private+"\n"))
	if err == nil || strings.Contains(err.Error(), private) {
		t.Fatalf("unexpected secret handling: %v", err)
	}
}

func TestOptionalIntegrationsDefaultToLocalDownloads(t *testing.T) {
	cfg, err := Load(writeConfig(t, "tasks:\n- id: favorites\n  playlist: '123'\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Cloud115.Enabled || cfg.Cloud115.MediaEnabled || cfg.Cloud115.NavidromeEnabled || cfg.Cloud115.NavidromePlaylistSync {
		t.Fatal("optional integrations must remain disabled by default")
	}
	if len(cfg.Tasks) != 1 || !strings.HasPrefix(cfg.Tasks[0].Directory, cfg.Storage.MusicDir+string(filepath.Separator)) {
		t.Fatal("a default subscription must save to the local music directory")
	}
}

func TestMediaAndNavidromeRequireExplicitSwitches(t *testing.T) {
	for _, item := range []struct {
		name, settings string
		valid          bool
	}{
		{"disabled endpoints", "  media_listen: invalid\n  navidrome_url: incomplete\n  navidrome_mount_root: relative\n  navidrome_playlist_sync: true\n", true},
		{"disabled address with credentials", "  navidrome_url: https://user:fixture@example.com\n", false},
		{"disabled media address with a token query", "  media_base_url: https://media.example.com?token=fixture-only\n", false},
		{"cloud without media", "  enabled: true\n  target_cid: '1234'\n  media_listen: invalid\n", true},
		{"media port only", "  media_enabled: true\n  media_listen: '127.0.0.1:18081'\n", true},
		{"invalid media port", "  media_enabled: true\n  media_listen: '127.0.0.1:70000'\n", false},
		{"management port conflict", "  media_enabled: true\n  media_listen: '127.0.0.1:8080'\n", false},
		{"Navidrome without media", "  navidrome_enabled: true\n  navidrome_url: http://127.0.0.1:4533\n  navidrome_mount_root: /music\n", false},
		{"Navidrome without cloud", "  media_enabled: true\n  navidrome_enabled: true\n  navidrome_url: http://127.0.0.1:4533\n  navidrome_mount_root: /music\n", true},
		{"enabled Navidrome missing address", "  media_enabled: true\n  navidrome_enabled: true\n  navidrome_mount_root: /music\n", false},
		{"playlist sync without cloud", "  media_enabled: true\n  navidrome_enabled: true\n  navidrome_url: http://127.0.0.1:4533\n  navidrome_mount_root: /music\n  navidrome_playlist_sync: true\n", false},
	} {
		t.Run(item.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, "cloud115:\n"+item.settings+"tasks: []\n"))
			if (err == nil) != item.valid {
				t.Fatalf("valid = %t, error = %v", item.valid, err)
			}
		})
	}
}

func TestEditorPersistsOptionalSwitchesAndCustomMediaPort(t *testing.T) {
	const mediaListen = "127.0.0.1:18081"
	t.Setenv("MELOPORT_MEDIA_LISTEN", mediaListen)
	path, editor := editorFixture(t, editorExample)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	settings := cfg.Cloud115.Settings()
	settings.MediaEnabled, settings.NavidromeEnabled = true, true
	settings.MediaBaseURL = "http://127.0.0.1:18081"
	settings.NavidromeURL, settings.NavidromeMountRoot = "http://127.0.0.1:4533", "/music"
	if _, err := editor.Cloud115(settings); err != nil {
		t.Fatal(err)
	}
	saved, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Cloud115.Enabled || !saved.Cloud115.MediaEnabled || !saved.Cloud115.NavidromeEnabled || saved.Cloud115.MediaListen != mediaListen || saved.Cloud115.MediaBaseURL != settings.MediaBaseURL {
		t.Fatal("the independent switches or custom media address did not survive a reload")
	}
	if saved.Storage.MusicDir != cfg.Storage.MusicDir || saved.Tasks[0].Directory != cfg.Tasks[0].Directory {
		t.Fatal("Navidrome settings changed local download destinations")
	}
	settings.MediaEnabled, settings.NavidromeEnabled = false, false
	if _, err := editor.Cloud115(settings); err != nil {
		t.Fatal(err)
	}
	disabled, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Cloud115.MediaEnabled || disabled.Cloud115.NavidromeEnabled || disabled.Cloud115.NavidromeURL != settings.NavidromeURL || disabled.Cloud115.MediaListen != mediaListen {
		t.Fatal("disabling an integration must preserve its saved addresses")
	}
}

func TestCloudDeploymentEnvironmentSurvivesWebSavesWithoutPersistence(t *testing.T) {
	environment := map[string]string{
		"MELOPORT_115_CLIENT_ID":      " 987654321 ",
		"MELOPORT_115_POLL_INTERVAL":  " 5m ",
		"MELOPORT_115_TIMEOUT":        " 12s ",
		"MELOPORT_MEDIA_LISTEN":       " 127.0.0.1:18082 ",
		"MELOPORT_NAVIDROME_USERNAME": " environment-only-user ",
	}
	for name, value := range environment {
		t.Setenv(name, value)
	}
	path := writeConfig(t, `cloud115:
  client_id: "100197303" # Keep the legacy application.
  access_token_env: LEGACY_115_TOKEN
  poll_interval: 2m
  timeout: 25s
  media_listen: "127.0.0.1:18081"
  navidrome_username: legacy-user
  navidrome_password_env: LEGACY_NAVIDROME_PASSWORD
tasks: []
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	checkEnvironment := func(cloud Cloud115) {
		t.Helper()
		if cloud.ClientID != "987654321" || cloud.PollInterval.Duration != 5*time.Minute || cloud.Timeout.Duration != 12*time.Second ||
			cloud.MediaListen != "127.0.0.1:18082" || cloud.NavidromeUsername != "environment-only-user" ||
			cloud.AccessTokenEnv != "LEGACY_115_TOKEN" || cloud.NavidromePasswordEnv != "LEGACY_NAVIDROME_PASSWORD" {
			t.Fatal("deployment overrides or legacy credential aliases were lost")
		}
	}
	checkEnvironment(cfg.Cloud115)
	settings := cfg.Cloud115.Settings()
	settings.Enabled, settings.TargetCID = true, "12345"
	settings.MediaEnabled, settings.NavidromeEnabled, settings.NavidromePlaylistSync = true, true, true
	settings.NavidromeURL, settings.NavidromeMountRoot = "http://127.0.0.1:4533", "/music"
	editor := NewEditor(path)
	saved, err := editor.Cloud115(settings)
	if err != nil {
		t.Fatal(err)
	}
	checkEnvironment(saved.Cloud115)
	saved, err = editor.Settings(SettingsInput{Timezone: "UTC", Schedule: DefaultSchedule, Quality: "lossless"})
	if err != nil {
		t.Fatal(err)
	}
	checkEnvironment(saved.Cloud115)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"987654321", "environment-only-user", "127.0.0.1:18082", "poll_interval: 5m", "timeout: 12s"} {
		if strings.Contains(string(raw), value) {
			t.Fatal("a Web save persisted a deployment environment value")
		}
	}
	if !strings.Contains(string(raw), "Keep the legacy application.") {
		t.Fatal("a Web save removed a deployment comment")
	}
	for name := range environment {
		t.Setenv(name, "")
	}
	legacy, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Cloud115.ClientID != "100197303" || legacy.Cloud115.PollInterval.Duration != 2*time.Minute ||
		legacy.Cloud115.Timeout.Duration != 25*time.Second || legacy.Cloud115.MediaListen != "127.0.0.1:18081" || legacy.Cloud115.NavidromeUsername != "legacy-user" {
		t.Fatal("Web saves changed the legacy deployment settings")
	}
}

func TestCloudDeploymentEnvironmentValidationDoesNotEchoValues(t *testing.T) {
	for _, name := range []string{"MELOPORT_115_CLIENT_ID", "MELOPORT_115_POLL_INTERVAL", "MELOPORT_115_TIMEOUT", "MELOPORT_MEDIA_LISTEN", "MELOPORT_NAVIDROME_USERNAME"} {
		t.Setenv(name, "")
	}
	path := writeConfig(t, `cloud115:
  enabled: true
  target_cid: "12345"
  media_enabled: true
  navidrome_enabled: true
  navidrome_playlist_sync: true
  navidrome_url: http://127.0.0.1:4533
  navidrome_mount_root: /music
  navidrome_username: fixture-user
tasks: []
`)
	for _, item := range []struct{ name, variable, value string }{
		{"malformed interval", "MELOPORT_115_POLL_INTERVAL", "private-fixture-interval"},
		{"short interval", "MELOPORT_115_POLL_INTERVAL", "14s"},
		{"malformed timeout", "MELOPORT_115_TIMEOUT", "private-fixture-timeout"},
		{"long timeout", "MELOPORT_115_TIMEOUT", "61s"},
		{"invalid application", "MELOPORT_115_CLIENT_ID", "private-fixture-application"},
		{"conflicting media listener", "MELOPORT_MEDIA_LISTEN", "127.0.0.1:8080"},
		{"invalid sync username", "MELOPORT_NAVIDROME_USERNAME", "private-fixture\nusername"},
	} {
		t.Run(item.name, func(t *testing.T) {
			t.Setenv(item.variable, item.value)
			_, err := Load(path)
			if err == nil || strings.Contains(err.Error(), item.value) {
				t.Fatalf("invalid deployment setting was accepted or echoed: %v", err)
			}
		})
	}
}

func TestSameRuntimeTracksEnabledMediaAndNavidromeSettings(t *testing.T) {
	base := cloud115Defaults()
	other := base
	other.MediaListen, other.NavidromeURL = "127.0.0.1:18081", "https://music.example.org"
	other.NavidromePlaylistSync = true
	if !base.SameRuntime(other) {
		t.Fatal("disabled settings should not require a restart")
	}
	other.MediaEnabled = true
	if base.SameRuntime(other) {
		t.Fatal("enabling a media listener must require a restart")
	}
	base.MediaEnabled = true
	other = base
	other.MediaListen = "127.0.0.1:18081"
	if base.SameRuntime(other) {
		t.Fatal("changing an active media port must require a restart")
	}
	other = base
	other.NavidromeURL = "https://music.example.org"
	if !base.SameRuntime(other) {
		t.Fatal("saving an inactive Navidrome address must not enable its proxy")
	}
	other.NavidromeEnabled = true
	if base.SameRuntime(other) {
		t.Fatal("enabling a Navidrome proxy must require a restart")
	}
}
