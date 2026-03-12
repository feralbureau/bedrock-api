package providers

// youtube.go — youtube music provider for the bedrock gRPC aggregator.
//
// uses the innertube api via github.com/wslyyy/youtube-go for metadata.
// auth stays unauthenticated unless YOUTUBE_COOKIES is supplied for age-locked content.
// streaming: innertube has drm, so getstreamurl falls back to soundcloud like the spotify/deezer bridge.
//
// the innertube api returns deeply nested maps; the helpers pull the fields we need for tracks, albums, artists, and playlists.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	pb "github.com/feralbureau/bedrock-api/bedrock"

	innertube "github.com/wslyyy/youtube-go"
)

// sentinel errors

var (
	ErrYTNotFound = errors.New("youtube: resource not found")
	ErrYTNoStream = errors.New("youtube: no direct stream URL found across all clients")
	ErrYTAPI      = errors.New("youtube: innertube api error")
)

// constants

const (
	ytHTTPTimeout       = 12 * time.Second
	ytStreamHTTPTimeout = 5 * time.Second // shorter timeout per stream client attempt
	// youtube music search filter params (base64-encoded protobuf).
	// egwkaqiiaq== filters to songs only
	ytParamsSongs = "EgWKAQIIAQ%3D%3D"
)

// provider struct

// ytStreamClient is one entry in the stream fallback pool.
type ytStreamClient struct {
	name           string
	client         *innertube.InnerTube
	extraClientCtx map[string]interface{} // extra fields merged into context.client
	needsEmbedURL  bool                   // set context.thirdParty.embedUrl (embedded-player clients)
}

// player makes an Innertube /player call with the client-appropriate body.
// pre-populating context.client before contextualise runs keeps our extra fields while still letting the library inject clientName/clientVersion.
func (sc *ytStreamClient) player(videoID string) (map[string]interface{}, error) {
	body := map[string]interface{}{
		"videoId": videoID,
		"context": map[string]interface{}{
			"client": map[string]interface{}{},
		},
	}
	clientCtx := body["context"].(map[string]interface{})["client"].(map[string]interface{})
	for k, v := range sc.extraClientCtx {
		clientCtx[k] = v
	}
	if sc.needsEmbedURL {
		body["context"].(map[string]interface{})["thirdParty"] = map[string]interface{}{
			"embedUrl": "https://www.youtube.com/embed/" + videoID,
		}
	}
	return sc.client.Call("PLAYER", nil, body)
}

// buildStreamPool returns the ordered client fallback chain used by GetStreamURL
// clients earlier in the list are tried first formats with signatureCipher are skipped because we cannot run js deobfuscation

// order mirrors metrolists stream_fallback_clients approach, adapted for go
// credits to github.com/MetrolistGroup/Metrolist
//  1. TVHTML5_SIMPLY_EMBEDDED_PLAYER – embeddedplayer bypass for age restricted content
//  2. TVHTML5 (v7.20260213)          – tv client with poToken support
//  3. ANDROID_VR 1.43.32            – non adaptive bitrate, avoids audio stuttering
//  4. ANDROID_VR 1.61.48            – newer Android VR variant
//  5. ANDROID 21.03.38              – mobile client, returns direct URLs consistently
//  6. IOS 21.03.1                   – iPhone client, returns direct URLs consistently
//  7. WEB 2.20260213                – standard web client, last resort

func buildStreamPool() []ytStreamClient {
	streamHTTPClient := &http.Client{Timeout: ytStreamHTTPTimeout}
	mk := func(clientName, clientVersion string, clientID int, apiKey, userAgent, referer string) *innertube.InnerTube {
		ctx := innertube.ClientContext{
			ClientName:    clientName,
			ClientVersion: clientVersion,
			ClientID:      clientID,
			APIKey:        apiKey,
			UserAgent:     userAgent,
			Referer:       referer,
		}
		return &innertube.InnerTube{
			Adaptor: innertube.NewInnerTubeAdaptor(ctx, streamHTTPClient),
		}
	}

	const (
		keyAndroid = "AIzaSyA8eiZmM1FaDVjRy-df2KTyQ_vz_yYM39w"
		keyIOS     = "AIzaSyB-63vPrdThhKuerbB2N_l7Kwwcxj6yUAc"
		keyTV      = "AIzaSyDCU8hByM-4DrUqRUYnGn-3llEO78bcxq8"
		keyWeb     = "AIzaSyAO_FJ2SlqU8Q4STEHLGCilw_Y9_11qcW8"
		uaAndroid  = "com.google.android.youtube/21.03.38 (Linux; U; Android 14) gzip"
		uaIOS      = "com.google.ios.youtube/21.03.1 (iPhone16,2; U; CPU iOS 18_2 like Mac OS X;)"
	)

	return []ytStreamClient{
		{
			name:          "TVHTML5_SIMPLY_EMBEDDED_PLAYER",
			client:        mk("TVHTML5_SIMPLY_EMBEDDED_PLAYER", "2.0", 85, "", innertube.USER_AGENT_TV_HTML5, innertube.REFERER_YOUTUBE),
			needsEmbedURL: true,
		},
		{
			name:   "TVHTML5",
			client: mk("TVHTML5", "7.20260213.00.00", 7, keyTV, innertube.USER_AGENT_TV_HTML5, innertube.REFERER_YOUTUBE),
		},
		{
			name:           "ANDROID_VR_1_43_32",
			client:         mk("ANDROID_VR", "1.43.32", 28, keyAndroid, uaAndroid, ""),
			extraClientCtx: map[string]interface{}{"androidSdkVersion": 34},
		},
		{
			name:           "ANDROID_VR_1_61_48",
			client:         mk("ANDROID_VR", "1.61.48", 28, keyAndroid, uaAndroid, ""),
			extraClientCtx: map[string]interface{}{"androidSdkVersion": 34},
		},
		{
			name:           "ANDROID",
			client:         mk("ANDROID", "21.03.38", 3, keyAndroid, uaAndroid, ""),
			extraClientCtx: map[string]interface{}{"androidSdkVersion": 34},
		},
		{
			name:           "IOS",
			client:         mk("IOS", "21.03.1", 5, keyIOS, uaIOS, ""),
			extraClientCtx: map[string]interface{}{"osVersion": "18.2.22C152", "deviceModel": "iPhone16,2"},
		},
		{
			name:   "WEB",
			client: mk("WEB", "2.20260213.00.00", 1, keyWeb, innertube.USER_AGENT_WEB, innertube.REFERER_YOUTUBE),
		},
	}
}

type YouTubeProvider struct {
	mu         sync.Mutex
	client     *innertube.InnerTube // web_remix for search/browse
	streamPool []ytStreamClient     // ordered fallback chain for streaming
}

