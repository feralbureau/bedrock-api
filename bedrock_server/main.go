// package main: simple bedrock grpc server.
// searches fan out to providers in parallel using goroutines + waitgroups.
// provider failures are collected and returned as partial results.
// provider adapters are stubs here; swap in real http/sdk calls when ready.

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	pb "github.com/feralbureau/bedrock-api/bedrock"
	"github.com/feralbureau/bedrock-api/pkg/token"
	"github.com/feralbureau/bedrock-api/providers"
	"github.com/feralbureau/bedrock-api/server/middleware"
	"github.com/feralbureau/bedrock-api/store"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var port = flag.Int("port", 50052, "bedrock grpc port")
var proxyAddr = flag.String("proxy-addr", ":8080", "bedrock proxy addr")
var proxyHost = flag.String("proxy-host", "localhost:8080", "bedrock proxy host for rewriting")

// build info, set via ldflags during docker build or ci
var (
	buildVersion = "0.0.0-dev"
	buildCommit  = "unknown"
	buildTime    = "unknown"
)

// trackProvider is the provider interface for a platform
type trackProvider interface {
	Platform() pb.Platform
	SearchTracks(ctx context.Context, query string, limit int) ([]*pb.Track, error)
	SearchAlbums(ctx context.Context, query string, limit int) ([]*pb.Album, error)
	SearchArtists(ctx context.Context, query string, limit int) ([]*pb.Artist, error)
	SearchPlaylists(ctx context.Context, query string, limit int) ([]*pb.Playlist, error)
	GetTrack(ctx context.Context, platformID string) (*pb.Track, error)
	GetAlbum(ctx context.Context, platformID string) (*pb.Album, []*pb.Track, error)
	GetArtist(ctx context.Context, platformID string) (*pb.Artist, []*pb.Track, []*pb.Album, error)
	GetPlaylist(ctx context.Context, platformID string) (*pb.Playlist, []*pb.Track, error)
	GetStreamURL(ctx context.Context, platformID string, preferredFormat string) (*pb.GetStreamURLResponse, error)
	GetSimilarTracks(ctx context.Context, platformID string, limit int) ([]*pb.Track, error)
}

// simple stub provider. replace with real impls.
type stubProvider struct {
	platform pb.Platform
}

func (s *stubProvider) Platform() pb.Platform { return s.platform }

func (s *stubProvider) SearchTracks(_ context.Context, query string, limit int) ([]*pb.Track, error) {
	// todo: replace stub with real http/sdk call
	log.Printf("[%s] SearchTracks stub: query=%q limit=%d", pb.Platform_name[int32(s.platform)], query, limit)
	return nil, nil
}

func (s *stubProvider) SearchAlbums(_ context.Context, query string, limit int) ([]*pb.Album, error) {
	log.Printf("[%s] SearchAlbums stub: query=%q limit=%d", pb.Platform_name[int32(s.platform)], query, limit)
	return nil, nil
}

func (s *stubProvider) SearchArtists(_ context.Context, query string, limit int) ([]*pb.Artist, error) {
	log.Printf("[%s] SearchArtists stub: query=%q limit=%d", pb.Platform_name[int32(s.platform)], query, limit)
	return nil, nil
}

func (s *stubProvider) SearchPlaylists(_ context.Context, query string, limit int) ([]*pb.Playlist, error) {
	log.Printf("[%s] SearchPlaylists stub: query=%q limit=%d", pb.Platform_name[int32(s.platform)], query, limit)
	return nil, nil
}

func (s *stubProvider) GetTrack(_ context.Context, id string) (*pb.Track, error) {
	log.Printf("[%s] GetTrack stub: id=%q", pb.Platform_name[int32(s.platform)], id)
	return nil, nil
}

func (s *stubProvider) GetAlbum(_ context.Context, id string) (*pb.Album, []*pb.Track, error) {
	log.Printf("[%s] GetAlbum stub: id=%q", pb.Platform_name[int32(s.platform)], id)
	return nil, nil, nil
}

func (s *stubProvider) GetArtist(_ context.Context, id string) (*pb.Artist, []*pb.Track, []*pb.Album, error) {
	log.Printf("[%s] GetArtist stub: id=%q", pb.Platform_name[int32(s.platform)], id)
	return nil, nil, nil, nil
}

func (s *stubProvider) GetPlaylist(_ context.Context, id string) (*pb.Playlist, []*pb.Track, error) {
	log.Printf("[%s] GetPlaylist stub: id=%q", pb.Platform_name[int32(s.platform)], id)
	return nil, nil, nil
}

func (s *stubProvider) GetStreamURL(_ context.Context, id string, format string) (*pb.GetStreamURLResponse, error) {
	log.Printf("[%s] GetStreamURL stub: id=%q format=%q", pb.Platform_name[int32(s.platform)], id, format)
	return nil, nil
}

func (s *stubProvider) GetSimilarTracks(_ context.Context, id string, limit int) ([]*pb.Track, error) {
	log.Printf("[%s] GetSimilarTracks stub: id=%q limit=%d", pb.Platform_name[int32(s.platform)], id, limit)
	return nil, nil
}

// provider lists are filled in main after loading .env so constructors can read env vars.
var allProviders []trackProvider
var providerMap map[pb.Platform]trackProvider

