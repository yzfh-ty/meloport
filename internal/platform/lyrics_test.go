package platform

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestLyricsRejectsInvalidOrOversizedContent(t *testing.T) {
	for _, value := range []string{"\x00", "\xff", strings.Repeat("a", maxLyricsBytes+1)} {
		if got, err := NormalizeLyrics(value); err == nil || got != "" {
			t.Errorf("accepted invalid lyrics with length %d", len(value))
		}
	}
	for _, value := range []string{"not:base64", base64.StdEncoding.EncodeToString([]byte("\x00")), strings.Repeat("a", base64.StdEncoding.EncodedLen(maxLyricsBytes)+1)} {
		if got, err := DecodeLyricsBase64(value); err == nil || got != "" {
			t.Errorf("accepted invalid encoded lyrics with length %d", len(value))
		}
	}
}