// new youtube provider creates an innertube client
func NewYouTubeProvider() (*YouTubeProvider, error) {
	httpClient := &http.Client{Timeout: ytHTTPTimeout}

	// web_remix (id=67, v1.20260213) is the youtube music web client for search and browse
	mainCtx := innertube.ClientContext{
		ClientName:    "WEB_REMIX",
		ClientVersion: "1.20260213.01.00",
		ClientID:      67,
		APIKey:        "AIzaSyC9XL3ZjWddXya6X74dJoCTL-WEYFDNX30",
		UserAgent:     innertube.USER_AGENT_WEB,
		Referer:       innertube.REFERER_YOUTUBE_MUSIC,
	}
	it := &innertube.InnerTube{
		Adaptor: innertube.NewInnerTubeAdaptor(mainCtx, httpClient),
	}

	pool := buildStreamPool()
	log.Printf("[youtube] initialised WEB_REMIX search client + %d-client stream pool", len(pool))

	return &YouTubeProvider{
		client:     it,
		streamPool: pool,
	}, nil
}

func (p *YouTubeProvider) Platform() pb.Platform {
	return pb.Platform_PLATFORM_YOUTUBE
}

// id helpers

func ytNamespacedID(videoID string) string {
	return "youtube:" + videoID
}

func ytStripPrefix(id string) string {
	return strings.TrimPrefix(id, "youtube:")
}

// innerTube response parsing helpers
//
// innertube returns deeply nested map[string]interface{} responses
// these helpers safely navigate the structure to extract the fields we need without panicking on missing keys

// safeStr extracts a string from a nested map path
func safeStr(m map[string]interface{}, keys ...string) string {
	var current interface{} = m
	for _, k := range keys {
		if cm, ok := current.(map[string]interface{}); ok {
			current = cm[k]
		} else {
			return ""
		}
	}
	if s, ok := current.(string); ok {
		return s
	}
	return ""
}

// safeSlice extracts a []interface{} from a nested map path
func safeSlice(m map[string]interface{}, keys ...string) []interface{} {
	var current interface{} = m
	for _, k := range keys {
		if cm, ok := current.(map[string]interface{}); ok {
			current = cm[k]
		} else {
			return nil
		}
	}
	if s, ok := current.([]interface{}); ok {
		return s
	}
	return nil
}

// safeMap extracts a map[string]interface{} from a nested map path
func safeMap(m map[string]interface{}, keys ...string) map[string]interface{} {
	var current interface{} = m
	for _, k := range keys {
		if cm, ok := current.(map[string]interface{}); ok {
			current = cm[k]
		} else {
			return nil
		}
	}
	if cm, ok := current.(map[string]interface{}); ok {
		return cm
	}
	return nil
}

// safeFloat extracts a float64 from a nested map path
func safeFloat(m map[string]interface{}, keys ...string) float64 {
	var current interface{} = m
	for _, k := range keys {
		if cm, ok := current.(map[string]interface{}); ok {
			current = cm[k]
		} else {
			return 0
		}
	}
	switch v := current.(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case string:
		f, _ := strconv.ParseFloat(v, 64)
		return f
	}
	return 0
}

// extractText extracts a string from an InnerTube text object (which can be a
// simple string, a map with simpleText, or a map with runs).
func (p *YouTubeProvider) extractText(m interface{}) string {
	if m == nil {
		return ""
	}
	// if it's already a string, return it
	if s, ok := m.(string); ok {
		return s
	}
	// if it's a map, look for text fields
	mm, ok := m.(map[string]interface{})
	if !ok {
		return ""
	}

	if s := safeStr(mm, "simpleText"); s != "" {
		return s
	}
	if s := safeStr(mm, "text"); s != "" {
		return s
	}

	// try runs array
	runs := safeSlice(mm, "runs")
	if len(runs) > 0 {
		var parts []string
		for _, r := range runs {
			if rm, ok := r.(map[string]interface{}); ok {
				if text := safeStr(rm, "text"); text != "" {
					parts = append(parts, text)
				}
			} else if rs, ok := r.(string); ok {
				parts = append(parts, rs)
			}
		}
		return strings.Join(parts, "")
	}

	// try nested text object
	if textObj := safeMap(mm, "text"); textObj != nil {
		return p.extractText(textObj)
	}

	return ""
}

// findItems recursively searches for shelves and grids containing music items.
func (p *YouTubeProvider) findItems(m interface{}) []interface{} {
	if m == nil {
		return nil
	}

	// if it's a map, check for shelves
	if mm, ok := m.(map[string]interface{}); ok {
		// look for known shelf types
		for _, key := range []string{"musicShelfRenderer", "musicPlaylistShelfRenderer", "gridRenderer"} {
			if shelf := safeMap(mm, key); shelf != nil {
				if contents := safeSlice(shelf, "contents"); len(contents) > 0 {
					return contents
				}
			}
		}

		// recurse into all map values
		for _, v := range mm {
			if items := p.findItems(v); len(items) > 0 {
				return items
			}
		}
	}

	// if it's a slice, recurse into elements
	if ss, ok := m.([]interface{}); ok {
		for _, v := range ss {
			if items := p.findItems(v); len(items) > 0 {
				return items
			}
		}
	}

	return nil
}

// extractRunsText is kept for background compatibility but calls extractText
func extractRunsText(m map[string]interface{}, key string) string {
	obj := safeMap(m, key)
	if obj == nil {
		return ""
	}

	// simple text first
	if s := safeStr(obj, "simpleText"); s != "" {
		return s
	}
	// runs array
	runs := safeSlice(obj, "runs")
	if len(runs) == 0 {
		// try text field
		if s, ok := obj["text"].(string); ok {
			return s
		}
		return ""
	}
	var parts []string
	for _, r := range runs {
		if rm, ok := r.(map[string]interface{}); ok {
			if text := safeStr(rm, "text"); text != "" {
				parts = append(parts, text)
			}
		} else if rs, ok := r.(string); ok {
			parts = append(parts, rs)
		}
	}
	return strings.Join(parts, "")
}

// extractThumbnailURL picks the highest-resolution thumbnail.
func extractThumbnailURL(m map[string]interface{}, key string) string {
	obj := safeMap(m, key)
	if obj == nil {
		return ""
	}
	thumbs := safeSlice(obj, "thumbnails")
	if len(thumbs) == 0 {
		return ""
	}
	// last thumbnail is usually the largest
	last := thumbs[len(thumbs)-1]
	if tm, ok := last.(map[string]interface{}); ok {
		url := safeStr(tm, "url")
		// innertube sometimes returns protocol-relative urls
		if strings.HasPrefix(url, "//") {
			url = "https:" + url
		}
		return url
	}
	return ""
}

