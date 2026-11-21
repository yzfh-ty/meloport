package chksz

import (
	"errors"
	"testing"
)

func TestIsQualityUnavailable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"explicit unavailable quality", &APIError{Status: 404, Message: "requested quality is unavailable"}, true},
		{"explicit unsupported Chinese quality", &APIError{Status: 200, Message: "不支持该音质"}, true},
		{"missing track", &APIError{Status: 404, Message: "track not found"}, false},
		{"authentication", &APIError{Status: 401, Message: "quality unavailable"}, false},
		{"quota", &APIError{Status: 402, Message: "quality unavailable"}, false},
		{"rate limited", &APIError{Status: 429, Message: "quality unavailable"}, false},
		{"generic error", errors.New("quality unavailable"), false},
		{"no quality keyword", &APIError{Status: 404, Message: "resource unavailable"}, false},
		{"no availability keyword", &APIError{Status: 404, Message: "invalid quality"}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsQualityUnavailable(test.err); got != test.want {
				t.Fatalf("IsQualityUnavailable(%v) = %t, want %t", test.err, got, test.want)
			}
		})
	}
}
