package cloudsync

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"meloport/internal/cloud115"
	"meloport/internal/state"
)

type directoryFixture map[string][]cloud115.File

func (f directoryFixture) List(_ context.Context, cid string) ([]cloud115.File, error) {
	return f[cid], nil
}

func fixture(t *testing.T) (Options, state.Download, string, string) {
	t.Helper()
	dir := t.TempDir()
	audio := []byte("tagged audio fixture")
	item := state.Download{
		TaskID: "daily", Provider: "netease", TrackID: "47", Path: filepath.Join(dir, "工作 歌曲.flac"),
		Size: int64(len(audio)), SHA256: fmt.Sprintf("%x", sha256.Sum256(audio)), TagFingerprint: "finished",
	}
	if err := os.WriteFile(item.Path, audio, 0o600); err != nil {
		t.Fatal(err)
	}
	opts := Options{TargetCID: "50", MountDir: filepath.Join(dir, "stage"), STRMDir: filepath.Join(dir, "strm"), MediaBaseURL: "https://media.example.org/library/"}
	if err := os.Mkdir(opts.MountDir, 0o750); err != nil {
		t.Fatal(err)
	}
	return opts, item, fmt.Sprintf("%x", sha1.Sum(audio)), filepath.Join(dir, "state.db")
}

func TestMountedPublicationPreservesLocalAudioAndRecoversAfterCopy(t *testing.T) {
	opts, item, sha1sum, database := fixture(t)
	store, err := state.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := New(store, nil, opts)
	if err := ensureDirectories(opts.MountDir, []string{item.TaskID}, false); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(opts.MountDir, item.TaskID, filepath.Base(item.Path))
	intent, err := store.SaveCloudIntent(t.Context(), state.CloudFile{RootCID: opts.TargetCID, TaskID: item.TaskID, Provider: item.Provider, TrackID: item.TrackID, LocalPath: item.Path, TargetPath: target, RelativePath: item.TaskID + "/" + filepath.Base(item.Path), Size: item.Size, SHA1: sha1sum, MediaID: randomID()})
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(item.Path)
	if err := copyMounted(t.Context(), item, info, target); err != nil {
		t.Fatal(err)
	}
	// Model process termination after rename and before the published flag.
	if ready, err := service.Ready(t.Context(), item.TaskID, item.Provider, item.TrackID); err != nil || !ready {
		t.Fatalf("complete copied audio was not recovered: %t %v", ready, err)
	}
	recovered, found, err := store.GetCloudFile(t.Context(), opts.TargetCID, item.TaskID, item.Provider, item.TrackID)
	if err != nil || !found || !recovered.Published || recovered.MediaID != intent.MediaID {
		t.Fatal("publication recovery lost its durable identity", err)
	}
	if data, err := os.ReadFile(item.Path); err != nil || fmt.Sprintf("%x", sha256.Sum256(data)) != item.SHA256 {
		t.Fatal("local download was changed", err)
	}
	entries, _ := os.ReadDir(filepath.Dir(target))
	if len(entries) != 1 || entries[0].Name() != filepath.Base(target) {
		t.Fatal("temporary upload file was not cleaned")
	}
}

func TestMountedPublicationRequiresExistingRootAndCleansCancelledCopies(t *testing.T) {
	opts, item, _, database := fixture(t)
	store, err := state.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	opts.MountDir = filepath.Join(t.TempDir(), "not-mounted")
	if copied, err := New(store, nil, opts).Stage(t.Context(), item); err == nil || copied {
		t.Fatal("missing mount root was silently created")
	}
	if _, err := os.Stat(opts.MountDir); !os.IsNotExist(err) {
		t.Fatal("mount root must not be auto-created")
	}
	if _, err := os.Stat(item.Path); err != nil {
		t.Fatal("failed mount publication lost local audio", err)
	}
	opts.MountDir = t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	info, _ := os.Stat(item.Path)
	if err := copyMounted(ctx, item, info, filepath.Join(opts.MountDir, "cancelled.flac")); err == nil {
		t.Fatal("cancelled copy succeeded")
	}
	entries, _ := os.ReadDir(opts.MountDir)
	if len(entries) != 0 {
		t.Fatal("cancelled copy left a partial or published audio file")
	}
}