func initProviders() {
	// spotify needs client id + secret. if missing, use stub so other providers keep working.
	var spotifyProvider trackProvider
	sp, err := providers.NewSpotifyProvider()
	if err != nil {
		log.Printf("[spotify] provider disabled: %v", err)
		spotifyProvider = &stubProvider{platform: pb.Platform_PLATFORM_SPOTIFY}
	} else {
		log.Printf("[spotify] provider enabled")
		spotifyProvider = sp
	}

	// youtube needs innertube client. if init fails, use stub so other providers keep working.
	var youtubeProvider trackProvider
	yt, ytErr := providers.NewYouTubeProvider()
	if ytErr != nil {
		log.Printf("[youtube] provider disabled: %v", ytErr)
		youtubeProvider = &stubProvider{platform: pb.Platform_PLATFORM_YOUTUBE}
	} else {
		log.Printf("[youtube] provider enabled")
		youtubeProvider = yt
	}

	allProviders = []trackProvider{
		spotifyProvider,
		&stubProvider{platform: pb.Platform_PLATFORM_YANDEX},
		&stubProvider{platform: pb.Platform_PLATFORM_VK},
		providers.NewDeezerProvider(),
		providers.NewSoundCloudProvider(),
		youtubeProvider,
	}
	providerMap = make(map[pb.Platform]trackProvider, len(allProviders))
	for _, p := range allProviders {
		providerMap[p.Platform()] = p
	}
}

// clamp request limit to [1, 50]
func defaultLimit(v int32) int {
	if v <= 0 {
		return 20
	}
	if v > 50 {
		return 50
	}
	return int(v)
}

// selectProviders returns providers matching requested list; empty => all providers
func selectProviders(requested []pb.Platform) []trackProvider {
	if len(requested) == 0 {
		return allProviders
	}
	out := make([]trackProvider, 0, len(requested))
	for _, plat := range requested {
		if p, ok := providerMap[plat]; ok {
			out = append(out, p)
		}
	}
	return out
}

// parsePlatformID splits namespaced id like "spotify:abc" to (platform, native).
// returns unspecified platform when parsing fails.
func parsePlatformID(id string) (pb.Platform, string) {
	idx := strings.IndexByte(id, ':')
	if idx < 0 {
		return pb.Platform_PLATFORM_UNSPECIFIED, id
	}
	prefix := id[:idx]
	native := id[idx+1:]
	switch strings.ToLower(prefix) {
	case "spotify":
		return pb.Platform_PLATFORM_SPOTIFY, native
	case "yandex":
		return pb.Platform_PLATFORM_YANDEX, native
	case "vk":
		return pb.Platform_PLATFORM_VK, native
	case "deezer":
		return pb.Platform_PLATFORM_DEEZER, native
	case "soundcloud":
		return pb.Platform_PLATFORM_SOUNDCLOUD, native
	case "youtube":
		return pb.Platform_PLATFORM_YOUTUBE, native
	default:
		return pb.Platform_PLATFORM_UNSPECIFIED, id
	}
}

// responseStatus picks ok or partial based on provider errors
func responseStatus(errs []*pb.ProviderError) pb.ResponseStatus {
	if len(errs) == 0 {
		return pb.ResponseStatus_STATUS_OK
	}
	return pb.ResponseStatus_STATUS_PARTIAL
}

// per-provider timeout on top of RPC deadline
const providerTimeout = 8 * time.Second

// withProviderTimeout returns a child context capped at providerTimeout
func withProviderTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, providerTimeout)
}

type bedrockServer struct {
	pb.UnimplementedBedrockServiceServer
	lyrics         *lrcClient
	geniusLyrics   *geniusClient
	userStore      store.UserStore
	jwtManager     *token.JWTManager
	refreshManager *token.JWTManager
}

// search tracks: fan out to providers in parallel and merge results.
// return partial status if any provider failed.
func (s *bedrockServer) SearchTracks(ctx context.Context, req *pb.SearchRequest) (*pb.SearchTracksResponse, error) {
	if strings.TrimSpace(req.GetQuery()) == "" {
		return nil, status.Error(codes.InvalidArgument, "query must not be empty")
	}

	providers := selectProviders(req.GetPlatforms())
	limit := defaultLimit(req.GetLimit())

	// pre-allocate result slots to avoid lock churn
	type result struct {
		tracks []*pb.Track
		err    *pb.ProviderError
	}
	results := make([]result, len(providers))

	var wg sync.WaitGroup
	wg.Add(len(providers))

	for i, p := range providers {
		i, p := i, p
		go func() {
			defer wg.Done()
			pCtx, cancel := withProviderTimeout(ctx)
			defer cancel()

			tracks, err := p.SearchTracks(pCtx, req.GetQuery(), limit)
			if err != nil {
				log.Printf("SearchTracks [%s] error: %v", pb.Platform_name[int32(p.Platform())], err)
				results[i].err = &pb.ProviderError{
					Platform: p.Platform(),
					Message:  err.Error(),
				}
				return
			}
			results[i].tracks = tracks
		}()
	}
	wg.Wait()

	// merge without extra allocations
	var tracks []*pb.Track
	var provErrs []*pb.ProviderError
	for _, r := range results {
		for _, t := range r.tracks {
			rewriteTrack(t, *proxyHost)
			tracks = append(tracks, t)
		}
		if r.err != nil {
			provErrs = append(provErrs, r.err)
		}
	}

	return &pb.SearchTracksResponse{
		Tracks: tracks,
		Status: responseStatus(provErrs),
		Errors: provErrs,
	}, nil
}

