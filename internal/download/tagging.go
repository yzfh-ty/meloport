package download

import (
	"bytes"
	"context"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"go.senan.xyz/taglib"
	"meloport/internal/model"
)

const maxCoverBytes = 8 << 20
const maxTagGrowth = 16 << 20
const maxLyricsBytes = 256 << 10

// Tag fills missing tags on a tracked file. Work happens on a separate copy;
// failures or cancellation before replacement leave the original bytes intact.
func (d *Downloader) Tag(ctx context.Context, track model.Track, path string) (model.DownloadResult, error) {
	stage, result, err := d.prepareTags(ctx, track, path, strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), "."))
	if err != nil {
		return model.DownloadResult{}, err
	}
	if stage.path != "" {
		defer stage.remove()
		if err := ctx.Err(); err != nil {
			return model.DownloadResult{}, err
		}
		if !unchangedTagInput(path, stage.original) {
			return model.DownloadResult{}, errors.New("audio file changed during tagging")
		}
		if err := os.Rename(stage.path, path); err != nil {
			return model.DownloadResult{}, errors.New("cannot replace the audio file with its tagged copy")
		}
	}
	result.Path = path
	return result, nil
}

type tagStage struct {
	path, dir string
	original  os.FileInfo
}

func (s tagStage) remove() {
	if s.path != "" {
		_ = os.Remove(s.path)
		_ = os.Remove(s.dir)
	}
}

