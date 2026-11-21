package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, value string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDefaultsInheritanceAndPaths(t *testing.T) {
	path := writeConfig(t, `schedule: "15 4 * * *"
quality: hires
tasks:
  - id: first
    playlist: "https://music.163.com/#/playlist?id=123"
  - id: second
    playlist: "456"
    schedule: "0 22 * * 1-5"
    quality: standard
    enabled: false
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tasks[0].Schedule != "15 4 * * *" || cfg.Tasks[0].Quality != "hires" || cfg.Tasks[0].PlaylistID != "123" || !cfg.Tasks[0].IsEnabled() {
		t.Fatalf("wrong inherited task: %+v", cfg.Tasks[0])
	}
	if cfg.Tasks[1].Schedule != "0 22 * * 1-5" || cfg.Tasks[1].Quality != "standard" || cfg.Tasks[1].IsEnabled() {
		t.Fatalf("wrong overridden task: %+v", cfg.Tasks[1])
	}
	if cfg.Storage.Database != filepath.Join(filepath.Dir(path), "data", "meloport.db") || cfg.Tasks[0].Directory != filepath.Join(filepath.Dir(path), "data", "music", "first") {
		t.Fatal("relative paths not based on config directory")
	}
	if cfg.Timezone != "Asia/Shanghai" || cfg.API.MinInterval.Duration != 3100*time.Millisecond || cfg.RunOnStart {
		t.Fatal("unsafe defaults")
	}
	if !cfg.AutoDowngrade || cfg.Tasks[0].Model().AutoDowngrade != cfg.AutoDowngrade {
		t.Fatal("automatic quality downgrade should be enabled by default and inherited by tasks")
	}
	schedule, err := ParseSchedule(cfg.Tasks[0].Schedule)
	if err != nil {
		t.Fatal(err)
	}
	date := time.Date(2026, 11, 20, 4, 14, 0, 0, cfg.Location)
	if next := schedule.Next(date); next.Hour() != 4 || next.Minute() != 15 || next.Day() != 20 {
		t.Fatalf("wrong next run: %s", next)
	}
}

func TestParsePlaylist(t *testing.T) {
	valid := map[string]string{
		"123": "123", " 123 ": "123",
		"https://music.163.com/playlist?id=123":                   "123",
		"https://music.163.com/#/playlist?id=123":                 "123",
		"https://y.music.163.com/m/playlist?id=123&creatorId=456": "123",
	}
	for input, want := range valid {
		t.Run(input, func(t *testing.T) {
			got, err := ParsePlaylist(input)
			if err != nil || got != want {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
	for _, input := range []string{"", "0", "-1", "1e10", "0123", "https://music.163.com.evil.test/playlist?id=123", "https://evil.test/#/playlist?id=123", "https://user:pass@music.163.com/playlist?id=123", "https://music.163.com/song?id=123", "https://163cn.tv/abc", "https://music.163.com:443/playlist?id=123", "https://music.163.com/playlist?id=../../secret"} {
		t.Run(input, func(t *testing.T) {
			if _, err := ParsePlaylist(input); err == nil {
				t.Fatal("accepted invalid playlist")
			}
		})
	}
}

func TestRejectInvalidConfiguration(t *testing.T) {
	base := "tasks:\n  - id: test\n    playlist: '123'\n"
	cases := map[string]string{
		"unknown field":        "api_key: secret\n" + base,
		"bad timezone":         "timezone: Mars/Future\n" + base,
		"six cron fields":      "schedule: '0 0 3 * * *'\n" + base,
		"descriptor":           "schedule: '@daily'\n" + base,
		"timezone override":    "schedule: 'CRON_TZ=UTC 0 3 * * *'\n" + base,
		"impossible cron":      "schedule: '0 0 31 2 *'\n" + base,
		"bad quality":          "quality: ultra\n" + base,
		"fast API":             "api:\n  min_interval: 1ms\n" + base,
		"integer duration":     "api:\n  timeout: 30\n" + base,
		"zero timeout":         "api:\n  timeout: 0s\n" + base,
		"http remote":          "api:\n  base_url: http://example.com\n" + base,
		"credentials":          "api:\n  base_url: https://a:b@example.com\n" + base,
		"key in query":         "api:\n  base_url: https://example.com?apikey=chksz_secret\n" + base,
		"bad env name":         "api:\n  key_env: 'KEY=secret'\n" + base,
		"download limit":       "download:\n  max_size_mb: 0\n" + base,
		"public health":        "server:\n  listen: ':8080'\n" + base,
		"bad listen":           "server:\n  listen: localhost\n" + base,
		"unsafe task":          "tasks:\n - id: ../outside\n   playlist: '123'\n",
		"reserved task":        "tasks:\n - id: CON\n   playlist: '123'\n",
		"unsupported platform": "tasks:\n - id: test\n   platform: unknown\n   playlist: '123'\n",
		"duplicate id":         base + "  - id: TEST\n    playlist: '456'\n",
		"duplicate output":     "tasks:\n - id: one\n   playlist: '123'\n   directory: same\n - id: two\n   playlist: '456'\n   directory: same\n",
		"multi doc":            base + "---\n" + base,
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, value)); err == nil {
				t.Fatal("accepted invalid config")
			}
		})
	}
}

func TestOversizedAndSecretDiagnostics(t *testing.T) {
	const key = "chksz_not_a_real_key_for_test"
	for _, value := range []string{"api_key: " + key + "\ntasks: []\n", "tasks: '" + key + "'\n", "tasks: []\n" + strings.Repeat("# comment\n", 150000) + "---\ntasks: []"} {
		_, err := Load(writeConfig(t, value))
		if err == nil || strings.Contains(err.Error(), key) {
			t.Fatalf("unsafe error: %v", err)
		}
	}
}

func TestExampleConfig(t *testing.T) {
	if _, err := Load(filepath.Join("..", "..", "config.example.yaml")); err != nil {
		t.Fatal(err)
	}
}
