package providers

// deezer provider for the bedrock grpc aggregator.
// simple, lowercase comments; replace with more detail only when needed.
//
// auth: none for public deezer api.
// methods: search, get single items (track/album/artist/playlist),
//          similar tracks (artist top-tracks + album-first-tracks + search),
//          get stream url signals that deezer doesn't provide full stream.
//
// note: deezer public api exposes 30s previews only. getstreamurl returns
// an error so the server bridge can route to soundcloud for full streams.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	pb "github.com/feralbureau/bedrock-api/bedrock"
)

// sentinel errors
var (
	ErrDeezerNotFound = errors.New("deezer: resource not found (404)")
	ErrDeezerNoStream = errors.New("deezer: provider does not support full-length streaming via public API")
	ErrDeezerAPI      = errors.New("deezer: api error")
)

// constants
const (
	dzAPIBase     = "https://api.deezer.com"
	dzHTTPTimeout = 10 * time.Second

	// deezer paginates at 25 by default; cap at 100 (hard limit per page)
	dzMaxLimit = 100
)

// internal json structs
// only fields we use are declared; decoder ignores the rest

type dzError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Code    int    `json:"code"`
}

type dzPage[T any] struct {
	Data  []T      `json:"data"`
	Total int      `json:"total"`
	Next  *string  `json:"next"`
	Error *dzError `json:"error"`
}

type dzArtistSimple struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Picture string `json:"picture_xl"`
	Link    string `json:"link"`
}

type dzTrack struct {
	ID         int64          `json:"id"`
	Title      string         `json:"title"`
	Duration   int32          `json:"duration"` // seconds
	Rank       int32          `json:"rank"`
	PreviewURL string         `json:"preview"`
	Link       string         `json:"link"`
	Artist     dzArtistSimple `json:"artist"`
	Album      dzAlbumSimple  `json:"album"`
	Error      *dzError       `json:"error"`
}

type dzAlbumSimple struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	CoverXL  string `json:"cover_xl"`
	CoverBig string `json:"cover_big"`
	CoverMed string `json:"cover_medium"`
	Link     string `json:"link"`
}

type dzAlbumFull struct {
	ID          int64           `json:"id"`
	Title       string          `json:"title"`
	AlbumType   string          `json:"record_type"`
	CoverXL     string          `json:"cover_xl"`
	CoverBig    string          `json:"cover_big"`
	CoverMed    string          `json:"cover_medium"`
	ReleaseDate string          `json:"release_date"`
	NbTracks    int32           `json:"nb_tracks"`
	Link        string          `json:"link"`
	Artist      dzArtistSimple  `json:"artist"`
	Tracks      dzPage[dzTrack] `json:"tracks"`
	Error       *dzError        `json:"error"`
}

type dzArtistFull struct {
	ID      int64    `json:"id"`
	Name    string   `json:"name"`
	PicXL   string   `json:"picture_xl"`
	PicBig  string   `json:"picture_big"`
	PicMed  string   `json:"picture_medium"`
	NbAlbum int32    `json:"nb_album"`
	NbFan   int64    `json:"nb_fan"`
	Link    string   `json:"link"`
	Error   *dzError `json:"error"`
}

type dzAlbumItem struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	RecordType  string `json:"record_type"`
	CoverXL     string `json:"cover_xl"`
	CoverBig    string `json:"cover_big"`
	CoverMed    string `json:"cover_medium"`
	ReleaseDate string `json:"release_date"`
	NbTracks    int32  `json:"nb_tracks"`
	Link        string `json:"link"`
}

type dzPlaylistFull struct {
	ID       int64           `json:"id"`
	Title    string          `json:"title"`
	Desc     string          `json:"description"`
	PicXL    string          `json:"picture_xl"`
	PicBig   string          `json:"picture_big"`
	PicMed   string          `json:"picture_medium"`
	NbTracks int32           `json:"nb_tracks"`
	Link     string          `json:"link"`
	Creator  dzCreator       `json:"creator"`
	Tracks   dzPage[dzTrack] `json:"tracks"`
	Error    *dzError        `json:"error"`
}