// search albums fans out to providers in parallel
func (s *bedrockServer) SearchAlbums(ctx context.Context, req *pb.SearchRequest) (*pb.SearchAlbumsResponse, error) {
	if strings.TrimSpace(req.GetQuery()) == "" {
		return nil, status.Error(codes.InvalidArgument, "query must not be empty")
	}

	providers := selectProviders(req.GetPlatforms())
	limit := defaultLimit(req.GetLimit())

	type result struct {
		albums []*pb.Album
		err    *pb.ProviderError
	}
	results := make([]result, len(providers))

	var wg sync.WaitGroup
	wg.Add(len(providers))

	for i, p := range providers {
		i, p := i, p
		go func() {
			defer wg.Done()
			pCtx, cancel := withProviderTimeout(ctx)
			defer cancel()

			albums, err := p.SearchAlbums(pCtx, req.GetQuery(), limit)
			if err != nil {
				log.Printf("SearchAlbums [%s] error: %v", pb.Platform_name[int32(p.Platform())], err)
				results[i].err = &pb.ProviderError{Platform: p.Platform(), Message: err.Error()}
				return
			}
			results[i].albums = albums
		}()
	}
	wg.Wait()

	var albums []*pb.Album
	var provErrs []*pb.ProviderError
	for _, r := range results {
		for _, a := range r.albums {
			rewriteAlbum(a, nil, *proxyHost)
			albums = append(albums, a)
		}
		if r.err != nil {
			provErrs = append(provErrs, r.err)
		}
	}

	return &pb.SearchAlbumsResponse{
		Albums: albums,
		Status: responseStatus(provErrs),
		Errors: provErrs,
	}, nil
}

// search artists fans out to providers in parallel
func (s *bedrockServer) SearchArtists(ctx context.Context, req *pb.SearchRequest) (*pb.SearchArtistsResponse, error) {
	if strings.TrimSpace(req.GetQuery()) == "" {
		return nil, status.Error(codes.InvalidArgument, "query must not be empty")
	}

	providers := selectProviders(req.GetPlatforms())
	limit := defaultLimit(req.GetLimit())

	type result struct {
		artists []*pb.Artist
		err     *pb.ProviderError
	}
	results := make([]result, len(providers))

	var wg sync.WaitGroup
	wg.Add(len(providers))

	for i, p := range providers {
		i, p := i, p
		go func() {
			defer wg.Done()
			pCtx, cancel := withProviderTimeout(ctx)
			defer cancel()

			artists, err := p.SearchArtists(pCtx, req.GetQuery(), limit)
			if err != nil {
				log.Printf("SearchArtists [%s] error: %v", pb.Platform_name[int32(p.Platform())], err)
				results[i].err = &pb.ProviderError{Platform: p.Platform(), Message: err.Error()}
				return
			}
			results[i].artists = artists
		}()
	}
	wg.Wait()

	var artists []*pb.Artist
	var provErrs []*pb.ProviderError
	for _, r := range results {
		artists = append(artists, r.artists...)
		if r.err != nil {
			provErrs = append(provErrs, r.err)
		}
	}

	for _, art := range artists {
		rewriteArtist(art, *proxyHost)
	}

	return &pb.SearchArtistsResponse{
		Artists: artists,
		Status:  responseStatus(provErrs),
		Errors:  provErrs,
	}, nil
}

// search playlists fans out to providers in parallel
func (s *bedrockServer) SearchPlaylists(ctx context.Context, req *pb.SearchRequest) (*pb.SearchPlaylistsResponse, error) {
	if strings.TrimSpace(req.GetQuery()) == "" {
		return nil, status.Error(codes.InvalidArgument, "query must not be empty")
	}

	providers := selectProviders(req.GetPlatforms())
	limit := defaultLimit(req.GetLimit())

	type result struct {
		playlists []*pb.Playlist
		err       *pb.ProviderError
	}
	results := make([]result, len(providers))

	var wg sync.WaitGroup
	wg.Add(len(providers))

	for i, p := range providers {
		i, p := i, p
		go func() {
			defer wg.Done()
			pCtx, cancel := withProviderTimeout(ctx)
			defer cancel()

			playlists, err := p.SearchPlaylists(pCtx, req.GetQuery(), limit)
			if err != nil {
				log.Printf("SearchPlaylists [%s] error: %v", pb.Platform_name[int32(p.Platform())], err)
				results[i].err = &pb.ProviderError{Platform: p.Platform(), Message: err.Error()}
				return
			}
			results[i].playlists = playlists
		}()
	}
	wg.Wait()

	var playlists []*pb.Playlist
	var provErrs []*pb.ProviderError
	for _, r := range results {
		for _, pl := range r.playlists {
			rewritePlaylist(pl, nil, *proxyHost)
			playlists = append(playlists, pl)
		}
		if r.err != nil {
			provErrs = append(provErrs, r.err)
		}
	}

	return &pb.SearchPlaylistsResponse{
		Playlists: playlists,
		Status:    responseStatus(provErrs),
		Errors:    provErrs,
	}, nil
}

// get track: resolve platform and fetch
func (s *bedrockServer) GetTrack(ctx context.Context, req *pb.GetTrackRequest) (*pb.GetTrackResponse, error) {
	if req.GetTrackId() == "" {
		return nil, status.Error(codes.InvalidArgument, "track_id must not be empty")
	}

	plat, nativeID := parsePlatformID(req.GetTrackId())
	p, ok := providerMap[plat]
	if !ok {
		return &pb.GetTrackResponse{
			Status: pb.ResponseStatus_STATUS_ERROR,
			Error:  fmt.Sprintf("unknown platform in track_id: %q", req.GetTrackId()),
		}, nil
	}

	pCtx, cancel := withProviderTimeout(ctx)
	defer cancel()

	track, err := p.GetTrack(pCtx, nativeID)
	if err != nil {
		log.Printf("GetTrack [%s] id=%q error: %v", pb.Platform_name[int32(plat)], nativeID, err)
		return &pb.GetTrackResponse{
			Status: pb.ResponseStatus_STATUS_ERROR,
			Error:  err.Error(),
		}, nil
	}
	if track == nil {
		return &pb.GetTrackResponse{
			Status: pb.ResponseStatus_STATUS_ERROR,
			Error:  "track not found",
		}, nil
	}

	rewriteTrack(track, *proxyHost)

	return &pb.GetTrackResponse{
		Track:  track,
		Status: pb.ResponseStatus_STATUS_OK,
	}, nil
}

