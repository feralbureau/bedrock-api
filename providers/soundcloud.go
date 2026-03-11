package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	pb "example/grpc/bedrock"
)

var (
	ErrSCNotFound   = errors.New("soundcloud: not found (404)")
	ErrSCAuth       = errors.New("soundcloud: auth error (401/403)")
	ErrSCNoClientID = errors.New("soundcloud: no client_id configured")
	ErrSCNoStream   = errors.New("soundcloud: no streamable transcoding found")
)

const (
	scAPIV2Base   = "https://api-v2.soundcloud.com"
	scUserAgent   = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	scAppVersion  = "1702458641"
	scAppLocale   = "en"
	scHTTPTimeout = 10 * time.Second
	scBatchSize   = 30 // max ids per /tracks?ids= call
)

type scUser struct {
	ID             int64  `json:"id"`
	Username       string `json:"username"`
	FullName       string `json:"full_name"`
	AvatarURL      string `json:"avatar_url"`
	PermalinkURL   string `json:"permalink_url"`
	FollowersCount int64  `json:"followers_count"`
	TrackCount     int    `json:"track_count"`
	Description    string `json:"description"`
}

type scFormat struct {
	Protocol string `json:"protocol"` // "progressive" | "hls"
	MimeType string `json:"mime_type"`
}

type scTranscoding struct {
	URL      string   `json:"url"`
	Preset   string   `json:"preset"`   // e.g. "mp3_0_0", "opus_0_0"
	Duration int64    `json:"duration"` // ms
	Format   scFormat `json:"format"`
	Quality  string   `json:"quality"`
}

type scMedia struct {
	Transcodings []scTranscoding `json:"transcodings"`
}

type scTrack struct {
	ID                int64   `json:"id"`
	Title             string  `json:"title"`
	PermalinkURL      string  `json:"permalink_url"`
	ArtworkURL        string  `json:"artwork_url"`
	Duration          int32   `json:"duration"`       // ms, may be preview-clipped
	FullDuration      int32   `json:"full_duration"`  // ms, always full length
	Genre             string  `json:"genre"`
	Streamable        bool    `json:"streamable"`
	Public            bool    `json:"public"`
	Policy            string  `json:"policy"` // "ALLOW" | "SNIP" | "BLOCK" | "MONETIZE"
	MonetizationModel string  `json:"monetization_model"`
	User              scUser  `json:"user"`
	Media             scMedia `json:"media"`
}

type scSearchResult struct {
	Collection   []scTrack `json:"collection"`
	NextHref     string    `json:"next_href"`
	TotalResults int       `json:"total_results"`
}

type scUserSearchResult struct {
	Collection []scUser `json:"collection"`
	NextHref   string   `json:"next_href"`
}

type scPlaylistSearchResult struct {
	Collection []scPlaylist `json:"collection"`
	NextHref   string       `json:"next_href"`
}

type scPlaylist struct {
	ID           int64     `json:"id"`
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	ArtworkURL   string    `json:"artwork_url"`
	PermalinkURL string    `json:"permalink_url"`
	TrackCount   int32     `json:"track_count"`
	IsAlbum      bool      `json:"is_album"`
	PlaylistType string    `json:"playlist_type"` // "album" | ""
	User         scUser    `json:"user"`
	Tracks       []scTrack `json:"tracks"` // first ~5 full objects, rest are id-only stubs
}

type scStreamResponse struct {
	URL string `json:"url"`
}

// related endpoint returns either a collection wrapper or a bare array
type scRelatedResult struct {
	Collection []scTrack `json:"collection"`
}

// clientIDManager rotates through SOUNDCLOUD_CLIENT_IDS (comma-separated env var).
// thread-safe via mu.
type clientIDManager struct {
	mu      sync.RWMutex
	all     []string // never mutated after init
	working string   // "" means needs re-probe
}

// scFallbackClientIDs is a compile-time seed list used when
var scFallbackClientIDs = []string{
	"1IzwHiVxAHeYKAMqN0IIGD3ZARgJy2kl",
}

