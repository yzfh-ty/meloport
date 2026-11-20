package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

func cliConfig(t *testing.T, extra string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	value := "server:\n  listen: ''\ntasks:\n  - id: test\n    playlist: '123'\n" + extra
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCLIValidateWithoutKeyOrDatabase(t *testing.T) {
	path := cliConfig(t, "")
	t.Setenv("CHKSZ_API_KEY", "")
	var out, diagnostic bytes.Buffer
	if err := Run(context.Background(), []string{"validate", "-config", path}, &out, &diagnostic, "test"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Configuration valid") {
		t.Fatal(out.String())
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "data")); !os.IsNotExist(err) {
		t.Fatal("validation wrote data files")
	}
}

func TestCLISafeArgumentsAndHelp(t *testing.T) {
	const key = "chksz_diagnostic_test_value"
	for _, args := range [][]string{{"unknown"}, {"status", "-limit", key}, {"serve", "-" + key}, {"version", "extra"}} {
		var out, diagnostic bytes.Buffer
		err := Run(context.Background(), args, &out, &diagnostic, "test")
		if err == nil {
			t.Fatal("accepted invalid args", args)
		}
		if strings.Contains(diagnostic.String()+err.Error(), key) {
			t.Fatal("secret leaked in CLI diagnostics")
		}
	}
	var out, diagnostic bytes.Buffer
	if err := Run(context.Background(), []string{"-help"}, &out, &diagnostic, "test"); err != nil || !strings.Contains(diagnostic.String(), "Usage:") {
		t.Fatalf("help: %v %s", err, &diagnostic)
	}
	if err := Run(context.Background(), []string{"version"}, &out, &diagnostic, "test-version"); err != nil || !strings.Contains(out.String(), "test-version") {
		t.Fatal("version command failed")
	}
}

func TestCLIServiceStopsAndUnlocks(t *testing.T) {
	path := cliConfig(t, "")
	t.Setenv("CHKSZ_API_KEY", "")
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	var out, diagnostic bytes.Buffer
	if err := Run(ctx, []string{"serve", "-config", path}, &out, &diagnostic, "test"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diagnostic.String(), "service stopped") {
		t.Fatal("service did not complete shutdown", diagnostic.String())
	}
	if strings.Contains(diagnostic.String(), "chksz_local_placeholder_not_real") {
		t.Fatal("key leaked")
	}
	lock := flock.New(filepath.Join(filepath.Dir(path), "data", "meloport.db.lock"))
	defer lock.Close()
	if locked, err := lock.TryLock(); err != nil || !locked {
		t.Fatalf("writer lock left held: %t %v", locked, err)
	}
	if err := lock.Unlock(); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), []string{"status", "-config", path}, &out, &diagnostic, "test"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "[]") {
		t.Fatalf("expected empty history: %s", out.String())
	}
}

func TestCLIRejectsSecondWriter(t *testing.T) {
	path := cliConfig(t, "")
	t.Setenv("CHKSZ_API_KEY", "chksz_local_placeholder_not_real")
	directory := filepath.Join(filepath.Dir(path), "data")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	lock := flock.New(filepath.Join(directory, "meloport.db.lock"))
	defer lock.Close()
	if locked, err := lock.TryLock(); err != nil || !locked {
		t.Fatal("couldn't create test writer lock", err)
	}
	err := Run(context.Background(), []string{"sync", "-config", path}, &bytes.Buffer{}, &bytes.Buffer{}, "test")
	if err == nil || !strings.Contains(err.Error(), "another serve or sync") {
		t.Fatalf("wrong lock behavior: %v", err)
	}
}
