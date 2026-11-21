package model

import (
	"strings"
	"testing"
)

func TestCollectionFolderKeepsPortableStableChartIdentity(t *testing.T) {
	for _, test := range []struct{ taskID, title, want string }{
		{"rank-a", "华语榜", "华语榜--rank-a"},
		{"weekly", "  Top / 50  ", "Top-50--weekly"},
		{"empty", "---", "collection--empty"},
	} {
		if got := CollectionFolder(test.taskID, test.title); got != test.want {
			t.Errorf("CollectionFolder(%q, %q) = %q, want %q", test.taskID, test.title, got, test.want)
		}
	}
	got := CollectionFolder("long", strings.Repeat("榜", 100))
	name := strings.TrimSuffix(got, "--long")
	if len([]byte(name)) > 96 {
		t.Fatalf("collection title segment exceeds its byte limit: %d bytes", len(name))
	}
}