func newClientIDManager() *clientIDManager {
	raw := os.Getenv("SOUNDCLOUD_CLIENT_IDS")
	var ids []string
	for _, s := range strings.Split(raw, ",") {
		if t := strings.TrimSpace(s); t != "" {
			ids = append(ids, t)
		}
	}
	// without appearing twice if someone also puts them in the env var.
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		seen[id] = struct{}{}
	}
	for _, id := range scFallbackClientIDs {
		if _, dup := seen[id]; !dup {
			ids = append(ids, id)
		}
	}
	return &clientIDManager{all: ids}
}

func (m *clientIDManager) get(ctx context.Context, client *http.Client) (string, error) {
	m.mu.RLock()
	if m.working != "" {
		id := m.working
		m.mu.RUnlock()
		return id, nil
	}
	m.mu.RUnlock()

	// write lock to avoid thundering herd on cold start
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.working != "" {
		return m.working, nil
	}
	if len(m.all) == 0 {
		return "", ErrSCNoClientID
	}

	for _, id := range m.all {
		if m.probe(ctx, client, id) {
			m.working = id
			   log.Printf("[soundcloud] client_id validated: %.8s...", id)
			return id, nil
		}
	}
	return "", ErrSCNoClientID
}

func (m *clientIDManager) invalidate() {
	m.mu.Lock()
	m.working = ""
	m.mu.Unlock()
	log.Printf("[soundcloud] client_id invalidated, will re-probe")
}

