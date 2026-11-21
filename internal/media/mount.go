package media

import (
	"strings"
	"unicode"
)

// ValidMountRoot accepts absolute POSIX and Windows drive paths as seen by the
// Subsonic server, independent of the OS running Meloport.
func ValidMountRoot(value string) bool {
	_, _, ok := splitAbsolute(value)
	return ok
}

func splitAbsolute(value string) (volume string, parts []string, ok bool) {
	if len(value) == 0 || len(value) > 32767 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return "", nil, false
	}
	if len(value) >= 3 && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) && value[1] == ':' && (value[2] == '/' || value[2] == '\\') {
		volume, value = strings.ToUpper(value[:2]), strings.ReplaceAll(value[3:], "\\", "/")
	} else if strings.HasPrefix(value, "/") && !strings.Contains(value, "\\") {
		value = value[1:]
	} else {
		return "", nil, false
	}
	if strings.HasPrefix(value, "/") {
		return "", nil, false
	}
	if value = strings.TrimSuffix(value, "/"); value == "" {
		return volume, nil, true
	}
	parts = strings.Split(value, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", nil, false
		}
		if volume != "" && (strings.ContainsAny(part, `:<>"|?*`) || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ")) {
			return "", nil, false
		}
	}
	return volume, parts, true
}

func mountedPath(root, name string) (string, bool) {
	rootVolume, rootParts, ok := splitAbsolute(root)
	if !ok || strings.HasSuffix(name, "/") || strings.HasSuffix(name, "\\") {
		return "", false
	}
	// Modern Subsonic responses can expose a synthetic relative path built
	// from metadata. Even if its name matches, it does not identify a file.
	volume, parts, ok := splitAbsolute(name)
	if !ok || volume != rootVolume || len(parts) <= len(rootParts) {
		return "", false
	}
	for i, part := range rootParts {
		if part != parts[i] && (volume == "" || !strings.EqualFold(part, parts[i])) {
			return "", false
		}
	}
	return strings.Join(parts[len(rootParts):], "/"), true
}