// get album: resolve platform and fetch album + tracks
func (s *bedrockServer) GetAlbum(ctx context.Context, req *pb.GetAlbumRequest) (*pb.GetAlbumResponse, error) {
	if req.GetAlbumId() == "" {
		return nil, status.Error(codes.InvalidArgument, "album_id must not be empty")
	}

	plat, nativeID := parsePlatformID(req.GetAlbumId())
	p, ok := providerMap[plat]
	if !ok {
		return &pb.GetAlbumResponse{
			Status: pb.ResponseStatus_STATUS_ERROR,
			Error:  fmt.Sprintf("unknown platform in album_id: %q", req.GetAlbumId()),
		}, nil
	}

	pCtx, cancel := withProviderTimeout(ctx)
	defer cancel()

	album, tracks, err := p.GetAlbum(pCtx, nativeID)
	if err != nil {
		log.Printf("GetAlbum [%s] id=%q error: %v", pb.Platform_name[int32(plat)], nativeID, err)
		return &pb.GetAlbumResponse{
			Status: pb.ResponseStatus_STATUS_ERROR,
			Error:  err.Error(),
		}, nil
	}
	if album == nil {
		return &pb.GetAlbumResponse{
			Status: pb.ResponseStatus_STATUS_ERROR,
			Error:  "album not found",
		}, nil
	}

	rewriteAlbum(album, tracks, *proxyHost)

	return &pb.GetAlbumResponse{
		Album:  album,
		Tracks: tracks,
		Status: pb.ResponseStatus_STATUS_OK,
	}, nil
}

// get artist: resolve platform and fetch profile + top tracks + albums
func (s *bedrockServer) GetArtist(ctx context.Context, req *pb.GetArtistRequest) (*pb.GetArtistResponse, error) {
	if req.GetArtistId() == "" {
		return nil, status.Error(codes.InvalidArgument, "artist_id must not be empty")
	}

	plat, nativeID := parsePlatformID(req.GetArtistId())
	p, ok := providerMap[plat]
	if !ok {
		return &pb.GetArtistResponse{
			Status: pb.ResponseStatus_STATUS_ERROR,
			Error:  fmt.Sprintf("unknown platform in artist_id: %q", req.GetArtistId()),
		}, nil
	}

	pCtx, cancel := withProviderTimeout(ctx)
	defer cancel()

	artist, topTracks, albums, err := p.GetArtist(pCtx, nativeID)
	if err != nil {
		log.Printf("GetArtist [%s] id=%q error: %v", pb.Platform_name[int32(plat)], nativeID, err)
		return &pb.GetArtistResponse{
			Status: pb.ResponseStatus_STATUS_ERROR,
			Error:  err.Error(),
		}, nil
	}
	if artist == nil {
		return &pb.GetArtistResponse{
			Status: pb.ResponseStatus_STATUS_ERROR,
			Error:  "artist not found",
		}, nil
	}

	rewriteArtist(artist, *proxyHost)
	for _, t := range topTracks {
		rewriteTrack(t, *proxyHost)
	}
	for _, a := range albums {
		rewriteAlbum(a, nil, *proxyHost)
	}

	return &pb.GetArtistResponse{
		Artist:    artist,
		TopTracks: topTracks,
		Albums:    albums,
		Status:    pb.ResponseStatus_STATUS_OK,
	}, nil
}

// get playlist: resolve platform and fetch playlist + tracks
func (s *bedrockServer) GetPlaylist(ctx context.Context, req *pb.GetPlaylistRequest) (*pb.GetPlaylistResponse, error) {
	if req.GetPlaylistId() == "" {
		return nil, status.Error(codes.InvalidArgument, "playlist_id must not be empty")
	}

	plat, nativeID := parsePlatformID(req.GetPlaylistId())
	p, ok := providerMap[plat]
	if !ok {
		return &pb.GetPlaylistResponse{
			Status: pb.ResponseStatus_STATUS_ERROR,
			Error:  fmt.Sprintf("unknown platform in playlist_id: %q", req.GetPlaylistId()),
		}, nil
	}

	pCtx, cancel := withProviderTimeout(ctx)
	defer cancel()

	playlist, tracks, err := p.GetPlaylist(pCtx, nativeID)
	if err != nil {
		log.Printf("GetPlaylist [%s] id=%q error: %v", pb.Platform_name[int32(plat)], nativeID, err)
		return &pb.GetPlaylistResponse{
			Status: pb.ResponseStatus_STATUS_ERROR,
			Error:  err.Error(),
		}, nil
	}
	if playlist == nil {
		return &pb.GetPlaylistResponse{
			Status: pb.ResponseStatus_STATUS_ERROR,
			Error:  "playlist not found",
		}, nil
	}

	rewritePlaylist(playlist, tracks, *proxyHost)

	return &pb.GetPlaylistResponse{
		Playlist: playlist,
		Tracks:   tracks,
		Status:   pb.ResponseStatus_STATUS_OK,
	}, nil
}

