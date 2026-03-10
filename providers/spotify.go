package providers

// spotify.go вЂ” Spotify provider for the bedrock gRPC aggregator.
//
//         tokenManager fetches and auto-refreshes the bearer token.
//         Reads SPOTIFY_CLIENT_ID and SPOTIFY_CLIENT_SECRET from env.
//
//          GetTrack, GetAlbum, GetArtist, GetPlaylist,
//          GetSimilarTracks (Recommendations API),
//          GetStreamURL (returns STATUS_ERROR вЂ” Spotify has no audio stream).
//
//   typed internal structs в†’ single doRequest helper в†’ mapper functions в†’ pb.*

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	pb "example/grpc/bedrock"
)

// в”Ђв”Ђ sentinel errors в”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђ

var (
	ErrSpotifyNoCredentials = errors.New("spotify: SPOTIFY_CLIENT_ID or SPOTIFY_CLIENT_SECRET not set")
	ErrSpotifyAuth          = errors.New("spotify: authentication failed (check client credentials)")
	ErrSpotifyNotFound      = errors.New("spotify: resource not found (404)")
	ErrSpotifyNoStream      = errors.New("spotify: provider does not support streaming directly")
)

// в”Ђв”Ђ constants в”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђ

const (
	spAPIBase      = "https://api.spotify.com/v1"
	spTokenURL     = "https://accounts.spotify.com/api/token"
	spHTTPTimeout  = 10 * time.Second
	spMarket       = "US"
	// tokenExpiryGrace: refresh the token this many seconds before it actually
	// expires to avoid races between clock skew and in-flight requests.
	spTokenExpiryGrace = 30 * time.Second
)

// в”Ђв”Ђ OAuth2 token manager в”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђ

// spToken holds one access token and its computed expiry wall-clock time.
type spToken struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"` // seconds
	expiresAt   time.Time
}

// tokenManager handles Client-Credentials OAuth2 for the Spotify Web API.
//
// the cached token.  When the token is within spTokenExpiryGrace of expiry,
// the next caller proactively refreshes it so requests are never blocked on
// an expired token.
type tokenManager struct {
	mu           sync.Mutex
	httpClient   *http.Client
	clientID     string
	clientSecret string
	current      *spToken
}

func newTokenManager(httpClient *http.Client) (*tokenManager, error) {
	cid := os.Getenv("SPOTIFY_CLIENT_ID")
	csec := os.Getenv("SPOTIFY_CLIENT_SECRET")
	if cid == "" || csec == "" {
		return nil, ErrSpotifyNoCredentials
	}
	return &tokenManager{
		httpClient:   httpClient,
		clientID:     cid,
		clientSecret: csec,
	}, nil
}

// token returns a valid access token, fetching or refreshing as needed.
func (m *tokenManager) token(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.current != nil && time.Now().Add(spTokenExpiryGrace).Before(m.current.expiresAt) {
		return m.current.AccessToken, nil
	}

	return m.refresh(ctx)
}

// refresh performs the Client Credentials grant and stores the new token.
func (m *tokenManager) refresh(ctx context.Context) (string, error) {
	body := url.Values{"grant_type": {"client_credentials"}}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, spTokenURL, strings.NewReader(body.Encode()))
	if err != nil {
		return "", fmt.Errorf("spotify: tokenManager build request: %w", err)
	}

	creds := base64.StdEncoding.EncodeToString([]byte(m.clientID + ":" + m.clientSecret))
	req.Header.Set("Authorization", "Basic "+creds)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("spotify: tokenManager http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("spotify: token endpoint HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var tok spToken
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", fmt.Errorf("spotify: tokenManager decode: %w", err)
	}
	if tok.AccessToken == "" {
		return "", fmt.Errorf("spotify: token endpoint returned empty access_token")
	}

	if tok.ExpiresIn <= 0 {
	}
	tok.expiresAt = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	m.current = &tok

	log.Printf("[spotify] access token refreshed, expires in %ds", tok.ExpiresIn)
	return tok.AccessToken, nil
}

// в”Ђв”Ђ Spotify JSON response structs в”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђ

type spImage struct {
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

type spArtistSimple struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	ExternalURLs map[string]string `json:"external_urls"`
}

type spArtistFull struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Images       []spImage         `json:"images"`
	Genres       []string          `json:"genres"`
	Followers    spFollowers       `json:"followers"`
	Popularity   int               `json:"popularity"`
	ExternalURLs map[string]string `json:"external_urls"`
}

type spFollowers struct {
	Total int64 `json:"total"`
}

type spAlbumSimple struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	AlbumType    string            `json:"album_type"`
	Images       []spImage         `json:"images"`
	ReleaseDate  string            `json:"release_date"`
	TotalTracks  int               `json:"total_tracks"`
	Artists      []spArtistSimple  `json:"artists"`
	ExternalURLs map[string]string `json:"external_urls"`
}

