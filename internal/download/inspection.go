package download

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"go.senan.xyz/taglib"
)

var ErrUnsupportedTags = errors.New("audio format does not support tag writing")

// AudioTags reports the current file tags without revealing artwork or lyrics.
type AudioTags struct {
	Title  string
	Artist []string
	Album  string
	Cover  bool
	Lyrics bool
}

func (d *Downloader) InspectTags(ctx context.Context, path string) (AudioTags, error) {
	if err := ctx.Err(); err != nil {
		return AudioTags{}, err
	}
	if !taggableFormat(strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")) {
		return AudioTags{}, ErrUnsupportedTags
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > d.localLimit() {
		return AudioTags{}, errors.New("audio inspection requires an intact regular file")
	}
	var tags AudioTags
	err = tagLibCall(func() error {
		properties, err := taglib.ReadProperties(path)
		if err != nil || properties.Format == "" || properties.Channels == 0 || properties.SampleRate == 0 {
			return errors.New("cannot read audio properties")
		}
		values, err := taglib.ReadTags(path)
		if err != nil {
			return errors.New("cannot read audio tags")
		}
		if title := cleanTagValues(values[taglib.Title]); len(title) > 0 {
			tags.Title = title[0]
		}
		if album := cleanTagValues(values[taglib.Album]); len(album) > 0 {
			tags.Album = album[0]
		}
		tags.Artist = cleanTagValues(values[taglib.Artist])
		tags.Cover = len(properties.Images) != 0
		tags.Lyrics = hasEmbeddedLyrics(values)
		return nil
	})
	if err != nil {
		return AudioTags{}, err
	}
	if !unchangedTagInput(path, info) {
		return AudioTags{}, errors.New("audio file changed during inspection")
	}
	return tags, ctx.Err()
}