func (d *Downloader) prepareTags(ctx context.Context, track model.Track, path, ext string) (tagStage, model.DownloadResult, error) {
	var stage tagStage
	var result model.DownloadResult
	if err := ctx.Err(); err != nil {
		return stage, result, err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > d.localLimit() {
		return stage, result, errors.New("audio tagging requires an intact regular file")
	}
	values := trackTags(track)
	cover := model.CanonicalCoverURL(track.CoverURL)
	fetchLyrics := d.canFetchLyrics(track)
	hasInput := len(values) != 0 || cover != "" || fetchLyrics
	if !hasInput || !taggableFormat(ext) {
		result, err = d.inspectTagOutput(ctx, path)
		result.TagsComplete = err == nil
		if hasInput {
			result.TagWarning = "this audio format does not support tag writing"
		}
		return stage, result, err
	}

	// TagLib selects the container by extension. The staging directory is on
	// the same filesystem but hidden from music library scanners.
	dir, err := os.MkdirTemp(filepath.Dir(path), ".meloport-tags-*")
	if err != nil {
		return stage, result, errors.New("cannot create a staging directory for audio tags")
	}
	stage = tagStage{path: filepath.Join(dir, "track."+ext), dir: dir, original: info}
	keep := false
	defer func() {
		if !keep {
			stage.remove()
		}
	}()
	if err := d.copyTagInput(ctx, path, stage.path, info); err != nil {
		return tagStage{}, result, err
	}

	var changed bool
	var warning string
	err = tagLibCall(func() error {
		properties, err := taglib.ReadProperties(stage.path)
		if err != nil || properties.Format == "" || properties.Channels == 0 || properties.SampleRate == 0 {
			return errors.New("cannot read audio properties for tagging")
		}
		original, err := taglib.ReadTags(stage.path)
		if err != nil {
			return errors.New("cannot read existing audio tags")
		}
		for key := range values {
			if len(cleanTagValues(original[key])) != 0 {
				delete(values, key)
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if fetchLyrics && !hasEmbeddedLyrics(original) {
			lyrics, err := d.fetchLyrics(ctx, track)
			switch {
			case ctx.Err() != nil:
				return ctx.Err()
			case err != nil:
				warning = appendTagWarning(warning, "lyrics could not be downloaded")
			case lyrics != "":
				values[taglib.Lyrics] = []string{lyrics}
			}
		}
		if len(values) != 0 {
			if err := taglib.WriteTags(stage.path, values, 0); err != nil {
				return errors.New("cannot write audio tags")
			}
			changed = true
		}
		if cover != "" && len(properties.Images) == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
			data, mime, err := d.fetchCover(ctx, cover)
			switch {
			case ctx.Err() != nil:
				return ctx.Err()
			case err != nil:
				warning = appendTagWarning(warning, "cover could not be downloaded")
			default:
				// Stage cover edits separately so a failed image write cannot
				// damage successfully written text tags.
				if d.applyCover(ctx, stage, ext, data, mime) {
					changed = true
				} else {
					warning = appendTagWarning(warning, "cover could not be embedded in this audio file")
				}
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !changed {
			return nil
		}
		if ext == "mp3" {
			// TagLib's default MPEG save also creates an ID3v1 trailer. If
			// none existed, adding it can disturb gapless/padding detection
			// in readers that compare the Xing byte count to file size.
			// Keep existing ID3v1 tags; newly supplied Unicode tags live in
			// ID3v2 and do not need a second, legacy trailer.
			if err := preserveID3v1Presence(path, stage.path); err != nil {
				return err
			}
		}
		readback, err := taglib.ReadTags(stage.path)
		if err != nil {
			return errors.New("cannot verify the written audio tags")
		}
		optionalMissing := false
		for key, want := range values {
			if slices.Equal(readback[key], want) {
				continue
			}
			if key == taglib.Title || key == taglib.Artist || key == taglib.Album {
				return errors.New("the audio format did not retain the requested title, artist or album")
			}
			if key == taglib.Lyrics {
				warning = appendTagWarning(warning, "lyrics could not be embedded in this audio file")
				continue
			}
			optionalMissing = true
		}
		if optionalMissing {
			warning = appendTagWarning(warning, "some optional tags are not supported by this audio format")
		}
		final, err := taglib.ReadProperties(stage.path)
		if err != nil || final.Format != properties.Format || final.InnerCodec != properties.InnerCodec || final.Length != properties.Length || final.SampleRate != properties.SampleRate || final.Channels != properties.Channels || final.BitDepth != properties.BitDepth {
			return errors.New("tagged audio failed playback property verification")
		}
		return nil
	})
	if err != nil {
		return tagStage{}, result, err
	}
	if !unchangedTagInput(path, info) {
		return tagStage{}, result, errors.New("audio file changed during tagging")
	}
	if !changed {
		result, err = d.inspectTagOutput(ctx, path)
		result.TagsComplete, result.TagWarning = err == nil, warning
		return tagStage{}, result, err
	}
	file, err := os.OpenFile(stage.path, os.O_RDWR, 0)
	if err != nil {
		return tagStage{}, result, errors.New("cannot open staged audio after tagging")
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if syncErr != nil || closeErr != nil {
		return tagStage{}, result, errors.New("cannot flush tagged audio to disk")
	}
	result, found, err := d.existing(ctx, stage.path)
	if err != nil || !found {
		if ctx.Err() != nil {
			return tagStage{}, result, ctx.Err()
		}
		return tagStage{}, result, errors.New("cannot verify the staged audio file")
	}
	keep = true
	result.TagsComplete, result.TagWarning = true, warning
	return stage, result, nil
}

func preserveID3v1Presence(original, staged string) error {
	hasTag := func(path string) (bool, error) {
		file, err := os.Open(path)
		if err != nil {
			return false, err
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || info.Size() < 128 {
			return false, err
		}
		var signature [3]byte
		_, err = file.ReadAt(signature[:], info.Size()-128)
		return string(signature[:]) == "TAG", err
	}
	existed, err := hasTag(original)
	if err != nil {
		return errors.New("cannot inspect original MP3 legacy tag")
	}
	if existed {
		return nil
	}
	generated, err := hasTag(staged)
	if err != nil {
		return errors.New("cannot inspect staged MP3 legacy tag")
	}
	if !generated {
		return nil
	}
	info, err := os.Stat(staged)
	if err != nil || os.Truncate(staged, info.Size()-128) != nil {
		return errors.New("cannot preserve original MP3 legacy tag layout")
	}
	return nil
}

func unchangedTagInput(path string, info os.FileInfo) bool {
	current, err := os.Lstat(path)
	return err == nil && os.SameFile(info, current) && info.Size() == current.Size() && info.ModTime().Equal(current.ModTime())
}

func (d *Downloader) inspectTagOutput(ctx context.Context, path string) (model.DownloadResult, error) {
	result, found, err := d.existing(ctx, path)
	if err == nil && !found {
		err = errors.New("audio file disappeared during tagging")
	}
	return result, err
}

func (d *Downloader) copyTagInput(ctx context.Context, source, target string, info os.FileInfo) error {
	in, err := os.Open(source)
	if err != nil {
		return errors.New("cannot open audio for tagging")
	}
	defer in.Close()
	opened, err := in.Stat()
	if err != nil || !os.SameFile(info, opened) || info.Size() != opened.Size() || !info.ModTime().Equal(opened.ModTime()) {
		return errors.New("audio file changed before tagging")
	}
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errors.New("cannot stage audio for tagging")
	}
	size, copyErr := io.Copy(out, &contextReader{ctx: ctx, reader: io.LimitReader(in, d.localLimit()+1)})
	chmodErr := out.Chmod(info.Mode().Perm())
	closeErr := out.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	if copyErr != nil || chmodErr != nil || closeErr != nil || size != info.Size() {
		return errors.New("cannot copy complete audio for tagging")
	}
	return nil
}

func (d *Downloader) applyCover(ctx context.Context, stage tagStage, ext string, data []byte, mime string) bool {
	candidate := filepath.Join(stage.dir, "cover."+ext)
	defer os.Remove(candidate)
	info, err := os.Stat(stage.path)
	if err != nil || d.copyTagInput(ctx, stage.path, candidate, info) != nil {
		return false
	}
	err = tagLibCall(func() error {
		if err := taglib.WriteImageOptions(candidate, data, 0, "Front Cover", "", mime); err != nil {
			return err
		}
		verified, err := taglib.ReadImage(candidate)
		if err != nil || !bytes.Equal(verified, data) {
			return errors.New("embedded cover could not be verified")
		}
		return nil
	})
	return err == nil && ctx.Err() == nil && os.Rename(candidate, stage.path) == nil
}

func (d *Downloader) fetchCover(ctx context.Context, rawURL string) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", errors.New("invalid cover URL")
	}
	req.Header.Set("Accept", "image/jpeg, image/png")
	client := *d.client
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > 3 || req.URL.Scheme != "https" || model.CanonicalCoverURL(req.URL.String()) == "" {
			return errors.New("invalid cover redirect")
		}
		return d.client.CheckRedirect(req, via)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", errors.New("cover request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.ContentLength > maxCoverBytes {
		return nil, "", errors.New("cover response was not usable")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxCoverBytes+1))
	if err != nil || len(data) > maxCoverBytes {
		return nil, "", errors.New("cover exceeds the allowed size or is incomplete")
	}
	config, kind, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width < 1 || config.Width > 4096 || config.Height < 1 || config.Height > 4096 || (kind != "png" && kind != "jpeg") {
		return nil, "", errors.New("cover is not a supported JPEG or PNG image")
	}
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		return nil, "", errors.New("cover image is incomplete or damaged")
	}
	return data, "image/" + kind, nil
}

func (d *Downloader) hasTagInput(track model.Track) bool {
	return len(trackTags(track)) != 0 || model.CanonicalCoverURL(track.CoverURL) != "" || d.canFetchLyrics(track)
}

func (d *Downloader) canFetchLyrics(track model.Track) bool {
	return d.lyrics != nil && track.Platform != ""
}

func hasEmbeddedLyrics(tags map[string][]string) bool {
	for key, values := range tags {
		// TagLib maps ID3 lyric frames with custom descriptions to LYRICS:<description>.
		name, _, _ := strings.Cut(strings.ToUpper(key), ":")
		switch name {
		case taglib.Lyrics, "UNSYNCEDLYRICS", "SYNCEDLYRICS":
			for _, value := range values {
				if strings.TrimSpace(value) != "" {
					return true
				}
			}
		}
	}
	return false
}

func (d *Downloader) fetchLyrics(ctx context.Context, track model.Track) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	lyrics, err := d.lyrics.Lyrics(ctx, track.Platform, track)
	if err != nil {
		return "", err
	}
	if len(lyrics) > maxLyricsBytes {
		return "", errors.New("lyrics exceed the allowed size")
	}
	// Retain line breaks and timestamps, and remove controls that cannot be
	// carried in TagLib's string values (such as NUL and vertical tab).
	lyrics = strings.ReplaceAll(lyrics, "\r\n", "\n")
	lyrics = strings.ReplaceAll(lyrics, "\r", "\n")
	lyrics = strings.Map(func(r rune) rune {
		if r != '\n' && r != '\t' && unicode.IsControl(r) {
			return -1
		}
		return r
	}, lyrics)
	return strings.TrimSpace(lyrics), nil
}

