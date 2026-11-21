package media

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
)

const maxFormSize = 64 << 10

func (h *handler) redirectStream(w http.ResponseWriter, r *http.Request) bool {
	params, ok := streamParams(r)
	if !ok {
		return false
	}
	songPath := h.songPath(r, params)
	relative, ok := mountedPath(h.mountRoot, songPath)
	if !ok {
		return false
	}
	pickCode, found, err := h.lookup.ByPath(r.Context(), relative)
	return err == nil && found && pickCode != "" && h.redirect(w, r, pickCode)
}

func streamParams(r *http.Request) (url.Values, bool) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, false
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
	case http.MethodPost:
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/x-www-form-urlencoded" || r.Body == nil || r.ContentLength > maxFormSize {
			return nil, false
		}
		original := r.Body
		body, err := io.ReadAll(io.LimitReader(original, maxFormSize+1))
		// Retain the exact original body, including unread bytes, for fallback.
		r.Body = &replayBody{Reader: io.MultiReader(bytes.NewReader(body), original), Closer: original}
		if err != nil || len(body) > maxFormSize {
			return nil, false
		}
		form, err := url.ParseQuery(string(body))
		if err != nil {
			return nil, false
		}
		for key, values := range form {
			query[key] = append(query[key], values...)
		}
	default:
		return nil, false
	}
	// Unknown options may change the playback format. Preserve the upstream
	// behavior, and reject ambiguous parameters before using any credential.
	for key, values := range query {
		if len(values) != 1 {
			return nil, false
		}
		switch key {
		case "id", "u", "p", "t", "s", "v", "c", "f", "apiKey", "jwt", "format", "maxBitRate":
		default:
			return nil, false
		}
	}
	format, bitrate := query.Get("format"), query.Get("maxBitRate")
	if (format != "" && format != "raw") || (bitrate != "" && bitrate != "0") || query.Get("id") == "" || query.Get("c") == "" || query.Get("v") == "" {
		return nil, false
	}
	if query.Get("apiKey") != "" {
		return query, !query.Has("u") && !query.Has("p") && !query.Has("t") && !query.Has("s") && !query.Has("jwt")
	}
	return query, query.Get("u") != "" && (query.Get("p") != "" || query.Get("jwt") != "" || query.Get("t") != "" && query.Get("s") != "")
}

type replayBody struct {
	io.Reader
	io.Closer
}

func (h *handler) songPath(r *http.Request, params url.Values) string {
	form := url.Values{"f": {"json"}}
	for _, key := range []string{"id", "u", "p", "t", "s", "apiKey", "jwt", "v", "c"} {
		if params.Has(key) {
			form.Set(key, params.Get(key))
		}
	}
	// Resolve with only the client's own Subsonic credentials. Use a form so
	// that credentials from an inbound body do not move to a logged URL.
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, h.songURL.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", r.UserAgent())
	// Preserve the upstream player selection used for real paths and transcoding.
	for _, cookie := range r.Header.Values("Cookie") {
		req.Header.Add("Cookie", cookie)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	const maxSongResponse = 256 << 10
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSongResponse+1))
	if err != nil || len(data) > maxSongResponse {
		return ""
	}
	var result struct {
		Subsonic struct {
			Status string `json:"status"`
			Song   struct {
				ID                    string `json:"id"`
				Path                  string `json:"path"`
				TranscodedSuffix      string `json:"transcodedSuffix"`
				TranscodedContentType string `json:"transcodedContentType"`
			} `json:"song"`
		} `json:"subsonic-response"`
	}
	if err := json.Unmarshal(data, &result); err != nil || result.Subsonic.Status != "ok" || result.Subsonic.Song.ID != params.Get("id") {
		return ""
	}
	if params.Get("format") != "raw" && (result.Subsonic.Song.TranscodedSuffix != "" || result.Subsonic.Song.TranscodedContentType != "") {
		return ""
	}
	return result.Subsonic.Song.Path
}