type spAlbumFull struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	AlbumType    string            `json:"album_type"`
	Images       []spImage         `json:"images"`
	ReleaseDate  string            `json:"release_date"`
	TotalTracks  int               `json:"total_tracks"`
	Artists      []spArtistSimple  `json:"artists"`
	Tracks       spTrackPage       `json:"tracks"`
	ExternalURLs map[string]string `json:"external_urls"`
	Popularity   int               `json:"popularity"`
}

type spTrack struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Artists      []spArtistSimple  `json:"artists"`
	Album        spAlbumSimple     `json:"album"`
	DurationMs   int32             `json:"duration_ms"`
	PreviewURL   string            `json:"preview_url"`
	ExternalURLs map[string]string `json:"external_urls"`
	Popularity   int32             `json:"popularity"`
	IsPlayable   bool              `json:"is_playable"`
	TrackType    string            `json:"type"` // "track"
}

// spTrackSimple is the stripped-down track object returned inside album.tracks
// (it lacks the album field, as the album is implied).
type spTrackSimple struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Artists      []spArtistSimple  `json:"artists"`
	DurationMs   int32             `json:"duration_ms"`
	PreviewURL   string            `json:"preview_url"`
	ExternalURLs map[string]string `json:"external_urls"`
	TrackType    string            `json:"type"`
}

type spPlaylist struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Description  string            `json:"description"`
	Images       []spImage         `json:"images"`
	Owner        spPlaylistOwner   `json:"owner"`
	Tracks       spPlaylistTracks  `json:"tracks"`
	ExternalURLs map[string]string `json:"external_urls"`
}

type spPlaylistOwner struct {
	DisplayName string `json:"display_name"`
	ID          string `json:"id"`
}

// spPlaylistTracks is the paged wrapper for items inside a playlist.
type spPlaylistTracks struct {
	Total int                  `json:"total"`
	Items []spPlaylistTrackItem `json:"items"`
}

type spPlaylistTrackItem struct {
	Track *spTrack `json:"track"` // nil when item was removed / is a local file
}

// в”Ђв”Ђ paged collection wrappers в”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђ

type spTrackPage struct {
	Items []spTrack `json:"items"`
	Total int       `json:"total"`
	Next  string    `json:"next"`
}

type spTrackSimplePage struct {
	Items []spTrackSimple `json:"items"`
	Total int             `json:"total"`
	Next  string          `json:"next"`
}

type spAlbumPage struct {
	Items []spAlbumSimple `json:"items"`
	Total int             `json:"total"`
	Next  string          `json:"next"`
}

type spArtistPage struct {
	Items []spArtistFull `json:"items"`
	Total int            `json:"total"`
	Next  string         `json:"next"`
}

type spPlaylistPage struct {
	Items []*spPlaylist `json:"items"`
	Total int           `json:"total"`
	Next  string        `json:"next"`
}

// в”Ђв”Ђ search response envelopes в”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђ

type spSearchTracksResponse struct {
	Tracks spTrackPage `json:"tracks"`
}

type spSearchAlbumsResponse struct {
	Albums spAlbumPage `json:"albums"`
}

type spSearchArtistsResponse struct {
	Artists spArtistPage `json:"artists"`
}

type spSearchPlaylistsResponse struct {
	Playlists spPlaylistPage `json:"playlists"`
}

// в”Ђв”Ђ artist-context responses в”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђ

type spArtistTopTracksResponse struct {
	Tracks []spTrack `json:"tracks"`
}

type spArtistAlbumsResponse struct {
	Items []spAlbumSimple `json:"items"`
	Total int             `json:"total"`
	Next  string          `json:"next"`
}

// в”Ђв”Ђ recommendations в”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђ

// spArtistTopTracksResponse and spArtistAlbumsResponse are also used in
// from the Spotify Web API (deprecated November 2024).
//   1. artist top-tracks (primary signal)
//   2. artist's recent albums в†’ first track from each (variety)
//   3. search "artist title" to catch covers / remixes (relevance boost)

// в”Ђв”Ђ provider struct в”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђ

type SpotifyProvider struct {
	// apiClient is used for all JSON API calls (api.spotify.com/v1).
	// endpoint so that auth failures on the API path don't confuse the
	// token refresh logic.
	apiClient *http.Client

	tokens *tokenManager
}

// so the caller can decide to fall back to a stubProvider instead of crashing.
func NewSpotifyProvider() (*SpotifyProvider, error) {
	// for simplicity; the token endpoint is a different hostname so there
	// is no connection-pool interference.
	httpClient := &http.Client{Timeout: spHTTPTimeout}

	tm, err := newTokenManager(httpClient)
	if err != nil {
		return nil, err
	}

	return &SpotifyProvider{
		apiClient: httpClient,
		tokens:    tm,
	}, nil
}

func (p *SpotifyProvider) Platform() pb.Platform {
	return pb.Platform_PLATFORM_SPOTIFY
}

// в”Ђв”Ђ HTTP helpers в”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђ

