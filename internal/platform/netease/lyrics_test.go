package netease

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"meloport/internal/model"
)

func TestLyricsUsesOriginalNetEaseSongText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if r.Method != http.MethodGet || r.URL.Path != "/api/song/lyric" || query.Get("lv") != "1" {
			t.Errorf("unexpected NetEase lyric request: %s %s", r.Method, r.URL)
		}
		switch query.Get("id") {
		case "123":
			fmt.Fprint(w, `{"code":200,"lrc":{"lyric":"\uFEFF[00:02.00]一句歌词\r\n[00:04.00]第二句\r\n"},"tlyric":{"lyric":"translation"}}`)
		case "124":
			fmt.Fprint(w, `{"code":200,"nolyric":true,"lrc":{"lyric":"instrumental placeholder"}}`)
		default:
			t.Error("unexpected song ID")
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	client := fixtureClient(server)
	for id, want := range map[string]string{"123": "[00:02.00]一句歌词\n[00:04.00]第二句", "124": ""} {
		got, err := client.Lyrics(context.Background(), model.Track{ID: id})
		if err != nil || got != want {
			t.Errorf("Lyrics(%q) = %q, %v; want %q", id, got, err, want)
		}
	}
}
