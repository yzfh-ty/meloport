package model

import "testing"

func TestCanonicalPlaylistCoverURLs(t *testing.T) {
	valid := map[string]string{
		"http://p2.music.126.net/cover.jpg":                                                "https://p2.music.126.net/cover.jpg",
		"http://imge.kugou.com/soft/collection/{size}/cover.jpg":                           "https://imge.kugou.com/soft/collection/300/cover.jpg",
		"https://music-file.y.qq.com/songlist/u/public/cover.jpg?imageView2/4/w/600/h/600": "https://music-file.y.qq.com/songlist/u/public/cover.jpg?imageView2/4/w/300/h/300",
	}
	for raw, expected := range valid {
		if got := CanonicalPlaylistCoverURL(raw); got != expected {
			t.Errorf("cover %q: got %q, want %q", raw, got, expected)
		}
	}
	for _, raw := range []string{
		"https://music-file.y.qq.com/songlist/cover.jpg?token=private",
		"https://music-file.y.qq.com/songlist/cover.jpg?imageView2/4/w/600/h/600&token=private",
		"https://music-file.y.qq.com/other/private.jpg",
		"https://music-file.y.qq.com.evil.test/songlist/cover.jpg",
		"https://user:secret@music-file.y.qq.com/songlist/cover.jpg",
		"https://music-file.y.qq.com:8443/songlist/cover.jpg",
		"https://music-file.y.qq.com/songlist/cover.jpg#private",
		"https://p2.music.126.net/cover.jpg?token=private",
	} {
		if got := CanonicalPlaylistCoverURL(raw); got != "" {
			t.Errorf("retained unsafe playlist cover %q", raw)
		}
	}
}
