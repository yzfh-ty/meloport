package chksz

import (
	"errors"
	"net/http"
	"strings"
)

// IsQualityUnavailable reports only explicit quality-availability failures.
// It intentionally excludes ordinary missing-track errors, authentication,
// quota, rate-limit, and service failures so a retry cannot mask a different
// problem or spend extra API calls unnecessarily.
func IsQualityUnavailable(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.Status != http.StatusBadRequest && apiErr.Status != http.StatusNotFound && apiErr.Status != http.StatusUnprocessableEntity && apiErr.Status != http.StatusOK {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(apiErr.Message))
	if message == "" {
		return false
	}
	mentionsQuality := strings.Contains(message, "quality") || strings.Contains(message, "level") || strings.Contains(message, "size") || strings.Contains(message, "音质") || strings.Contains(message, "品质")
	if !mentionsQuality {
		return false
	}
	return containsAny(message,
		"unavailable", "not available", "unsupported", "not supported", "not found", "does not exist", "doesn't exist", "cannot provide", "can't provide",
		"不可用", "不支持", "暂不提供", "没有该音质", "不存在",
	)
}

func containsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}
