package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const editorExample = `# Keep the document heading.
timezone: Asia/Shanghai
schedule: "0 3 * * *" # Keep the global schedule comment.
quality: lossless
api:
  key_env: MY_CHKSZ_KEY # Keep the environment variable name.
  timeout: 45s
metadata:
  timeout: 35s
  fallback_to_api: true
  qq_cookie_env: MY_QQ_COOKIE
storage:
  database: state/local.db
  music_dir: music
server:
  listen: ''
tasks:
  # Keep this task's heading.
  - id: first
    playlist: '123'
    directory: original-folder
  # Keep the unrelated task.
  - id: second
    platform: qq
    playlist: '456'
    schedule: '0 20 * * *'
    quality: flac
    enabled: false
`

func editorFixture(t *testing.T, contents string) (string, *Editor) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return path, NewEditor(path)
}

func editorBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func noEditorTemps(t *testing.T, path string) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".meloport-config-*.yaml"))
	if err != nil || len(files) != 0 {
		t.Fatalf("staged configuration leaked: %v %v", files, err)
	}
}

func TestEditorTasksReadRawValues(t *testing.T) {
	path, editor := editorFixture(t, editorExample)
	before := editorBytes(t, path)
	tasks, err := editor.Tasks()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatal(tasks)
	}
	first := tasks[0]
	if first.Schedule != "" || first.Quality != "" || first.Platform != "" || first.Directory != "original-folder" || !first.Enabled {
		t.Fatalf("inherited/defaulted values leaked into raw DTO: %+v", first)
	}
	if first.SourceType != "playlist" || first.SourceID != "123" || first.Playlist != "123" {
		t.Fatalf("legacy playlist source was not represented for the editor: %+v", first)
	}
	if tasks[1].Enabled || tasks[1].Schedule != "0 20 * * *" || tasks[1].Quality != "flac" {
		t.Fatalf("wrong raw override: %+v", tasks[1])
	}
	if !bytes.Equal(before, editorBytes(t, path)) {
		t.Fatal("read changed configuration")
	}
	noEditorTemps(t, path)
}

func TestEditorUpdatePreservesCommentsAndUnrelatedFields(t *testing.T) {
	path, editor := editorFixture(t, editorExample)
	beforeInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := editor.Tasks()
	if err != nil {
		t.Fatal(err)
	}
	input := tasks[0]
	input.Playlist = "https://music.163.com/#/playlist?id=789"
	input.Directory = "new-folder"
	cfg, err := editor.Upsert("first", input, false)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tasks[0].PlaylistID != "789" || cfg.Tasks[0].Schedule != "0 3 * * *" || cfg.Tasks[0].Directory != filepath.Join(filepath.Dir(path), "music", "new-folder") {
		t.Fatalf("wrong effective result: %+v", cfg.Tasks[0])
	}
	if cfg.Storage.Database != filepath.Join(filepath.Dir(path), "state", "local.db") {
		t.Fatal("temporary filename changed relative path resolution")
	}
	raw := string(editorBytes(t, path))
	for _, preserved := range []string{"Keep the document heading.", "Keep the global schedule comment.", "Keep the environment variable name.", "Keep this task's heading.", "Keep the unrelated task.", "key_env: MY_CHKSZ_KEY", "qq_cookie_env: MY_QQ_COOKIE", "timeout: 45s", "timeout: 35s", "database: state/local.db", "music_dir: music"} {
		if !strings.Contains(raw, preserved) {
			t.Errorf("lost unrelated field/comment %q", preserved)
		}
	}
	after, err := editor.Tasks()
	if err != nil {
		t.Fatal(err)
	}
	if after[0].Schedule != "" || after[0].Quality != "" || filepath.IsAbs(after[0].Directory) || after[1] != tasks[1] {
		t.Fatalf("raw inheritance/unrelated task changed: %+v", after)
	}
	afterInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if beforeInfo.Mode().Perm() != afterInfo.Mode().Perm() {
		t.Fatal("file mode changed")
	}
	noEditorTemps(t, path)
}

