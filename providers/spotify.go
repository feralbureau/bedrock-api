// spotify provider backed by the spotify-wrapper submodule.
//
// uses the private spotify partner api via feralbureau/spotify-wrapper.
// auth flows through the wrapper session/totp flow so client secrets stay hidden.
//
// supported methods:
//   - SearchTracks, GetTrack
//   - SearchArtists, GetArtist   (top-tracks via SearchTracks)
//   - GetAlbum  (partner API, full track list with cover/artist filled in)
//   - GetPlaylist
//   - GetSimilarTracks           (implemented via GetTrack + SearchTracks)
//   - SearchAlbums / SearchPlaylists — not yet exposed by the partner API; log+nil
//   - GetStreamURL — Spotify has no public audio stream (STATUS_ERROR, same as before)
package providers

import (
	"context"
	"fmt"
	"log"
	"strings"

	pb "github.com/feralbureau/bedrock-api/bedrock"

	spotapi "github.com/spotapi/spotapi-go/pkg/spotapi"
)

// spotifyProvider

// spotify provider implements trackProvider via feralbureau/spotify-wrapper.
type SpotifyProvider struct {
	c *spotapi.Client
}

// new spotify provider boots the tls-fingerprinted client.
// no other env vars are needed.
func NewSpotifyProvider() (*SpotifyProvider, error) {
	c, err := spotapi.NewClient("en")
	if err != nil {
		return nil, fmt.Errorf("spotify: init wrapper: %w", err)
	}
	return &SpotifyProvider{c: c}, nil
}

func (p *SpotifyProvider) Platform() pb.Platform {
	return pb.Platform_PLATFORM_SPOTIFY
}

// id helpers

// spNS returns a fully-namespaced Spotify ID, e.g. "spotify:track:<id>".
func spNS(kind, id string) string { return "spotify:" + kind + ":" + id }

// spBare strips any "spotify:<kind>:" prefix and returns the raw ID.
func spBare(id string) string {
	// handle spotify:track:xxx, spotify:xxx, or bare ids.
	parts := strings.SplitN(id, ":", 3)
	switch len(parts) {
	case 3:
		return parts[2]
	case 2:
		return parts[1]
	default:
		return id
	}
}

// mapping helpers

func mapWrapperTrack(t spotapi.Track) *pb.Track {
	artists := make([]*pb.Artist, 0, len(t.Artists))
	for i, name := range t.Artists {
		if name == "" {
			continue
		}
		var id string
		if i < len(t.ArtistIDs) {
			if bare := t.ArtistIDs[i]; bare != "" {
				id = spNS("artist", bare)
			}
		}
		artists = append(artists, &pb.Artist{
			Id:     id,
			Name:   name,
			Source: pb.Platform_PLATFORM_SPOTIFY,
		})
	}
	return &pb.Track{
		Id:          spNS("track", t.ID),
		PlatformId:  t.ID,
		Title:       t.Title,
		Artist:      t.Artist,
		Artists:     artists,
		AlbumTitle:  t.AlbumTitle,
		CoverUrl:    t.CoverURL,
		DurationMs:  int32(t.DurationMs),
		PreviewUrl:  t.PreviewURL,
		ExternalUrl: "https://open.spotify.com/track/" + t.ID,
		Source:      pb.Platform_PLATFORM_SPOTIFY,
	}
}

func mapWrapperArtist(a spotapi.Artist) *pb.Artist {
	return &pb.Artist{
		Id:          spNS("artist", a.ID),
		Name:        a.Name,
		ImageUrl:    a.AvatarURL,
		Followers:   a.Followers,
		ExternalUrl: "https://open.spotify.com/artist/" + a.ID,
		Source:      pb.Platform_PLATFORM_SPOTIFY,
	}
}

func mapWrapperAlbum(al spotapi.Album) *pb.Album {
	return &pb.Album{
		Id:          spNS("album", al.ID),
		PlatformId:  al.ID,
		Title:       al.Title,
		Artist:      al.Artist,
		CoverUrl:    al.CoverURL,
		TotalTracks: int32(al.TotalTracks),
		ReleaseDate: al.ReleaseDate,
		ExternalUrl: "https://open.spotify.com/album/" + al.ID,
		Source:      pb.Platform_PLATFORM_SPOTIFY,
	}
}