// parseDurationText converts "3:45", "1:02:30", or "45" to milliseconds.
func parseDurationText(s string) int32 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	// remove non-numeric prefix/suffix if any (e.g. "Duration: 3:45")
	lastSpace := strings.LastIndex(s, " ")
	if lastSpace != -1 {
		s = s[lastSpace+1:]
	}

	parts := strings.Split(s, ":")
	var total int
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return 0
		}
		total = total*60 + n
	}
	return int32(total * 1000)
}

// search response parsers

// parseSearchTracksFromShelf extracts tracks from YouTube Music search response
// the response structure is:
// contents.tabbedSearchResultsRenderer.tabs[0].tabRenderer.content
//
//	.sectionListRenderer.contents[].musicShelfRenderer.contents[]
//	  .musicResponsiveListItemRenderer
func (p *YouTubeProvider) parseSearchTracks(data map[string]interface{}, limit int) []*pb.Track {
	var tracks []*pb.Track

	// navigate to the shelf contents
	contents := p.findMusicShelfContents(data)
	if contents == nil {
		// try alternate path for non-music search
		contents = p.findSearchContents(data)
	}

	for _, item := range contents {
		if len(tracks) >= limit {
			break
		}
		im, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		track := p.parseMusicListItem(im)
		if track != nil {
			tracks = append(tracks, track)
		}
	}
	return tracks
}

// findMusicShelfContents navigates to musicShelfRenderer contents
func (p *YouTubeProvider) findMusicShelfContents(data map[string]interface{}) []interface{} {
	// path 1: tabbedSearchResultsRenderer (youtube music)
	tabs := safeSlice(data, "contents", "tabbedSearchResultsRenderer", "tabs")
	if len(tabs) > 0 {
		tab0 := safeMap(tabs[0].(map[string]interface{}), "tabRenderer", "content", "sectionListRenderer")
		if tab0 != nil {
			sections := safeSlice(tab0, "contents")
			for _, sec := range sections {
				if sm, ok := sec.(map[string]interface{}); ok {
					shelf := safeMap(sm, "musicShelfRenderer")
					if shelf != nil {
						return safeSlice(shelf, "contents")
					}
				}
			}
		}
	}

	// path 2: sectionListRenderer directly
	sections := safeSlice(data, "contents", "sectionListRenderer", "contents")
	for _, sec := range sections {
		if sm, ok := sec.(map[string]interface{}); ok {
			shelf := safeMap(sm, "musicShelfRenderer")
			if shelf != nil {
				return safeSlice(shelf, "contents")
			}
		}
	}

	return nil
}

// findSearchContents extracts video results from regular YouTube search.
func (p *YouTubeProvider) findSearchContents(data map[string]interface{}) []interface{} {
	sections := safeSlice(data, "contents", "sectionListRenderer", "contents")
	for _, sec := range sections {
		if sm, ok := sec.(map[string]interface{}); ok {
			renderer := safeMap(sm, "itemSectionRenderer")
			if renderer != nil {
				return safeSlice(renderer, "contents")
			}
		}
	}
	return nil
}

// parseMusicListItem extracts a track from a musicResponsiveListItemRenderer.
func (p *YouTubeProvider) parseMusicListItem(item map[string]interface{}) *pb.Track {
	renderer := safeMap(item, "musicResponsiveListItemRenderer")
	if renderer == nil {
		renderer = safeMap(item, "videoRenderer")
	}
	if renderer == nil {
		renderer = safeMap(item, "musicTwoRowItemRenderer")
	}
	if renderer == nil {
		renderer = safeMap(item, "playlistPanelVideoRenderer")
	}
	if renderer == nil {
		return nil
	}

	// extract video ID from overlay or playlistItemData
	videoID := safeStr(renderer, "playlistItemData", "videoId")
	if videoID == "" {
		overlay := safeMap(renderer, "overlay", "musicItemThumbnailOverlayRenderer",
			"content", "musicPlayButtonRenderer", "playNavigationEndpoint",
			"watchEndpoint")
		if overlay != nil {
			videoID = safeStr(overlay, "videoId")
		}
	}
	if videoID == "" {
		// try flexColumns navigation endpoint
		cols := safeSlice(renderer, "flexColumns")
		if len(cols) > 0 {
			col0 := cols[0]
			if cm, ok := col0.(map[string]interface{}); ok {
				runs := safeSlice(cm, "musicResponsiveListItemFlexColumnRenderer", "text", "runs")
				if len(runs) > 0 {
					if rm, ok := runs[0].(map[string]interface{}); ok {
						videoID = safeStr(rm, "navigationEndpoint", "watchEndpoint", "videoId")
					}
				}
			}
		}
	}
	if videoID == "" {
		return nil
	}

	// extract title and artist from flexColumns or alternative fields
	title := ""
	artist := ""
	album := ""
	durationText := ""

	cols := safeSlice(renderer, "flexColumns")
	for i, col := range cols {
		cm, ok := col.(map[string]interface{})
		if !ok {
			continue
		}
		flexRenderer := safeMap(cm, "musicResponsiveListItemFlexColumnRenderer")
		if flexRenderer == nil {
			continue
		}
		text := p.extractText(flexRenderer)

		if i == 0 {
			title = text
			continue
		}

		// secondary columns often have "Song • Artist • Album • Duration"
		// or they might just contain one of those fields
		if text != "" {
			if strings.Contains(text, " • ") {
				parts := strings.Split(text, " • ")
				for _, p := range parts {
					p = strings.TrimSpace(p)
					if p == "" {
						continue
					}
					// if it looks like a duration, take it
					if strings.Contains(p, ":") && !strings.ContainsAny(p, "abcdefghijklmnopqrstuvwxyz") {
						durationText = p
						continue
					}
					// skip type descriptors
					if strings.EqualFold(p, "Song") || strings.EqualFold(p, "Video") || strings.EqualFold(p, "Single") {
						continue
					}
					// assign to artist then album
					if artist == "" {
						artist = p
					} else if album == "" {
						album = p
					}
				}
			} else {
				// single field in column
				if strings.Contains(text, ":") && !strings.ContainsAny(text, "abcdefghijklmnopqrstuvwxyz") {
					durationText = text
				} else if artist == "" {
					artist = text
				} else if album == "" {
					album = text
				}
			}
		}
	}

	// fallback for non-responsive renderers (videoRenderer, twoRow, playlistPanel)
	if title == "" {
		title = p.extractText(renderer["title"])
	}
	if artist == "" {
		artist = p.extractText(renderer["shortBylineText"])
		if artist == "" {
			artist = p.extractText(renderer["longBylineText"])
		}
		if artist == "" {
			artist = p.extractText(renderer["ownerText"])
		}
		if artist == "" {
			artist = p.extractText(renderer["subtitle"])
		}
	}

	// thumbnailOverlay / overlay — field can be a map or []interface{} depending on client/context.
	// musicTimeStatusRenderer stores the time under "title", not directly as text.
	if durationText == "" {
		var timeStatus map[string]interface{}

		// direct map paths
		if timeStatus == nil {
			timeStatus = safeMap(renderer, "thumbnailOverlay", "musicItemThumbnailOverlayRenderer", "timeStatus", "musicTimeStatusRenderer")
		}
		if timeStatus == nil {
			timeStatus = safeMap(renderer, "overlay", "musicItemThumbnailOverlayRenderer", "timeStatus", "musicTimeStatusRenderer")
		}
		if timeStatus == nil {
			timeStatus = safeMap(renderer, "overlay", "timeStatus", "musicTimeStatusRenderer")
		}

		// slice paths — WEB_REMIX returns these fields as arrays
		for _, field := range []string{"thumbnailOverlay", "overlay"} {
			if timeStatus != nil {
				break
			}
			for _, ov := range safeSlice(renderer, field) {
				ovm, ok := ov.(map[string]interface{})
				if !ok {
					continue
				}
				if ts := safeMap(ovm, "musicItemThumbnailOverlayRenderer", "timeStatus", "musicTimeStatusRenderer"); ts != nil {
					timeStatus = ts
					break
				}
			}
		}

		if timeStatus != nil {
			// time is stored under "title" in musicTimeStatusRenderer
			durationText = p.extractText(timeStatus["title"])
			if durationText == "" {
				durationText = p.extractText(timeStatus)
			}
		}
	}

	// one more check for lengths
	if durationText == "" {
		durationText = p.extractText(renderer["lengthText"])
	}
	if durationText == "" {
		durationText = p.extractText(renderer["lengthSeconds"])
	}

	// last resort from fixedColumns (common in album tracks)
	if durationText == "" {
		fixedCols := safeSlice(renderer, "fixedColumns")
		for _, fc := range fixedCols {
			if fcm, ok := fc.(map[string]interface{}); ok {
				fixedCol := safeMap(fcm, "musicResponsiveListItemFixedColumnRenderer")
				if fixedCol != nil {
					dt := p.extractText(fixedCol)
					if dt != "" && strings.Contains(dt, ":") {
						durationText = dt
					}
				}
			}
		}
	}

	if title == "" {
		title = "Unknown Title"
	}
	if artist == "" {
		artist = "Unknown Artist"
	}

	thumbnail := extractThumbnailURL(renderer, "thumbnail")

	return &pb.Track{
		Id:           ytNamespacedID(videoID),
		PlatformId:   videoID,
		Title:        title,
		Artist:       artist,
		AlbumTitle:   album,
		CoverUrl:     thumbnail,
		DurationMs:   parseDurationText(durationText),
		ExternalUrl:  "https://music.youtube.com/watch?v=" + videoID,
		IsStreamable: false,
		Source:       pb.Platform_PLATFORM_YOUTUBE,
	}
}