// bridge path routes spotify/deezer via soundcloud. other platforms go direct.
func (s *bedrockServer) GetStreamURL(ctx context.Context, req *pb.GetStreamURLRequest) (*pb.GetStreamURLResponse, error) {
	if req.GetTrackId() == "" {
		return nil, status.Error(codes.InvalidArgument, "track_id must not be empty")
	}

	trackID := req.GetTrackId()
	preferredFormat := req.GetPreferredFormat()

	// bridge/resolution path
	plat, _ := parsePlatformID(trackID)
	switch plat {
	case pb.Platform_PLATFORM_SOUNDCLOUD, pb.Platform_PLATFORM_SPOTIFY, pb.Platform_PLATFORM_DEEZER, pb.Platform_PLATFORM_YOUTUBE:
		// youtube may exhaust a stream pool then fall back to soundcloud; give extra time
		bridgeTimeout := providerTimeout * 3
		if plat == pb.Platform_PLATFORM_YOUTUBE {
			bridgeTimeout = providerTimeout * 6
		}
		bCtx, cancel := context.WithTimeout(ctx, bridgeTimeout)
		defer cancel()

		log.Printf("[GetStreamURL] resolve path | track_id=%q format=%q", trackID, preferredFormat)

		resp, err := resolveStreamURL(bCtx, trackID, preferredFormat)
		if err != nil {
			log.Printf("[GetStreamURL] bridge error | track_id=%q: %v", trackID, err)
			return &pb.GetStreamURLResponse{
				Status: pb.ResponseStatus_STATUS_ERROR,
				Error:  err.Error(),
			}, nil
		}
		if resp == nil || resp.GetStreamUrl() == "" {
			return &pb.GetStreamURLResponse{
				Status: pb.ResponseStatus_STATUS_ERROR,
				Error:  "no streamable source found",
			}, nil
		}

		service := strings.ToLower(strings.TrimPrefix(pb.Platform_name[int32(plat)], "PLATFORM_"))
		_, nativeID := parsePlatformID(trackID)
		resp.StreamUrl = fmt.Sprintf("http://%s/stream/%s/%s", *proxyHost, service, nativeID)

		resp.Status = pb.ResponseStatus_STATUS_OK
		return resp, nil
	}

	// legacy direct-provider path for other platforms.
	p, ok := providerMap[plat]
	if !ok {
		return &pb.GetStreamURLResponse{
			Status: pb.ResponseStatus_STATUS_ERROR,
			Error:  fmt.Sprintf("unknown platform in track_id: %q", trackID),
		}, nil
	}

	pCtx, cancel := withProviderTimeout(ctx)
	defer cancel()

	_, nativeID := parsePlatformID(trackID)
	log.Printf("[GetStreamURL] direct path | platform=%s id=%q format=%q",
		pb.Platform_name[int32(plat)], nativeID, preferredFormat)

	resp, err := p.GetStreamURL(pCtx, nativeID, preferredFormat)
	if err != nil {
		log.Printf("[GetStreamURL] direct error | platform=%s id=%q: %v",
			pb.Platform_name[int32(plat)], nativeID, err)
		return &pb.GetStreamURLResponse{
			Status: pb.ResponseStatus_STATUS_ERROR,
			Error:  err.Error(),
		}, nil
	}
	if resp == nil || resp.GetStreamUrl() == "" {
		return &pb.GetStreamURLResponse{
			Status: pb.ResponseStatus_STATUS_ERROR,
			Error:  "no streamable source found",
		}, nil
	}

	service := strings.ToLower(strings.TrimPrefix(pb.Platform_name[int32(plat)], "PLATFORM_"))
	resp.StreamUrl = fmt.Sprintf("http://%s/stream/%s/%s", *proxyHost, service, nativeID)

	resp.Status = pb.ResponseStatus_STATUS_OK
	return resp, nil
}

// get similar tracks: ask original provider first, then fan out to others to fill.
func (s *bedrockServer) GetSimilarTracks(ctx context.Context, req *pb.GetSimilarTracksRequest) (*pb.GetSimilarTracksResponse, error) {
	if req.GetTrackId() == "" {
		return nil, status.Error(codes.InvalidArgument, "track_id must not be empty")
	}

	limit := defaultLimit(req.GetLimit())
	plat, nativeID := parsePlatformID(req.GetTrackId())

	// phase 1: primary provider
	var primaryTracks []*pb.Track
	var provErrs []*pb.ProviderError

	if p, ok := providerMap[plat]; ok {
		pCtx, cancel := withProviderTimeout(ctx)
		tracks, err := p.GetSimilarTracks(pCtx, nativeID, limit)
		cancel()
		if err != nil {
			log.Printf("GetSimilarTracks [%s] id=%q error: %v", pb.Platform_name[int32(plat)], nativeID, err)
			provErrs = append(provErrs, &pb.ProviderError{Platform: plat, Message: err.Error()})
		} else {
			primaryTracks = tracks
		}
	}

	// if enough from primary, return early
	if len(primaryTracks) >= limit {
		return &pb.GetSimilarTracksResponse{
			Tracks: primaryTracks[:limit],
			Status: responseStatus(provErrs),
			Errors: provErrs,
		}, nil
	}

	// phase 2: fan out to other providers
	remaining := limit - len(primaryTracks)
	type result struct {
		tracks []*pb.Track
		err    *pb.ProviderError
	}

	var secondaryProviders []trackProvider
	for _, p := range allProviders {
		if p.Platform() != plat {
			secondaryProviders = append(secondaryProviders, p)
		}
	}

	secResults := make([]result, len(secondaryProviders))
	var wg sync.WaitGroup
	wg.Add(len(secondaryProviders))

	for i, p := range secondaryProviders {
		i, p := i, p
		go func() {
			defer wg.Done()
			pCtx, cancel := withProviderTimeout(ctx)
			defer cancel()

			// use original id as seed for secondaries
			tracks, err := p.GetSimilarTracks(pCtx, req.GetTrackId(), remaining)
			if err != nil {
				log.Printf("GetSimilarTracks secondary [%s] error: %v", pb.Platform_name[int32(p.Platform())], err)
				secResults[i].err = &pb.ProviderError{Platform: p.Platform(), Message: err.Error()}
				return
			}
			secResults[i].tracks = tracks
		}()
	}
	wg.Wait()

	allTracks := make([]*pb.Track, 0, limit)
	for _, t := range primaryTracks {
		rewriteTrack(t, *proxyHost)
		allTracks = append(allTracks, t)
	}
	for _, r := range secResults {
		for _, t := range r.tracks {
			rewriteTrack(t, *proxyHost)
			allTracks = append(allTracks, t)
			if len(allTracks) >= limit {
				break
			}
		}
		if r.err != nil {
			provErrs = append(provErrs, r.err)
		}
		if len(allTracks) >= limit {
			break
		}
	}

	if len(allTracks) > limit {
		allTracks = allTracks[:limit]
	}

	return &pb.GetSimilarTracksResponse{
		Tracks: allTracks,
		Status: responseStatus(provErrs),
		Errors: provErrs,
	}, nil
}