// doRequest is the single authenticated GET path for all api.spotify.com/v1 calls.
// status codes uniformly.  On 401 it triggers a forced token refresh and
// retries once, matching the same retry pattern used in soundcloud.go.
func (p *SpotifyProvider) doRequest(ctx context.Context, endpoint string, params url.Values) (*http.Response, error) {
	const maxAttempts = 2
	forceRefresh := false

	for attempt := 0; attempt < maxAttempts; attempt++ {
		var tok string
		var err error

		if forceRefresh {
			p.tokens.mu.Lock()
			tok, err = p.tokens.refresh(ctx)
			p.tokens.mu.Unlock()
		} else {
			tok, err = p.tokens.token(ctx)
		}
		if err != nil {
			return nil, fmt.Errorf("spotify: doRequest auth: %w", err)
		}

		rawURL := endpoint
		if len(params) > 0 {
			sep := "?"
			if strings.Contains(endpoint, "?") {
				sep = "&"
			}
			rawURL = endpoint + sep + params.Encode()
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, fmt.Errorf("spotify: build request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Accept", "application/json")

		resp, err := p.apiClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("spotify: http: %w", err)
		}

		switch {
		case resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated:
			return resp, nil

		case resp.StatusCode == http.StatusNotFound:
			resp.Body.Close()
			return nil, ErrSpotifyNotFound

		case resp.StatusCode == http.StatusUnauthorized:
			resp.Body.Close()
			forceRefresh = true
			if attempt < maxAttempts-1 {
				log.Printf("[spotify] 401 received, forcing token refresh (attempt %d)", attempt+1)
				continue
			}
			return nil, ErrSpotifyAuth

		case resp.StatusCode == http.StatusTooManyRequests:
			resp.Body.Close()
			return nil, fmt.Errorf("spotify: rate limited (429)")

		default:
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			return nil, fmt.Errorf("spotify: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
		}
	}

	return nil, ErrSpotifyAuth
}

// decodeSpotify decodes a JSON response body into dst and closes the body.
func decodeSpotify(resp *http.Response, dst any) error {
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("spotify: json decode: %w", err)
	}
	return nil
}

// в”Ђв”Ђ image helpers в”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђ

// bestImage picks the highest-resolution image URL from a slice of spImage.
func bestImage(images []spImage) string {
	if len(images) == 0 {
		return ""
	}
	for _, img := range images {
		if img.Width >= 600 && img.URL != "" {
			return img.URL
		}
	}
	return images[0].URL
}

// в”Ђв”Ђ ID helpers в”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђ

func spNamespacedID(nativeID string) string {
	return "spotify:" + nativeID
}

// spStripPrefix strips "spotify:" from a namespaced ID so callers can pass
// either "spotify:4Z8W4fKeB5YxbusRsdQVPb" or the raw Spotify ID.
func spStripPrefix(id string) string {
	return strings.TrimPrefix(id, "spotify:")
}

// в”Ђв”Ђ mapping helpers в”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђ

// joinArtists flattens a slice of simple artist objects into a display string.
func joinArtists(artists []spArtistSimple) string {
	if len(artists) == 0 {
		return "Unknown Artist"
	}
	names := make([]string, 0, len(artists))
	for _, a := range artists {
		if a.Name != "" {
			names = append(names, a.Name)
		}
	}
	if len(names) == 0 {
		return "Unknown Artist"
	}
	return strings.Join(names, ", ")
}

// mapSpArtistSimples converts Spotify simple artist objects into pb.Artist messages.
// and images вЂ” those fields are omitted (zero value) unless explicitly fetched.
func mapSpArtistSimples(artists []spArtistSimple) []*pb.Artist {
	out := make([]*pb.Artist, 0, len(artists))
	for _, a := range artists {
		if a.ID == "" {
			continue
		}
		out = append(out, &pb.Artist{
			Id:          spNamespacedID(a.ID),
			Name:        a.Name,
			ExternalUrl: a.ExternalURLs["spotify"],
			Source:      pb.Platform_PLATFORM_SPOTIFY,
		})
	}
	return out
}

// mapSpTrack converts a full Spotify track object to pb.Track.
func mapSpTrack(t *spTrack) *pb.Track {
	title := t.Name
	if title == "" {
		title = "Unknown Title"
	}

	coverURL := bestImage(t.Album.Images)
	albumTitle := t.Album.Name

	return &pb.Track{
		Id:           spNamespacedID(t.ID),
		PlatformId:   t.ID,
		Title:        title,
		Artist:       joinArtists(t.Artists),
		Artists:      mapSpArtistSimples(t.Artists),
		AlbumTitle:   albumTitle,
		CoverUrl:     coverURL,
		DurationMs:   t.DurationMs,
		PreviewUrl:   t.PreviewURL,
		ExternalUrl:  t.ExternalURLs["spotify"],
		Popularity:   t.Popularity,
		Source:       pb.Platform_PLATFORM_SPOTIFY,
	}
}