// parseVideoRenderer handles regular YouTube video search results.
func (p *YouTubeProvider) parseVideoRenderer(vr map[string]interface{}) *pb.Track {
	videoID := safeStr(vr, "videoId")
	if videoID == "" {
		return nil
	}

	title := extractRunsText(vr, "title")
	if title == "" {
		title = safeStr(vr, "title", "simpleText")
	}
	if title == "" {
		title = "Unknown Title"
	}

	artist := extractRunsText(vr, "ownerText")
	if artist == "" {
		artist = extractRunsText(vr, "shortBylineText")
	}
	if artist == "" {
		artist = "Unknown Artist"
	}

	durationText := safeStr(vr, "lengthText", "simpleText")
	if durationText == "" {
		durationText = extractRunsText(vr, "lengthText")
	}

	thumbnail := extractThumbnailURL(vr, "thumbnail")

	return &pb.Track{
		Id:           ytNamespacedID(videoID),
		PlatformId:   videoID,
		Title:        title,
		Artist:       artist,
		CoverUrl:     thumbnail,
		DurationMs:   parseDurationText(durationText),
		ExternalUrl:  "https://music.youtube.com/watch?v=" + videoID,
		IsStreamable: false,
		Source:       pb.Platform_PLATFORM_YOUTUBE,
	}
}

// player response parser

func (p *YouTubeProvider) parsePlayerTrack(data map[string]interface{}, videoID string) *pb.Track {
	details := safeMap(data, "videoDetails")

	// if videoDetails is missing try microformat as a last-resort metadata source
	if details == nil {
		mf := safeMap(data, "microformat", "playerMicroformatRenderer")
		if mf == nil {
			return nil
		}
		title := p.extractText(mf["title"])
		owner := safeStr(mf, "ownerChannelName")
		thumbnail := extractThumbnailURL(mf, "thumbnail")
		lengthSec := safeStr(mf, "lengthSeconds")
		dur, _ := strconv.Atoi(lengthSec)
		if title == "" {
			return nil
		}
		return &pb.Track{
			Id:           ytNamespacedID(videoID),
			PlatformId:   videoID,
			Title:        title,
			Artist:       owner,
			CoverUrl:     thumbnail,
			DurationMs:   int32(dur * 1000),
			ExternalUrl:  "https://music.youtube.com/watch?v=" + videoID,
			IsStreamable: false,
			Source:       pb.Platform_PLATFORM_YOUTUBE,
		}
	}

	title := safeStr(details, "title")
	if title == "" {
		title = "Unknown Title"
	}

	artist := safeStr(details, "author")
	if artist == "" {
		artist = "Unknown Artist"
	}

	durationSec := safeStr(details, "lengthSeconds")
	dur, _ := strconv.Atoi(durationSec)

	thumbnail := extractThumbnailURL(details, "thumbnail")

	return &pb.Track{
		Id:           ytNamespacedID(videoID),
		PlatformId:   videoID,
		Title:        title,
		Artist:       artist,
		CoverUrl:     thumbnail,
		DurationMs:   int32(dur * 1000),
		ExternalUrl:  "https://music.youtube.com/watch?v=" + videoID,
		IsStreamable: false,
		Source:       pb.Platform_PLATFORM_YOUTUBE,
	}
}

// provider interface methods

func (p *YouTubeProvider) SearchTracks(ctx context.Context, query string, limit int) ([]*pb.Track, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	params := ytParamsSongs
	data, err := p.client.Search(&query, &params, nil)
	if err != nil {
		log.Printf("[youtube] SearchTracks error: %v", err)
		return nil, fmt.Errorf("youtube: SearchTracks: %w", err)
	}

	tracks := p.parseSearchTracks(data, limit)
	log.Printf("[youtube] SearchTracks query=%q returned %d tracks", query, len(tracks))
	return tracks, nil
}

