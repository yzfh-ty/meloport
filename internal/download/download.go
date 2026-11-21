// Package download saves complete audio files without exposing signed URLs.
package download

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"meloport/internal/model"
)

const defaultMaxBytes int64 = 512 << 20

var safeBasename = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,159}$`)

// These extensions are deliberately finite. A remote format or filename is
// never used directly to construct a local path.
var extensions = []string{"mp3", "flac", "m4a", "aac", "ogg", "opus", "wav", "aiff", "ape", "wv"}

type Options struct {
	Timeout    time.Duration
	MaxBytes   int64
	HTTPClient *http.Client
}

type Downloader struct {
	client   *http.Client
	maxBytes int64
}

func New(opts Options) (*Downloader, error) {
	if opts.Timeout < 0 || opts.MaxBytes < 0 {
		return nil, errors.New("download timeout and maximum size must not be negative")
	}
	if opts.Timeout == 0 {
		opts.Timeout = 10 * time.Minute
	}
	if opts.MaxBytes == 0 {
		opts.MaxBytes = defaultMaxBytes
	}
	if opts.MaxBytes == int64(^uint64(0)>>1) {
		return nil, errors.New("download maximum size is too large")
	}
	client := http.Client{}
	if opts.HTTPClient != nil {
		client = *opts.HTTPClient
	}
	client.Timeout = opts.Timeout
	// Audio URLs are often on a different host from the API. Never send the
	// caller's cookies or authentication headers to them or their redirects.
	client.Jar = nil
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 {
			return errors.New("too many audio redirects")
		}
		if !validURL(req.URL) {
			return errors.New("invalid audio redirect")
		}
		req.Header.Del("Authorization")
		req.Header.Del("Cookie")
		req.Header.Del("Proxy-Authorization")
		req.Header.Del("Referer")
		return nil
	}
	return &Downloader{client: &client, maxBytes: opts.MaxBytes}, nil
}

// Download saves an audio file under directory/basename.<detected format>.
// A complete matching local file is recovered without a network request. A
// temporary file is published with a hard link: creation is atomic and cannot
// replace a destination that another process has already committed.
func (d *Downloader) Download(ctx context.Context, audio model.ResolvedTrack, directory, basename string) (model.DownloadResult, error) {
	var empty model.DownloadResult
	if !safeBasename.MatchString(basename) || strings.HasSuffix(basename, ".") || isReservedName(basename) {
		return empty, errors.New("invalid audio filename")
	}
	if strings.TrimSpace(directory) == "" {
		return empty, errors.New("audio directory must not be empty")
	}
	if err := ctx.Err(); err != nil {
		return empty, fmt.Errorf("download interrupted: %w", err)
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		return empty, errors.New("cannot resolve audio directory")
	}
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return empty, errors.New("cannot create audio directory")
	}
	for _, ext := range extensions {
		path := filepath.Join(directory, basename+"."+ext)
		result, exists, err := d.existing(ctx, path)
		if err != nil {
			return empty, err
		}
		if exists {
			if len(trackTags(audio.Track)) == 0 && model.CanonicalCoverURL(audio.Track.CoverURL) == "" {
				result.TagsComplete = true
				return result, nil
			}
			return d.Tag(ctx, audio.Track, path)
		}
	}
	parsed, err := url.Parse(audio.URL)
	if err != nil || !validURL(parsed) {
		return empty, errors.New("audio URL must use HTTP or HTTPS without credentials")
	}
	if audio.Size < 0 || audio.Size > d.maxBytes {
		return empty, errors.New("audio size exceeds configured limit or is invalid")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return empty, errors.New("cannot construct audio request")
	}
	req.Header.Set("Accept", "audio/*, application/octet-stream;q=0.9")
	req.Header.Set("User-Agent", "meloport/1")
	resp, err := d.client.Do(req)
	if err != nil {
		return empty, requestError(ctx)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return empty, fmt.Errorf("audio server returned HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > d.maxBytes {
		return empty, errors.New("audio response exceeds configured size limit")
	}
	if resp.ContentLength == 0 {
		return empty, errors.New("audio response is empty")
	}
	if audio.Size > 0 && resp.ContentLength > 0 && resp.ContentLength != audio.Size {
		return empty, errors.New("audio response size does not match expected size")
	}
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if strings.HasPrefix(mediaType, "text/") || strings.Contains(mediaType, "json") || strings.Contains(mediaType, "xml") {
		return empty, errors.New("audio server returned a non-audio document")
	}
	header := make([]byte, 512)
	n, readErr := io.ReadFull(resp.Body, header)
	if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
		return empty, requestError(ctx)
	}
	header = header[:n]
	ext := detectFormat(header)
	if ext == "" {
		return empty, errors.New("audio response has an unsupported or invalid file signature")
	}
	file, err := os.CreateTemp(directory, "."+basename+"-*.part")
	if err != nil {
		return empty, errors.New("cannot create temporary audio file")
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	defer file.Close()
	if err := file.Chmod(0o640); err != nil {
		return empty, errors.New("cannot set audio file permissions")
	}
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(io.MultiReader(bytes.NewReader(header), resp.Body), d.maxBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return empty, requestError(ctx)
		}
		return empty, errors.New("audio transfer or local write failed")
	}
	if size > d.maxBytes {
		return empty, errors.New("audio response exceeds configured size limit")
	}
	if size == 0 || (resp.ContentLength > 0 && size != resp.ContentLength) || (audio.Size > 0 && size != audio.Size) {
		return empty, errors.New("audio transfer is incomplete or has an unexpected size")
	}
	if err := ctx.Err(); err != nil {
		return empty, requestError(ctx)
	}
	if err := file.Sync(); err != nil {
		return empty, errors.New("cannot flush audio file to disk")
	}
	if err := file.Close(); err != nil {
		return empty, errors.New("cannot close completed audio file")
	}
	result := model.DownloadResult{Size: size, SHA256: hex.EncodeToString(hash.Sum(nil))}
	publishPath := tempPath
	hasTags := len(trackTags(audio.Track)) != 0 || model.CanonicalCoverURL(audio.Track.CoverURL) != ""
	if hasTags {
		stage, tagged, tagErr := d.prepareTags(ctx, audio.Track, tempPath, ext)
		if tagErr != nil {
			if ctx.Err() != nil {
				return empty, ctx.Err()
			}
			result.TagWarning = "audio was saved, but tags could not be written; retry local tagging"
		} else {
			defer stage.remove()
			result = tagged
			if stage.path != "" {
				publishPath = stage.path
			}
		}
	} else {
		result.TagsComplete = true
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	path := filepath.Join(directory, basename+"."+ext)
	if err := os.Link(publishPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			result, exists, checkErr := d.existing(ctx, path)
			if checkErr != nil {
				return empty, checkErr
			}
			if exists {
				result.TagsComplete = !hasTags
				return result, nil
			}
		}
		return empty, errors.New("cannot atomically publish audio file (destination filesystem must support hard links)")
	}
	result.Path = path
	return result, nil
}

func (d *Downloader) existing(ctx context.Context, path string) (model.DownloadResult, bool, error) {
	var empty model.DownloadResult
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return empty, false, nil
	}
	if err != nil {
		return empty, false, errors.New("cannot inspect existing audio file")
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > d.localLimit() {
		return empty, true, errors.New("existing audio destination is not a valid regular audio file; preserved without changes")
	}
	file, err := os.Open(path)
	if err != nil {
		return empty, true, errors.New("cannot read existing audio file")
	}
	defer file.Close()
	header := make([]byte, 512)
	n, readErr := io.ReadFull(file, header)
	if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
		return empty, true, errors.New("cannot inspect existing audio content")
	}
	if detectFormat(header[:n]) == "" {
		return empty, true, errors.New("existing audio file has an invalid signature; preserved without changes")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return empty, true, errors.New("cannot read existing audio file")
	}
	hash := sha256.New()
	size, err := io.Copy(hash, &contextReader{ctx: ctx, reader: io.LimitReader(file, d.localLimit()+1)})
	if err != nil {
		if ctx.Err() != nil {
			return empty, true, requestError(ctx)
		}
		return empty, true, errors.New("cannot hash existing audio file")
	}
	current, err := file.Stat()
	if err != nil || size != info.Size() || current.Size() != info.Size() || !current.ModTime().Equal(info.ModTime()) {
		return empty, true, errors.New("existing audio file changed during inspection")
	}
	return model.DownloadResult{Path: path, Size: size, SHA256: hex.EncodeToString(hash.Sum(nil))}, true, nil
}

func validURL(value *url.URL) bool {
	return value != nil && (value.Scheme == "https" || value.Scheme == "http") && value.Host != "" && value.User == nil && value.Opaque == ""
}

func requestError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("audio transfer interrupted: %w", err)
	}
	// net/http errors include the entire URL, which may contain signed secrets.
	return errors.New("audio transfer failed or timed out")
}

func isReservedName(base string) bool {
	name := strings.ToUpper(strings.SplitN(base, ".", 2)[0])
	switch name {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return true
	}
	return false
}

func detectFormat(header []byte) string {
	if len(header) < 8 {
		return ""
	}
	switch {
	case bytes.HasPrefix(header, []byte("fLaC")):
		return "flac"
	case bytes.HasPrefix(header, []byte("ID3")) && len(header) >= 10 && header[3] != 0xff && header[4] != 0xff:
		return "mp3"
	case bytes.HasPrefix(header, []byte("OggS")):
		if bytes.Contains(header, []byte("OpusHead")) {
			return "opus"
		}
		return "ogg"
	case bytes.HasPrefix(header, []byte("MAC ")):
		return "ape"
	case bytes.HasPrefix(header, []byte("wvpk")):
		return "wv"
	}
	if len(header) >= 12 {
		if bytes.Equal(header[:4], []byte("RIFF")) && bytes.Equal(header[8:12], []byte("WAVE")) {
			return "wav"
		}
		if bytes.Equal(header[:4], []byte("FORM")) && (bytes.Equal(header[8:12], []byte("AIFF")) || bytes.Equal(header[8:12], []byte("AIFC"))) {
			return "aiff"
		}
		if bytes.Equal(header[4:8], []byte("ftyp")) {
			return "m4a"
		}
	}
	if header[0] == 0xff && header[1]&0xf6 == 0xf0 && header[2]&0x3c != 0x3c {
		return "aac"
	}
	if header[0] == 0xff && header[1]&0xe0 == 0xe0 && header[1]&0x18 != 0x08 && header[1]&0x06 != 0 && header[2]&0xf0 != 0xf0 && header[2]&0xf0 != 0 && header[2]&0x0c != 0x0c {
		return "mp3"
	}
	return ""
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
