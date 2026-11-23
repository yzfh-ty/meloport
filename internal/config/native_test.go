package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func nativeConfigFile(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNativePlatformConfigurationAndModel(t *testing.T) {
	path := nativeConfigFile(t, `quality: lossless
schedule: '0 3 * * *'
tasks:
  - id: wy
    platform: netease
    playlist: 'https://music.163.com/#/playlist?id=123'
  - id: tx
    platform: qq
    playlist: 'https://y.qq.com/n/ryqq/playlist/456'
    schedule: '30 4 * * *'
  - id: kg
    platform: kugou
    playlist: 'https://www.kugou.com/yy/special/single/789.html'
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []struct{ platform, id, quality string }{{"netease", "123", "lossless"}, {"qq", "456", "flac"}, {"kugou", "id_789", "flac"}} {
		task := cfg.Tasks[i]
		if task.Platform != want.platform || task.PlaylistID != want.id || task.Quality != want.quality {
			t.Fatalf("wrong native task: %+v", task)
		}
		model := task.Model()
		if model.Platform != want.platform || model.PlaylistID != want.id || model.Quality != want.quality || model.Directory != filepath.Join(filepath.Dir(path), "data", "music", task.ID) {
			t.Fatalf("task model lost native identity: %+v", model)
		}
	}
	if cfg.Tasks[0].Schedule != "0 3 * * *" || cfg.Tasks[1].Schedule != "30 4 * * *" || cfg.Tasks[2].Schedule != "0 3 * * *" {
		t.Fatal("global/per-task schedule inheritance changed")
	}
}

func TestNativePlatformIDFamiliesRemainDistinct(t *testing.T) {
	tests := []struct{ platform, input, want string }{
		{"netease", "123", "123"},
		{"netease", "https://y.music.163.com/m/playlist?id=123", "123"},
		{"qq", "https://i.y.qq.com/n2/m/share/details/taoge.html?id=456", "456"},
		{"kugou", "123", "123"},
		{"kugou", "id_123", "id_123"},
		{"kugou", "gcid_encoded", "gcid_encoded"},
		{"kugou", "https://m3ws.kugou.com/share/index.php?chain=abc&global_collection_id=def", "https://m3ws.kugou.com/share/index.php?chain=abc&global_collection_id=def"},
	}
	for _, test := range tests {
		t.Run(test.platform+":"+test.input, func(t *testing.T) {
			got, err := ParsePlaylistFor(test.platform, test.input)
			if err != nil || got != test.want {
				t.Fatalf("got %q %v want %q", got, err, test.want)
			}
		})
	}
	for _, source := range []string{"", "kw", "kuwo", "mg", "migu", "QQ", "not-registered"} {
		if _, err := ParsePlaylistFor(source, "123"); err == nil {
			t.Fatalf("unregistered platform accepted: %s", source)
		}
	}
}

func TestNativeQualityInheritanceAndExplicitQualityStrictness(t *testing.T) {
	for _, test := range []struct{ global, netease, other string }{{"lossless", "lossless", "flac"}, {"standard", "standard", "128k"}, {"exhigh", "exhigh", "320k"}, {"jymaster", "jymaster", "master"}, {"flac", "lossless", "flac"}, {"master", "jymaster", "master"}, {"hires", "hires", "hires"}} {
		t.Run(test.global, func(t *testing.T) {
			cfg, err := Load(nativeConfigFile(t, fmt.Sprintf("quality: %s\ntasks:\n - id: wy\n   playlist: '123'\n - id: qq\n   platform: qq\n   playlist: '456'\n - id: kg\n   platform: kugou\n   playlist: 'id_789'\n", test.global)))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Tasks[0].Quality != test.netease || cfg.Tasks[1].Quality != test.other || cfg.Tasks[2].Quality != test.other {
				t.Fatalf("wrong quality mapping %+v", cfg.Tasks)
			}
		})
	}
	for _, test := range []struct{ platform, quality string }{{"netease", "flac"}, {"qq", "lossless"}, {"kugou", "jymaster"}, {"qq", "flac24bit"}} {
		_, err := Load(nativeConfigFile(t, fmt.Sprintf("tasks:\n - id: test\n   platform: %s\n   playlist: '123'\n   quality: %s\n", test.platform, test.quality)))
		if err == nil {
			t.Fatalf("explicit non-native quality silently aliased: %+v", test)
		}
	}
}

func TestNativeMetadataDefaultsAndEnvironmentNames(t *testing.T) {
	cfg, err := Load(nativeConfigFile(t, "tasks: []\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Tasks) != 0 || cfg.Metadata.MinInterval.Duration != time.Second || cfg.Metadata.NeteaseCookieEnv != "MELOPORT_NETEASE_COOKIE" || cfg.Metadata.QQCookieEnv != "MELOPORT_QQ_COOKIE" || cfg.Metadata.KugouCookieEnv != "MELOPORT_KUGOU_COOKIE" {
		t.Fatalf("wrong metadata defaults %+v", cfg.Metadata)
	}
	cfg, err = Load(nativeConfigFile(t, "metadata:\n fallback_to_api: false\n netease_cookie_env: CUSTOM_WY\n qq_cookie_env: CUSTOM_QQ\n kugou_cookie_env: ''\ntasks: []\n"))
	if err != nil || cfg.Metadata.NeteaseCookieEnv != "CUSTOM_WY" || cfg.Metadata.QQCookieEnv != "CUSTOM_QQ" || cfg.Metadata.KugouCookieEnv != "" {
		t.Fatalf("wrong explicit metadata settings %+v %v", cfg, err)
	}
	for _, invalid := range []string{
		"netease_cookie_env: 'KEY=value'", "qq_cookie_env: 'bad name'", "kugou_cookie_env: '123START'",
		"timeout: 0s", "timeout: 11m", "min_interval: 1ms", "min_interval: 2h",
	} {
		if _, err := Load(nativeConfigFile(t, "metadata:\n  "+invalid+"\ntasks: []\n")); err == nil {
			t.Fatal("invalid metadata accepted:", invalid)
		}
	}
}

func TestConfigCapsTasksAndRejectsUnsupportedPlatforms(t *testing.T) {
	var content strings.Builder
	content.WriteString("tasks:\n")
	for i := 0; i < 1001; i++ {
		fmt.Fprintf(&content, " - id: task%d\n   playlist: '123'\n", i)
	}
	if _, err := Load(nativeConfigFile(t, content.String())); err == nil {
		t.Fatal("accepted over1000 tasks")
	}
	for _, source := range []string{"kuwo", "migu", "unknown"} {
		if _, err := Load(nativeConfigFile(t, fmt.Sprintf("tasks:\n - id: unsupported\n   platform: %s\n   playlist: '123'\n", source))); err == nil {
			t.Fatal("unsupported platform accepted", source)
		}
	}
}

func TestEnsureOnlyCreatesMissingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.yaml")
	if err := Ensure(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || len(cfg.Tasks) != 0 {
		t.Fatalf("bootstrap invalid: %+v %v", cfg, err)
	}
	if !bytes.Contains(data, []byte("tasks: []")) || bytes.Contains(data, []byte("chksz_")) {
		t.Fatal("bootstrap contains task placeholders or keys")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("bootstrap mode=%o", info.Mode().Perm())
	}
	custom := []byte("# existing config, preserve exact bytes\ntasks: []\n")
	if err := os.WriteFile(path, custom, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(path); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, custom) {
		t.Fatal("Ensure overwrote an existing configuration", err)
	}
}

func TestEnsureConcurrentCreationIsSafe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	var wg sync.WaitGroup
	errorsCh := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errorsCh <- Ensure(path) }()
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Load(path); err != nil {
		t.Fatal("concurrent bootstrap left invalid file", err)
	}
}
