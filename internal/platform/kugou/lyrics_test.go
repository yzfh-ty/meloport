package kugou

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"meloport/internal/model"
)

func TestLyricsDownloadsOnlyExactHashCandidate(t *testing.T) {
	const text = "[00:02.00]酷狗原词"
	wantedHash := hashFor(10)
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if r.Method != http.MethodGet || r.Header.Get("Cookie") != "" || query.Has("keyword") {
			t.Error("Kugou lyrics must use a credential-free hash lookup")
		}
		switch r.URL.Path {
		case "/search":
			switch query.Get("hash") {
			case strings.ToUpper(wantedHash):
				fmt.Fprint(w, `{"status":200,"errcode":200,"candidates":[{"hitlayer":7,"id":"21","accesskey":"wrong"},{"hitlayer":1,"id":"22","accesskey":"candidate+/key="}]}`)
			case strings.ToUpper(hashFor(11)):
				fmt.Fprint(w, `{"status":200,"errcode":200,"candidates":[{"hitlayer":2,"id":"21","accesskey":"wrong"}]}`)
			case strings.ToUpper(hashFor(12)):
				fmt.Fprint(w, `{"status":400,"errcode":400,"candidates":[]}`)
			default:
				t.Error("unexpected audio hash")
				w.WriteHeader(http.StatusBadRequest)
			}
		case "/download":
			if query.Get("id") != "22" || query.Get("fmt") != "lrc" || query.Get("charset") != "utf8" || query.Get("accesskey") != "candidate+/key=" {
				t.Error("Kugou download did not use the exact lyric candidate")
			}
			fmt.Fprintf(w, `{"status":200,"error_code":0,"fmt":"lrc","id":"22","content":%q}`, base64.StdEncoding.EncodeToString([]byte(text)))
		default:
			t.Errorf("unexpected Kugou lyric endpoint: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	for id, want := range map[string]string{wantedHash: text, hashFor(11): "", hashFor(12): "", "12345": ""} {
		got, err := client.Lyrics(context.Background(), model.Track{ID: id, Name: "Ignored title"})
		if err != nil || got != want {
			t.Errorf("Lyrics(%q) = %q, %v; want %q", id, got, err, want)
		}
	}
}