func (p *YouTubeProvider) SearchAlbums(_ context.Context, query string, limit int) ([]*pb.Album, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	// youtube music album search filter: EgWKAQIYAQ==
	albumParams := "EgWKAQIYAQ%3D%3D"
	data, err := p.client.Search(&query, &albumParams, nil)
	if err != nil {
		log.Printf("[youtube] SearchAlbums error: %v", err)
		return nil, fmt.Errorf("youtube: SearchAlbums: %w", err)
	}

	var albums []*pb.Album
	contents := p.findMusicShelfContents(data)
	for _, item := range contents {
		if len(albums) >= limit {
			break
		}
		im, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		album := p.parseMusicAlbumItem(im)
		if album != nil {
			albums = append(albums, album)
		}
	}
	log.Printf("[youtube] SearchAlbums query=%q returned %d albums", query, len(albums))
	return albums, nil
}

func (p *YouTubeProvider) parseMusicAlbumItem(item map[string]interface{}) *pb.Album {
	renderer := safeMap(item, "musicResponsiveListItemRenderer")
	if renderer == nil {
		return nil
	}

	// extract browseId for album
	browseID := ""
	cols := safeSlice(renderer, "flexColumns")
	if len(cols) > 0 {
		if cm, ok := cols[0].(map[string]interface{}); ok {
			runs := safeSlice(cm, "musicResponsiveListItemFlexColumnRenderer", "text", "runs")
			if len(runs) > 0 {
				if rm, ok := runs[0].(map[string]interface{}); ok {
					browseID = safeStr(rm, "navigationEndpoint", "browseEndpoint", "browseId")
				}
			}
		}
	}
	if browseID == "" {
		browseID = safeStr(renderer, "navigationEndpoint", "browseEndpoint", "browseId")
	}
	if browseID == "" {
		return nil
	}

	title := ""
	artist := ""
	for i, col := range cols {
		cm, ok := col.(map[string]interface{})
		if !ok {
			continue
		}
		flexCol := safeMap(cm, "musicResponsiveListItemFlexColumnRenderer")
		if flexCol == nil {
			continue
		}
		text := extractRunsText(flexCol, "text")
		switch i {
		case 0:
			title = text
		case 1:
			parts := strings.Split(text, " • ")
			if len(parts) >= 2 {
				artist = parts[len(parts)-1]
			} else {
				artist = text
			}
		}
	}

	thumbnail := extractThumbnailURL(renderer, "thumbnail")

	return &pb.Album{
		Id:          ytNamespacedID(browseID),
		PlatformId:  browseID,
		Title:       title,
		Artist:      artist,
		CoverUrl:    thumbnail,
		ExternalUrl: "https://music.youtube.com/browse/" + browseID,
		AlbumType:   "album",
		Source:      pb.Platform_PLATFORM_YOUTUBE,
	}
}

func (p *YouTubeProvider) SearchArtists(_ context.Context, query string, limit int) ([]*pb.Artist, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	// youtube music artist filter: EgWKAQIgAQ==
	artistParams := "EgWKAQIgAQ%3D%3D"
	data, err := p.client.Search(&query, &artistParams, nil)
	if err != nil {
		log.Printf("[youtube] SearchArtists error: %v", err)
		return nil, fmt.Errorf("youtube: SearchArtists: %w", err)
	}

	var artists []*pb.Artist
	contents := p.findMusicShelfContents(data)
	for _, item := range contents {
		if len(artists) >= limit {
			break
		}
		im, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		art := p.parseMusicArtistItem(im)
		if art != nil {
			artists = append(artists, art)
		}
	}
	log.Printf("[youtube] SearchArtists query=%q returned %d artists", query, len(artists))
	return artists, nil
}

func (p *YouTubeProvider) parseMusicArtistItem(item map[string]interface{}) *pb.Artist {
	renderer := safeMap(item, "musicResponsiveListItemRenderer")
	if renderer == nil {
		return nil
	}

	browseID := ""
	cols := safeSlice(renderer, "flexColumns")
	if len(cols) > 0 {
		if cm, ok := cols[0].(map[string]interface{}); ok {
			runs := safeSlice(cm, "musicResponsiveListItemFlexColumnRenderer", "text", "runs")
			if len(runs) > 0 {
				if rm, ok := runs[0].(map[string]interface{}); ok {
					browseID = safeStr(rm, "navigationEndpoint", "browseEndpoint", "browseId")
				}
			}
		}
	}
	if browseID == "" {
		browseID = safeStr(renderer, "navigationEndpoint", "browseEndpoint", "browseId")
	}
	if browseID == "" {
		return nil
	}

	name := ""
	if len(cols) > 0 {
		if cm, ok := cols[0].(map[string]interface{}); ok {
			flexCol := safeMap(cm, "musicResponsiveListItemFlexColumnRenderer")
			if flexCol != nil {
				name = extractRunsText(flexCol, "text")
			}
		}
	}
	if name == "" {
		name = "Unknown Artist"
	}

	thumbnail := extractThumbnailURL(renderer, "thumbnail")

	return &pb.Artist{
		Id:          ytNamespacedID(browseID),
		Name:        name,
		ImageUrl:    thumbnail,
		ExternalUrl: "https://music.youtube.com/channel/" + browseID,
		Source:      pb.Platform_PLATFORM_YOUTUBE,
	}
}

func (p *YouTubeProvider) SearchPlaylists(_ context.Context, query string, limit int) ([]*pb.Playlist, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	// youtube music playlist filter: EgWKAQIoAQ==
	playlistParams := "EgWKAQIoAQ%3D%3D"
	data, err := p.client.Search(&query, &playlistParams, nil)
	if err != nil {
		log.Printf("[youtube] SearchPlaylists error: %v", err)
		return nil, fmt.Errorf("youtube: SearchPlaylists: %w", err)
	}

	var playlists []*pb.Playlist
	contents := p.findMusicShelfContents(data)
	for _, item := range contents {
		if len(playlists) >= limit {
			break
		}
		im, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		pl := p.parseMusicPlaylistItem(im)
		if pl != nil {
			playlists = append(playlists, pl)
		}
	}
	log.Printf("[youtube] SearchPlaylists query=%q returned %d playlists", query, len(playlists))
	return playlists, nil
}