type dzCreator struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type dzSearchArtist struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	PicXL  string `json:"picture_xl"`
	PicBig string `json:"picture_big"`
	PicMed string `json:"picture_medium"`
	NbFan  int64  `json:"nb_fan"`
	Link   string `json:"link"`
}

type dzSearchAlbum struct {
	ID          int64          `json:"id"`
	Title       string         `json:"title"`
	RecordType  string         `json:"record_type"`
	CoverXL     string         `json:"cover_xl"`
	CoverBig    string         `json:"cover_big"`
	CoverMed    string         `json:"cover_medium"`
	ReleaseDate string         `json:"release_date"`
	NbTracks    int32          `json:"nb_tracks"`
	Link        string         `json:"link"`
	Artist      dzArtistSimple `json:"artist"`
}

type dzSearchPlaylist struct {
	ID       int64     `json:"id"`
	Title    string    `json:"title"`
	PicXL    string    `json:"picture_xl"`
	PicBig   string    `json:"picture_big"`
	PicMed   string    `json:"picture_medium"`
	NbTracks int32     `json:"nb_tracks"`
	Link     string    `json:"link"`
	User     dzCreator `json:"user"`
}

// provider struct

type DeezerProvider struct {
	client *http.Client
	// mu is reserved for future rate-limit / token state
}

func NewDeezerProvider() *DeezerProvider {
	return &DeezerProvider{
		client: &http.Client{Timeout: dzHTTPTimeout},
	}
}

func (p *DeezerProvider) Platform() pb.Platform {
	return pb.Platform_PLATFORM_DEEZER
}

// http helper
// endpoint must start with "/" (e.g. "/track/123").
// params may be nil.
// deezer embeds errors inside a 200 body in {"error":{...}} so we decode that too.
func (p *DeezerProvider) doRequest(ctx context.Context, endpoint string, params url.Values) (*http.Response, error) {
	rawURL := dzAPIBase + endpoint
	if len(params) > 0 {
		rawURL += "?" + params.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("deezer: build request %s: %w", endpoint, err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("deezer: %s: %w", endpoint, err)
	}

	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, fmt.Errorf("deezer: %s: %w", endpoint, ErrDeezerNotFound)
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		return nil, fmt.Errorf("deezer: %s: http %d: %s", endpoint, resp.StatusCode, string(body))
	}

	return resp, nil
}

func decodeDeezer(resp *http.Response, dst any) error {
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("deezer: json decode: %w", err)
	}
	return nil
}

// id helpers

func dzNamespacedID(id int64) string {
	return "deezer:" + strconv.FormatInt(id, 10)
}

func dzStripPrefix(id string) string {
	return strings.TrimPrefix(id, "deezer:")
}

// image helpers

func dzBestCover(xl, big, med string) string {
	if xl != "" {
		return xl
	}
	if big != "" {
		return big
	}
	return med
}

func dzBestPicture(xl, big, med string) string {
	return dzBestCover(xl, big, med)
}

// mapping helpers

func mapDzTrack(t *dzTrack) *pb.Track {
	title := t.Title
	if title == "" {
		title = "Unknown Title"
	}
	artist := t.Artist.Name
	if artist == "" {
		artist = "Unknown Artist"
	}
	artists := []*pb.Artist{
		{
			Id:          dzNamespacedID(t.Artist.ID),
			Name:        artist,
			ImageUrl:    dzBestPicture(t.Artist.Picture, "", ""),
			ExternalUrl: t.Artist.Link,
			Source:      pb.Platform_PLATFORM_DEEZER,
		},
	}
	cover := dzBestCover(t.Album.CoverXL, t.Album.CoverBig, t.Album.CoverMed)

	return &pb.Track{
		Id:           dzNamespacedID(t.ID),
		PlatformId:   strconv.FormatInt(t.ID, 10),
		Title:        title,
		Artist:       artist,
		Artists:      artists,
		AlbumTitle:   t.Album.Title,
		CoverUrl:     cover,
		DurationMs:   t.Duration * 1000, // deezer stores seconds
		PreviewUrl:   t.PreviewURL,
		ExternalUrl:  t.Link,
		Popularity:   t.Rank,
		IsStreamable: false, // full stream requires auth; bridge handles it
		Source:       pb.Platform_PLATFORM_DEEZER,
	}
}