func TestEditorCreateDeleteAndIdentityProtection(t *testing.T) {
	path, editor := editorFixture(t, editorExample)
	before := editorBytes(t, path)
	tasks, err := editor.Tasks()
	if err != nil {
		t.Fatal(err)
	}
	input := tasks[0]
	input.ID = "renamed"
	if _, err := editor.Upsert("first", input, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("allowed id change: %v", err)
	}
	input.ID = "FIRST"
	if _, err := editor.Upsert("", input, true); !errors.Is(err, ErrConflict) {
		t.Fatalf("allowed duplicate id: %v", err)
	}
	if _, err := editor.Delete("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong missing delete: %v", err)
	}
	if _, err := editor.Upsert("missing", input, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong missing update: %v", err)
	}
	if !bytes.Equal(before, editorBytes(t, path)) {
		t.Fatal("rejected identity operation changed bytes")
	}
	input = TaskInput{ID: "third", Platform: "kugou", Playlist: "id_123", Enabled: true}
	cfg, err := editor.Upsert("", input, true)
	if err != nil || len(cfg.Tasks) != 3 {
		t.Fatalf("create: %+v %v", cfg, err)
	}
	audio := filepath.Join(filepath.Dir(path), "music", "original-folder", "netease-123.flac")
	if err := os.MkdirAll(filepath.Dir(audio), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(audio, []byte("untouched existing file"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err = editor.Delete("first")
	if err != nil || len(cfg.Tasks) != 2 {
		t.Fatalf("delete: %+v %v", cfg, err)
	}
	if _, err := os.Stat(audio); err != nil {
		t.Fatal("task delete removed audio", err)
	}
	noEditorTemps(t, path)
}

func TestEditorRankingSourceFieldsUseExplicitYAMLShape(t *testing.T) {
	path, editor := editorFixture(t, editorExample)
	input := TaskInput{ID: "ranking-task", Platform: "qq", SourceType: "ranking", SourceID: "26", Enabled: true}
	if _, err := editor.Upsert("", input, true); err != nil {
		t.Fatal(err)
	}
	raw := string(editorBytes(t, path))
	if !strings.Contains(raw, "source_type: ranking") || !strings.Contains(raw, "source_id: '26'") && !strings.Contains(raw, "source_id: \"26\"") && !strings.Contains(raw, "source_id: 26") {
		t.Fatalf("ranking fields were not persisted: %s", raw)
	}
	if strings.Contains(raw, "ranking-task\n    playlist:") {
		t.Fatal("ranking task retained legacy playlist field")
	}
	tasks, err := editor.Tasks()
	if err != nil {
		t.Fatal(err)
	}
	var got TaskInput
	for _, task := range tasks {
		if task.ID == input.ID {
			got = task
		}
	}
	if got.SourceType != "ranking" || got.SourceID != "26" || got.Playlist != "" {
		t.Fatalf("ranking fields were not read back: %+v", got)
	}
}

func TestEditorTaskCanOverrideAutomaticQualityDowngrade(t *testing.T) {
	_, editor := editorFixture(t, editorExample)
	downgrade := false
	if _, err := editor.Upsert("", TaskInput{ID: "rank-auto", Platform: "qq", SourceType: "ranking", SourceID: "26", AutoDowngrade: &downgrade, Enabled: true}, true); err != nil {
		t.Fatal(err)
	}
	tasks, err := editor.Tasks()
	if err != nil {
		t.Fatal(err)
	}
	var found *TaskInput
	for i := range tasks {
		if tasks[i].ID == "rank-auto" {
			found = &tasks[i]
		}
	}
	if found == nil || found.AutoDowngrade == nil || *found.AutoDowngrade {
		t.Fatalf("explicit task downgrade override was not persisted: %+v", found)
	}
	if _, err := editor.Upsert("rank-auto", TaskInput{ID: "rank-auto", Platform: "qq", SourceType: "ranking", SourceID: "26", Enabled: true}, false); err != nil {
		t.Fatal(err)
	}
	tasks, err = editor.Tasks()
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if task.ID == "rank-auto" && task.AutoDowngrade != nil {
			t.Fatalf("clearing task override should restore global inheritance: %+v", task)
		}
	}
}

func TestEditorPlaylistSourceKeepsLegacyYAMLShape(t *testing.T) {
	path, editor := editorFixture(t, editorExample)
	if _, err := editor.Upsert("", TaskInput{ID: "playlist-task", Platform: "netease", SourceType: "playlist", SourceID: "123", Enabled: true}, true); err != nil {
		t.Fatal(err)
	}
	raw := string(editorBytes(t, path))
	if !strings.Contains(raw, "playlist: '123'") && !strings.Contains(raw, "playlist: 123") {
		t.Fatalf("legacy playlist field was not persisted: %s", raw)
	}
	if strings.Contains(raw, "source_type: playlist") || strings.Contains(raw, "source_id: 123") {
		t.Fatal("ordinary playlist gained explicit source fields")
	}
}

func TestEditorInvalidCandidateLeavesOriginalExact(t *testing.T) {
	path, editor := editorFixture(t, editorExample)
	before := editorBytes(t, path)
	for _, input := range []TaskInput{
		{ID: "../escape", Playlist: "123", Enabled: true},
		{ID: "new", Playlist: "123", Schedule: "invalid cron", Enabled: true},
		{ID: "new", Platform: "qq", Playlist: "https://evil.test/?id=123", Enabled: true},
		{ID: "new", Playlist: "123", Quality: "chksz_do_not_echo_this", Enabled: true},
	} {
		_, err := editor.Upsert("", input, true)
		if err == nil {
			t.Fatal("accepted invalid task")
		}
		if strings.Contains(err.Error(), "chksz_do_not_echo_this") {
			t.Fatal("raw value leaked in error")
		}
		if !bytes.Equal(before, editorBytes(t, path)) {
			t.Fatal("invalid task modified file")
		}
		noEditorTemps(t, path)
	}
}

func TestEditorSettingsPreserveTaskInheritanceAndSecretsConfiguration(t *testing.T) {
	path, editor := editorFixture(t, editorExample)
	cfg, err := editor.Settings(SettingsInput{Timezone: "UTC", Schedule: "15 4 * * *", Quality: "hires", FallbackToAPI: false})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Timezone != "UTC" || cfg.Schedule != "15 4 * * *" || cfg.Quality != "hires" || cfg.Metadata.FallbackToAPI || cfg.API.KeyEnv != "MY_CHKSZ_KEY" || cfg.Metadata.QQCookieEnv != "MY_QQ_COOKIE" {
		t.Fatalf("wrong settings result: %+v", cfg)
	}
	if cfg.Tasks[0].Schedule != "15 4 * * *" || cfg.Tasks[0].Quality != "hires" || cfg.Tasks[1].Schedule != "0 20 * * *" || cfg.Tasks[1].Quality != "flac" {
		t.Fatal("task inheritance/overrides damaged")
	}
	tasks, err := editor.Tasks()
	if err != nil {
		t.Fatal(err)
	}
	if tasks[0].Schedule != "" || tasks[0].Quality != "" || tasks[0].Directory != "original-folder" {
		t.Fatal("effective defaults were persisted to task")
	}
	raw := editorBytes(t, path)
	if !bytes.Contains(raw, []byte("fallback_to_api: false")) || !bytes.Contains(raw, []byte("timeout: 35s")) {
		t.Fatal("metadata settings lost")
	}
	_, err = editor.Settings(SettingsInput{Timezone: "invalid_timezone", Schedule: "0 3 * * *", Quality: "lossless"})
	if err == nil || !bytes.Equal(raw, editorBytes(t, path)) {
		t.Fatal("invalid settings changed file")
	}
	noEditorTemps(t, path)
}

func TestEditorExternalEditConflict(t *testing.T) {
	path, editor := editorFixture(t, editorExample)
	external := append(editorBytes(t, path), []byte("\n# Added by another editor.\n")...)
	editor.beforePublish = func() {
		if err := os.WriteFile(path, external, 0600); err != nil {
			t.Fatal(err)
		}
	}
	_, err := editor.Settings(SettingsInput{Timezone: "UTC", Schedule: "0 4 * * *", Quality: "lossless", FallbackToAPI: true})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("external edit not detected: %v", err)
	}
	if !bytes.Equal(external, editorBytes(t, path)) {
		t.Fatal("external editor contents overwritten")
	}
	noEditorTemps(t, path)
}