// get lyrics: need either track_id or title+artist.
// if track_id is given, fetches metadata first to get duration for better matching.
// fans out to lrclib and genius in parallel; lrclib (synced) wins unless preferred_source is genius.
func (s *bedrockServer) GetLyrics(ctx context.Context, req *pb.LyricsRequest) (*pb.LyricsResponse, error) {
	title := strings.TrimSpace(req.GetTitle())
	artist := strings.TrimSpace(req.GetArtist())
	durationS := int(req.GetDurationS())

	// if track_id provided, fetch metadata
	if req.GetTrackId() != "" {
		tResp, err := s.GetTrack(ctx, &pb.GetTrackRequest{TrackId: req.GetTrackId()})
		if err == nil && tResp.GetStatus() == pb.ResponseStatus_STATUS_OK {
			tr := tResp.GetTrack()
			if title == "" {
				title = tr.GetTitle()
			}
			if artist == "" {
				artist = tr.GetArtist()
			}
			if durationS == 0 {
				durationS = int(tr.GetDurationMs() / 1000)
			}
		}
	}

	if title == "" || artist == "" {
		return nil, status.Error(codes.InvalidArgument, "could not resolve title/artist for lyrics")
	}

	log.Printf("GetLyrics: title=%q artist=%q duration=%ds preferred=%s", title, artist, durationS, req.GetPreferredSource())

	preferred := req.GetPreferredSource()

	// if client wants genius only, skip lrclib
	if preferred == pb.LyricsSource_LYRICS_SOURCE_GENIUS {
		if s.geniusLyrics == nil {
			return &pb.LyricsResponse{
				Status: pb.ResponseStatus_STATUS_ERROR,
				Error:  "genius not configured (missing GENIUS_ACCESS_TOKEN)",
				Type:   pb.LyricsType_LYRICS_TYPE_NONE,
			}, nil
		}
		resp, err := s.geniusLyrics.getLyrics(ctx, title, artist)
		if err != nil {
			log.Printf("GetLyrics genius error: %v", err)
			return &pb.LyricsResponse{
				Status: pb.ResponseStatus_STATUS_ERROR,
				Error:  err.Error(),
				Type:   pb.LyricsType_LYRICS_TYPE_NONE,
			}, nil
		}
		return resp, nil
	}

	// fan out to both sources in parallel; lrclib result is preferred when synced
	type result struct {
		resp *pb.LyricsResponse
		err  error
	}

	lrcCh := make(chan result, 1)
	go func() {
		r, err := s.lyrics.getLyrics(ctx, title, artist, durationS)
		lrcCh <- result{r, err}
	}()

	// genius is optional — only fan out if client is configured
	var geniusCh chan result
	if s.geniusLyrics != nil {
		geniusCh = make(chan result, 1)
		go func() {
			r, err := s.geniusLyrics.getLyrics(ctx, title, artist)
			geniusCh <- result{r, err}
		}()
	}

	lrcRes := <-lrcCh

	var geniusRes result
	if geniusCh != nil {
		geniusRes = <-geniusCh
	}

	// lrclib wins when it returns synced lyrics
	if lrcRes.err == nil && lrcRes.resp != nil && lrcRes.resp.GetType() == pb.LyricsType_LYRICS_TYPE_SYNCED {
		return lrcRes.resp, nil
	}

	// if genius has lyrics and lrclib didn't find anything useful, return genius
	if geniusRes.err == nil && geniusRes.resp != nil && geniusRes.resp.GetType() == pb.LyricsType_LYRICS_TYPE_PLAIN {
		// still attach lrclib plain as fallback if genius has nothing but lrclib does
		return geniusRes.resp, nil
	}

	// fall back to whatever lrclib returned (plain or none)
	if lrcRes.err != nil {
		log.Printf("GetLyrics lrclib error: %v", lrcRes.err)
		return &pb.LyricsResponse{
			Status: pb.ResponseStatus_STATUS_ERROR,
			Error:  lrcRes.err.Error(),
			Type:   pb.LyricsType_LYRICS_TYPE_NONE,
		}, nil
	}

	return lrcRes.resp, nil
}

// get annotations: fetches genius community annotations for a song.
// requires genius to be configured (GENIUS_ACCESS_TOKEN env var).
func (s *bedrockServer) GetAnnotations(ctx context.Context, req *pb.AnnotationsRequest) (*pb.AnnotationsResponse, error) {
	if s.geniusLyrics == nil {
		return &pb.AnnotationsResponse{
			Status: pb.ResponseStatus_STATUS_ERROR,
			Error:  "genius not configured (missing GENIUS_ACCESS_TOKEN)",
		}, nil
	}

	title := strings.TrimSpace(req.GetTitle())
	artist := strings.TrimSpace(req.GetArtist())

	// resolve metadata from track_id if supplied
	if req.GetTrackId() != "" {
		tResp, err := s.GetTrack(ctx, &pb.GetTrackRequest{TrackId: req.GetTrackId()})
		if err == nil && tResp.GetStatus() == pb.ResponseStatus_STATUS_OK {
			tr := tResp.GetTrack()
			if title == "" {
				title = tr.GetTitle()
			}
			if artist == "" {
				artist = tr.GetArtist()
			}
		}
	}

	if title == "" || artist == "" {
		return nil, status.Error(codes.InvalidArgument, "could not resolve title/artist for annotations")
	}

	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}

	log.Printf("GetAnnotations: title=%q artist=%q limit=%d", title, artist, limit)

	// give extra time — this does search + referents api + page scrape
	aCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	resp, err := s.geniusLyrics.getAnnotations(aCtx, title, artist, limit)
	if err != nil {
		log.Printf("GetAnnotations error: %v", err)
		return &pb.AnnotationsResponse{
			Status: pb.ResponseStatus_STATUS_ERROR,
			Error:  err.Error(),
		}, nil
	}
	return resp, nil
}