func (p *YouTubeProvider) parseMusicPlaylistItem(item map[string]interface{}) *pb.Playlist {
	renderer := safeMap(item, "musicResponsiveListItemRenderer")
	if renderer == nil {
		return nil
	}

	browseID := ""
	cols := safeSlice(renderer, "flexColumns")
	if len(cols) > 0 {
		if cm, ok := cols[0].(map[string]interface{}); ok {
			runs := safeSlice(cm, "musicResponsiveListItemFlexColumnRenderer", "text", "runs")
			if len(runs) > 0 {
				if rm, ok := runs[0].(map[string]interface{}); ok {
					browseID = safeStr(rm, "navigationEndpoint", "browseEndpoint", "browseId")
				}
			}
		}
	}
	if browseID == "" {
		browseID = safeStr(renderer, "navigationEndpoint", "browseEndpoint", "browseId")
	}
	if browseID == "" {
		return nil
	}

	title := ""
	owner := ""
	for i, col := range cols {
		cm, ok := col.(map[string]interface{})
		if !ok {
			continue
		}
		flexCol := safeMap(cm, "musicResponsiveListItemFlexColumnRenderer")
		if flexCol == nil {
			continue
		}
		text := extractRunsText(flexCol, "text")
		switch i {
		case 0:
			title = text
		case 1:
			parts := strings.Split(text, " • ")
			if len(parts) >= 2 {
				owner = parts[len(parts)-1]
			} else {
				owner = text
			}
		}
	}

	thumbnail := extractThumbnailURL(renderer, "thumbnail")

	return &pb.Playlist{
		Id:          ytNamespacedID(browseID),
		PlatformId:  browseID,
		Title:       title,
		Owner:       owner,
		CoverUrl:    thumbnail,
		ExternalUrl: "https://music.youtube.com/browse/" + browseID,
		Source:      pb.Platform_PLATFORM_YOUTUBE,
	}
}

// get single-item methods

func (p *YouTubeProvider) GetTrack(ctx context.Context, platformID string) (*pb.Track, error) {
	videoID := ytStripPrefix(platformID)

	// WEB_REMIX returns full videoDetails for metadata; try it first.
	data, err := p.client.Player(videoID)
	if err != nil {
		log.Printf("[youtube] GetTrack %s: WEB_REMIX player error: %v", videoID, err)
	} else {
		playability := safeStr(safeMap(data, "playabilityStatus"), "status")
		hasDetails := safeMap(data, "videoDetails") != nil
		hasMicroformat := safeMap(data, "microformat", "playerMicroformatRenderer") != nil
		log.Printf("[youtube] GetTrack %s: WEB_REMIX playability=%s videoDetails=%v microformat=%v",
			videoID, playability, hasDetails, hasMicroformat)
		track := p.parsePlayerTrack(data, videoID)
		if track != nil {
			return track, nil
		}
		log.Printf("[youtube] GetTrack %s: WEB_REMIX parsePlayerTrack returned nil, trying stream clients", videoID)
	}

	// fallback: try stream pool clients which use different auth contexts
	for _, sc := range p.streamPool {
		data, err := sc.player(videoID)
		if err != nil {
			log.Printf("[youtube] GetTrack %s: %s player error: %v", videoID, sc.name, err)
			continue
		}
		track := p.parsePlayerTrack(data, videoID)
		if track != nil {
			log.Printf("[youtube] GetTrack %s: resolved via %s", videoID, sc.name)
			return track, nil
		}
		log.Printf("[youtube] GetTrack %s: %s returned no videoDetails (playability=%s)",
			videoID, sc.name, safeStr(safeMap(data, "playabilityStatus"), "status"))
	}

	return nil, fmt.Errorf("youtube: GetTrack %s: %w", videoID, ErrYTNotFound)
}

func (p *YouTubeProvider) GetAlbum(_ context.Context, platformID string) (*pb.Album, []*pb.Track, error) {
	browseID := ytStripPrefix(platformID)

	data, err := p.client.Browse(&browseID, nil, nil)
	if err != nil {
		log.Printf("[youtube] GetAlbum %s error: %v", browseID, err)
		return nil, nil, fmt.Errorf("youtube: GetAlbum %s: %w", browseID, err)
	}

	// parse album header
	header := safeMap(data, "header", "musicImmersiveHeaderRenderer")
	if header == nil {
		header = safeMap(data, "header", "musicDetailHeaderRenderer")
	}
	if header == nil {
		header = safeMap(data, "header", "musicEditablePlaylistDetailHeaderRenderer")
	}

	title := ""
	artist := ""
	thumbnail := ""

	if header != nil {
		title = p.extractText(header["title"])
		artist = p.extractText(header["subtitle"])
		if artist == "" {
			artist = p.extractText(header["shortBylineText"])
		}
		thumbnail = extractThumbnailURL(header, "thumbnail")
		if thumbnail == "" {
			thumbnail = extractThumbnailURL(header, "menu")
		}
	}

	if title == "" {
		// try microformat for title
		title = safeStr(data, "microformat", "microformatDataRenderer", "title")
	}
	if artist == "" {
		// try microformat for artist
		artist = safeStr(data, "microformat", "microformatDataRenderer", "pageOwner")
	}
	if artist == "" {
		// try description for artist parsing? "Album by ..."
		desc := safeStr(data, "microformat", "microformatDataRenderer", "description")
		if strings.Contains(desc, "Album by ") {
			parts := strings.Split(desc, "Album by ")
			if len(parts) > 1 {
				artist = parts[1]
			}
		}
	}
	if title == "" {
		title = browseID
	}

	album := &pb.Album{
		Id:          ytNamespacedID(browseID),
		PlatformId:  browseID,
		Title:       title,
		Artist:      artist,
		CoverUrl:    thumbnail,
		ExternalUrl: "https://music.youtube.com/browse/" + browseID,
		AlbumType:   "album",
		Source:      pb.Platform_PLATFORM_YOUTUBE,
	}

	// parse tracks from shelf
	var tracks []*pb.Track
	items := p.findItems(data["contents"])
	for _, item := range items {
		if im, ok := item.(map[string]interface{}); ok {
			t := p.parseMusicListItem(im)
			if t != nil {
				tracks = append(tracks, t)
			}
		}
	}

	album.TotalTracks = int32(len(tracks))
	return album, tracks, nil
}