// probe fires a cheap /search/tracks?q=a&limit=1 to validate a key.
// inside get()).
func (m *clientIDManager) probe(ctx context.Context, client *http.Client, id string) bool {
	params := url.Values{
		"q":           {"a"},
		"limit":       {"1"},
		"client_id":   {id},
		"app_version": {scAppVersion},
		"app_locale":  {scAppLocale},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		scAPIV2Base+"/search/tracks?"+params.Encode(), nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", scUserAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	return resp.StatusCode == http.StatusOK
}

type SoundCloudProvider struct {
	// api client for all json api calls
	apiClient *http.Client

	// stream client for transcoding resolution, follows redirects
	streamClient *http.Client

	ids *clientIDManager
}

func NewSoundCloudProvider() *SoundCloudProvider {
	return &SoundCloudProvider{
		apiClient: &http.Client{
			Timeout: scHTTPTimeout,
			// log and handle 3xx responses explicitly.
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		streamClient: &http.Client{
			// nodes, etc.).  10 s is ample for a URL-resolution round-trip.
			Timeout: scHTTPTimeout,
		},
		ids: newClientIDManager(),
	}
}

func (p *SoundCloudProvider) Platform() pb.Platform {
	return pb.Platform_PLATFORM_SOUNDCLOUD
}

// doRequest is the single authenticated GET path for all api-v2 calls.
// (e.g. vanity URLs resolving to numeric IDs) work transparently.
func (p *SoundCloudProvider) doRequest(ctx context.Context, endpoint string, extra url.Values) (*http.Response, error) {
	const maxAttempts = 2

	for attempt := 0; attempt < maxAttempts; attempt++ {
		clientID, err := p.ids.get(ctx, p.apiClient)
		if err != nil {
			return nil, fmt.Errorf("soundcloud: %w", err)
		}

		params := url.Values{
			"client_id":   {clientID},
			"app_version": {scAppVersion},
			"app_locale":  {scAppLocale},
		}
		for k, vs := range extra {
			params[k] = vs
		}

		sep := "?"
		if strings.Contains(endpoint, "?") {
			sep = "&"
		}
		rawURL := endpoint + sep + params.Encode()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, fmt.Errorf("soundcloud: build request: %w", err)
		}
		req.Header.Set("User-Agent", scUserAgent)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Referer", "https://soundcloud.com/")

		resp, err := p.apiClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("soundcloud: http: %w", err)
		}

		switch {
		case resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated:
			return resp, nil

		case resp.StatusCode == http.StatusNotFound:
			resp.Body.Close()
			return nil, ErrSCNotFound

		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			resp.Body.Close()
			p.ids.invalidate()
			if attempt < maxAttempts-1 {
				continue
			}
			return nil, ErrSCAuth

		case resp.StatusCode >= 300 && resp.StatusCode < 400:
			location := resp.Header.Get("Location")
			resp.Body.Close()
			if location == "" {
				return nil, fmt.Errorf("soundcloud: HTTP %d with no Location header", resp.StatusCode)
			}
			endpoint = location
			extra = nil
			continue

		default:
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			return nil, fmt.Errorf("soundcloud: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
	}

	return nil, ErrSCAuth
}

// resolveTranscoding fetches a SoundCloud transcoding URL and returns the
// final playable stream URL inside it.
//
//
//  1. Transcoding URLs come pre-signed with a Track-Authorization token in
//     their query string.  doRequest always appends app_version & app_locale
//     which can invalidate that signature on some CDN configurations.
//     Here we append only client_id, which SoundCloud requires, and leave
//     everything else untouched.
//
//  2. The JSON response from the transcoding endpoint contains a `url` field
//     that points to the real CDN.  That CDN URL may itself redirect through
//     HTTPв†’HTTPS or edge-node hops.  We use streamClient (redirects allowed)
//     so we return the fully-resolved final URL to the caller.
func (p *SoundCloudProvider) resolveTranscoding(ctx context.Context, transcodingURL string) (string, error) {
	clientID, err := p.ids.get(ctx, p.apiClient)
	if err != nil {
		return "", fmt.Errorf("soundcloud: resolveTranscoding: %w", err)
	}

	// corrupt the pre-signed Track-Authorization token already on the URL.
	sep := "?"
	if strings.Contains(transcodingURL, "?") {
		sep = "&"
	}
	resolveURL := transcodingURL + sep + url.Values{"client_id": {clientID}}.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, resolveURL, nil)
	if err != nil {
		return "", fmt.Errorf("soundcloud: resolveTranscoding build request: %w", err)
	}
	req.Header.Set("User-Agent", scUserAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Referer", "https://soundcloud.com/")

	// returns JSON, not a redirect.
	resp, err := p.apiClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("soundcloud: resolveTranscoding http: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		// expected, fall through to decode
	case http.StatusNotFound:
		resp.Body.Close()
		return "", ErrSCNotFound
	case http.StatusUnauthorized, http.StatusForbidden:
		resp.Body.Close()
		p.ids.invalidate()
		return "", ErrSCAuth
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		resp.Body.Close()
		return "", fmt.Errorf("soundcloud: resolveTranscoding HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var sr scStreamResponse
	if err := decodeJSON(resp, &sr); err != nil {
		return "", err
	}
	if sr.URL == "" {
		return "", fmt.Errorf("soundcloud: resolveTranscoding: empty url in response")
	}
	return sr.URL, nil
}

func decodeJSON(resp *http.Response, dst any) error {
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("soundcloud: json decode: %w", err)
	}
	return nil
}

// replace any known sc size marker with requested size, fallback to raw
func artworkURL(raw, size string) string {
	if raw == "" {
		return ""
	}
	if size == "" {
		size = "t500x500"
	}
	for _, m := range []string{"badge", "tiny", "small", "t67x67", "mini", "t120x120", "large", "t300x300", "crop"} {
		if strings.Contains(raw, "-"+m+".") {
			return strings.Replace(raw, "-"+m+".", "-"+size+".", 1)
		}
	}
	return raw
}

// prefer track artwork, fallback to user avatar
func coverFromTrack(t *scTrack) string {
	if t.ArtworkURL != "" {
		return artworkURL(t.ArtworkURL, "t500x500")
	}
	if t.User.AvatarURL != "" {
		return artworkURL(t.User.AvatarURL, "t500x500")
	}
	return ""
}

func namespacedID(id int64) string {
	return "soundcloud:" + strconv.FormatInt(id, 10)
}

func mapTrack(t *scTrack) *pb.Track {
	artist := t.User.Username
	if artist == "" {
		artist = "Unknown Artist"
	}
	title := t.Title
	if title == "" {
		title = "Unknown Title"
	}

	hasProgressive := false
	for i := range t.Media.Transcodings {
		if t.Media.Transcodings[i].Format.Protocol == "progressive" && t.Media.Transcodings[i].URL != "" {
			hasProgressive = true
			break
		}
	}

	return &pb.Track{
		Id:           namespacedID(t.ID),
		PlatformId:   strconv.FormatInt(t.ID, 10),
		Title:        title,
		Artist:       artist,
		CoverUrl:     coverFromTrack(t),
		DurationMs:   t.Duration,
		ExternalUrl:  t.PermalinkURL,
		Genre:        t.Genre,
		IsStreamable: hasProgressive || t.Streamable,
		Source:       pb.Platform_PLATFORM_SOUNDCLOUD,
	}
}

func mapUser(u *scUser) *pb.Artist {
	name := u.Username
	if name == "" {
		name = u.FullName
	}
	return &pb.Artist{
		Id:          namespacedID(u.ID),
		Name:        name,
		ImageUrl:    artworkURL(u.AvatarURL, "t500x500"),
		Followers:   u.FollowersCount,
		ExternalUrl: u.PermalinkURL,
		Source:      pb.Platform_PLATFORM_SOUNDCLOUD,
	}
}

func mapPlaylist(pl *scPlaylist) *pb.Playlist {
	owner := pl.User.Username
	if owner == "" {
		owner = pl.User.FullName
	}
	return &pb.Playlist{
		Id:          namespacedID(pl.ID),
		PlatformId:  strconv.FormatInt(pl.ID, 10),
		Title:       pl.Title,
		Description: pl.Description,
		CoverUrl:    artworkURL(pl.ArtworkURL, "t500x500"),
		TotalTracks: pl.TrackCount,
		Owner:       owner,
		ExternalUrl: pl.PermalinkURL,
		Source:      pb.Platform_PLATFORM_SOUNDCLOUD,
	}
}

func mapPlaylistToAlbum(pl *scPlaylist) *pb.Album {
	artist := pl.User.Username
	if artist == "" {
		artist = pl.User.FullName
	}
	albumType := "album"
	if pl.PlaylistType != "" && pl.PlaylistType != "album" {
		albumType = pl.PlaylistType
	}
	return &pb.Album{
		Id:          namespacedID(pl.ID),
		PlatformId:  strconv.FormatInt(pl.ID, 10),
		Title:       pl.Title,
		Artist:      artist,
		CoverUrl:    artworkURL(pl.ArtworkURL, "t500x500"),
		TotalTracks: pl.TrackCount,
		ExternalUrl: pl.PermalinkURL,
		AlbumType:   albumType,
		Source:      pb.Platform_PLATFORM_SOUNDCLOUD,
	}
}

// fetch a single track using v2 /tracks?ids= endpoint, index 0. empty array means not found (errscnotfound)
func (p *SoundCloudProvider) fetchTrackByID(ctx context.Context, nativeID string) (*scTrack, error) {
	resp, err := p.doRequest(ctx, scAPIV2Base+"/tracks", url.Values{"ids": {nativeID}})
	if err != nil {
		return nil, err
	}

	var tracks []scTrack
	if err := decodeJSON(resp, &tracks); err != nil {
		return nil, err
	}
	if len(tracks) == 0 {
		return nil, ErrSCNotFound
	}
	return &tracks[0], nil
}

func (p *SoundCloudProvider) SearchTracks(ctx context.Context, query string, limit int) ([]*pb.Track, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	resp, err := p.doRequest(ctx, scAPIV2Base+"/search/tracks", url.Values{
		"q":      {query},
		"limit":  {strconv.Itoa(limit)},
		"offset": {"0"},
	})
	if err != nil {
		return nil, fmt.Errorf("SearchTracks: %w", err)
	}

	var result scSearchResult
	if err := decodeJSON(resp, &result); err != nil {
		return nil, err
	}

	tracks := make([]*pb.Track, 0, len(result.Collection))
	for i := range result.Collection {
		tracks = append(tracks, mapTrack(&result.Collection[i]))
	}
	return tracks, nil
}

// search albums hits /search/albums; falls back to /search/playlists on 404.
// heuristic: playlist_type == "album" OR (no type AND track_count <= 30)
func (p *SoundCloudProvider) SearchAlbums(ctx context.Context, query string, limit int) ([]*pb.Album, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	params := url.Values{
		"q":      {query},
		"limit":  {strconv.Itoa(limit)},
		"offset": {"0"},
	}

	resp, err := p.doRequest(ctx, scAPIV2Base+"/search/albums", params)
	if errors.Is(err, ErrSCNotFound) {
		resp, err = p.doRequest(ctx, scAPIV2Base+"/search/playlists", params)
	}
	if err != nil {
		return nil, fmt.Errorf("SearchAlbums: %w", err)
	}

	var result scPlaylistSearchResult
	if err := decodeJSON(resp, &result); err != nil {
		return nil, err
	}

	albums := make([]*pb.Album, 0, len(result.Collection))
	for i := range result.Collection {
		pl := &result.Collection[i]
		isAlbum := pl.PlaylistType == "album" ||
			(pl.PlaylistType == "" && pl.TrackCount > 0 && pl.TrackCount <= 30)
		if isAlbum {
			albums = append(albums, mapPlaylistToAlbum(pl))
		}
	}
	return albums, nil
}

func (p *SoundCloudProvider) SearchArtists(ctx context.Context, query string, limit int) ([]*pb.Artist, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	resp, err := p.doRequest(ctx, scAPIV2Base+"/search/users", url.Values{
		"q":      {query},
		"limit":  {strconv.Itoa(limit)},
		"offset": {"0"},
	})
	if err != nil {
		return nil, fmt.Errorf("SearchArtists: %w", err)
	}

	var result scUserSearchResult
	if err := decodeJSON(resp, &result); err != nil {
		return nil, err
	}

	artists := make([]*pb.Artist, 0, len(result.Collection))
	for i := range result.Collection {
		artists = append(artists, mapUser(&result.Collection[i]))
	}
	return artists, nil
}

// search playlists excludes album-type sets from results
func (p *SoundCloudProvider) SearchPlaylists(ctx context.Context, query string, limit int) ([]*pb.Playlist, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	resp, err := p.doRequest(ctx, scAPIV2Base+"/search/playlists", url.Values{
		"q":      {query},
		"limit":  {strconv.Itoa(limit)},
		"offset": {"0"},
	})
	if err != nil {
		return nil, fmt.Errorf("SearchPlaylists: %w", err)
	}

	var result scPlaylistSearchResult
	if err := decodeJSON(resp, &result); err != nil {
		return nil, err
	}

	playlists := make([]*pb.Playlist, 0, len(result.Collection))
	for i := range result.Collection {
		pl := &result.Collection[i]
		if pl.PlaylistType != "album" {
			playlists = append(playlists, mapPlaylist(pl))
		}
	}
	return playlists, nil
}

func (p *SoundCloudProvider) GetTrack(ctx context.Context, platformID string) (*pb.Track, error) {
	nativeID := stripPrefix(platformID)

	t, err := p.fetchTrackByID(ctx, nativeID)
	if err != nil {
		return nil, fmt.Errorf("GetTrack %s: %w", nativeID, err)
	}
	return mapTrack(t), nil
}

func (p *SoundCloudProvider) GetAlbum(ctx context.Context, platformID string) (*pb.Album, []*pb.Track, error) {
	nativeID := stripPrefix(platformID)

	// /playlists?ids= does not exist for playlists (unlike /tracks?ids= which does).
	resp, err := p.doRequest(ctx, scAPIV2Base+"/playlists/"+nativeID, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("GetAlbum %s: %w", nativeID, err)
	}

	var pl scPlaylist
	if err := decodeJSON(resp, &pl); err != nil {
		return nil, nil, fmt.Errorf("GetAlbum %s: decode: %w", nativeID, err)
	}
	if pl.ID == 0 {
		return nil, nil, fmt.Errorf("GetAlbum %s: %w", nativeID, ErrSCNotFound)
	}

	// even if SoundCloud classifies it differently.
	if !pl.IsAlbum && pl.PlaylistType != "album" {
		log.Printf("[soundcloud] GetAlbum %s: set is not flagged as album (is_album=%v playlist_type=%q), returning anyway",
			nativeID, pl.IsAlbum, pl.PlaylistType)
	}

	tracks, err := p.hydratePlaylistTracks(ctx, &pl)
	if err != nil {
		log.Printf("[soundcloud] GetAlbum %s: hydrate partial: %v", nativeID, err)
	}
	return mapPlaylistToAlbum(&pl), tracks, nil
}

// get artist fetches user profile and top tracks concurrently.
// sc has no per-artist album endpoint so albums is always nil
func (p *SoundCloudProvider) GetArtist(ctx context.Context, platformID string) (*pb.Artist, []*pb.Track, []*pb.Album, error) {
	nativeID := stripPrefix(platformID)

	type userResult struct {
		user *scUser
		err  error
	}
	type tracksResult struct {
		tracks []*pb.Track
		err    error
	}

	userCh := make(chan userResult, 1)
	tracksCh := make(chan tracksResult, 1)

	go func() {
		resp, err := p.doRequest(ctx, scAPIV2Base+"/users/"+nativeID, nil)
		if err != nil {
			userCh <- userResult{err: err}
			return
		}
		var u scUser
		if err := decodeJSON(resp, &u); err != nil {
			userCh <- userResult{err: err}
			return
		}
		userCh <- userResult{user: &u}
	}()

	go func() {
		resp, err := p.doRequest(ctx, scAPIV2Base+"/users/"+nativeID+"/tracks", url.Values{"limit": {"20"}})
		if err != nil {
			tracksCh <- tracksResult{err: err}
			return
		}
		var result scSearchResult
		if err := decodeJSON(resp, &result); err != nil {
			tracksCh <- tracksResult{err: err}
			return
		}
		tracks := make([]*pb.Track, 0, len(result.Collection))
		for i := range result.Collection {
			tracks = append(tracks, mapTrack(&result.Collection[i]))
		}
		tracksCh <- tracksResult{tracks: tracks}
	}()

	ur := <-userCh
	if ur.err != nil {
		return nil, nil, nil, fmt.Errorf("GetArtist %s: %w", nativeID, ur.err)
	}

	tr := <-tracksCh
	if tr.err != nil {
		log.Printf("[soundcloud] GetArtist %s: top tracks: %v", nativeID, tr.err)
	}

	return mapUser(ur.user), tr.tracks, nil, nil
}

// resolve a full soundcloud permalink url to an scplaylist using v2 /resolve endpoint, returns full resource object so we can decode straight into scplaylist
func (p *SoundCloudProvider) resolvePlaylistByURL(ctx context.Context, rawURL string) (*scPlaylist, error) {
	resp, err := p.doRequest(ctx, scAPIV2Base+"/resolve", url.Values{"url": {rawURL}})
	if err != nil {
		return nil, fmt.Errorf("resolvePlaylistByURL %q: %w", rawURL, err)
	}
	var pl scPlaylist
	if err := decodeJSON(resp, &pl); err != nil {
		return nil, fmt.Errorf("resolvePlaylistByURL %q: decode: %w", rawURL, err)
	}
	if pl.ID == 0 {
		return nil, fmt.Errorf("resolvePlaylistByURL %q: %w", rawURL, ErrSCNotFound)
	}
	return &pl, nil
}

func (p *SoundCloudProvider) GetPlaylist(ctx context.Context, platformID string) (*pb.Playlist, []*pb.Track, error) {
	// numeric native id (stripprefix would corrupt https url to //...)
	if strings.HasPrefix(platformID, "http://") || strings.HasPrefix(platformID, "https://") {
		pl, err := p.resolvePlaylistByURL(ctx, platformID)
		if err != nil {
			return nil, nil, fmt.Errorf("GetPlaylist %s: %w", platformID, err)
		}
		tracks, err := p.hydratePlaylistTracks(ctx, pl)
		if err != nil {
			log.Printf("[soundcloud] GetPlaylist url=%s: hydrate partial: %v", platformID, err)
		}
		return mapPlaylist(pl), tracks, nil
	}

	nativeID := stripPrefix(platformID)

	// /playlists?ids= does not exist for playlists (unlike /tracks?ids= which does).
	resp, err := p.doRequest(ctx, scAPIV2Base+"/playlists/"+nativeID, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("GetPlaylist %s: %w", nativeID, err)
	}

	var pl scPlaylist
	if err := decodeJSON(resp, &pl); err != nil {
		return nil, nil, fmt.Errorf("GetPlaylist %s: decode: %w", nativeID, err)
	}
	if pl.ID == 0 {
		return nil, nil, fmt.Errorf("GetPlaylist %s: %w", nativeID, ErrSCNotFound)
	}

	tracks, err := p.hydratePlaylistTracks(ctx, &pl)
	if err != nil {
		log.Printf("[soundcloud] GetPlaylist %s: hydrate partial: %v", nativeID, err)
	}
	return mapPlaylist(&pl), tracks, nil
}

// fetch full track metadata from api-v2 to get media.transcodings, pick best transcoding (progressive mp3 preferred, hls fallback), call resolvetranscoding with only client_id appended, returns url field from json
func (p *SoundCloudProvider) GetStreamURL(ctx context.Context, platformID string, preferredFormat string) (*pb.GetStreamURLResponse, error) {
	nativeID := stripPrefix(platformID)

	t, err := p.fetchTrackByID(ctx, nativeID)
	if err != nil {
		return nil, fmt.Errorf("GetStreamURL fetch track %s: %w", nativeID, err)
	}

	if len(t.Media.Transcodings) == 0 {
		return nil, fmt.Errorf("GetStreamURL %s: %w (no transcodings in metadata)", nativeID, ErrSCNoStream)
	}

	// then other HLS codecs.  Caller can force HLS by passing "hls".
	useHLS := strings.EqualFold(preferredFormat, "hls")
	transcodingURL, streamType, contentType := selectTranscoding(t.Media.Transcodings, useHLS)
	if transcodingURL == "" {
		return nil, fmt.Errorf("GetStreamURL %s: %w (transcodings present but none matched)", nativeID, ErrSCNoStream)
	}

	// pre-signed Track-Authorization query param already embedded in the URL.
	finalURL, err := p.resolveTranscoding(ctx, transcodingURL)
	if err != nil {
		return nil, fmt.Errorf("GetStreamURL resolve transcoding %s: %w", nativeID, err)
	}

	return &pb.GetStreamURLResponse{
		StreamUrl:   finalURL,
		Source:      pb.Platform_PLATFORM_SOUNDCLOUD,
		StreamType:  streamType,
		ContentType: contentType,
	}, nil
}

// linked_partitioning=true is required for the V2 endpoint to return a
// populated collection wrapper instead of an empty response.
// access[]=playable filters out non-streamable tracks.
func (p *SoundCloudProvider) GetSimilarTracks(ctx context.Context, platformID string, limit int) ([]*pb.Track, error) {
	nativeID := stripPrefix(platformID)
	if limit <= 0 {
		limit = 20
	}

	seedID, _ := strconv.ParseInt(nativeID, 10, 64)

	params := url.Values{
		"limit":               {strconv.Itoa(limit)},
		"linked_partitioning": {"true"},
		"access[]":            {"playable"},
	}

	resp, err := p.doRequest(ctx, scAPIV2Base+"/tracks/"+nativeID+"/related", params)
	if err != nil {
		return nil, fmt.Errorf("GetSimilarTracks %s: %w", nativeID, err)
	}

	var related scRelatedResult
	if err := decodeJSON(resp, &related); err != nil {
		return nil, fmt.Errorf("soundcloud: decode related: %w", err)
	}

	candidates := related.Collection
	tracks := make([]*pb.Track, 0, min(len(candidates), limit))
	for i := range candidates {
		if len(tracks) >= limit {
			break
		}
		if seedID != 0 && candidates[i].ID == seedID {
			continue
		}
		tracks = append(tracks, mapTrack(&candidates[i]))
	}
	return tracks, nil
}

// fill in id-only track stubs via /tracks?ids=...; sc returns only first ~5 tracks as full objects, rest as {id: 123}; order is reconstructed from original playlist stub
func (p *SoundCloudProvider) hydratePlaylistTracks(ctx context.Context, pl *scPlaylist) ([]*pb.Track, error) {
	if len(pl.Tracks) == 0 {
		return nil, nil
	}

	var stubIDs []string
	byID := make(map[int64]*scTrack, len(pl.Tracks))

	for i := range pl.Tracks {
		t := &pl.Tracks[i]
		if t.Title != "" && t.User.Username != "" {
			byID[t.ID] = t
		} else if t.ID != 0 {
			stubIDs = append(stubIDs, strconv.FormatInt(t.ID, 10))
		}
	}

	for i := 0; i < len(stubIDs); i += scBatchSize {
		end := i + scBatchSize
		if end > len(stubIDs) {
			end = len(stubIDs)
		}

		params := url.Values{"ids": {strings.Join(stubIDs[i:end], ",")}}
		resp, err := p.doRequest(ctx, scAPIV2Base+"/tracks", params)
		if err != nil {
			log.Printf("[soundcloud] hydratePlaylistTracks batch %d-%d: %v", i, end-1, err)
			break
		}

		var fetched []scTrack
		if err := decodeJSON(resp, &fetched); err != nil {
			log.Printf("[soundcloud] hydratePlaylistTracks batch decode: %v", err)
			break
		}
		for i := range fetched {
			byID[fetched[i].ID] = &fetched[i]
		}
	}

	// rebuild in original playlist order
	result := make([]*pb.Track, 0, len(pl.Tracks))
	for _, stub := range pl.Tracks {
		if t, ok := byID[stub.ID]; ok {
			result = append(result, mapTrack(t))
		}
	}
	return result, nil
}

// pick the best transcoding url, hls priority: opus > aac > mp3, fallback progressive; default: progressive, fallback hls mp3 > aac > opus
func selectTranscoding(ts []scTranscoding, preferHLS bool) (string, string, string) {
	if preferHLS {
		for _, codec := range []string{"opus", "aac", "mp3"} {
			if u, preset := findTranscoding(ts, "hls", codec); u != "" {
				return u, "hls", codecMIME(preset)
			}
		}
		if u, _ := findTranscoding(ts, "progressive", ""); u != "" {
			return u, "audio_stream", "audio/mpeg"
		}
		return "", "", ""
	}

	if u, _ := findTranscoding(ts, "progressive", ""); u != "" {
		return u, "audio_stream", "audio/mpeg"
	}
	for _, codec := range []string{"mp3", "aac", "opus"} {
		if u, preset := findTranscoding(ts, "hls", codec); u != "" {
			return u, "hls", codecMIME(preset)
		}
	}
	return "", "", ""
}

func findTranscoding(ts []scTranscoding, protocol, codec string) (string, string) {
	for i := range ts {
		t := &ts[i]
		if t.Format.Protocol != protocol || t.URL == "" {
			continue
		}
		if codec == "" || strings.Contains(strings.ToLower(t.Preset), codec) {
			return t.URL, t.Preset
		}
	}
	return "", ""
}

func codecMIME(preset string) string {
	lower := strings.ToLower(preset)
	switch {
	case strings.Contains(lower, "opus"):
		return "audio/ogg; codecs=opus"
	case strings.Contains(lower, "aac"):
		return "audio/aac"
	default:
		return "audio/mpeg"
	}
}

// stripPrefix strips the "soundcloud:" namespace so callers can pass either
// "soundcloud:123" or plain "123"
func stripPrefix(id string) string {
	if idx := strings.LastIndexByte(id, ':'); idx >= 0 {
		return id[idx+1:]
	}
	return id
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