// record play: logs a play event. event id generated server-side.
// in prod replace random id with uuid and persist to storage with dedupe.
func (s *bedrockServer) RecordPlay(ctx context.Context, req *pb.RecordPlayRequest) (*pb.RecordPlayResponse, error) {
	if req.GetTrackId() == "" {
		return nil, status.Error(codes.InvalidArgument, "track_id must not be empty")
	}

	eventID := uuid.New().String()

	log.Printf("RecordPlay: event_id=%s track_id=%q title=%q artist=%q duration_s=%d public=%v",
		eventID, req.GetTrackId(), req.GetTitle(), req.GetArtist(), req.GetDurationS(), req.GetIsPublic())

	// todo: persist event, add redis dedupe in prod
	return &pb.RecordPlayResponse{
		EventId: eventID,
		Status:  pb.ResponseStatus_STATUS_OK,
	}, nil
}

// get listening history: stub that supports limit/offset
func (s *bedrockServer) GetListeningHistory(ctx context.Context, req *pb.ListeningHistoryRequest) (*pb.ListeningHistoryResponse, error) {
	limit := req.GetLimit()
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	offset := req.GetOffset()
	if offset < 0 {
		offset = 0
	}

	log.Printf("GetListeningHistory: limit=%d offset=%d include_private=%v",
		limit, offset, req.GetIncludePrivate())

	// todo: query storage filtered by user from ctx metadata
	return &pb.ListeningHistoryResponse{
		Events: nil,
		Total:  0,
		Status: pb.ResponseStatus_STATUS_OK,
	}, nil
}

// get popular tracks: stub that would query play-count store and fan out metadata enrichment.
func (s *bedrockServer) GetPopularTracks(ctx context.Context, req *pb.PopularRequest) (*pb.PopularTracksResponse, error) {
	limit := req.GetLimit()
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	log.Printf("GetPopularTracks: limit=%d", limit)

	// todo: implement real logic
	return &pb.PopularTracksResponse{
		Items:  nil,
		Status: pb.ResponseStatus_STATUS_OK,
	}, nil
}

// get popular artists: stub
func (s *bedrockServer) GetPopularArtists(ctx context.Context, req *pb.PopularRequest) (*pb.PopularArtistsResponse, error) {
	limit := req.GetLimit()
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	log.Printf("GetPopularArtists: limit=%d", limit)

	// todo: implement real logic
	return &pb.PopularArtistsResponse{
		Items:  nil,
		Status: pb.ResponseStatus_STATUS_OK,
	}, nil
}

// infer platform from playlist/album url. returns unspecified on no match.
func importPlatformFromURL(raw string) (pb.Platform, error) {
	if strings.TrimSpace(raw) == "" {
		return pb.Platform_PLATFORM_UNSPECIFIED, fmt.Errorf("url must not be empty")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return pb.Platform_PLATFORM_UNSPECIFIED, fmt.Errorf("malformed url: %q", raw)
	}

	host := strings.ToLower(u.Host)
	host = strings.TrimPrefix(host, "www.")

	switch host {
	case "open.spotify.com":
		return pb.Platform_PLATFORM_SPOTIFY, nil
	case "music.yandex.ru", "music.yandex.com":
		return pb.Platform_PLATFORM_YANDEX, nil
	case "vk.com", "m.vk.com":
		return pb.Platform_PLATFORM_VK, nil
	case "deezer.com":
		return pb.Platform_PLATFORM_DEEZER, nil
	case "soundcloud.com":
		return pb.Platform_PLATFORM_SOUNDCLOUD, nil
	case "youtube.com", "music.youtube.com", "youtu.be":
		return pb.Platform_PLATFORM_YOUTUBE, nil
	default:
		return pb.Platform_PLATFORM_UNSPECIFIED, fmt.Errorf("unsupported platform host: %q", host)
	}
}

// import playlist: resolve a public playlist url and fetch it.
// platform_hint overrides url detection.
func (s *bedrockServer) ImportPlaylist(ctx context.Context, req *pb.ImportPlaylistRequest) (*pb.ImportPlaylistResponse, error) {
	if strings.TrimSpace(req.GetUrl()) == "" {
		return nil, status.Error(codes.InvalidArgument, "url must not be empty")
	}

	plat := req.GetPlatformHint()
	if plat == pb.Platform_PLATFORM_UNSPECIFIED {
		var err error
		plat, err = importPlatformFromURL(req.GetUrl())
		if err != nil {
			log.Printf("ImportPlaylist: url parse error: %v", err)
			return &pb.ImportPlaylistResponse{
				Status: pb.ResponseStatus_STATUS_ERROR,
				Error:  err.Error(),
			}, nil
		}
	}

	p, ok := providerMap[plat]
	if !ok {
		return &pb.ImportPlaylistResponse{
			Status: pb.ResponseStatus_STATUS_ERROR,
			Error:  fmt.Sprintf("no provider registered for platform: %s", pb.Platform_name[int32(plat)]),
		}, nil
	}

	log.Printf("ImportPlaylist: url=%q platform=%s", req.GetUrl(), pb.Platform_name[int32(plat)])

	// give more time for big playlists
	importCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	// todo: real providers should implement ImportPlaylist(ctx, url)
	playlist, tracks, err := p.GetPlaylist(importCtx, req.GetUrl())
	if err != nil {
		log.Printf("ImportPlaylist [%s] error: %v", pb.Platform_name[int32(plat)], err)
		return &pb.ImportPlaylistResponse{
			Status: pb.ResponseStatus_STATUS_ERROR,
			Error:  err.Error(),
		}, nil
	}

	// stub providers return nil - return empty but valid response
	if playlist == nil {
		playlist = &pb.Playlist{
			Source: plat,
		}
	}

	rewritePlaylist(playlist, tracks, *proxyHost)

	return &pb.ImportPlaylistResponse{
		Playlist:           playlist,
		Tracks:             tracks,
		Source:             plat,
		PlatformPlaylistId: playlist.GetPlatformId(),
		Status:             pb.ResponseStatus_STATUS_OK,
	}, nil
}

