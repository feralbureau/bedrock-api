// resolver.go - bridge logic for resolving streams across providers
//
// simple, lowercase comments. indie dev style.

package main

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"unicode"

	pb "github.com/feralbureau/bedrock-api/bedrock"
)

// query cleaning
// these regexes strip common noise like "feat.", "(remix)", "radio edit", etc.
var noisePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\s*[\(\[]?(?:feat\.?|ft\.?|featuring)\s+[^\)\]]+[\)\]]?`),
	regexp.MustCompile(`(?i)\s*[\(\[][^\)\]]*(remix|edit|mix|version|remaster(?:ed)?|deluxe|live|acoustic|instrumental|explicit|clean|anniversary|bonus)[^\)\]]*[\)\]]`),
}

// cleanQuery removes noisy bits from strings like "artist - title"
func cleanQuery(raw string) string {
	q := raw
	for _, re := range noisePatterns {
		q = re.ReplaceAllString(q, "")
	}
	// collapse extra spaces and trim
	q = strings.Join(strings.FieldsFunc(q, unicode.IsSpace), " ")
	return strings.TrimSpace(q)
}

// provider access helpers
// thin wrappers that read from providerMap (populated in initProviders).
// no retry or caching here; providers handle that.
func getDeezerProvider() (trackProvider, error) {
	p, ok := providerMap[pb.Platform_PLATFORM_DEEZER]
	if !ok {
		return nil, fmt.Errorf("deezer provider not registered")
	}
	return p, nil
}

func getSoundCloudProvider() (trackProvider, error) {
	p, ok := providerMap[pb.Platform_PLATFORM_SOUNDCLOUD]
	if !ok {
		return nil, fmt.Errorf("soundcloud provider not registered")
	}
	return p, nil
}

func getSpotifyProvider() (trackProvider, error) {
	p, ok := providerMap[pb.Platform_PLATFORM_SPOTIFY]
	if !ok {
		return nil, fmt.Errorf("spotify provider not registered")
	}
	// note: when spotify is disabled we keep a stubProvider in the map.
	return p, nil
}

func getYouTubeProvider() (trackProvider, error) {
	p, ok := providerMap[pb.Platform_PLATFORM_YOUTUBE]
	if !ok {
		return nil, fmt.Errorf("youtube provider not registered")
	}
	return p, nil
}

// resolveStreamURL decides how to get a playable url for a namespaced track id.
// supported bridges:
// - soundcloud:* -> direct soundcloud
// - spotify:*    -> spotify metadata -> soundcloud search -> soundcloud stream
// - deezer:*     -> deezer metadata -> soundcloud search -> soundcloud stream
func resolveStreamURL(ctx context.Context, trackID, preferredFormat string) (*pb.GetStreamURLResponse, error) {
	plat, nativeID := parsePlatformID(trackID)

	switch plat {
	case pb.Platform_PLATFORM_SOUNDCLOUD:
		return resolveSoundCloudDirect(ctx, nativeID, preferredFormat)

	case pb.Platform_PLATFORM_SPOTIFY:
		return resolveSpotifyViaSoundCloud(ctx, nativeID, preferredFormat)

	case pb.Platform_PLATFORM_DEEZER:
		return resolveDeezerViaSoundCloud(ctx, nativeID, preferredFormat)

	case pb.Platform_PLATFORM_YOUTUBE:
		return resolveYouTubeDirect(ctx, nativeID, preferredFormat)

	default:
		return nil, fmt.Errorf("stream resolution not supported for platform %q in track_id %q",
			pb.Platform_name[int32(plat)], trackID)
	}
}

// soundcloud direct: just call the provider
func resolveSoundCloudDirect(ctx context.Context, nativeID, preferredFormat string) (*pb.GetStreamURLResponse, error) {
	scProvider, err := getSoundCloudProvider()
	if err != nil {
		return nil, err
	}

	log.Printf("[resolver] soundcloud direct -> id=%q format=%q", nativeID, preferredFormat)

	resp, err := scProvider.GetStreamURL(ctx, nativeID, preferredFormat)
	if err != nil {
		return nil, fmt.Errorf("resolver: soundcloud getstreamurl id=%q: %w", nativeID, err)
	}
	return resp, nil
}

// deezer -> soundcloud bridge
// flow: fetch deezer metadata, build cleaned query, search soundcloud, resolve stream
func resolveDeezerViaSoundCloud(ctx context.Context, deezerNativeID, preferredFormat string) (*pb.GetStreamURLResponse, error) {
	log.Printf("[resolver] deezer -> soundcloud | deezer_id=%q", deezerNativeID)

	dzProvider, err := getDeezerProvider()
	if err != nil {
		return nil, fmt.Errorf("resolver: deezer provider unavailable: %w", err)
	}

	deezerTrack, err := dzProvider.GetTrack(ctx, deezerNativeID)
	if err != nil {
		return nil, fmt.Errorf("resolver: deezer gettrack id=%q: %w", deezerNativeID, err)
	}
	if deezerTrack == nil {
		return nil, fmt.Errorf("resolver: deezer returned nil track for id=%q", deezerNativeID)
	}

	artist := strings.TrimSpace(deezerTrack.GetArtist())
	title := strings.TrimSpace(deezerTrack.GetTitle())
	if artist == "" || title == "" {
		return nil, fmt.Errorf("resolver: deezer track id=%q has empty artist or title", deezerNativeID)
	}

	rawQuery := fmt.Sprintf("%s - %s", artist, title)
	query := cleanQuery(rawQuery)
	if query == "" {
		query = rawQuery
	}

	log.Printf("[resolver] search query -> raw=%q cleaned=%q", rawQuery, query)

	scProvider, err := getSoundCloudProvider()
	if err != nil {
		return nil, fmt.Errorf("resolver: soundcloud provider unavailable: %w", err)
	}

	const searchLimit = 3
	results, err := scProvider.SearchTracks(ctx, query, searchLimit)
	if err != nil {
		return nil, fmt.Errorf("resolver: soundcloud searchtracks query=%q: %w", query, err)
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("resolver: no soundcloud results for query=%q (deezer_id=%q)", query, deezerNativeID)
	}

	best := results[0]
	scNativeID := best.GetPlatformId()
	if scNativeID == "" {
		return nil, fmt.Errorf("resolver: soundcloud result missing platform_id (deezer_id=%q)", deezerNativeID)
	}

	log.Printf("[resolver] found sc match -> sc_id=%q title=%q artist=%q streamable=%v",
		scNativeID, best.GetTitle(), best.GetArtist(), best.GetIsStreamable())

	log.Printf("[resolver] fetching soundcloud stream -> sc_id=%q format=%q", scNativeID, preferredFormat)
	streamResp, err := scProvider.GetStreamURL(ctx, scNativeID, preferredFormat)
	if err != nil {
		return nil, fmt.Errorf("resolver: soundcloud getstreamurl sc_id=%q (deezer_id=%q): %w", scNativeID, deezerNativeID, err)
	}
	if streamResp == nil || streamResp.GetStreamUrl() == "" {
		return nil, fmt.Errorf("resolver: soundcloud returned empty stream url for sc_id=%q (deezer_id=%q)", scNativeID, deezerNativeID)
	}

	log.Printf("[resolver] stream obtained | deezer_id=%q -> sc_id=%q | url_prefix=%.60s…", deezerNativeID, scNativeID, streamResp.GetStreamUrl())

	streamResp.IsFallback = true
	streamResp.FallbackFrom = pb.Platform_name[int32(pb.Platform_PLATFORM_DEEZER)]

	return streamResp, nil
}

// resolveCoverURL fetches the raw cover image url for a given service and native id.
// tries track metadata first, then album.
func resolveCoverURL(ctx context.Context, service, nativeID string) (string, error) {
	plat, _ := parsePlatformID(service + ":" + nativeID)
	p, ok := providerMap[plat]
	if !ok {
		return "", fmt.Errorf("resolver: unknown platform %q", service)
	}

	log.Printf("[resolver] resolve cover | service=%s id=%s", service, nativeID)

	// first try as a track
	track, err := p.GetTrack(ctx, nativeID)
	if err == nil && track != nil && track.GetCoverUrl() != "" {
		return track.GetCoverUrl(), nil
	}

	// then try as an album
	album, _, err := p.GetAlbum(ctx, nativeID)
	if err == nil && album != nil && album.GetCoverUrl() != "" {
		return album.GetCoverUrl(), nil
	}

	return "", fmt.Errorf("resolver: could not resolve cover for %s:%s", service, nativeID)
}

// spotify -> soundcloud bridge
// flow: fetch spotify metadata, build cleaned query, search soundcloud, resolve stream
func resolveSpotifyViaSoundCloud(ctx context.Context, spotifyNativeID, preferredFormat string) (*pb.GetStreamURLResponse, error) {
	log.Printf("[resolver] spotify -> soundcloud | spotify_id=%q", spotifyNativeID)

	spProvider, err := getSpotifyProvider()
	if err != nil {
		return nil, fmt.Errorf("resolver: spotify provider unavailable: %w", err)
	}

	spotifyTrack, err := spProvider.GetTrack(ctx, spotifyNativeID)
	if err != nil {
		return nil, fmt.Errorf("resolver: spotify gettrack id=%q: %w", spotifyNativeID, err)
	}
	if spotifyTrack == nil {
		return nil, fmt.Errorf("resolver: spotify returned nil track for id=%q", spotifyNativeID)
	}

	artist := strings.TrimSpace(spotifyTrack.GetArtist())
	title := strings.TrimSpace(spotifyTrack.GetTitle())
	if artist == "" || title == "" {
		return nil, fmt.Errorf("resolver: spotify track id=%q has empty artist or title", spotifyNativeID)
	}

	rawQuery := fmt.Sprintf("%s - %s", artist, title)
	query := cleanQuery(rawQuery)
	if query == "" {
		// unlikely, but fallback if cleaning removes everything
		query = rawQuery
	}

	log.Printf("[resolver] search query -> raw=%q cleaned=%q", rawQuery, query)

	scProvider, err := getSoundCloudProvider()
	if err != nil {
		return nil, fmt.Errorf("resolver: soundcloud provider unavailable: %w", err)
	}

	const searchLimit = 3
	results, err := scProvider.SearchTracks(ctx, query, searchLimit)
	if err != nil {
		return nil, fmt.Errorf("resolver: soundcloud searchtracks query=%q: %w", query, err)
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("resolver: no soundcloud results for query=%q (spotify_id=%q)", query, spotifyNativeID)
	}

	best := results[0]
	scNativeID := best.GetPlatformId()
	if scNativeID == "" {
		return nil, fmt.Errorf("resolver: soundcloud result missing platform_id (spotify_id=%q)", spotifyNativeID)
	}

	log.Printf("[resolver] found sc match -> sc_id=%q title=%q artist=%q streamable=%v",
		scNativeID, best.GetTitle(), best.GetArtist(), best.GetIsStreamable())

	log.Printf("[resolver] fetching soundcloud stream -> sc_id=%q format=%q", scNativeID, preferredFormat)
	streamResp, err := scProvider.GetStreamURL(ctx, scNativeID, preferredFormat)
	if err != nil {
		return nil, fmt.Errorf("resolver: soundcloud getstreamurl sc_id=%q (spotify_id=%q): %w", scNativeID, spotifyNativeID, err)
	}
	if streamResp == nil || streamResp.GetStreamUrl() == "" {
		return nil, fmt.Errorf("resolver: soundcloud returned empty stream url for sc_id=%q (spotify_id=%q)", scNativeID, spotifyNativeID)
	}

	log.Printf("[resolver] stream obtained | spotify_id=%q -> sc_id=%q | url_prefix=%.60s…", spotifyNativeID, scNativeID, streamResp.GetStreamUrl())

	streamResp.IsFallback = true
	streamResp.FallbackFrom = pb.Platform_name[int32(pb.Platform_PLATFORM_SPOTIFY)]

	return streamResp, nil
}

func resolveYouTubeDirect(ctx context.Context, nativeID, preferredFormat string) (*pb.GetStreamURLResponse, error) {
	p, err := getYouTubeProvider()
	if err != nil {
		return nil, err
	}

	log.Printf("[resolver] youtube innertube | video_id=%q format=%q", nativeID, preferredFormat)

	resp, err := p.GetStreamURL(ctx, nativeID, preferredFormat)
	if err != nil {
		log.Printf("[resolver] youtube innertube failed | video_id=%q: %v", nativeID, err)
		return nil, fmt.Errorf("resolver: youtube getstreamurl id=%q: %w", nativeID, err)
	}

	if resp.GetSource() == pb.Platform_PLATFORM_YOUTUBE {
		log.Printf("[resolver] youtube innertube ok | video_id=%q url=%.60s…", nativeID, resp.GetStreamUrl())
	} else {
		log.Printf("[resolver] youtube unexpected fallback | video_id=%q source=%v", nativeID, resp.GetSource())
	}

	return resp, nil
}