func (p *YouTubeProvider) GetArtist(ctx context.Context, platformID string) (*pb.Artist, []*pb.Track, []*pb.Album, error) {
	browseID := ytStripPrefix(platformID)

	data, err := p.client.Browse(&browseID, nil, nil)
	if err != nil {
		log.Printf("[youtube] GetArtist %s error: %v", browseID, err)
		return nil, nil, nil, fmt.Errorf("youtube: GetArtist %s: %w", browseID, err)
	}

	// parse artist header
	header := safeMap(data, "header", "musicImmersiveHeaderRenderer")
	if header == nil {
		header = safeMap(data, "header", "musicVisualHeaderRenderer")
	}

	name := ""
	thumbnail := ""
	if header != nil {
		name = extractRunsText(header, "title")
		thumbnail = extractThumbnailURL(header, "thumbnail")
	}
	if name == "" {
		name = browseID
	}

	artist := &pb.Artist{
		Id:          ytNamespacedID(browseID),
		Name:        name,
		ImageUrl:    thumbnail,
		ExternalUrl: "https://music.youtube.com/channel/" + browseID,
		Source:      pb.Platform_PLATFORM_YOUTUBE,
	}

	// parse top tracks and albums from content shelves
	var topTracks []*pb.Track
	var albums []*pb.Album

	sections := safeSlice(data, "contents", "singleColumnBrowseResultsRenderer", "tabs")
	if len(sections) > 0 {
		if tm, ok := sections[0].(map[string]interface{}); ok {
			shelfContents := safeSlice(tm, "tabRenderer", "content", "sectionListRenderer", "contents")
			for _, sec := range shelfContents {
				if sm, ok := sec.(map[string]interface{}); ok {
					shelf := safeMap(sm, "musicShelfRenderer")
					if shelf != nil && len(topTracks) == 0 {
						items := safeSlice(shelf, "contents")
						for _, item := range items {
							if im, ok := item.(map[string]interface{}); ok {
								t := p.parseMusicListItem(im)
								if t != nil {
									topTracks = append(topTracks, t)
								}
							}
						}
					}
					// carousel for albums
					carousel := safeMap(sm, "musicCarouselShelfRenderer")
					if carousel != nil {
						items := safeSlice(carousel, "contents")
						for _, item := range items {
							if im, ok := item.(map[string]interface{}); ok {
								alb := p.parseCarouselAlbum(im)
								if alb != nil {
									albums = append(albums, alb)
								}
							}
						}
					}
				}
			}
		}
	}

	// artist top tracks don't include duration in the browse response.
	// one MusicGetQueue call fills in the missing lengthText for all tracks at once.
	if len(topTracks) > 0 {
		topTracks = p.enrichTracksWithDuration(ctx, topTracks)
	}

	return artist, topTracks, albums, nil
}

// enrichTracksWithDuration uses MusicGetQueue to backfill duration for tracks
// that came from a shelf that doesn't include lengthText (e.g. artist top tracks).
func (p *YouTubeProvider) enrichTracksWithDuration(ctx context.Context, tracks []*pb.Track) []*pb.Track {
	videoIDs := make([]string, len(tracks))
	for i, t := range tracks {
		videoIDs[i] = t.PlatformId
	}

	queueData, err := p.client.MusicGetQueue(&videoIDs, nil)
	if err != nil {
		log.Printf("[youtube] enrichTracksWithDuration MusicGetQueue error: %v", err)
		return tracks
	}

	// path: queueDatas[].content.playlistPanelVideoRenderer.{videoId, lengthText}
	durations := make(map[string]int32, len(videoIDs))
	for _, entry := range safeSlice(queueData, "queueDatas") {
		em, ok := entry.(map[string]interface{})
		if !ok {
			continue
		}
		renderer := safeMap(em, "content", "playlistPanelVideoRenderer")
		if renderer == nil {
			continue
		}
		vid := safeStr(renderer, "videoId")
		if vid == "" {
			continue
		}
		durText := extractRunsText(renderer, "lengthText")
		if durText == "" {
			durText = safeStr(renderer, "lengthText", "simpleText")
		}
		if ms := parseDurationText(durText); ms > 0 {
			durations[vid] = ms
		}
	}

	for _, t := range tracks {
		if t.DurationMs == 0 {
			if ms, ok := durations[t.PlatformId]; ok {
				t.DurationMs = ms
			}
		}
	}
	return tracks
}

func (p *YouTubeProvider) parseCarouselAlbum(item map[string]interface{}) *pb.Album {
	renderer := safeMap(item, "musicTwoRowItemRenderer")
	if renderer == nil {
		return nil
	}

	browseID := safeStr(renderer, "navigationEndpoint", "browseEndpoint", "browseId")
	if browseID == "" {
		return nil
	}

	title := extractRunsText(renderer, "title")
	subtitle := extractRunsText(renderer, "subtitle")
	thumbnail := extractThumbnailURL(renderer, "thumbnailRenderer")
	if thumbnail == "" {
		thumbNav := safeMap(renderer, "thumbnailRenderer", "musicThumbnailRenderer")
		if thumbNav != nil {
			thumbnail = extractThumbnailURL(thumbNav, "thumbnail")
		}
	}

	return &pb.Album{
		Id:          ytNamespacedID(browseID),
		PlatformId:  browseID,
		Title:       title,
		Artist:      subtitle,
		CoverUrl:    thumbnail,
		ExternalUrl: "https://music.youtube.com/browse/" + browseID,
		AlbumType:   "album",
		Source:      pb.Platform_PLATFORM_YOUTUBE,
	}
}

func (p *YouTubeProvider) GetPlaylist(_ context.Context, platformID string) (*pb.Playlist, []*pb.Track, error) {
	browseID := ytStripPrefix(platformID)

	data, err := p.client.Browse(&browseID, nil, nil)
	if err != nil {
		log.Printf("[youtube] GetPlaylist %s error: %v", browseID, err)
		return nil, nil, fmt.Errorf("youtube: GetPlaylist %s: %w", browseID, err)
	}

	header := safeMap(data, "header", "musicDetailHeaderRenderer")
	if header == nil {
		header = safeMap(data, "header", "musicImmersiveHeaderRenderer")
	}
	if header == nil {
		header = safeMap(data, "header", "musicEditablePlaylistDetailHeaderRenderer")
	}

	title := ""
	owner := ""
	thumbnail := ""
	if header != nil {
		title = p.extractText(header["title"])
		owner = p.extractText(header["subtitle"])
		if owner == "" {
			owner = p.extractText(header["ownerText"])
		}
		if owner == "" {
			owner = p.extractText(header["shortBylineText"])
		}
		thumbnail = extractThumbnailURL(header, "thumbnail")
	}

	if title == "" {
		// try browsing the whole page for a title if header failed
		title = p.extractText(safeMap(data, "header", "musicImmersiveHeaderRenderer", "title"))
		if title == "" {
			title = "YouTube Playlist"
		}
	}

	playlist := &pb.Playlist{
		Id:          ytNamespacedID(browseID),
		PlatformId:  browseID,
		Title:       title,
		Owner:       owner,
		CoverUrl:    thumbnail,
		ExternalUrl: "https://music.youtube.com/browse/" + browseID,
		Source:      pb.Platform_PLATFORM_YOUTUBE,
	}

	// parse tracks
	var tracks []*pb.Track
	items := p.findItems(data["contents"])
	for _, item := range items {
		if im, ok := item.(map[string]interface{}); ok {
			t := p.parseMusicListItem(im)
			if t != nil {
				tracks = append(tracks, t)
			}
		}
	}

	playlist.TotalTracks = int32(len(tracks))
	return playlist, tracks, nil
}