func mapDzAlbumFull(a *dzAlbumFull) *pb.Album {
	albumType := a.AlbumType
	if albumType == "" {
		albumType = "album"
	}
	return &pb.Album{
		Id:          dzNamespacedID(a.ID),
		PlatformId:  strconv.FormatInt(a.ID, 10),
		Title:       a.Title,
		Artist:      a.Artist.Name,
		CoverUrl:    dzBestCover(a.CoverXL, a.CoverBig, a.CoverMed),
		TotalTracks: a.NbTracks,
		ReleaseDate: a.ReleaseDate,
		ExternalUrl: a.Link,
		AlbumType:   albumType,
		Source:      pb.Platform_PLATFORM_DEEZER,
	}
}

func mapDzSearchAlbum(a *dzSearchAlbum) *pb.Album {
	albumType := a.RecordType
	if albumType == "" {
		albumType = "album"
	}
	return &pb.Album{
		Id:          dzNamespacedID(a.ID),
		PlatformId:  strconv.FormatInt(a.ID, 10),
		Title:       a.Title,
		Artist:      a.Artist.Name,
		CoverUrl:    dzBestCover(a.CoverXL, a.CoverBig, a.CoverMed),
		TotalTracks: a.NbTracks,
		ReleaseDate: a.ReleaseDate,
		ExternalUrl: a.Link,
		AlbumType:   albumType,
		Source:      pb.Platform_PLATFORM_DEEZER,
	}
}

func mapDzAlbumItem(a *dzAlbumItem, artistName string) *pb.Album {
	albumType := a.RecordType
	if albumType == "" {
		albumType = "album"
	}
	return &pb.Album{
		Id:          dzNamespacedID(a.ID),
		PlatformId:  strconv.FormatInt(a.ID, 10),
		Title:       a.Title,
		Artist:      artistName,
		CoverUrl:    dzBestCover(a.CoverXL, a.CoverBig, a.CoverMed),
		TotalTracks: a.NbTracks,
		ReleaseDate: a.ReleaseDate,
		ExternalUrl: a.Link,
		AlbumType:   albumType,
		Source:      pb.Platform_PLATFORM_DEEZER,
	}
}

func mapDzArtistFull(a *dzArtistFull) *pb.Artist {
	name := a.Name
	if name == "" {
		name = "Unknown Artist"
	}
	return &pb.Artist{
		Id:          dzNamespacedID(a.ID),
		Name:        name,
		ImageUrl:    dzBestPicture(a.PicXL, a.PicBig, a.PicMed),
		Followers:   a.NbFan,
		ExternalUrl: a.Link,
		Source:      pb.Platform_PLATFORM_DEEZER,
	}
}

func mapDzSearchArtist(a *dzSearchArtist) *pb.Artist {
	name := a.Name
	if name == "" {
		name = "Unknown Artist"
	}
	return &pb.Artist{
		Id:          dzNamespacedID(a.ID),
		Name:        name,
		ImageUrl:    dzBestPicture(a.PicXL, a.PicBig, a.PicMed),
		Followers:   a.NbFan,
		ExternalUrl: a.Link,
		Source:      pb.Platform_PLATFORM_DEEZER,
	}
}

func mapDzSearchPlaylist(pl *dzSearchPlaylist) *pb.Playlist {
	owner := pl.User.Name
	if owner == "" {
		owner = strconv.FormatInt(pl.User.ID, 10)
	}
	return &pb.Playlist{
		Id:          dzNamespacedID(pl.ID),
		PlatformId:  strconv.FormatInt(pl.ID, 10),
		Title:       pl.Title,
		CoverUrl:    dzBestPicture(pl.PicXL, pl.PicBig, pl.PicMed),
		TotalTracks: pl.NbTracks,
		Owner:       owner,
		ExternalUrl: pl.Link,
		Source:      pb.Platform_PLATFORM_DEEZER,
	}
}

func mapDzPlaylistFull(pl *dzPlaylistFull) *pb.Playlist {
	owner := pl.Creator.Name
	if owner == "" {
		owner = strconv.FormatInt(pl.Creator.ID, 10)
	}
	return &pb.Playlist{
		Id:          dzNamespacedID(pl.ID),
		PlatformId:  strconv.FormatInt(pl.ID, 10),
		Title:       pl.Title,
		Description: pl.Desc,
		CoverUrl:    dzBestPicture(pl.PicXL, pl.PicBig, pl.PicMed),
		TotalTracks: pl.NbTracks,
		Owner:       owner,
		ExternalUrl: pl.Link,
		Source:      pb.Platform_PLATFORM_DEEZER,
	}
}