func mapWrapperPlaylist(pl spotapi.Playlist) *pb.Playlist {
	return &pb.Playlist{
		Id:          spNS("playlist", pl.ID),
		PlatformId:  pl.ID,
		Title:       pl.Title,
		Description: pl.Description,
		CoverUrl:    pl.CoverURL,
		TotalTracks: int32(pl.TotalTracks),
		Owner:       pl.Owner,
		ExternalUrl: "https://open.spotify.com/playlist/" + pl.ID,
		Source:      pb.Platform_PLATFORM_SPOTIFY,
	}
}

// search

func (p *SpotifyProvider) SearchTracks(_ context.Context, query string, limit int) ([]*pb.Track, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	tracks, err := p.c.SearchTracks(query, limit, 0)
	if err != nil {
		return nil, fmt.Errorf("spotify: SearchTracks: %w", err)
	}
	out := make([]*pb.Track, 0, len(tracks))
	for _, t := range tracks {
		out = append(out, mapWrapperTrack(t))
	}
	return out, nil
}

// search albums hits searchv2.albumsv2 on the partner api.
func (p *SpotifyProvider) SearchAlbums(_ context.Context, query string, limit int) ([]*pb.Album, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	albums, err := p.c.SearchAlbums(query, limit, 0)
	if err != nil {
		return nil, fmt.Errorf("spotify: SearchAlbums: %w", err)
	}
	out := make([]*pb.Album, 0, len(albums))
	for _, al := range albums {
		out = append(out, mapWrapperAlbum(al))
	}
	return out, nil
}

func (p *SpotifyProvider) SearchArtists(_ context.Context, query string, limit int) ([]*pb.Artist, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	artists, err := p.c.SearchArtists(query, limit, 0)
	if err != nil {
		return nil, fmt.Errorf("spotify: SearchArtists: %w", err)
	}
	out := make([]*pb.Artist, 0, len(artists))
	for _, a := range artists {
		out = append(out, mapWrapperArtist(a))
	}
	return out, nil
}

// search playlists hits searchv2.playlists on the partner api.
func (p *SpotifyProvider) SearchPlaylists(_ context.Context, query string, limit int) ([]*pb.Playlist, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	playlists, err := p.c.SearchPlaylists(query, limit, 0)
	if err != nil {
		return nil, fmt.Errorf("spotify: SearchPlaylists: %w", err)
	}
	out := make([]*pb.Playlist, 0, len(playlists))
	for _, pl := range playlists {
		out = append(out, mapWrapperPlaylist(pl))
	}
	return out, nil
}

// get single items

func (p *SpotifyProvider) GetTrack(_ context.Context, platformID string) (*pb.Track, error) {
	id := spBare(platformID)
	t, err := p.c.GetTrack(id)
	if err != nil {
		return nil, fmt.Errorf("spotify: GetTrack %s: %w", id, err)
	}
	return mapWrapperTrack(*t), nil
}

func (p *SpotifyProvider) GetAlbum(_ context.Context, platformID string) (*pb.Album, []*pb.Track, error) {
	id := spBare(platformID)
	al, err := p.c.GetAlbum(id, 300, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("spotify: GetAlbum %s: %w", id, err)
	}

	pbAlbum := &pb.Album{
		Id:          spNS("album", al.ID),
		PlatformId:  al.ID,
		Title:       al.Title,
		Artist:      al.Artist,
		CoverUrl:    al.CoverURL,
		TotalTracks: int32(al.TotalTracks),
		ReleaseDate: al.ReleaseDate,
		ExternalUrl: "https://open.spotify.com/album/" + al.ID,
		Source:      pb.Platform_PLATFORM_SPOTIFY,
	}

	tracks := make([]*pb.Track, 0, len(al.Tracks))
	for _, t := range al.Tracks {
		tracks = append(tracks, mapWrapperTrack(t))
	}
	return pbAlbum, tracks, nil
}

