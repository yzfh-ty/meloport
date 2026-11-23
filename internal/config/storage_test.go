package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSettingsMusicDirectoryPersistsScopedValueAndRecalculatesTasks(t *testing.T) {
	contents := strings.Replace(editorExample, "database: state/local.db", "database: state/local.db # Preserve database comment.", 1)
	contents = strings.Replace(contents, "music_dir: music", "music_dir: music # Preserve root comment.", 1)
	path, editor := editorFixture(t, contents)
	oldRoot := filepath.Join(filepath.Dir(path), "music")
	oldAudio := filepath.Join(oldRoot, "original-folder", "already-downloaded.flac")
	if err := os.MkdirAll(filepath.Dir(oldAudio), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldAudio, []byte("original audio"), 0600); err != nil {
		t.Fatal(err)
	}
	absoluteDirectory := filepath.Join(filepath.Dir(path), "fixed-output")
	if _, err := editor.Upsert("", TaskInput{ID: "absolute", Playlist: "789", Directory: absoluteDirectory, Enabled: true}, true); err != nil {
		t.Fatal(err)
	}
	newRoot := "audio/new-root"
	cfg, err := editor.Settings(SettingsInput{Timezone: "UTC", Schedule: DefaultSchedule, Quality: "lossless", MusicDir: &newRoot})
	if err != nil {
		t.Fatal(err)
	}
	resolved := filepath.Join(filepath.Dir(path), "audio", "new-root")
	if cfg.Storage.MusicDirConfigured != newRoot || cfg.Storage.MusicDir != resolved || cfg.Tasks[0].Directory != filepath.Join(resolved, "original-folder") || cfg.Tasks[1].Directory != filepath.Join(resolved, "second") || cfg.Tasks[2].Directory != absoluteDirectory {
		t.Fatalf("wrong download root/task recalculation: storage=%+v tasks=%+v", cfg.Storage, cfg.Tasks)
	}
	for _, task := range cfg.Tasks {
		if task.Model().MusicRoot != resolved {
			t.Fatal("a storage edit left the Navidrome task mapping on the old download root")
		}
	}
	reloaded, err := Load(path)
	if err != nil || reloaded.Storage.MusicDirConfigured != newRoot || reloaded.Storage.MusicDir != resolved || reloaded.Tasks[0].Directory != cfg.Tasks[0].Directory {
		t.Fatalf("download root did not survive reload: %+v %v", reloaded, err)
	}
	raw := editorBytes(t, path)
	for _, preserved := range []string{"database: state/local.db # Preserve database comment.", "Preserve root comment.", "qq_cookie_env: MY_QQ_COOKIE", "directory: original-folder", "Keep the unrelated task."} {
		if !bytes.Contains(raw, []byte(preserved)) {
			t.Errorf("lost unrelated configuration/comment %q", preserved)
		}
	}
	if _, err := os.Stat(resolved); !os.IsNotExist(err) {
		t.Fatalf("saving root created or moved a folder: %v", err)
	}
	if data, err := os.ReadFile(oldAudio); err != nil || string(data) != "original audio" {
		t.Fatalf("saving root changed existing audio: %q %v", data, err)
	}
	noEditorTemps(t, path)
}

func TestSettingsOmittedMusicDirectoryPreservesExistingConfiguration(t *testing.T) {
	for _, contents := range []string{editorExample, "tasks: []\n"} {
		path, editor := editorFixture(t, contents)
		cfg, err := editor.Settings(SettingsInput{Timezone: "UTC", Schedule: DefaultSchedule, Quality: "lossless"})
		if err != nil {
			t.Fatal(err)
		}
		wantConfigured := "music"
		if contents != editorExample {
			wantConfigured = "data/music"
		}
		if cfg.Storage.MusicDirConfigured != wantConfigured {
			t.Fatalf("omitted field changed download root: %+v", cfg.Storage)
		}
		if contents == editorExample {
			if !bytes.Contains(editorBytes(t, path), []byte("music_dir: music")) {
				t.Fatal("omitting music_dir dropped stored value")
			}
		} else if bytes.Contains(editorBytes(t, path), []byte("storage:")) {
			t.Fatal("omitting music_dir wrote a default storage node")
		}
	}
}

func TestSettingsRejectInvalidMusicDirectoryWithoutPublishing(t *testing.T) {
	path, editor := editorFixture(t, editorExample)
	file := filepath.Join(filepath.Dir(path), "file.flac")
	if err := os.WriteFile(file, []byte("existing file"), 0600); err != nil {
		t.Fatal(err)
	}
	before := editorBytes(t, path)
	for _, value := range []string{"", "   ", "bad\x00path", file, filepath.Join(file, "child")} {
		_, err := editor.Settings(SettingsInput{Timezone: "UTC", Schedule: DefaultSchedule, Quality: "lossless", MusicDir: &value})
		if err == nil {
			t.Fatalf("accepted invalid download root %q", value)
		}
		if !bytes.Equal(before, editorBytes(t, path)) {
			t.Fatalf("invalid root %q changed configuration", value)
		}
		noEditorTemps(t, path)
	}
}
