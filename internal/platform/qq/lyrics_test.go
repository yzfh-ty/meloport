package qq

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"testing"

	"meloport/internal/model"
)

func TestLyricsUsesQQMusicMIDAndDecodesOriginal(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if r.Method != http.MethodGet || r.URL.Path != "/lyric/fcgi-bin/fcg_query_lyric_new.fcg" || query.Get("format") != "json" || r.Referer() != "https://y.qq.com/portal/player.html" {
			t.Errorf("unexpected QQ Music lyric request: %s %s (%s)", r.Method, r.URL, r.Referer())
		}
		switch query.Get("songmid") {
		case "SongMID":
			encoded := base64.StdEncoding.EncodeToString([]byte("[00:02:34]原文 &amp; 标点\r\n"))
			fmt.Fprintf(w, `{"retcode":0,"code":0,"subcode":0,"lyric":%q,"trans":"ignored"}`, encoded)
		case "MissingMID":
			fmt.Fprint(w, `{"retcode":1101,"code":1101,"subcode":1101}`)
		default:
			t.Error("unexpected song MID")
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	for mid, want := range map[string]string{"SongMID": "[00:02.34]原文 & 标点", "MissingMID": ""} {
		got, err := client.Lyrics(context.Background(), model.Track{ID: mid})
		if err != nil || got != want {
			t.Errorf("Lyrics(%q) = %q, %v; want %q", mid, got, err, want)
		}
	}
}