func TestUnpublishedLegacyIntentCopiesToTheNewMount(t *testing.T) {
	opts, item, sha1sum, database := fixture(t)
	store, err := state.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	oldPath := filepath.Join(t.TempDir(), "old-local-upload.flac")
	contents, _ := os.ReadFile(item.Path)
	if err := os.WriteFile(oldPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	intent, err := store.SaveCloudIntent(t.Context(), state.CloudFile{RootCID: opts.TargetCID, TaskID: item.TaskID, Provider: item.Provider, TrackID: item.TrackID, LocalPath: item.Path, TargetPath: oldPath, RelativePath: item.TaskID + "/" + filepath.Base(item.Path), Size: item.Size, SHA1: sha1sum, MediaID: randomID()})
	if err != nil {
		t.Fatal(err)
	}
	service := New(store, nil, opts)
	if ready, err := service.Ready(t.Context(), item.TaskID, item.Provider, item.TrackID); err != nil || ready {
		t.Fatal("old local path was treated as the mounted destination", err)
	}
	if copied, err := service.Stage(t.Context(), item); err != nil || !copied {
		t.Fatal("legacy intent was not republished to the mount", err)
	}
	updated, _, err := store.GetCloudFile(t.Context(), opts.TargetCID, item.TaskID, item.Provider, item.TrackID)
	if err != nil || updated.TargetPath == oldPath || updated.MediaID != intent.MediaID || !updated.Published {
		t.Fatal("legacy identity or new destination was lost", err)
	}
	if data, err := os.ReadFile(oldPath); err != nil || string(data) != string(contents) {
		t.Fatal("legacy local file was changed", err)
	}
}

func TestMountedPublicationAcrossVolumes(t *testing.T) {
	parent := os.Getenv("MELOPORT_TEST_MOUNT_PARENT")
	if parent == "" {
		t.Skip("set MELOPORT_TEST_MOUNT_PARENT to a different filesystem for the cross-volume check")
	}
	if !filepath.IsAbs(parent) {
		t.Fatal("cross-volume test parent must be absolute")
	}
	opts, item, _, database := fixture(t)
	mount, err := os.MkdirTemp(parent, "meloport-mount-")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(mount) != filepath.Clean(parent) || !strings.HasPrefix(filepath.Base(mount), "meloport-mount-") {
		t.Fatal("unexpected cross-volume test directory")
	}
	defer os.RemoveAll(mount)
	if filepath.VolumeName(mount) == filepath.VolumeName(item.Path) {
		t.Skip("test mount is on the source volume")
	}
	t.Logf("copying between source volume %s and mounted volume %s", filepath.VolumeName(item.Path), filepath.VolumeName(mount))
	opts.MountDir = mount
	store, err := state.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if copied, err := New(store, nil, opts).Stage(t.Context(), item); err != nil || !copied {
		t.Fatalf("cross-volume publication failed: %t %v", copied, err)
	}
	source, err := os.ReadFile(item.Path)
	if err != nil {
		t.Fatal(err)
	}
	target, err := os.ReadFile(filepath.Join(mount, item.TaskID, filepath.Base(item.Path)))
	if err != nil || string(source) != string(target) {
		t.Fatal("cross-volume copy changed bytes or removed source", err)
	}
}

func TestStageAndReconcileAfterCloudDrive2RemovesTheAudio(t *testing.T) {
	ctx := context.Background()
	opts, item, sha1sum, database := fixture(t)
	store, err := state.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	remote := directoryFixture{}
	service := New(store, remote, opts)
	if moved, err := service.Stage(ctx, item); err != nil || !moved {
		t.Fatalf("stage initial audio: moved=%t err=%v", moved, err)
	}
	mapping, found, err := store.GetCloudFile(ctx, opts.TargetCID, item.TaskID, item.Provider, item.TrackID)
	if err != nil || !found || !mapping.Published || mapping.ID != "" || len(mapping.MediaID) != 32 || mapping.SHA1 != sha1sum {
		t.Fatalf("expected a durable pending upload: found=%t err=%v", found, err)
	}
	if _, err := os.Stat(item.Path); err != nil {
		t.Fatalf("local download must be retained: %v", err)
	}
	if _, err := os.Stat(mapping.TargetPath); err != nil {
		t.Fatal(err)
	}
	// Simulate CloudDrive2 consuming the staged file before Meloport sees the remote data.
	if err := os.Remove(mapping.TargetPath); err != nil {
		t.Fatal(err)
	}
	if ready, err := service.Ready(ctx, item.TaskID, item.Provider, item.TrackID); err != nil || !ready {
		t.Fatalf("pending CloudDrive2 handoff must prevent another download: ready=%t err=%v", ready, err)
	}
	strm := filepath.Join(opts.STRMDir, item.TaskID, filepath.Base(item.Path)+".strm")
	if err := service.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(strm); !os.IsNotExist(err) {
		t.Fatalf("pending upload should not produce a STRM: %v", err)
	}
	file := cloud115.File{ID: "99", ParentID: "53", Name: filepath.Base(item.Path), PickCode: "abc123", Size: item.Size, SHA1: strings.ToUpper(sha1sum)}
	remote[opts.TargetCID] = []cloud115.File{{ID: "53", ParentID: opts.TargetCID, Name: item.TaskID, IsDir: true}}
	wrong := file
	wrong.SHA1 = strings.Repeat("1", 40)
	remote["53"] = []cloud115.File{wrong}
	if err := service.Reconcile(ctx); err == nil {
		t.Fatal("wrong hash accepted")
	}
	if _, found, err := store.GetCloudByMediaID(ctx, mapping.MediaID); err != nil || found {
		t.Fatalf("incorrect remote audio became playable: found=%t err=%v", found, err)
	}
	remote["53"] = []cloud115.File{file, wrong}
	if err := service.Reconcile(ctx); err == nil {
		t.Fatal("ambiguous remote files accepted")
	}
	otherFormat := file
	otherFormat.ID, otherFormat.Name = "100", strings.TrimSuffix(file.Name, ".flac")+".mp3"
	remote["53"] = []cloud115.File{file, otherFormat}
	if err := service.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	want := "https://media.example.org/library/media/" + mapping.MediaID + "\n"
	if data, err := os.ReadFile(strm); err != nil || string(data) != want {
		t.Fatalf("invalid STRM body: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := state.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	service = New(reopened, nil, opts)
	if ready, err := service.Ready(ctx, item.TaskID, item.Provider, item.TrackID); err != nil || !ready {
		t.Fatalf("missing local audio should be skipped after restart: ready=%t err=%v", ready, err)
	}
	if confirmed, found, err := reopened.GetCloudByRemotePath(ctx, opts.TargetCID, mapping.RelativePath); err != nil || !found || confirmed.ID != file.ID || confirmed.PickCode != file.PickCode || confirmed.MediaID != mapping.MediaID {
		t.Fatalf("remote path mapping lost across restart: found=%t err=%v", found, err)
	}
	stamp := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(strm, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if err := service.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(strm); err != nil || !info.ModTime().Equal(stamp) {
		t.Fatalf("unchanged STRM was rewritten: %v", err)
	}
	if err := os.Remove(strm); err != nil {
		t.Fatal(err)
	}
	if err := service.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(strm); err != nil || string(data) != want {
		t.Fatalf("STRM recovery changed play address: %v", err)
	}
}

func TestStageRetriesUnpublishedIntentWithoutOverwritingExistingAudio(t *testing.T) {
	ctx := context.Background()
	opts, item, sha1sum, database := fixture(t)
	store, err := state.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	name := filepath.Base(item.Path)
	staged := filepath.Join(opts.MountDir, item.TaskID, name)
	if err := os.MkdirAll(filepath.Dir(staged), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("existing audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	intent := state.CloudFile{RootCID: opts.TargetCID, TaskID: item.TaskID, Provider: item.Provider, TrackID: item.TrackID,
		LocalPath: item.Path, TargetPath: staged, RelativePath: item.TaskID + "/" + name, Size: item.Size, SHA1: strings.Repeat("2", 40), MediaID: randomID()}
	if _, err := store.SaveCloudIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}
	service := New(store, nil, opts)
	if ready, err := service.Ready(ctx, item.TaskID, item.Provider, item.TrackID); err == nil || ready {
		t.Fatalf("conflicting destination must be reported without publication: ready=%t err=%v", ready, err)
	}
	if moved, err := service.Stage(ctx, item); err == nil || moved {
		t.Fatalf("collision should preserve both files: moved=%t err=%v", moved, err)
	}
	if err := renameNoReplace(item.Path, staged); err == nil {
		t.Fatal("platform move must reject replacement")
	}
	if data, err := os.ReadFile(staged); err != nil || string(data) != "existing audio" {
		t.Fatalf("collision changed staged file: %v", err)
	}
	if err := os.Remove(staged); err != nil {
		t.Fatal(err)
	}
	if moved, err := service.Stage(ctx, item); err != nil || !moved {
		t.Fatalf("cannot retry corrected pending intent: moved=%t err=%v", moved, err)
	}
	mapping, found, err := store.GetCloudFile(ctx, opts.TargetCID, item.TaskID, item.Provider, item.TrackID)
	if err != nil || !found || !mapping.Published || mapping.MediaID != intent.MediaID || mapping.SHA1 != sha1sum {
		t.Fatalf("retry did not update fingerprint with the same media ID: found=%t err=%v", found, err)
	}
}

func TestReconcileConfirmsInterruptedHandoffOnlyWithRemoteMatch(t *testing.T) {
	ctx := context.Background()
	opts, item, sha1sum, database := fixture(t)
	store, err := state.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	name := filepath.Base(item.Path)
	intent := state.CloudFile{RootCID: opts.TargetCID, TaskID: item.TaskID, Provider: item.Provider, TrackID: item.TrackID,
		LocalPath: item.Path, TargetPath: filepath.Join(opts.MountDir, item.TaskID, name),
		RelativePath: item.TaskID + "/" + name, Size: item.Size, SHA1: sha1sum, MediaID: randomID()}
	if _, err := store.SaveCloudIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}
	// Model a crash before MarkCloudPublished, followed by CloudDrive2 consuming the
	// staged file before 115's directory listing exposes it.
	if err := os.Remove(item.Path); err != nil {
		t.Fatal(err)
	}
	remote := directoryFixture{}
	service := New(store, remote, opts)
	if ready, err := service.Ready(ctx, item.TaskID, item.Provider, item.TrackID); err != nil || !ready {
		t.Fatalf("uncertain handoff should suppress a new download: ready=%t err=%v", ready, err)
	}
	if err := service.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if pending, found, err := store.GetCloudFile(ctx, opts.TargetCID, item.TaskID, item.Provider, item.TrackID); err != nil || !found || pending.Published || pending.ID != "" {
		t.Fatalf("missing files are insufficient to confirm publication: found=%t err=%v", found, err)
	}
	remote[opts.TargetCID] = []cloud115.File{{ID: "53", ParentID: opts.TargetCID, Name: item.TaskID, IsDir: true}}
	remote["53"] = []cloud115.File{{ID: "99", ParentID: "53", Name: name, PickCode: "abc123", Size: item.Size, SHA1: sha1sum}}
	if err := service.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if confirmed, found, err := store.GetCloudByMediaID(ctx, intent.MediaID); err != nil || !found || !confirmed.Published || confirmed.ID != "99" {
		t.Fatalf("verified remote match must atomically recover publication: found=%t err=%v", found, err)
	}
}
