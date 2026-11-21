package syncer

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"meloport/internal/chksz"
	"meloport/internal/model"
)

type qualityResolver struct {
	qualities []string
	resolve   func(string) (model.ResolvedTrack, error)
}

func (r *qualityResolver) Resolve(_ context.Context, _ string, _ model.Track, quality string) (model.ResolvedTrack, error) {
	r.qualities = append(r.qualities, quality)
	return r.resolve(quality)
}

func TestResolveWithFallbackDowngradesUntilAvailable(t *testing.T) {
	resolver := &qualityResolver{resolve: func(quality string) (model.ResolvedTrack, error) {
		if quality != "320k" {
			return model.ResolvedTrack{}, &chksz.APIError{Status: 404, Message: "requested quality is unavailable"}
		}
		return model.ResolvedTrack{URL: "https://audio.example/song.mp3"}, nil
	}}
	resolved, actual, err := ResolveWithFallback(context.Background(), resolver, "qq", model.Track{ID: "track"}, "master")
	if err != nil || actual != "320k" || resolved.URL == "" {
		t.Fatalf("ResolveWithFallback() = (%+v, %q, %v)", resolved, actual, err)
	}
	want := []string{"master", "hires", "flac", "320k"}
	if !reflect.DeepEqual(resolver.qualities, want) {
		t.Fatalf("qualities = %v, want %v", resolver.qualities, want)
	}
}

func TestResolveWithFallbackStopsForOtherErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{"missing track", &chksz.APIError{Status: 404, Message: "track not found"}},
		{"quota", &chksz.APIError{Status: 402, Message: "quality unavailable"}},
		{"network", errors.New("connection failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolver := &qualityResolver{resolve: func(string) (model.ResolvedTrack, error) {
				return model.ResolvedTrack{}, test.err
			}}
			_, _, err := ResolveWithFallback(context.Background(), resolver, "netease", model.Track{ID: "track"}, "lossless")
			if err == nil || !errors.Is(err, test.err) {
				t.Fatalf("expected original error %v, got %v", test.err, err)
			}
			if len(resolver.qualities) != 1 || resolver.qualities[0] != "lossless" {
				t.Fatalf("unexpected retry after %v: %v", test.err, resolver.qualities)
			}
		})
	}
}

func TestResolveWithFallbackHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	resolver := &qualityResolver{resolve: func(string) (model.ResolvedTrack, error) {
		cancel()
		return model.ResolvedTrack{}, &chksz.APIError{Status: 404, Message: "quality unavailable"}
	}}
	_, _, err := ResolveWithFallback(ctx, resolver, "kugou", model.Track{ID: "track"}, "flac")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if !reflect.DeepEqual(resolver.qualities, []string{"flac"}) {
		t.Fatalf("attempted another quality after cancellation: %v", resolver.qualities)
	}
}

func TestResolveWithFallbackReturnsLowestTierFailure(t *testing.T) {
	resolver := &qualityResolver{resolve: func(quality string) (model.ResolvedTrack, error) {
		return model.ResolvedTrack{}, &chksz.APIError{Status: 404, Message: "requested quality unavailable: " + quality}
	}}
	_, actual, err := ResolveWithFallback(context.Background(), resolver, "netease", model.Track{ID: "track"}, "exhigh")
	if err == nil || actual != "" {
		t.Fatalf("expected exhausted ladder, actual=%q err=%v", actual, err)
	}
	want := []string{"exhigh", "standard"}
	if !reflect.DeepEqual(resolver.qualities, want) {
		t.Fatalf("qualities = %v, want %v", resolver.qualities, want)
	}
}

func TestResolveWithFallbackDoesNotRepeatUnsupportedQuality(t *testing.T) {
	resolver := &qualityResolver{resolve: func(string) (model.ResolvedTrack, error) {
		return model.ResolvedTrack{}, errors.New("unsupported platform audio quality")
	}}
	_, _, err := ResolveWithFallback(context.Background(), resolver, "qq", model.Track{ID: "track"}, "bad-quality")
	if err == nil || !reflect.DeepEqual(resolver.qualities, []string{"bad-quality"}) {
		t.Fatalf("unsupported quality was retried: attempts=%v err=%v", resolver.qualities, err)
	}
}