// mapSpTrackSimpleWithAlbum converts a stripped-down track (from an album
// tracks listing) to pb.Track by grafting in the parent album's metadata.
func mapSpTrackSimpleWithAlbum(t *spTrackSimple, album *spAlbumFull) *pb.Track {
	title := t.Name
	if title == "" {
		title = "Unknown Title"
	}

	coverURL := bestImage(album.Images)

	return &pb.Track{
		Id:           spNamespacedID(t.ID),
		PlatformId:   t.ID,
		Title:        title,
		Artist:       joinArtists(t.Artists),
		Artists:      mapSpArtistSimples(t.Artists),
		AlbumTitle:   album.Name,
		CoverUrl:     coverURL,
		DurationMs:   t.DurationMs,
		PreviewUrl:   t.PreviewURL,
		ExternalUrl:  t.ExternalURLs["spotify"],
		IsStreamable: false,
		Source:       pb.Platform_PLATFORM_SPOTIFY,
	}
}

// mapSpAlbumSimple converts a Spotify simplified album object to pb.Album.
func mapSpAlbumSimple(a *spAlbumSimple) *pb.Album {
	albumType := a.AlbumType
	if albumType == "" {
		albumType = "album"
	}
	return &pb.Album{
		Id:          spNamespacedID(a.ID),
		PlatformId:  a.ID,
		Title:       a.Name,
		Artist:      joinArtists(a.Artists),
		Artists:     mapSpArtistSimples(a.Artists),
		CoverUrl:    bestImage(a.Images),
		TotalTracks: int32(a.TotalTracks),
		ReleaseDate: a.ReleaseDate,
		ExternalUrl: a.ExternalURLs["spotify"],
		AlbumType:   albumType,
		Source:      pb.Platform_PLATFORM_SPOTIFY,
	}
}

// mapSpAlbumFull converts a full Spotify album object to pb.Album.
func mapSpAlbumFull(a *spAlbumFull) *pb.Album {
	albumType := a.AlbumType
	if albumType == "" {
		albumType = "album"
	}
	return &pb.Album{
		Id:          spNamespacedID(a.ID),
		PlatformId:  a.ID,
		Title:       a.Name,
		Artist:      joinArtists(a.Artists),
		Artists:     mapSpArtistSimples(a.Artists),
		CoverUrl:    bestImage(a.Images),
		TotalTracks: int32(a.TotalTracks),
		ReleaseDate: a.ReleaseDate,
		ExternalUrl: a.ExternalURLs["spotify"],
		AlbumType:   albumType,
		Source:      pb.Platform_PLATFORM_SPOTIFY,
	}
}

// mapSpArtistFull converts a full Spotify artist object to pb.Artist.
func mapSpArtistFull(a *spArtistFull) *pb.Artist {
	name := a.Name
	if name == "" {
		name = "Unknown Artist"
	}
	return &pb.Artist{
		Id:          spNamespacedID(a.ID),
		Name:        name,
		ImageUrl:    bestImage(a.Images),
		Genres:      a.Genres,
		Followers:   a.Followers.Total,
		ExternalUrl: a.ExternalURLs["spotify"],
		Source:      pb.Platform_PLATFORM_SPOTIFY,
	}
}

// mapSpPlaylist converts a Spotify playlist object to pb.Playlist.
func mapSpPlaylist(pl *spPlaylist) *pb.Playlist {
	owner := pl.Owner.DisplayName
	if owner == "" {
		owner = pl.Owner.ID
	}
	return &pb.Playlist{
		Id:          spNamespacedID(pl.ID),
		PlatformId:  pl.ID,
		Title:       pl.Name,
		Description: pl.Description,
		CoverUrl:    bestImage(pl.Images),
		TotalTracks: int32(pl.Tracks.Total),
		Owner:       owner,
		ExternalUrl: pl.ExternalURLs["spotify"],
		Source:      pb.Platform_PLATFORM_SPOTIFY,
	}
}

// в”Ђв”Ђ Search methods в”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђ

func (p *SpotifyProvider) SearchTracks(ctx context.Context, query string, limit int) ([]*pb.Track, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	resp, err := p.doRequest(ctx, spAPIBase+"/search", url.Values{
		"q":      {query},
		"type":   {"track"},
		"limit":  {fmt.Sprintf("%d", limit)},
		"market": {spMarket},
	})
	if err != nil {
		return nil, fmt.Errorf("spotify: SearchTracks: %w", err)
	}

	var result spSearchTracksResponse
	if err := decodeSpotify(resp, &result); err != nil {
		return nil, err
	}

	tracks := make([]*pb.Track, 0, len(result.Tracks.Items))
	for i := range result.Tracks.Items {
		tracks = append(tracks, mapSpTrack(&result.Tracks.Items[i]))
	}
	return tracks, nil
}

func (p *SpotifyProvider) SearchAlbums(ctx context.Context, query string, limit int) ([]*pb.Album, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	resp, err := p.doRequest(ctx, spAPIBase+"/search", url.Values{
		"q":      {query},
		"type":   {"album"},
		"limit":  {fmt.Sprintf("%d", limit)},
		"market": {spMarket},
	})
	if err != nil {
		return nil, fmt.Errorf("spotify: SearchAlbums: %w", err)
	}

	var result spSearchAlbumsResponse
	if err := decodeSpotify(resp, &result); err != nil {
		return nil, err
	}

	albums := make([]*pb.Album, 0, len(result.Albums.Items))
	for i := range result.Albums.Items {
		albums = append(albums, mapSpAlbumSimple(&result.Albums.Items[i]))
	}
	return albums, nil
}