// search methods

func (p *DeezerProvider) SearchTracks(ctx context.Context, query string, limit int) ([]*pb.Track, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if limit <= 0 || limit > dzMaxLimit {
		limit = 20
	}

	resp, err := p.doRequest(ctx, "/search/track", url.Values{
		"q":     {query},
		"limit": {strconv.Itoa(limit)},
	})
	if err != nil {
		return nil, fmt.Errorf("deezer: SearchTracks: %w", err)
	}

	var page dzPage[dzTrack]
	if err := decodeDeezer(resp, &page); err != nil {
		return nil, err
	}
	if page.Error != nil {
		return nil, fmt.Errorf("deezer: SearchTracks: api error %d: %s", page.Error.Code, page.Error.Message)
	}

	out := make([]*pb.Track, 0, len(page.Data))
	for i := range page.Data {
		out = append(out, mapDzTrack(&page.Data[i]))
	}
	return out, nil
}

func (p *DeezerProvider) SearchAlbums(ctx context.Context, query string, limit int) ([]*pb.Album, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if limit <= 0 || limit > dzMaxLimit {
		limit = 20
	}

	resp, err := p.doRequest(ctx, "/search/album", url.Values{
		"q":     {query},
		"limit": {strconv.Itoa(limit)},
	})
	if err != nil {
		return nil, fmt.Errorf("deezer: SearchAlbums: %w", err)
	}

	var page dzPage[dzSearchAlbum]
	if err := decodeDeezer(resp, &page); err != nil {
		return nil, err
	}
	if page.Error != nil {
		return nil, fmt.Errorf("deezer: SearchAlbums: api error %d: %s", page.Error.Code, page.Error.Message)
	}

	out := make([]*pb.Album, 0, len(page.Data))
	for i := range page.Data {
		out = append(out, mapDzSearchAlbum(&page.Data[i]))
	}
	return out, nil
}

func (p *DeezerProvider) SearchArtists(ctx context.Context, query string, limit int) ([]*pb.Artist, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if limit <= 0 || limit > dzMaxLimit {
		limit = 20
	}

	resp, err := p.doRequest(ctx, "/search/artist", url.Values{
		"q":     {query},
		"limit": {strconv.Itoa(limit)},
	})
	if err != nil {
		return nil, fmt.Errorf("deezer: SearchArtists: %w", err)
	}

	var page dzPage[dzSearchArtist]
	if err := decodeDeezer(resp, &page); err != nil {
		return nil, err
	}
	if page.Error != nil {
		return nil, fmt.Errorf("deezer: SearchArtists: api error %d: %s", page.Error.Code, page.Error.Message)
	}

	out := make([]*pb.Artist, 0, len(page.Data))
	for i := range page.Data {
		out = append(out, mapDzSearchArtist(&page.Data[i]))
	}
	return out, nil
}

func (p *DeezerProvider) SearchPlaylists(ctx context.Context, query string, limit int) ([]*pb.Playlist, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if limit <= 0 || limit > dzMaxLimit {
		limit = 20
	}

	resp, err := p.doRequest(ctx, "/search/playlist", url.Values{
		"q":     {query},
		"limit": {strconv.Itoa(limit)},
	})
	if err != nil {
		return nil, fmt.Errorf("deezer: SearchPlaylists: %w", err)
	}

	var page dzPage[dzSearchPlaylist]
	if err := decodeDeezer(resp, &page); err != nil {
		return nil, err
	}
	if page.Error != nil {
		return nil, fmt.Errorf("deezer: SearchPlaylists: api error %d: %s", page.Error.Code, page.Error.Message)
	}

	out := make([]*pb.Playlist, 0, len(page.Data))
	for i := range page.Data {
		out = append(out, mapDzSearchPlaylist(&page.Data[i]))
	}
	return out, nil
}

// get single items

