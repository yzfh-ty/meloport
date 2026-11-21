package platform

import (
	"encoding/base64"
	"errors"
	"strings"
	"unicode/utf8"
)

const maxLyricsBytes = 256 << 10

// NormalizeLyrics preserves lyric text and timestamps while normalizing line
// endings and bounding the content that can be embedded into audio tags.
func NormalizeLyrics(value string) (string, error) {
	if len(value) > maxLyricsBytes || !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
		return "", errors.New("platform returned invalid lyric text")
	}
	value = strings.TrimPrefix(value, "\uFEFF")
	value = strings.ReplaceAll(value, "\r\n", "\n")
	return strings.TrimSpace(strings.ReplaceAll(value, "\r", "\n")), nil
}

// DecodeLyricsBase64 decodes the bounded UTF-8 payload used by QQ and Kugou.
func DecodeLyricsBase64(value string) (string, error) {
	if len(value) > base64.StdEncoding.EncodedLen(maxLyricsBytes) {
		return "", errors.New("platform lyric content exceeds the size limit")
	}
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return "", errors.New("platform returned invalid Base64 lyric content")
	}
	return NormalizeLyrics(string(decoded))
}