func trackTags(track model.Track) map[string][]string {
	tags := map[string][]string{}
	set := func(key string, values ...string) {
		if cleaned := cleanTagValues(values); len(cleaned) != 0 {
			tags[key] = cleaned
		}
	}
	set(taglib.Title, track.Name)
	set(taglib.Artist, track.Artists...)
	set(taglib.Album, track.Album)
	set(taglib.AlbumArtist, track.AlbumArtists...)
	if track.TrackNumber > 0 {
		set(taglib.TrackNumber, strconv.Itoa(track.TrackNumber))
	}
	if track.DiscNumber > 0 {
		set(taglib.DiscNumber, strconv.Itoa(track.DiscNumber))
	}
	set(taglib.Date, track.Year)
	return tags
}

func cleanTagValues(values []string) []string {
	var cleaned []string
	for _, value := range values {
		value = strings.TrimSpace(strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return ' '
			}
			return r
		}, value))
		if value != "" && !slices.Contains(cleaned, value) {
			cleaned = append(cleaned, value)
		}
	}
	return cleaned
}

func taggableFormat(ext string) bool {
	switch ext {
	case "mp3", "flac", "m4a", "ogg", "opus", "wav", "aiff", "ape", "wv":
		return true
	default:
		return false
	}
}

func appendTagWarning(existing, message string) string {
	if existing == "" {
		return message
	}
	return existing + "; " + message
}

// The library can panic when a malformed file causes an invalid WASM memory
// read. Treat it as a tag failure and keep the audio bytes untouched.
func tagLibCall(run func() error) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("audio tag parser could not process this file")
		}
	}()
	return run()
}

func (d *Downloader) localLimit() int64 {
	const max = int64(^uint64(0) >> 1)
	if d.maxBytes > max-maxTagGrowth-1 {
		return max - 1
	}
	return d.maxBytes + maxTagGrowth
}