func (p *SpotifyProvider) GetArtist(_ context.Context, platformID string) (*pb.Artist, []*pb.Track, []*pb.Album, error) {
	id := spBare(platformID)

	artist, discAlbums, err := p.c.GetArtist(id)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("spotify: GetArtist %s: %w", id, err)
	}
	pbArtist := mapWrapperArtist(*artist)

	// top tracks reuse search tracks on the artist name query.
	// the partner api has no artist-top-tracks endpoint.
	var pbTracks []*pb.Track
	if artist.Name != "" {
		tracks, tErr := p.c.SearchTracks(artist.Name, 10, 0)
		if tErr != nil {
			log.Printf("[spotify] GetArtist %s: top-tracks search: %v", id, tErr)
		} else {
			pbTracks = make([]*pb.Track, 0, len(tracks))
			for _, t := range tracks {
				pbTracks = append(pbTracks, mapWrapperTrack(t))
			}
		}
	}

	var pbAlbums []*pb.Album
	if len(discAlbums) > 0 {
		pbAlbums = make([]*pb.Album, 0, len(discAlbums))
		for _, al := range discAlbums {
			pbAlbums = append(pbAlbums, mapWrapperAlbum(al))
		}
	}

	return pbArtist, pbTracks, pbAlbums, nil
}

func (p *SpotifyProvider) GetPlaylist(_ context.Context, platformID string) (*pb.Playlist, []*pb.Track, error) {
	id := spBare(platformID)
	// strip open.spotify.com URL if the caller passed a full URL
	if idx := strings.LastIndex(id, "/"); idx >= 0 {
		id = id[idx+1:]
		if q := strings.IndexByte(id, '?'); q >= 0 {
			id = id[:q]
		}
	}

	pl, err := p.c.GetPlaylist(id, 100, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("spotify: GetPlaylist %s: %w", id, err)
	}

	pbPlaylist := &pb.Playlist{
		Id:          spNS("playlist", pl.ID),
		PlatformId:  pl.ID,
		Title:       pl.Title,
		Description: pl.Description,
		CoverUrl:    pl.CoverURL,
		TotalTracks: int32(pl.TotalTracks),
		Owner:       pl.Owner,
		ExternalUrl: "https://open.spotify.com/playlist/" + pl.ID,
		Source:      pb.Platform_PLATFORM_SPOTIFY,
	}

	tracks := make([]*pb.Track, 0, len(pl.Tracks))
	for _, t := range pl.Tracks {
		tracks = append(tracks, mapWrapperTrack(t))
	}
	return pbPlaylist, tracks, nil
}

// stream

// getstreamurl always returns status error because spotify has no public stream.
// the resolver routes spotify tracks through soundcloud metadata.
func (p *SpotifyProvider) GetStreamURL(_ context.Context, _ string, _ string) (*pb.GetStreamURLResponse, error) {
	return &pb.GetStreamURLResponse{
		Source: pb.Platform_PLATFORM_SPOTIFY,
		Status: pb.ResponseStatus_STATUS_ERROR,
		Error:  "spotify: provider does not support streaming directly",
	}, nil
}

// similar tracks

// getsimilartracks resolves the seed then searches artist plus title for related tracks while skipping the seed itself.
func (p *SpotifyProvider) GetSimilarTracks(ctx context.Context, platformID string, limit int) ([]*pb.Track, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	seed, err := p.GetTrack(ctx, platformID)
	if err != nil {
		return nil, fmt.Errorf("spotify: GetSimilarTracks: seed: %w", err)
	}

	query := seed.Artist + " " + seed.Title
	candidates, err := p.c.SearchTracks(query, limit+5, 0)
	if err != nil {
		return nil, fmt.Errorf("spotify: GetSimilarTracks: search: %w", err)
	}

	rawSeed := spBare(seed.Id)
	seen := map[string]struct{}{rawSeed: {}}
	out := make([]*pb.Track, 0, limit)
	for _, t := range candidates {
		if len(out) >= limit {
			break
		}
		if _, dup := seen[t.ID]; dup {
			continue
		}
		seen[t.ID] = struct{}{}
		out = append(out, mapWrapperTrack(t))
	}
	return out, nil
}