func (p *DeezerProvider) GetTrack(ctx context.Context, platformID string) (*pb.Track, error) {
	nativeID := dzStripPrefix(platformID)

	resp, err := p.doRequest(ctx, "/track/"+nativeID, nil)
	if err != nil {
		return nil, fmt.Errorf("deezer: GetTrack %s: %w", nativeID, err)
	}

	var t dzTrack
	if err := decodeDeezer(resp, &t); err != nil {
		return nil, err
	}
	if t.Error != nil {
		return nil, fmt.Errorf("deezer: GetTrack %s: api error %d: %s", nativeID, t.Error.Code, t.Error.Message)
	}
	if t.ID == 0 {
		return nil, fmt.Errorf("deezer: GetTrack %s: %w", nativeID, ErrDeezerNotFound)
	}
	return mapDzTrack(&t), nil
}

// get album returns album metadata and its first page of tracks.
// deezer embeds up to 50 tracks in /album/{id} response.
func (p *DeezerProvider) GetAlbum(ctx context.Context, platformID string) (*pb.Album, []*pb.Track, error) {
	nativeID := dzStripPrefix(platformID)

	resp, err := p.doRequest(ctx, "/album/"+nativeID, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("deezer: GetAlbum %s: %w", nativeID, err)
	}

	var a dzAlbumFull
	if err := decodeDeezer(resp, &a); err != nil {
		return nil, nil, err
	}
	if a.Error != nil {
		return nil, nil, fmt.Errorf("deezer: GetAlbum %s: api error %d: %s", nativeID, a.Error.Code, a.Error.Message)
	}
	if a.ID == 0 {
		return nil, nil, fmt.Errorf("deezer: GetAlbum %s: %w", nativeID, ErrDeezerNotFound)
	}

	tracks := make([]*pb.Track, 0, len(a.Tracks.Data))
	for i := range a.Tracks.Data {
		t := &a.Tracks.Data[i]
		// album-embedded track stubs lack album cover; graft it from parent
		if t.Album.CoverXL == "" {
			t.Album.CoverXL = a.CoverXL
			t.Album.CoverBig = a.CoverBig
			t.Album.CoverMed = a.CoverMed
			t.Album.Title = a.Title
		}
		tracks = append(tracks, mapDzTrack(t))
	}
	return mapDzAlbumFull(&a), tracks, nil
}

