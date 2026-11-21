package qq

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"meloport/internal/model"
)

func TestTrackMetadataVerifiesReturnedMID(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Detail struct {
				Module string `json:"module"`
				Method string `json:"method"`
				Param  struct {
					MID string `json:"song_mid"`
				} `json:"param"`
			} `json:"req_1"`
		}
		if r.Method != http.MethodPost || r.URL.Path != "/cgi-bin/musicu.fcg" || json.NewDecoder(r.Body).Decode(&request) != nil || request.Detail.Module != "music.pf_song_detail_svr" || request.Detail.Method != "get_song_detail_yqq" || request.Detail.Param.MID == "" {
			t.Error("incorrect QQ detail request")
			http.Error(w, "unexpected details request", http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, `{"code":0,"req_1":{"code":0,"data":{"track_info":{"mid":"ExactMID","title":"Correct song","album":{"mid":"AlbumMID","name":"Original album"}}}}}`)
	})
	got, err := client.TrackMetadata(context.Background(), model.Track{ID: "ExactMID"})
	if err != nil || got.Name != "Correct song" || got.Album != "Original album" || got.CoverURL != "https://y.gtimg.cn/music/photo_new/T002R800x800M000AlbumMID.jpg" {
		t.Fatalf("unexpected song details: %+v, %v", got, err)
	}
	if _, err := client.TrackMetadata(context.Background(), model.Track{ID: "DifferentMID"}); err == nil {
		t.Fatal("a different song MID was accepted")
	}
}