// get service status: health info for downstream deps.
// force_refresh probes live; otherwise we'd return cached (not implemented).
func (s *bedrockServer) GetServiceStatus(ctx context.Context, req *pb.ServiceStatusRequest) (*pb.ServiceStatusResponse, error) {
	log.Printf("GetServiceStatus: force_refresh=%v", req.GetForceRefresh())

	checkedAt := time.Now().UTC().Format(time.RFC3339)

	if !req.GetForceRefresh() {
		// todo: return cached status
		log.Printf("GetServiceStatus: cache miss - falling through to live probe")
	}

	// build probe targets: one per provider + lyrics sources
	type probeTarget struct {
		name     string
		platform pb.Platform
	}

	targets := make([]probeTarget, 0, len(allProviders)+2)
	for _, p := range allProviders {
		name := strings.ToLower(strings.TrimPrefix(pb.Platform_name[int32(p.Platform())], "PLATFORM_"))
		targets = append(targets, probeTarget{name: name, platform: p.Platform()})
	}
	targets = append(targets,
		probeTarget{name: "lrclib", platform: pb.Platform_PLATFORM_UNSPECIFIED},
		probeTarget{name: "genius", platform: pb.Platform_PLATFORM_UNSPECIFIED},
	)

	deps := make([]*pb.DependencyStatus, len(targets))
	var wg sync.WaitGroup
	wg.Add(len(targets))

	for i, t := range targets {
		i, t := i, t
		go func() {
			defer wg.Done()
			start := time.Now()

			// todo: replace with real lightweight probe per dependency
			health := pb.ServiceHealth_HEALTH_OK
			detail := "ok (stub - no real probe implemented)"
			latencyMs := int32(time.Since(start).Milliseconds())

			deps[i] = &pb.DependencyStatus{
				Name:      t.name,
				Health:    health,
				Detail:    detail,
				LatencyMs: latencyMs,
			}
		}()
	}
	wg.Wait()

	return &pb.ServiceStatusResponse{
		Dependencies: deps,
		CheckedAt:    checkedAt,
		Status:       pb.ResponseStatus_STATUS_OK,
	}, nil
}

// loadDotEnv tries a few places for a .env file and loads the first one found.
// locations: .env, bedrock_server/.env, executable dir/.env
func loadDotEnv() {
	seen := map[string]bool{}

	candidates := []string{
		".env",
		filepath.Join("bedrock_server", ".env"),
	}

	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		p := filepath.Join(filepath.Dir(exe), ".env")
		candidates = append(candidates, p)
	}

	var unique []string
	for _, p := range candidates {
		abs, err := filepath.Abs(p)
		if err != nil {
			abs = p
		}
		if !seen[abs] {
			seen[abs] = true
			unique = append(unique, p)
		}
	}

	for _, path := range unique {
		if _, err := os.Stat(path); err == nil {
			if err := godotenv.Load(path); err != nil {
				log.Printf("bedrock: failed to load .env from %q: %v", path, err)
			} else {
				log.Printf("bedrock: loaded environment from %q", path)
			}
			return
		}
	}

	log.Printf("bedrock: no .env file found in %v - relying on OS environment", unique)
}

func main() {
	flag.Parse()

	loadDotEnv()

	ctx := context.Background()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("bedrock: DATABASE_URL environment variable is not set")
	}

	// initialize the database connection pool
	dbPool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("bedrock: failed to connect to database: %v", err)
	}
	defer dbPool.Close()

	// verify the connection by pinging the database
	if err := dbPool.Ping(ctx); err != nil {
		log.Fatalf("bedrock: failed to ping database: %v", err)
	}
	log.Println("bedrock: successfully connected to the database")

	// init providers after env is loaded
	initProviders()

	// start proxy server in background
	go startProxyServer(*proxyAddr)

	addr := fmt.Sprintf(":%d", *port)
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("bedrock: failed to listen on %s: %v", addr, err)
	}

	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		log.Fatal("bedrock: JWT_SECRET environment variable is not set")
	}

	// public methods bypass JWT check — everything else requires a valid Bearer access token.
	publicMethods := map[string]bool{
		"/bedrock.BedrockService/Register":     true,
		"/bedrock.BedrockService/Login":        true,
		"/bedrock.BedrockService/RefreshToken": true,
	}

	authInterceptor := middleware.NewAuthInterceptor(newJWTManager(jwtSecret), publicMethods)

	grpcServer := grpc.NewServer(
		grpc.UnaryInterceptor(authInterceptor.Unary()),
		grpc.StreamInterceptor(authInterceptor.Stream()),
	)

	pb.RegisterBedrockServiceServer(grpcServer, &bedrockServer{
		lyrics:         newLrcClient(),
		geniusLyrics:   newGeniusClient(),
		userStore:      store.NewUserStore(dbPool),
		jwtManager:     newJWTManager(jwtSecret),
		refreshManager: newRefreshManager(jwtSecret),
	})

	log.Printf("bedrock: grpc server listening on %s", addr)

	// graceful shutdown on SIGINT/SIGTERM
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("bedrock: received signal %v, shutting down gracefully...", sig)
		grpcServer.GracefulStop()
	}()

	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("bedrock: server exited with error: %v", err)
	}
}
