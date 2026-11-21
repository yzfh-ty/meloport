package kugou

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"meloport/internal/model"
)

func TestTrackMetadataPreservesHashAndResolvesNumericAudioID(t *testing.T) {
	baseHash, flacHash := hashFor(12), hashFor(13)
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Data []map[string]string `json:"data"`
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v3/album_audio/audio" || json.NewDecoder(r.Body).Decode(&request) != nil || len(request.Data) != 1 {
			t.Error("incorrect Kugou detail request")
			http.Error(w, "unexpected detail request", http.StatusBadRequest)
			return
		}
		if request.Data[0]["hash"] != flacHash && request.Data[0]["audio_id"] != "42" && request.Data[0]["audio_id"] != "43" {
			t.Errorf("unexpected Kugou identity: %v", request.Data[0])
		}
		track := map[string]any{
			"songname": "Exact match",
			"audio_info": map[string]any{
				"audio_id": "42", "hash": strings.ToUpper(baseHash), "hash_flac": strings.ToUpper(flacHash),
			},
		}
		jsonReply(t, w, map[string]any{"error_code": 0, "data": []any{[]any{track}}})
	})
	for id, wantHash := range map[string]string{strings.ToUpper(flacHash): flacHash, "42": baseHash} {
		got, err := client.TrackMetadata(context.Background(), model.Track{ID: id})
		if err != nil || got.ID != id || got.Hash != wantHash || got.Name != "Exact match" {
			t.Fatalf("unexpected Kugou details for %s: %+v, %v", id, got, err)
		}
	}
	if _, err := client.TrackMetadata(context.Background(), model.Track{ID: "43"}); err == nil {
		t.Fatal("a different Kugou audio ID was accepted")
	}
}