// itagFromFormat extracts the integer itag value from an InnerTube format map.
func itagFromFormat(fm map[string]interface{}) int {
	switch v := fm["itag"].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n, _ := strconv.Atoi(v)
		return n
	}
	return 0
}

// extractBestAudioURL returns the highest-quality direct audio URL from a
// player response.  Formats that only carry a signatureCipher are skipped
// because Go has no access to YouTube's obfuscated JS player.
//
// itag priority (audio-only first, then muxed fallbacks):
//
//	251 – opus 160 kbps   140 – m4a AAC 128 kbps
//	250 – opus  70 kbps   249 – opus  50 kbps
//	141 – m4a AAC 256 kbps  256/258 – m4a high-res
//	 18 – mp4 360p muxed    22 – mp4 720p muxed
func extractBestAudioURL(data map[string]interface{}) (url string, itag int) {
	sd := safeMap(data, "streamingData")
	if sd == nil {
		return "", 0
	}

	adaptive := safeSlice(sd, "adaptiveFormats")
	muxed := safeSlice(sd, "formats")
	all := append(adaptive, muxed...)

	// index only formats that carry a direct url (skip cipher-only).
	byItag := make(map[int]string, len(all))
	for _, f := range all {
		fm, ok := f.(map[string]interface{})
		if !ok {
			continue
		}
		u := safeStr(fm, "url")
		if u == "" {
			continue
		}
		if t := itagFromFormat(fm); t > 0 {
			byItag[t] = u
		}
	}

	for _, want := range []int{251, 140, 250, 249, 141, 256, 258, 18, 22} {
		if u, ok := byItag[want]; ok {
			return u, want
		}
	}
	// fallback: any direct url.
	for t, u := range byItag {
		return u, t
	}
	return "", 0
}

// getstreamurl resolves a direct audio playback url for a youtube video using
// a multi-client fallback chain. each client in the pool is tried in order;
// the first one that returns at least one format with a direct (non-ciphered)
// url wins.
//
// fallback chain: TVHTML5_SIMPLY_EMBEDDED_PLAYER, TVHTML5, ANDROID_VR (1.43 / 1.61), ANDROID, IOS, WEB
func (p *YouTubeProvider) GetStreamURL(ctx context.Context, platformID string, _ string) (*pb.GetStreamURLResponse, error) {
	videoID := ytStripPrefix(platformID)

	for _, sc := range p.streamPool {
		data, err := sc.player(videoID)
		if err != nil {
			log.Printf("[youtube] GetStreamURL %s: %s player error: %v", videoID, sc.name, err)
			continue
		}

		sd := safeMap(data, "streamingData")
		adaptive := safeSlice(data, "streamingData", "adaptiveFormats")
		muxed := safeSlice(data, "streamingData", "formats")
		directCount := 0
		cipherCount := 0
		for _, f := range append(adaptive, muxed...) {
			if fm, ok := f.(map[string]interface{}); ok {
				if safeStr(fm, "url") != "" {
					directCount++
				} else if safeStr(fm, "signatureCipher") != "" || safeStr(fm, "cipher") != "" {
					cipherCount++
				}
			}
		}
		log.Printf("[youtube] GetStreamURL %s: %s streamingData=%v adaptiveFormats=%d formats=%d direct=%d cipher=%d",
			videoID, sc.name, sd != nil, len(adaptive), len(muxed), directCount, cipherCount)

		u, itag := extractBestAudioURL(data)
		if u != "" {
			log.Printf("[youtube] GetStreamURL %s: resolved via %s (itag %d)", videoID, sc.name, itag)
			return &pb.GetStreamURLResponse{
				StreamUrl: u,
				Source:    pb.Platform_PLATFORM_YOUTUBE,
				Status:    pb.ResponseStatus_STATUS_OK,
			}, nil
		}
		log.Printf("[youtube] GetStreamURL %s: %s returned no direct URL, trying next client", videoID, sc.name)
	}

	return nil, ErrYTNoStream
}

// getsimilartracks uses the "Next" endpoint with a ytm radio playlist (RDAMVM{id}) to get a full queue of related tracks without a playlistId.
func (p *YouTubeProvider) GetSimilarTracks(ctx context.Context, platformID string, limit int) ([]*pb.Track, error) {
	videoID := ytStripPrefix(platformID)
	if limit <= 0 {
		limit = 20
	}

	// rdamvm{videoId} is the youtube music auto-radio mix based on a track and populates the up-next queue.
	radioID := "RDAMVM" + videoID
	data, err := p.client.Next(&videoID, &radioID, nil, nil, nil)
	if err != nil {
		log.Printf("[youtube] GetSimilarTracks %s error: %v", videoID, err)
		return nil, fmt.Errorf("youtube: GetSimilarTracks %s: %w", videoID, err)
	}

	tabs := safeSlice(data, "contents", "singleColumnMusicWatchNextResultsRenderer",
		"tabbedRenderer", "watchNextTabbedResultsRenderer", "tabs")

	var tracks []*pb.Track
	for _, tab := range tabs {
		tm, ok := tab.(map[string]interface{})
		if !ok {
			continue
		}
		queue := safeMap(tm, "tabRenderer", "content", "musicQueueRenderer", "content", "playlistPanelRenderer")
		if queue == nil {
			continue
		}
		for _, item := range safeSlice(queue, "contents") {
			if len(tracks) >= limit {
				break
			}
			im, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			renderer := safeMap(im, "playlistPanelVideoRenderer")
			if renderer == nil {
				continue
			}
			vid := safeStr(renderer, "videoId")
			if vid == "" || vid == videoID {
				continue
			}
			title := extractRunsText(renderer, "title")
			artist := extractRunsText(renderer, "shortBylineText")
			if artist == "" {
				artist = extractRunsText(renderer, "longBylineText")
			}
			durText := safeStr(renderer, "lengthText", "simpleText")
			if durText == "" {
				durText = extractRunsText(renderer, "lengthText")
			}
			tracks = append(tracks, &pb.Track{
				Id:          ytNamespacedID(vid),
				PlatformId:  vid,
				Title:       title,
				Artist:      artist,
				CoverUrl:    extractThumbnailURL(renderer, "thumbnail"),
				DurationMs:  parseDurationText(durText),
				ExternalUrl: "https://music.youtube.com/watch?v=" + vid,
				Source:      pb.Platform_PLATFORM_YOUTUBE,
			})
		}
	}

	log.Printf("[youtube] GetSimilarTracks %s returned %d tracks", videoID, len(tracks))
	return tracks, nil
}
