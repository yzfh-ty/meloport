package syncer

import (
	"context"
	"fmt"

	"meloport/internal/chksz"
	"meloport/internal/model"
)

// ResolveWithFallback requests the configured quality first, then walks only
// lower tiers when ChKSz explicitly says that quality is unavailable. The
// caller controls whether this behavior is enabled; use the ordinary resolver
// directly when automatic downgrade is disabled.
func ResolveWithFallback(ctx context.Context, resolver AudioResolver, platform string, track model.Track, requested string) (model.ResolvedTrack, string, error) {
	ladder, found := qualityLadder(platform, requested)
	if !found {
		resolved, err := resolver.Resolve(ctx, platform, track, requested)
		if err != nil {
			return model.ResolvedTrack{}, "", err
		}
		return resolved, requested, nil
	}

	var unavailableErr error
	for _, quality := range ladder {
		if err := ctx.Err(); err != nil {
			return model.ResolvedTrack{}, "", err
		}
		resolved, err := resolver.Resolve(ctx, platform, track, quality)
		if err == nil {
			return resolved, quality, nil
		}
		if ctx.Err() != nil {
			return model.ResolvedTrack{}, "", ctx.Err()
		}
		if !chksz.IsQualityUnavailable(err) {
			return model.ResolvedTrack{}, "", err
		}
		unavailableErr = err
	}
	return model.ResolvedTrack{}, "", fmt.Errorf("no available quality at or below %s: %w", requested, unavailableErr)
}

func qualityLadder(platform, requested string) ([]string, bool) {
	switch platform {
	case "netease":
		switch requested {
		case "jymaster":
			return []string{"jymaster", "sky", "jyeffect", "hires", "lossless", "exhigh", "standard"}, true
		case "sky":
			return []string{"sky", "jyeffect", "hires", "lossless", "exhigh", "standard"}, true
		case "jyeffect":
			return []string{"jyeffect", "hires", "lossless", "exhigh", "standard"}, true
		case "hires":
			return []string{"hires", "lossless", "exhigh", "standard"}, true
		case "lossless":
			return []string{"lossless", "exhigh", "standard"}, true
		case "exhigh":
			return []string{"exhigh", "standard"}, true
		case "standard":
			return []string{"standard"}, true
		}
	case "qq", "kugou":
		switch requested {
		case "master":
			return []string{"master", "hires", "flac", "320k", "128k"}, true
		case "hires":
			return []string{"hires", "flac", "320k", "128k"}, true
		case "flac":
			return []string{"flac", "320k", "128k"}, true
		case "320k":
			return []string{"320k", "128k"}, true
		case "128k":
			return []string{"128k"}, true
		}
	default:
		return nil, false
	}
	return nil, false
}