func (p *SpotifyProvider) SearchArtists(ctx context.Context, query string, limit int) ([]*pb.Artist, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	resp, err := p.doRequest(ctx, spAPIBase+"/search", url.Values{
		"q":      {query},
		"type":   {"artist"},
		"limit":  {fmt.Sprintf("%d", limit)},
		"market": {spMarket},
	})
	if err != nil {
		return nil, fmt.Errorf("spotify: SearchArtists: %w", err)
	}

	var result spSearchArtistsResponse
	if err := decodeSpotify(resp, &result); err != nil {
		return nil, err
	}

	artists := make([]*pb.Artist, 0, len(result.Artists.Items))
	for i := range result.Artists.Items {
		artists = append(artists, mapSpArtistFull(&result.Artists.Items[i]))
	}
	return artists, nil
}

func (p *SpotifyProvider) SearchPlaylists(ctx context.Context, query string, limit int) ([]*pb.Playlist, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	resp, err := p.doRequest(ctx, spAPIBase+"/search", url.Values{
		"q":      {query},
		"type":   {"playlist"},
		"limit":  {fmt.Sprintf("%d", limit)},
		"market": {spMarket},
	})
	if err != nil {
		return nil, fmt.Errorf("spotify: SearchPlaylists: %w", err)
	}

	var result spSearchPlaylistsResponse
	if err := decodeSpotify(resp, &result); err != nil {
		return nil, err
	}

	playlists := make([]*pb.Playlist, 0, len(result.Playlists.Items))
	for _, pl := range result.Playlists.Items {
		// playlists вЂ” skip any entry that decoded to a nil pointer or has no ID.
		if pl == nil || pl.ID == "" || pl.Name == "" {
			continue
		}
		playlists = append(playlists, mapSpPlaylist(pl))
	}
	return playlists, nil
}

// в”Ђв”Ђ Get single-item methods в”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђ

func (p *SpotifyProvider) GetTrack(ctx context.Context, platformID string) (*pb.Track, error) {
	nativeID := spStripPrefix(platformID)

	resp, err := p.doRequest(ctx, spAPIBase+"/tracks/"+nativeID, url.Values{
		"market": {spMarket},
	})
	if err != nil {
		return nil, fmt.Errorf("spotify: GetTrack %s: %w", nativeID, err)
	}

	var t spTrack
	if err := decodeSpotify(resp, &t); err != nil {
		return nil, err
	}
	if t.ID == "" {
		return nil, fmt.Errorf("spotify: GetTrack %s: %w", nativeID, ErrSpotifyNotFound)
	}
	return mapSpTrack(&t), nil
}