func TestEditorDeleteLastAndCreateFromEmpty(t *testing.T) {
	path, editor := editorFixture(t, "# minimal file\napi:\n  key_env: CUSTOM_KEY\ntasks:\n - id: only\n   playlist: '123'\n")
	cfg, err := editor.Delete("only")
	if err != nil || len(cfg.Tasks) != 0 {
		t.Fatalf("delete last task: %+v %v", cfg, err)
	}
	tasks, err := editor.Tasks()
	if err != nil || tasks == nil || len(tasks) != 0 {
		t.Fatalf("empty task DTO: %+v %v", tasks, err)
	}
	cfg, err = editor.Upsert("", TaskInput{ID: "new", Playlist: "456", Enabled: true}, true)
	if err != nil || len(cfg.Tasks) != 1 || cfg.Tasks[0].ID != "new" || cfg.API.KeyEnv != "CUSTOM_KEY" {
		t.Fatalf("create after delete: %+v %v", cfg, err)
	}
	if !strings.Contains(string(editorBytes(t, path)), "minimal file") {
		t.Fatal("lost heading")
	}
	noEditorTemps(t, path)
}

func TestEditorSerializesConcurrentCreates(t *testing.T) {
	_, editor := editorFixture(t, editorExample)
	var wg sync.WaitGroup
	errorsCh := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := editor.Upsert("", TaskInput{ID: fmt.Sprintf("task%d", i), Playlist: "123", Enabled: true}, true)
			errorsCh <- err
		}(i)
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	tasks, err := editor.Tasks()
	if err != nil || len(tasks) != 10 {
		t.Fatalf("lost concurrent create: %d %v", len(tasks), err)
	}
}

func TestEditorRejectsMalformedDocuments(t *testing.T) {
	for _, contents := range []string{"[]\n", "tasks: []\n---\ntasks: []\n", "tasks: []\ntasks: []\n", "tasks: wrong\n", "tasks:\n - id: one\n   enabled: not-a-boolean\n", strings.Repeat("# large comment\n", 100000)} {
		_, editor := editorFixture(t, contents)
		if _, err := editor.Tasks(); err == nil {
			t.Fatal("accepted malformed editable document")
		}
	}
}