// get artist: profile + top tracks + albums. runs three goroutines and collects.
func (p *DeezerProvider) GetArtist(ctx context.Context, platformID string) (*pb.Artist, []*pb.Track, []*pb.Album, error) {
	nativeID := dzStripPrefix(platformID)

	type artistResult struct {
		artist *pb.Artist
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

	// goroutine 1: full artist profile
	go func() {
		resp, err := p.doRequest(ctx, "/artist/"+nativeID, nil)
		if err != nil {
			artistCh <- artistResult{err: err}
			return
		}
		var a dzArtistFull
		if err := decodeDeezer(resp, &a); err != nil {
			artistCh <- artistResult{err: err}
			return
		}
		if a.Error != nil {
			artistCh <- artistResult{err: fmt.Errorf("api error %d: %s", a.Error.Code, a.Error.Message)}
			return
		}
		artistCh <- artistResult{artist: mapDzArtistFull(&a)}
	}()

	// goroutine 2: top tracks
	go func() {
		resp, err := p.doRequest(ctx, "/artist/"+nativeID+"/top", url.Values{"limit": {"20"}})
		if err != nil {
			tracksCh <- tracksResult{err: err}
			return
		}
		var page dzPage[dzTrack]
		if err := decodeDeezer(resp, &page); err != nil {
			tracksCh <- tracksResult{err: err}
			return
		}
		out := make([]*pb.Track, 0, len(page.Data))
		for i := range page.Data {
			out = append(out, mapDzTrack(&page.Data[i]))
		}
		tracksCh <- tracksResult{tracks: out}
	}()

	// goroutine 3: albums (first page)
	go func() {
		resp, err := p.doRequest(ctx, "/artist/"+nativeID+"/albums", url.Values{"limit": {"25"}})
		if err != nil {
			albumsCh <- albumsResult{err: err}
			return
		}
		var page dzPage[dzAlbumItem]
		if err := decodeDeezer(resp, &page); err != nil {
			albumsCh <- albumsResult{err: err}
			return
		}

		out := make([]*pb.Album, 0, len(page.Data))
		for i := range page.Data {
			out = append(out, mapDzAlbumItem(&page.Data[i], ""))
		}
		albumsCh <- albumsResult{albums: out}
	}()

	// collect results
	ar := <-artistCh
	if ar.err != nil {
		<-tracksCh
		<-albumsCh
		return nil, nil, nil, fmt.Errorf("deezer: GetArtist %s: %w", nativeID, ar.err)
	}

	tr := <-tracksCh
	if tr.err != nil {
		log.Printf("[deezer] GetArtist %s: top-tracks: %v", nativeID, tr.err)
	}

	alr := <-albumsCh
	if alr.err != nil {
		log.Printf("[deezer] GetArtist %s: albums: %v", nativeID, alr.err)
	}

	// back-fill artist name into albums
	artistName := ar.artist.GetName()
	for _, alb := range alr.albums {
		if alb.Artist == "" {
			alb.Artist = artistName
		}
	}

	return ar.artist, tr.tracks, alr.albums, nil
}

// get playlist and its tracks. request max 100 to reduce extra pages.
func (p *DeezerProvider) GetPlaylist(ctx context.Context, platformID string) (*pb.Playlist, []*pb.Track, error) {
	nativeID := dzStripPrefix(platformID)

	resp, err := p.doRequest(ctx, "/playlist/"+nativeID, url.Values{
		"limit": {"100"},
		"index": {"0"},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("deezer: GetPlaylist %s: %w", nativeID, err)
	}

	var pl dzPlaylistFull
	if err := decodeDeezer(resp, &pl); err != nil {
		return nil, nil, err
	}
	if pl.Error != nil {
		return nil, nil, fmt.Errorf("deezer: GetPlaylist %s: api error %d: %s", nativeID, pl.Error.Code, pl.Error.Message)
	}
	if pl.ID == 0 {
		return nil, nil, fmt.Errorf("deezer: GetPlaylist %s: %w", nativeID, ErrDeezerNotFound)
	}

	tracks := make([]*pb.Track, 0, len(pl.Tracks.Data))
	for i := range pl.Tracks.Data {
		tracks = append(tracks, mapDzTrack(&pl.Tracks.Data[i]))
	}
	return mapDzPlaylistFull(&pl), tracks, nil
}

// stream url
// deezer doesn't provide full stream via public api.
// return ErrDeezerNoStream so server bridge can route to soundcloud.
func (p *DeezerProvider) GetStreamURL(_ context.Context, platformID string, _ string) (*pb.GetStreamURLResponse, error) {
	log.Printf("[deezer] GetStreamURL %s: no public stream — bridge required", dzStripPrefix(platformID))
	return nil, ErrDeezerNoStream
}

// similar tracks
// three-signal fanout: artist top tracks, album first track per recent album,
// and search "artist title" to catch remixes/covers.
// dedupe by native id; exclude seed track.
func (p *DeezerProvider) GetSimilarTracks(ctx context.Context, platformID string, limit int) ([]*pb.Track, error) {
	nativeID := dzStripPrefix(platformID)
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	// step 1: resolve seed track
	seedTrack, err := p.GetTrack(ctx, nativeID)
	if err != nil {
		return nil, fmt.Errorf("deezer: GetSimilarTracks: seed track: %w", err)
	}

	artistID := ""
	// need numeric artist id for /artist/{id}/top
	{
		resp, err := p.doRequest(ctx, "/track/"+nativeID, nil)
		if err == nil {
			var raw dzTrack
			if err := decodeDeezer(resp, &raw); err == nil && raw.Artist.ID != 0 {
				artistID = strconv.FormatInt(raw.Artist.ID, 10)
			}
		}
	}

	seedTitle := seedTrack.GetTitle()
	seedArtist := seedTrack.GetArtist()

	type fetchResult struct {
		tracks []*pb.Track
		err    error
	}

	topTracksCh := make(chan fetchResult, 1)
	albumTracksCh := make(chan fetchResult, 1)
	searchCh := make(chan fetchResult, 1)

	// signal 1: artist top tracks
	go func() {
		if artistID == "" {
			topTracksCh <- fetchResult{}
			return
		}
		resp, err := p.doRequest(ctx, "/artist/"+artistID+"/top", url.Values{"limit": {"50"}})
		if err != nil {
			topTracksCh <- fetchResult{err: err}
			return
		}
		var page dzPage[dzTrack]
		if err := decodeDeezer(resp, &page); err != nil {
			topTracksCh <- fetchResult{err: err}
			return
		}
		out := make([]*pb.Track, 0, len(page.Data))
		for i := range page.Data {
			out = append(out, mapDzTrack(&page.Data[i]))
		}
		topTracksCh <- fetchResult{tracks: out}
	}()

	// signal 2: first track of each recent album (up to 5)
	go func() {
		if artistID == "" {
			albumTracksCh <- fetchResult{}
			return
		}
		resp, err := p.doRequest(ctx, "/artist/"+artistID+"/albums", url.Values{"limit": {"10"}})
		if err != nil {
			albumTracksCh <- fetchResult{err: err}
			return
		}
		var page dzPage[dzAlbumItem]
		if err := decodeDeezer(resp, &page); err != nil {
			albumTracksCh <- fetchResult{err: err}
			return
		}

		const maxAlbums = 5
		albums := page.Data
		if len(albums) > maxAlbums {
			albums = albums[:maxAlbums]
		}

		type albumTrack struct {
			idx   int
			track *pb.Track
		}
		perAlbumCh := make(chan albumTrack, len(albums))

		for idx, alb := range albums {
			alb := alb
			idx := idx
			go func() {
				if alb.ID == 0 {
					perAlbumCh <- albumTrack{idx: idx}
					return
				}
				aResp, err := p.doRequest(ctx, "/album/"+strconv.FormatInt(alb.ID, 10)+"/tracks", url.Values{"limit": {"1"}})
				if err != nil {
					perAlbumCh <- albumTrack{idx: idx}
					return
				}
				var trackPage dzPage[dzTrack]
				if err := decodeDeezer(aResp, &trackPage); err != nil || len(trackPage.Data) == 0 {
					perAlbumCh <- albumTrack{idx: idx}
					return
				}
				t := &trackPage.Data[0]
				// graft cover from album item
				if t.Album.CoverXL == "" {
					t.Album.CoverXL = alb.CoverXL
					t.Album.CoverBig = alb.CoverBig
					t.Album.CoverMed = alb.CoverMed
					t.Album.Title = alb.Title
				}
				perAlbumCh <- albumTrack{idx: idx, track: mapDzTrack(t)}
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

	// signal 3: search "artist title" for remixes/covers
	go func() {
		q := seedArtist + " " + seedTitle
		resp, err := p.doRequest(ctx, "/search/track", url.Values{
			"q":     {q},
			"limit": {strconv.Itoa(limit)},
		})
		if err != nil {
			searchCh <- fetchResult{err: err}
			return
		}
		var page dzPage[dzTrack]
		if err := decodeDeezer(resp, &page); err != nil {
			searchCh <- fetchResult{err: err}
			return
		}
		out := make([]*pb.Track, 0, len(page.Data))
		for i := range page.Data {
			out = append(out, mapDzTrack(&page.Data[i]))
		}
		searchCh <- fetchResult{tracks: out}
	}()

	// collect
	tr1 := <-topTracksCh
	tr2 := <-albumTracksCh
	tr3 := <-searchCh

	if tr1.err != nil {
		log.Printf("[deezer] GetSimilarTracks %s: top-tracks: %v", nativeID, tr1.err)
	}
	if tr2.err != nil {
		log.Printf("[deezer] GetSimilarTracks %s: album-tracks: %v", nativeID, tr2.err)
	}
	if tr3.err != nil {
		log.Printf("[deezer] GetSimilarTracks %s: search: %v", nativeID, tr3.err)
	}

	seen := make(map[string]struct{}, limit*2)
	seen[nativeID] = struct{}{} // exclude seed

	out := make([]*pb.Track, 0, limit)
	// priority order: top tracks -> search results -> album tracks
	for _, bucket := range [][]*pb.Track{tr1.tracks, tr3.tracks, tr2.tracks} {
		for _, t := range bucket {
			if len(out) >= limit {
				break
			}
			id := dzStripPrefix(t.GetId())
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

	return out, nil
}