// has more than 50 tracks (Spotify's single-page limit).
func (p *SpotifyProvider) GetAlbum(ctx context.Context, platformID string) (*pb.Album, []*pb.Track, error) {
	nativeID := spStripPrefix(platformID)

	resp, err := p.doRequest(ctx, spAPIBase+"/albums/"+nativeID, url.Values{
		"market": {spMarket},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("spotify: GetAlbum %s: %w", nativeID, err)
	}

	var album spAlbumFull
	if err := decodeSpotify(resp, &album); err != nil {
		return nil, nil, fmt.Errorf("spotify: GetAlbum %s: decode: %w", nativeID, err)
	}
	if album.ID == "" {
		return nil, nil, fmt.Errorf("spotify: GetAlbum %s: %w", nativeID, ErrSpotifyNotFound)
	}

	tracks, err := p.fetchAlbumTracks(ctx, &album)
	if err != nil {
		log.Printf("[spotify] GetAlbum %s: track listing partial: %v", nativeID, err)
	}

	return mapSpAlbumFull(&album), tracks, nil
}

// fetchAlbumTracks collects all tracks for an album, paginating beyond the
// first page returned inside the full album object.  Up to 5 additional pages
// are fetched sequentially (Spotify paginates at 50 items per page, so this
// covers albums up to 300 tracks вЂ” more than any real album).
//
// with album info redundantly included).  Subsequent pages from the
// /albums/{id}/tracks endpoint return []spTrackSimple (no embedded album).
func (p *SpotifyProvider) fetchAlbumTracks(ctx context.Context, album *spAlbumFull) ([]*pb.Track, error) {
	const maxExtraPages = 5

	// spTrack and spTrackSimple share the same essential fields; we project
	// spTrack в†’ spTrackSimple so the rest of the function stays uniform.
	items := make([]spTrackSimple, 0, album.TotalTracks)
	for _, t := range album.Tracks.Items {
		items = append(items, spTrackSimple{
			ID:           t.ID,
			Name:         t.Name,
			Artists:      t.Artists,
			DurationMs:   t.DurationMs,
			PreviewURL:   t.PreviewURL,
			ExternalURLs: t.ExternalURLs,
			TrackType:    t.TrackType,
		})
	}
	nextURL := album.Tracks.Next

	for pageNum := 0; pageNum < maxExtraPages && nextURL != ""; pageNum++ {
		resp, err := p.doRequest(ctx, nextURL, nil)
		if err != nil {
			return spTrackSimplePageToTracks(items, album), fmt.Errorf("page %d: %w", pageNum+1, err)
		}
		var pg spTrackSimplePage
		if err := decodeSpotify(resp, &pg); err != nil {
			return spTrackSimplePageToTracks(items, album), err
		}
		items = append(items, pg.Items...)
		nextURL = pg.Next
	}

	return spTrackSimplePageToTracks(items, album), nil
}

func spTrackSimplePageToTracks(items []spTrackSimple, album *spAlbumFull) []*pb.Track {
	tracks := make([]*pb.Track, 0, len(items))
	for i := range items {
		if items[i].ID == "" {
			continue
		}
		tracks = append(tracks, mapSpTrackSimpleWithAlbum(&items[i], album))
	}
	return tracks
}

// concurrently.  If top tracks or album listing fail, they are logged and
// omitted (partial content) вЂ” the artist profile itself is the critical piece.
func (p *SpotifyProvider) GetArtist(ctx context.Context, platformID string) (*pb.Artist, []*pb.Track, []*pb.Album, error) {
	nativeID := spStripPrefix(platformID)

	type artistResult struct {
		artist *spArtistFull
		err    error
	}
	type tracksResult struct {
		tracks []*pb.Track
		err    error
	}
	type albumsResult struct {
		albums []*pb.Album
		err    error
	}

	artistCh := make(chan artistResult, 1)
	tracksCh := make(chan tracksResult, 1)
	albumsCh := make(chan albumsResult, 1)

	go func() {
		resp, err := p.doRequest(ctx, spAPIBase+"/artists/"+nativeID, nil)
		if err != nil {
			artistCh <- artistResult{err: err}
			return
		}
		var a spArtistFull
		if err := decodeSpotify(resp, &a); err != nil {
			artistCh <- artistResult{err: err}
			return
		}
		artistCh <- artistResult{artist: &a}
	}()

	go func() {
		resp, err := p.doRequest(ctx, spAPIBase+"/artists/"+nativeID+"/top-tracks", url.Values{
			"market": {spMarket},
		})
		if err != nil {
			tracksCh <- tracksResult{err: err}
			return
		}
		var r spArtistTopTracksResponse
		if err := decodeSpotify(resp, &r); err != nil {
			tracksCh <- tracksResult{err: err}
			return
		}
		tracks := make([]*pb.Track, 0, len(r.Tracks))
		for i := range r.Tracks {
			tracks = append(tracks, mapSpTrack(&r.Tracks[i]))
		}
		tracksCh <- tracksResult{tracks: tracks}
	}()

	go func() {
		resp, err := p.doRequest(ctx, spAPIBase+"/artists/"+nativeID+"/albums", url.Values{
			"include_groups": {"album,single"},
			"market":         {spMarket},
			"limit":          {"50"},
		})
		if err != nil {
			albumsCh <- albumsResult{err: err}
			return
		}
		var r spArtistAlbumsResponse
		if err := decodeSpotify(resp, &r); err != nil {
			albumsCh <- albumsResult{err: err}
			return
		}
		albums := make([]*pb.Album, 0, len(r.Items))
		for i := range r.Items {
			albums = append(albums, mapSpAlbumSimple(&r.Items[i]))
		}
		albumsCh <- albumsResult{albums: albums}
	}()

	ar := <-artistCh
	if ar.err != nil {
		<-tracksCh
		<-albumsCh
		return nil, nil, nil, fmt.Errorf("spotify: GetArtist %s: %w", nativeID, ar.err)
	}

	tr := <-tracksCh
	if tr.err != nil {
		log.Printf("[spotify] GetArtist %s: top tracks partial: %v", nativeID, tr.err)
	}

	alr := <-albumsCh
	if alr.err != nil {
		log.Printf("[spotify] GetArtist %s: albums partial: %v", nativeID, alr.err)
	}

	return mapSpArtistFull(ar.artist), tr.tracks, alr.albums, nil
}

// are fetched sequentially (mirrors the Python spotipy logic exactly).
func (p *SpotifyProvider) GetPlaylist(ctx context.Context, platformID string) (*pb.Playlist, []*pb.Track, error) {
	nativeID := spStripPrefix(platformID)

	// e.g. "https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M" в†’ "37i9dQZF1DXcBWIGoYBM5M"
	if strings.Contains(nativeID, "/") {
		parts := strings.Split(nativeID, "/")
		nativeID = parts[len(parts)-1]
		if idx := strings.IndexByte(nativeID, '?'); idx >= 0 {
			nativeID = nativeID[:idx]
		}
	}

	// (e.g. omitting "id" at the root level causes playlist.ID to decode as ""),
	// which our empty-ID guard then treats as a 404.  The full response is small
	// enough that bandwidth is not a concern.
	resp, err := p.doRequest(ctx, spAPIBase+"/playlists/"+nativeID, url.Values{
		"market": {spMarket},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("spotify: GetPlaylist %s: %w", nativeID, err)
	}

	var playlist spPlaylist
	if err := decodeSpotify(resp, &playlist); err != nil {
		return nil, nil, fmt.Errorf("spotify: GetPlaylist %s: decode: %w", nativeID, err)
	}
	if playlist.ID == "" {
		return nil, nil, fmt.Errorf("spotify: GetPlaylist %s: %w", nativeID, ErrSpotifyNotFound)
	}

	tracks, err := p.collectPlaylistTracks(ctx, &playlist)
	if err != nil {
		log.Printf("[spotify] GetPlaylist %s: track collection partial: %v", nativeID, err)
	}

	return mapSpPlaylist(&playlist), tracks, nil
}

// collectPlaylistTracks accumulates all tracks across pagination.
// fetched by following the "next" cursor returned by the items endpoint.
func (p *SpotifyProvider) collectPlaylistTracks(ctx context.Context, playlist *spPlaylist) ([]*pb.Track, error) {
	const maxPages = 20 // guard: 20 Г— 100 = 2 000 tracks maximum

	tracks := make([]*pb.Track, 0, playlist.Tracks.Total)
	for i := range playlist.Tracks.Items {
		item := &playlist.Tracks.Items[i]
		if item.Track == nil || item.Track.ID == "" {
			continue // local files or removed tracks have nil/empty track
		}
		tracks = append(tracks, mapSpTrack(item.Track))
	}

	offset := len(playlist.Tracks.Items)
	nativeID := playlist.ID

	for page := 1; page < maxPages && offset < playlist.Tracks.Total; page++ {
		resp, err := p.doRequest(ctx, spAPIBase+"/playlists/"+nativeID+"/tracks", url.Values{
			"market": {spMarket},
			"limit":  {"100"},
			"offset": {fmt.Sprintf("%d", offset)},
		})
		if err != nil {
			return tracks, fmt.Errorf("page %d: %w", page, err)
		}

		var result spPlaylistTracks
		if err := decodeSpotify(resp, &result); err != nil {
			return tracks, err
		}

		for i := range result.Items {
			item := &result.Items[i]
			if item.Track == nil || item.Track.ID == "" {
				continue
			}
			tracks = append(tracks, mapSpTrack(item.Track))
		}

		offset += len(result.Items)
		if len(result.Items) == 0 {
			break
		}
	}

	return tracks, nil
}

// в”Ђв”Ђ Stream в”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђ

// audio stream URLs via its public Web API.  Bridging to a stream source
// (e.g. SoundCloud cross-lookup) will be implemented in a dedicated layer.
func (p *SpotifyProvider) GetStreamURL(_ context.Context, platformID string, _ string) (*pb.GetStreamURLResponse, error) {
	return &pb.GetStreamURLResponse{
		Source: pb.Platform_PLATFORM_SPOTIFY,
		Status: pb.ResponseStatus_STATUS_ERROR,
		Error:  "spotify: provider does not support streaming directly",
	}, nil
}

// в”Ђв”Ђ Similar tracks (Recommendations API) в”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђ

//
// current free-tier public API:
//
//  1. Artist top-tracks  вЂ” strongest relevance signal; Spotify returns up to
//     10 tracks for each artist associated with the seed track.
//  2. Artist albums в†’ first track per album вЂ” adds variety beyond top-10.
//  3. Text search "artist title" вЂ” catches remixes, covers, and related works.
//
func (p *SpotifyProvider) GetSimilarTracks(ctx context.Context, platformID string, limit int) ([]*pb.Track, error) {
	nativeID := spStripPrefix(platformID)
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	// в”Ђв”Ђ Step 1: resolve the seed track to get artist IDs + title. в”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђ
	seedTrack, err := p.GetTrack(ctx, nativeID)
	if err != nil {
		return nil, fmt.Errorf("spotify: GetSimilarTracks: seed track: %w", err)
	}

	artistIDs := make([]string, 0, len(seedTrack.Artists))
	for _, a := range seedTrack.Artists {
		if raw := spStripPrefix(a.Id); raw != "" {
			artistIDs = append(artistIDs, raw)
		}
	}
	if len(artistIDs) == 0 {
		return nil, nil
	}

	primaryArtistID := artistIDs[0]
	seedTitle := seedTrack.Title
	seedArtist := seedTrack.Artist

	// в”Ђв”Ђ Step 2: fan out three concurrent fetches for the primary artist. в”Ђв”Ђв”Ђв”Ђв”Ђ
	type fetchResult struct {
		tracks []*pb.Track
		err    error
	}

	topTracksCh := make(chan fetchResult, 1)
	albumTracksCh := make(chan fetchResult, 1)
	searchCh := make(chan fetchResult, 1)

	go func() {
		resp, err := p.doRequest(ctx, spAPIBase+"/artists/"+primaryArtistID+"/top-tracks", url.Values{
			"market": {spMarket},
		})
		if err != nil {
			topTracksCh <- fetchResult{err: err}
			return
		}
		var r spArtistTopTracksResponse
		if err := decodeSpotify(resp, &r); err != nil {
			topTracksCh <- fetchResult{err: err}
			return
		}
		out := make([]*pb.Track, 0, len(r.Tracks))
		for i := range r.Tracks {
			out = append(out, mapSpTrack(&r.Tracks[i]))
		}
		topTracksCh <- fetchResult{tracks: out}
	}()

	// concurrently (capped at 5) so we don't burn the provider timeout on
	// serial requests.
	go func() {
		resp, err := p.doRequest(ctx, spAPIBase+"/artists/"+primaryArtistID+"/albums", url.Values{
			"include_groups": {"album,single"},
			"market":         {spMarket},
			"limit":          {"10"},
		})
		if err != nil {
			albumTracksCh <- fetchResult{err: err}
			return
		}
		var r spArtistAlbumsResponse
		if err := decodeSpotify(resp, &r); err != nil {
			albumTracksCh <- fetchResult{err: err}
			return
		}

		const maxAlbums = 5
		albums := r.Items
		if len(albums) > maxAlbums {
			albums = albums[:maxAlbums]
		}

		type albumTrack struct {
			idx   int
			track *pb.Track
		}
		perAlbumCh := make(chan albumTrack, len(albums))

		for idx, alb := range albums {
			alb := alb // capture
			idx := idx
			go func() {
				if alb.ID == "" {
					perAlbumCh <- albumTrack{idx: idx}
					return
				}
				aresp, err := p.doRequest(ctx, spAPIBase+"/albums/"+alb.ID+"/tracks", url.Values{
					"market": {spMarket},
					"limit":  {"1"},
				})
				if err != nil {
					perAlbumCh <- albumTrack{idx: idx}
					return
				}
				var page spTrackSimplePage
				if err := decodeSpotify(aresp, &page); err != nil || len(page.Items) == 0 {
					perAlbumCh <- albumTrack{idx: idx}
					return
				}
				t := mapSpTrackSimpleWithAlbum(&page.Items[0], &spAlbumFull{
					ID:      alb.ID,
					Name:    alb.Name,
					Images:  alb.Images,
					Artists: alb.Artists,
				})
				perAlbumCh <- albumTrack{idx: idx, track: t}
			}()
		}

		ordered := make([]*pb.Track, len(albums))
		for range albums {
			res := <-perAlbumCh
			if res.track != nil {
				ordered[res.idx] = res.track
			}
		}

		out := make([]*pb.Track, 0, len(albums))
		for _, t := range ordered {
			if t != nil {
				out = append(out, t)
			}
		}
		albumTracksCh <- fetchResult{tracks: out}
	}()

	go func() {
		q := seedArtist + " " + seedTitle
		resp, err := p.doRequest(ctx, spAPIBase+"/search", url.Values{
			"q":      {q},
			"type":   {"track"},
			"limit":  {fmt.Sprintf("%d", limit)},
			"market": {spMarket},
		})
		if err != nil {
			searchCh <- fetchResult{err: err}
			return
		}
		var r spSearchTracksResponse
		if err := decodeSpotify(resp, &r); err != nil {
			searchCh <- fetchResult{err: err}
			return
		}
		out := make([]*pb.Track, 0, len(r.Tracks.Items))
		for i := range r.Tracks.Items {
			out = append(out, mapSpTrack(&r.Tracks.Items[i]))
		}
		searchCh <- fetchResult{tracks: out}
	}()

	// в”Ђв”Ђ Step 3: collect results, deduplicate, trim. в”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђ
	tr1 := <-topTracksCh
	tr2 := <-albumTracksCh
	tr3 := <-searchCh

	if tr1.err != nil {
		log.Printf("[spotify] GetSimilarTracks %s: top-tracks fetch: %v", nativeID, tr1.err)
	}
	if tr2.err != nil {
		log.Printf("[spotify] GetSimilarTracks %s: album-tracks fetch: %v", nativeID, tr2.err)
	}
	if tr3.err != nil {
		log.Printf("[spotify] GetSimilarTracks %s: search fetch: %v", nativeID, tr3.err)
	}

	seen := make(map[string]struct{}, limit*2)
	seen[nativeID] = struct{}{}

	out := make([]*pb.Track, 0, limit)
	for _, bucket := range [][]*pb.Track{tr1.tracks, tr3.tracks, tr2.tracks} {
		for _, t := range bucket {
			if len(out) >= limit {
				break
			}
			id := spStripPrefix(t.GetId())
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, t)
		}
		if len(out) >= limit {
			break
		}
	}

	for _, extraArtistID := range artistIDs[1:] {
		if len(out) >= limit {
			break
		}
		resp, err := p.doRequest(ctx, spAPIBase+"/artists/"+extraArtistID+"/top-tracks", url.Values{
			"market": {spMarket},
		})
		if err != nil {
			continue
		}
		var r spArtistTopTracksResponse
		if err := decodeSpotify(resp, &r); err != nil {
			continue
		}
		for i := range r.Tracks {
			if len(out) >= limit {
				break
			}
			id := r.Tracks[i].ID
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, mapSpTrack(&r.Tracks[i]))
		}
	}

	return out, nil
}
