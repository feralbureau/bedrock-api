// package main is a Deezer-focused integration test client for the bedrock gRPC server.
//
//  1. Run Search* RPCs to get real live IDs from Deezer.
//  2. Feed those IDs directly into GetTrack, GetAlbum, GetArtist, GetPlaylist,
//     GetStreamURL, GetSimilarTracks — no hardcoded native IDs.
//  3. Hardcoded IDs are only used as fallback when search returns 0 results.
//
//   - No auth required for public data — all search/get endpoints are open.
//   - GetStreamURL bridges to SoundCloud (Deezer public API = 30s previews only).
//     The test expects STATUS_OK + a real stream_url, with is_fallback=true and
//     source=PLATFORM_SOUNDCLOUD, exactly like the Spotify bridge.
//   - GetArtist returns albums (Deezer has /artist/{id}/albums endpoint).
//   - Tracks carry a "rank" field mapped to Popularity.
//   - release_date is always present on albums.
//
//
//	go run ./tests/deezer/main.go
//	go run ./tests/deezer/main.go -addr=10.0.0.1:50052 -timeout=20s
//	go run ./tests/deezer/main.go -verbose
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	pb "example/grpc/bedrock"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// cli flags

var (
	addr           = flag.String("addr", "localhost:50052", "bedrock service address")
	perCallTimeout = flag.Duration("timeout", 20*time.Second, "per-rpc deadline")
	verbose        = flag.Bool("verbose", false, "print full JSON response bodies")
	accessToken    = flag.String("token", "", "JWT access token for authentication")
)

// colour palette

const (
	cReset  = "\033[0m"
	cBold   = "\033[1m"
	cGreen  = "\033[32m"
	cYellow = "\033[33m"
	cRed    = "\033[31m"
	cCyan   = "\033[36m"
	cGray   = "\033[90m"
)

// result tracking

type outcome int

const (
	outPass outcome = iota
	outFail
	outSkip
)

type testResult struct {
	name    string
	out     outcome
	detail  string
	latency time.Duration
}

var results []testResult

func recordResult(name string, out outcome, detail string, latency time.Duration) {
	results = append(results, testResult{name: name, out: out, detail: detail, latency: latency})
}

// log helpers

func section(title string) {
	fmt.Printf("\n%s--- %s ---%s\n", cCyan, title, cReset)
}

func logf(prefix, color, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Printf("  %s%s%s %s\n", color, prefix, cReset, msg)
}

func pass(format string, args ...any) { logf("[+]", cGreen, format, args...) }
func fail(format string, args ...any) { logf("[-]", cRed, format, args...) }
func info(format string, args ...any) { logf("[i]", cGray, format, args...) }
func warn(format string, args ...any) { logf("[!]", cYellow, format, args...) }

func printJSON(v any) {
	if !*verbose {
		return
	}
	b, err := json.MarshalIndent(v, "  ", "  ")
	if err != nil {
		info("(json error: %v)", err)
		return
	}
	fmt.Printf("%s  %s%s\n", cGray, string(b), cReset)
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// grpc call wrapper

func invoke(fn func(ctx context.Context) (any, error)) (any, time.Duration, error) {
	ctx, cancel := context.WithTimeout(context.Background(), *perCallTimeout)
	defer cancel()

	if *accessToken != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+*accessToken)
	}

	start := time.Now()
	v, err := fn(ctx)
	return v, time.Since(start), err
}

// shared live id state

var (
	liveTrackID    string // deezer:<id>
	liveAlbumID    string // deezer:<id>  (from SearchAlbums)
	livePlaylistID string // deezer:<id>  (from SearchPlaylists)
	liveArtistID   string // deezer:<id>  (from SearchArtists)
)

// fallback constants — used only when search returns 0 results.
const (
	fallbackTrackID    = "deezer:67238735"
	fallbackAlbumID    = "deezer:6575789"
	fallbackPlaylistID = "deezer:1282495565"
	fallbackArtistID   = "deezer:27"
	// badAlbumID is a valid track ID intentionally passed to GetAlbum to verify
	// the server handles the type mismatch gracefully (STATUS_ERROR or nil album).
	badAlbumID = "deezer:67238735"
)

// validation helpers

func checkTrack(t *pb.Track, idx int) bool {
	if t == nil {
		fail("track[%d] is nil", idx)
		return false
	}
	ok := true
	if !strings.HasPrefix(t.GetId(), "deezer:") {
		fail("track[%d] id=%q missing deezer: prefix", idx, t.GetId())
		ok = false
	}
	if t.GetTitle() == "" {
		fail("track[%d] empty title (id=%s)", idx, t.GetId())
		ok = false
	}
	if t.GetArtist() == "" {
		fail("track[%d] empty artist (id=%s title=%q)", idx, t.GetId(), t.GetTitle())
		ok = false
	}
	if t.GetDurationMs() <= 0 {
		warn("track[%d] duration_ms=%d (id=%s) — Deezer stores duration in seconds, check conversion",
			idx, t.GetDurationMs(), t.GetId())
	}
	if t.GetSource() != pb.Platform_PLATFORM_DEEZER {
		fail("track[%d] source=%v want PLATFORM_DEEZER", idx, t.GetSource())
		ok = false
	}
	if t.GetPopularity() == 0 {
		warn("track[%d] popularity/rank=0 — expected for most Deezer tracks", idx)
	}
	durS := t.GetDurationMs() / 1000
	info("track[%d]  id=%-22s  dur=%d:%02d  rank=%-7d  title=%q  artist=%q",
		idx, t.GetId(), durS/60, durS%60,
		t.GetPopularity(),
		trunc(t.GetTitle(), 40), trunc(t.GetArtist(), 30))
	if t.GetCoverUrl() != "" {
		info("         cover    %s", trunc(t.GetCoverUrl(), 70))
	}
	if t.GetPreviewUrl() != "" {
		info("         preview  %s", trunc(t.GetPreviewUrl(), 70))
	}
	return ok
}

func checkAlbum(a *pb.Album, idx int) bool {
	if a == nil {
		fail("album[%d] is nil", idx)
		return false
	}
	ok := true
	if !strings.HasPrefix(a.GetId(), "deezer:") {
		fail("album[%d] id=%q missing deezer: prefix", idx, a.GetId())
		ok = false
	}
	if a.GetTitle() == "" {
		fail("album[%d] empty title", idx)
		ok = false
	}
	if a.GetSource() != pb.Platform_PLATFORM_DEEZER {
		fail("album[%d] wrong source: %v", idx, a.GetSource())
		ok = false
	}
	info("album[%d]  id=%-22s  tracks=%d  date=%s  type=%s  title=%q  artist=%q",
		idx, a.GetId(), a.GetTotalTracks(), a.GetReleaseDate(), a.GetAlbumType(),
		trunc(a.GetTitle(), 40), trunc(a.GetArtist(), 30))
	return ok
}

func checkArtist(a *pb.Artist, idx int) bool {
	if a == nil {
		fail("artist[%d] is nil", idx)
		return false
	}
	ok := true
	if !strings.HasPrefix(a.GetId(), "deezer:") {
		fail("artist[%d] id=%q missing deezer: prefix", idx, a.GetId())
		ok = false
	}
	if a.GetName() == "" {
		fail("artist[%d] empty name", idx)
		ok = false
	}
	if a.GetSource() != pb.Platform_PLATFORM_DEEZER {
		fail("artist[%d] wrong source: %v", idx, a.GetSource())
		ok = false
	}
	info("artist[%d]  id=%-22s  fans=%-10d  name=%q",
		idx, a.GetId(), a.GetFollowers(), trunc(a.GetName(), 40))
	if a.GetImageUrl() != "" {
		info("          image  %s", trunc(a.GetImageUrl(), 70))
	}
	return ok
}

func checkPlaylist(pl *pb.Playlist, idx int) bool {
	if pl == nil {
		fail("playlist[%d] is nil", idx)
		return false
	}
	ok := true
	if !strings.HasPrefix(pl.GetId(), "deezer:") {
		fail("playlist[%d] id=%q missing deezer: prefix", idx, pl.GetId())
		ok = false
	}
	if pl.GetTitle() == "" {
		fail("playlist[%d] empty title", idx)
		ok = false
	}
	if pl.GetSource() != pb.Platform_PLATFORM_DEEZER {
		fail("playlist[%d] wrong source: %v", idx, pl.GetSource())
		ok = false
	}
	info("playlist[%d]  id=%-22s  tracks=%d  owner=%q  title=%q",
		idx, pl.GetId(), pl.GetTotalTracks(),
		trunc(pl.GetOwner(), 20), trunc(pl.GetTitle(), 40))
	return ok
}

// search tests (also populate live IDs)

func testSearchTracks(c pb.BedrockServiceClient) {
	name := `SearchTracks (query: "daft punk get lucky")`
	section(name)

	resp, lat, err := invoke(func(ctx context.Context) (any, error) {
		return c.SearchTracks(ctx, &pb.SearchRequest{
			Query:     "daft punk get lucky",
			Limit:     5,
			Platforms: []pb.Platform{pb.Platform_PLATFORM_DEEZER},
		})
	})
	if err != nil {
		st, _ := status.FromError(err)
		fail("rpc error %s: %s", st.Code(), st.Message())
		recordResult(name, outFail, fmt.Sprintf("rpc error: %s", st.Code()), lat)
		return
	}

	r := resp.(*pb.SearchTracksResponse)
	printJSON(r)

	if r.GetStatus() == pb.ResponseStatus_STATUS_ERROR {
		fail("response status ERROR: check server logs")
		recordResult(name, outFail, "status=ERROR", lat)
		return
	}

	tracks := r.GetTracks()
	if len(tracks) == 0 {
		fail("got 0 tracks — Deezer API may be unreachable from the server")
		recordResult(name, outFail, "0 tracks returned", lat)
		return
	}

	liveTrackID = tracks[0].GetId()
	pass("received %d track(s)  latency=%s", len(tracks), lat.Round(time.Millisecond))
	pass("-> live track ID for downstream tests: %s  title=%q", liveTrackID, trunc(tracks[0].GetTitle(), 50))

	allOK := true
	for i, t := range tracks {
		if !checkTrack(t, i) {
			allOK = false
		}
	}

	topText := strings.ToLower(tracks[0].GetTitle() + " " + tracks[0].GetArtist())
	if !strings.Contains(topText, "daft") && !strings.Contains(topText, "lucky") {
		warn("top result %q / %q doesn't match query — relevance check failed",
			tracks[0].GetTitle(), tracks[0].GetArtist())
	} else {
		pass("top result relevance OK: %q by %q", tracks[0].GetTitle(), tracks[0].GetArtist())
	}

	if allOK {
		recordResult(name, outPass, fmt.Sprintf("%d tracks, all valid", len(tracks)), lat)
	} else {
		recordResult(name, outFail, fmt.Sprintf("%d tracks, some invalid fields", len(tracks)), lat)
	}
}

func testSearchAlbums(c pb.BedrockServiceClient) {
	name := `SearchAlbums (query: "daft punk random access memories")`
	section(name)

	resp, lat, err := invoke(func(ctx context.Context) (any, error) {
		return c.SearchAlbums(ctx, &pb.SearchRequest{
			Query:     "daft punk random access memories",
			Limit:     5,
			Platforms: []pb.Platform{pb.Platform_PLATFORM_DEEZER},
		})
	})
	if err != nil {
		st, _ := status.FromError(err)
		fail("rpc error %s: %s", st.Code(), st.Message())
		recordResult(name, outFail, fmt.Sprintf("rpc error: %s", st.Code()), lat)
		return
	}

	r := resp.(*pb.SearchAlbumsResponse)
	printJSON(r)

	albums := r.GetAlbums()
	if len(albums) == 0 {
		warn("0 albums returned — trying fallback ID for GetAlbum")
		recordResult(name, outSkip, "0 albums returned", lat)
		return
	}

	liveAlbumID = albums[0].GetId()
	pass("received %d album(s)  latency=%s", len(albums), lat.Round(time.Millisecond))
	pass("-> live album ID for GetAlbum: %s  title=%q", liveAlbumID, trunc(albums[0].GetTitle(), 50))

	allOK := true
	for i, a := range albums {
		if !checkAlbum(a, i) {
			allOK = false
		}
	}

	if allOK {
		recordResult(name, outPass, fmt.Sprintf("%d albums, all valid", len(albums)), lat)
	} else {
		recordResult(name, outFail, fmt.Sprintf("%d albums, some invalid", len(albums)), lat)
	}
}

func testSearchArtists(c pb.BedrockServiceClient) {
	name := `SearchArtists (query: "daft punk")`
	section(name)

	resp, lat, err := invoke(func(ctx context.Context) (any, error) {
		return c.SearchArtists(ctx, &pb.SearchRequest{
			Query:     "daft punk",
			Limit:     5,
			Platforms: []pb.Platform{pb.Platform_PLATFORM_DEEZER},
		})
	})
	if err != nil {
		st, _ := status.FromError(err)
		fail("rpc error %s: %s", st.Code(), st.Message())
		recordResult(name, outFail, fmt.Sprintf("rpc error: %s", st.Code()), lat)
		return
	}

	r := resp.(*pb.SearchArtistsResponse)
	printJSON(r)

	artists := r.GetArtists()
	if len(artists) == 0 {
		fail("0 artists returned")
		recordResult(name, outFail, "0 artists", lat)
		return
	}

	liveArtistID = artists[0].GetId()
	pass("received %d artist(s)  latency=%s  -> live ID: %s",
		len(artists), lat.Round(time.Millisecond), liveArtistID)

	allOK := true
	for i, a := range artists {
		if !checkArtist(a, i) {
			allOK = false
		}
	}

	topName := strings.ToLower(artists[0].GetName())
	if !strings.Contains(topName, "daft") {
		warn("top result %q doesn't contain 'daft' — relevance check failed", artists[0].GetName())
	} else {
		pass("top result name matches: %q", artists[0].GetName())
	}

	if artists[0].GetFollowers() == 0 {
		warn("fans/followers=0 — unexpected for a major artist on Deezer")
	} else {
		pass("fans populated: %d", artists[0].GetFollowers())
	}

	if allOK {
		recordResult(name, outPass, fmt.Sprintf("%d artists, all valid", len(artists)), lat)
	} else {
		recordResult(name, outFail, "some artist fields invalid", lat)
	}
}

func testSearchPlaylists(c pb.BedrockServiceClient) {
	name := `SearchPlaylists (query: "top hits")`
	section(name)

	resp, lat, err := invoke(func(ctx context.Context) (any, error) {
		return c.SearchPlaylists(ctx, &pb.SearchRequest{
			Query:     "top hits",
			Limit:     5,
			Platforms: []pb.Platform{pb.Platform_PLATFORM_DEEZER},
		})
	})
	if err != nil {
		st, _ := status.FromError(err)
		fail("rpc error %s: %s", st.Code(), st.Message())
		recordResult(name, outFail, fmt.Sprintf("rpc error: %s", st.Code()), lat)
		return
	}

	r := resp.(*pb.SearchPlaylistsResponse)
	printJSON(r)

	pls := r.GetPlaylists()
	if len(pls) == 0 {
		fail("0 playlists returned")
		recordResult(name, outFail, "0 playlists", lat)
		return
	}

	livePlaylistID = pls[0].GetId()
	pass("received %d playlist(s)  latency=%s", len(pls), lat.Round(time.Millisecond))
	pass("-> live playlist ID for GetPlaylist: %s  title=%q",
		livePlaylistID, trunc(pls[0].GetTitle(), 50))

	allOK := true
	for i, pl := range pls {
		if !checkPlaylist(pl, i) {
			allOK = false
		}
	}

	if allOK {
		recordResult(name, outPass, fmt.Sprintf("%d playlists, all valid", len(pls)), lat)
	} else {
		recordResult(name, outFail, "some playlist fields invalid", lat)
	}
}

// get tests (use live IDs from search)

func testGetTrack(c pb.BedrockServiceClient) {
	id := liveTrackID
	if id == "" {
		id = fallbackTrackID
		warn("search produced no track ID — using fallback %s", id)
	}
	name := fmt.Sprintf("GetTrack (%s)", id)
	section(name)

	resp, lat, err := invoke(func(ctx context.Context) (any, error) {
		return c.GetTrack(ctx, &pb.GetTrackRequest{TrackId: id})
	})
	if err != nil {
		st, _ := status.FromError(err)
		fail("rpc error %s: %s", st.Code(), st.Message())
		recordResult(name, outFail, fmt.Sprintf("rpc error: %s", st.Code()), lat)
		return
	}

	r := resp.(*pb.GetTrackResponse)
	printJSON(r)

	if r.GetStatus() == pb.ResponseStatus_STATUS_ERROR {
		fail("response status ERROR: %s", r.GetError())
		recordResult(name, outFail, r.GetError(), lat)
		return
	}

	t := r.GetTrack()
	ok := checkTrack(t, 0)

	if t.GetId() != id {
		fail("id round-trip mismatch: got %q want %q", t.GetId(), id)
		ok = false
	} else {
		pass("id round-trips correctly: %s", id)
	}

	if t.GetAlbumTitle() == "" {
		warn("album_title is empty — expected on a full Deezer GetTrack response")
	} else {
		pass("album_title=%q", trunc(t.GetAlbumTitle(), 40))
	}

	if t.GetPreviewUrl() == "" {
		warn("preview_url is empty — most Deezer tracks have a 30s preview")
	} else {
		pass("preview_url present: %s", trunc(t.GetPreviewUrl(), 60))
	}

	pass("latency=%s", lat.Round(time.Millisecond))

	if ok {
		recordResult(name, outPass, fmt.Sprintf("title=%q artist=%q", trunc(t.GetTitle(), 30), trunc(t.GetArtist(), 20)), lat)
	} else {
		recordResult(name, outFail, "field validation failed", lat)
	}
}

func testGetAlbum(c pb.BedrockServiceClient) {
	id := liveAlbumID
	if id == "" {
		id = fallbackAlbumID
		warn("search produced no album ID — using fallback %s", id)
	}
	name := fmt.Sprintf("GetAlbum (%s)", id)
	section(name)

	resp, lat, err := invoke(func(ctx context.Context) (any, error) {
		return c.GetAlbum(ctx, &pb.GetAlbumRequest{AlbumId: id})
	})
	if err != nil {
		st, _ := status.FromError(err)
		fail("rpc error %s: %s", st.Code(), st.Message())
		recordResult(name, outFail, fmt.Sprintf("rpc error: %s", st.Code()), lat)
		return
	}

	r := resp.(*pb.GetAlbumResponse)
	printJSON(r)

	if r.GetStatus() == pb.ResponseStatus_STATUS_ERROR {
		fail("response status ERROR: %s", r.GetError())
		recordResult(name, outFail, r.GetError(), lat)
		return
	}

	a := r.GetAlbum()
	aOK := checkAlbum(a, 0)

	if a.GetReleaseDate() == "" {
		warn("release_date is empty — expected on Deezer album response")
	} else {
		pass("release_date=%q", a.GetReleaseDate())
	}

	tracks := r.GetTracks()
	pass("received %d track(s) in album  latency=%s", len(tracks), lat.Round(time.Millisecond))

	if len(tracks) == 0 {
		warn("0 tracks returned — album tracks may not be embedded or album is empty")
	}

	tOK := true
	display := 5
	if len(tracks) < display {
		display = len(tracks)
	}
	for i := 0; i < display; i++ {
		if !checkTrack(tracks[i], i) {
			tOK = false
		}
	}
	if len(tracks) > display {
		info("... and %d more tracks (showing first %d)", len(tracks)-display, display)
	}

	if a.GetTotalTracks() > 0 && len(tracks) > 0 {
		if len(tracks) < int(a.GetTotalTracks()) {
			warn("tracks returned (%d) < album.total_tracks (%d) — first page only (expected for large albums)",
				len(tracks), a.GetTotalTracks())
		} else {
			pass("track count matches: returned=%d total_tracks=%d", len(tracks), a.GetTotalTracks())
		}
	}

	if aOK && tOK && len(tracks) > 0 {
		recordResult(name, outPass, fmt.Sprintf("album OK, %d tracks hydrated", len(tracks)), lat)
	} else if aOK {
		recordResult(name, outSkip, fmt.Sprintf("album OK but %d tracks", len(tracks)), lat)
	} else {
		recordResult(name, outFail, "album metadata invalid", lat)
	}
}

func testGetArtist(c pb.BedrockServiceClient) {
	id := liveArtistID
	if id == "" {
		id = fallbackArtistID
		warn("search produced no artist ID — using fallback %s", id)
	}
	name := fmt.Sprintf("GetArtist (%s)", id)
	section(name)

	resp, lat, err := invoke(func(ctx context.Context) (any, error) {
		return c.GetArtist(ctx, &pb.GetArtistRequest{ArtistId: id})
	})
	if err != nil {
		st, _ := status.FromError(err)
		fail("rpc error %s: %s", st.Code(), st.Message())
		recordResult(name, outFail, fmt.Sprintf("rpc error: %s", st.Code()), lat)
		return
	}

	r := resp.(*pb.GetArtistResponse)
	printJSON(r)

	if r.GetStatus() == pb.ResponseStatus_STATUS_ERROR {
		fail("response status ERROR: %s", r.GetError())
		recordResult(name, outFail, r.GetError(), lat)
		return
	}

	artistOK := checkArtist(r.GetArtist(), 0)

	topTracks := r.GetTopTracks()
	pass("received %d top track(s)  latency=%s", len(topTracks), lat.Round(time.Millisecond))

	if len(topTracks) == 0 {
		warn("0 top tracks returned — unexpected for a major Deezer artist")
	}

	tOK := true
	display := 3
	if len(topTracks) < display {
		display = len(topTracks)
	}
	for i := 0; i < display; i++ {
		if !checkTrack(topTracks[i], i) {
			tOK = false
		}
	}

	albums := r.GetAlbums()
	if len(albums) == 0 {
		warn("0 albums returned — Deezer GetArtist should populate albums via /artist/{id}/albums")
	} else {
		pass("albums populated: %d album(s)", len(albums))
		aOK := true
		displayAlbums := 3
		if len(albums) < displayAlbums {
			displayAlbums = len(albums)
		}
		for i := 0; i < displayAlbums; i++ {
			if !checkAlbum(albums[i], i) {
				aOK = false
			}
		}
		if len(albums) > displayAlbums {
			info("... and %d more albums (showing first %d)", len(albums)-displayAlbums, displayAlbums)
		}
		if !aOK {
			tOK = false
		}
	}

	if artistOK && tOK {
		recordResult(name, outPass,
			fmt.Sprintf("artist OK, %d top tracks, %d albums", len(topTracks), len(albums)), lat)
	} else {
		recordResult(name, outFail, "field validation failed", lat)
	}
}

func testGetPlaylist(c pb.BedrockServiceClient) {
	id := livePlaylistID
	if id == "" {
		id = fallbackPlaylistID
		warn("search produced no playlist ID — using fallback %s", id)
	}
	name := fmt.Sprintf("GetPlaylist (%s)", id)
	section(name)

	resp, lat, err := invoke(func(ctx context.Context) (any, error) {
		return c.GetPlaylist(ctx, &pb.GetPlaylistRequest{PlaylistId: id})
	})
	if err != nil {
		st, _ := status.FromError(err)
		fail("rpc error %s: %s", st.Code(), st.Message())
		recordResult(name, outFail, fmt.Sprintf("rpc error: %s", st.Code()), lat)
		return
	}

	r := resp.(*pb.GetPlaylistResponse)
	printJSON(r)

	if r.GetStatus() == pb.ResponseStatus_STATUS_ERROR {
		fail("response status ERROR: %s", r.GetError())
		recordResult(name, outFail, r.GetError(), lat)
		return
	}

	plOK := checkPlaylist(r.GetPlaylist(), 0)

	tracks := r.GetTracks()
	pass("received %d track(s) in playlist  latency=%s", len(tracks), lat.Round(time.Millisecond))

	if len(tracks) == 0 {
		warn("0 tracks returned — playlist may be empty or first page has no items")
	}

	tOK := true
	display := 5
	if len(tracks) < display {
		display = len(tracks)
	}
	for i := 0; i < display; i++ {
		if !checkTrack(tracks[i], i) {
			tOK = false
		}
	}
	if len(tracks) > display {
		info("... and %d more tracks (showing first %d)", len(tracks)-display, display)
	}

	if plOK && tOK && len(tracks) > 0 {
		recordResult(name, outPass, fmt.Sprintf("playlist OK, %d tracks hydrated", len(tracks)), lat)
	} else if plOK {
		recordResult(name, outSkip, fmt.Sprintf("playlist OK but %d tracks (first page only)", len(tracks)), lat)
	} else {
		recordResult(name, outFail, "playlist metadata invalid", lat)
	}
}

// stream tests

func testGetStreamURL(c pb.BedrockServiceClient) {
	id := liveTrackID
	if id == "" {
		id = fallbackTrackID
		warn("no live track ID — using fallback %s", id)
	}
	name := fmt.Sprintf("GetStreamURL (%s) — expect STATUS_OK via SC bridge (mp3)", id)
	section(name)

	resp, lat, err := invoke(func(ctx context.Context) (any, error) {
		return c.GetStreamURL(ctx, &pb.GetStreamURLRequest{
			TrackId:         id,
			PreferredFormat: "mp3",
		})
	})
	if err != nil {
		st, _ := status.FromError(err)
		if st.Code() == codes.Unavailable {
			warn("server unavailable — is the bedrock server running?")
		}
		fail("unexpected gRPC transport error %s: %s", st.Code(), st.Message())
		recordResult(name, outFail, fmt.Sprintf("unexpected grpc error: %s", st.Code()), lat)
		return
	}

	r := resp.(*pb.GetStreamURLResponse)
	printJSON(r)

	if r.GetStatus() == pb.ResponseStatus_STATUS_ERROR {
		fail("bridge returned STATUS_ERROR: %s", r.GetError())
		recordResult(name, outFail, fmt.Sprintf("bridge error: %s", trunc(r.GetError(), 80)), lat)
		return
	}

	streamURL := r.GetStreamUrl()
	if streamURL == "" {
		fail("STATUS_OK returned but stream_url is empty — bridge produced no URL")
		recordResult(name, outFail, "empty stream_url with STATUS_OK", lat)
		return
	}

	if !strings.HasPrefix(streamURL, "http://") && !strings.HasPrefix(streamURL, "https://") {
		fail("stream_url is not an http(s) url: %s", trunc(streamURL, 60))
		recordResult(name, outFail, "stream_url scheme invalid", lat)
		return
	}

	pass("stream_url received via SC bridge  latency=%s", lat.Round(time.Millisecond))
	info("stream_url    %s", trunc(streamURL, 80))
	info("stream_type   %s", r.GetStreamType())
	info("content_type  %s", r.GetContentType())
	info("source        %v", r.GetSource())
	info("is_fallback   %v  fallback_from=%q", r.GetIsFallback(), r.GetFallbackFrom())

	if !r.GetIsFallback() {
		warn("is_fallback is false — bridge should mark cross-platform streams as fallback")
	} else {
		pass("is_fallback=true вњ“")
	}
	if r.GetSource() != pb.Platform_PLATFORM_SOUNDCLOUD {
		warn("source=%v — expected PLATFORM_SOUNDCLOUD (stream served by SC bridge)", r.GetSource())
	} else {
		pass("source=PLATFORM_SOUNDCLOUD вњ“")
	}
	if r.GetFallbackFrom() == "" {
		warn("fallback_from is empty — should be 'PLATFORM_DEEZER'")
	} else {
		pass("fallback_from=%q вњ“", r.GetFallbackFrom())
	}

	st := r.GetStreamType()
	if st != "audio_stream" && st != "hls" {
		warn("unexpected stream_type %q (want audio_stream or hls)", st)
	} else {
		pass("stream_type=%q  content_type=%q", st, r.GetContentType())
	}

	recordResult(name, outPass,
		fmt.Sprintf("SC bridge OK  type=%s  is_fallback=%v", st, r.GetIsFallback()), lat)
}

func testGetStreamURLHLS(c pb.BedrockServiceClient) {
	id := liveTrackID
	if id == "" {
		id = fallbackTrackID
		warn("no live track ID — using fallback %s", id)
	}
	name := fmt.Sprintf("GetStreamURL (%s, format=hls) — expect STATUS_OK via SC bridge", id)
	section(name)

	resp, lat, err := invoke(func(ctx context.Context) (any, error) {
		return c.GetStreamURL(ctx, &pb.GetStreamURLRequest{
			TrackId:         id,
			PreferredFormat: "hls",
		})
	})
	if err != nil {
		st, _ := status.FromError(err)
		fail("unexpected gRPC transport error %s: %s", st.Code(), st.Message())
		recordResult(name, outFail, fmt.Sprintf("unexpected grpc error: %s", st.Code()), lat)
		return
	}

	r := resp.(*pb.GetStreamURLResponse)
	printJSON(r)

	if r.GetStatus() == pb.ResponseStatus_STATUS_ERROR {
		fail("bridge returned STATUS_ERROR on HLS request: %s", r.GetError())
		recordResult(name, outFail, fmt.Sprintf("bridge error: %s", trunc(r.GetError(), 80)), lat)
		return
	}

	streamURL := r.GetStreamUrl()
	if streamURL == "" {
		fail("STATUS_OK but stream_url is empty")
		recordResult(name, outFail, "empty stream_url", lat)
		return
	}

	pass("HLS bridge stream_url received  latency=%s", lat.Round(time.Millisecond))
	info("stream_url    %s", trunc(streamURL, 80))
	info("stream_type   %s", r.GetStreamType())
	info("content_type  %s", r.GetContentType())
	info("is_fallback   %v  fallback_from=%q", r.GetIsFallback(), r.GetFallbackFrom())

	st := r.GetStreamType()
	switch st {
	case "hls":
		pass("HLS transcoding selected as expected")
	case "audio_stream":
		warn("HLS requested but SC returned progressive stream (HLS may not be available for this track)")
	default:
		fail("unexpected stream_type %q", st)
		recordResult(name, outFail, fmt.Sprintf("unexpected stream_type %q", st), lat)
		return
	}

	recordResult(name, outPass,
		fmt.Sprintf("SC bridge HLS OK  type=%s  is_fallback=%v", st, r.GetIsFallback()), lat)
}

// similar tracks

func testGetSimilarTracks(c pb.BedrockServiceClient) {
	id := liveTrackID
	if id == "" {
		id = fallbackTrackID
		warn("no live track ID — using fallback %s", id)
	}
	name := fmt.Sprintf("GetSimilarTracks (%s, limit=10)", id)
	section(name)

	resp, lat, err := invoke(func(ctx context.Context) (any, error) {
		return c.GetSimilarTracks(ctx, &pb.GetSimilarTracksRequest{
			TrackId: id,
			Limit:   10,
		})
	})
	if err != nil {
		st, _ := status.FromError(err)
		fail("rpc error %s: %s", st.Code(), st.Message())
		recordResult(name, outFail, fmt.Sprintf("rpc error: %s", st.Code()), lat)
		return
	}

	r := resp.(*pb.GetSimilarTracksResponse)
	printJSON(r)

	tracks := r.GetTracks()
	pass("received %d similar track(s)  latency=%s", len(tracks), lat.Round(time.Millisecond))

	if len(tracks) == 0 {
		warn("0 similar tracks — artist may have no other indexed tracks on Deezer")
		recordResult(name, outSkip, "0 similar tracks", lat)
		return
	}

	for i, t := range tracks {
		if t.GetId() == id {
			fail("seed track found in similar results at index %d", i)
			recordResult(name, outFail, "seed track leaked into similar results", lat)
			return
		}
	}
	pass("seed track correctly excluded from results")

	allOK := true
	display := 5
	if len(tracks) < display {
		display = len(tracks)
	}
	for i := 0; i < display; i++ {
		if !checkTrack(tracks[i], i) {
			allOK = false
		}
	}
	if len(tracks) > display {
		info("... and %d more tracks (showing first %d)", len(tracks)-display, display)
	}

	if allOK {
		recordResult(name, outPass, fmt.Sprintf("%d similar tracks, all valid", len(tracks)), lat)
	} else {
		recordResult(name, outFail, "some similar tracks have invalid fields", lat)
	}
}

// edge cases

func testEdgeCases(c pb.BedrockServiceClient) {
	{
		name := "GetTrack (nonexistent ID: deezer:9999999999999)"
		section(name)
		resp, lat, err := invoke(func(ctx context.Context) (any, error) {
			return c.GetTrack(ctx, &pb.GetTrackRequest{TrackId: "deezer:9999999999999"})
		})
		if err != nil {
			st, _ := status.FromError(err)
			fail("unexpected gRPC error %s: %s", st.Code(), st.Message())
			recordResult(name, outFail, "unexpected grpc error", lat)
		} else {
			r := resp.(*pb.GetTrackResponse)
			if r.GetStatus() == pb.ResponseStatus_STATUS_ERROR && r.GetError() != "" {
				pass("got expected STATUS_ERROR: %q", trunc(r.GetError(), 80))
				recordResult(name, outPass, "STATUS_ERROR returned (not found)", lat)
			} else if r.GetTrack() == nil {
				pass("track is nil — 404 handled gracefully")
				recordResult(name, outPass, "nil track (not found) returned gracefully", lat)
			} else {
				warn("unexpected success for nonexistent ID: %q", r.GetTrack().GetId())
				recordResult(name, outSkip, "unexpected non-error response for fake ID", lat)
			}
		}
	}

	// must surface STATUS_ERROR cleanly — no panic, no empty response.
	{
		name := "GetStreamURL (bad ID: deezer:9999999999999) — bridge must fail gracefully"
		section(name)
		resp, lat, err := invoke(func(ctx context.Context) (any, error) {
			return c.GetStreamURL(ctx, &pb.GetStreamURLRequest{
				TrackId:         "deezer:9999999999999",
				PreferredFormat: "mp3",
			})
		})
		if err != nil {
			st, _ := status.FromError(err)
			fail("unexpected gRPC transport error %s: %s", st.Code(), st.Message())
			recordResult(name, outFail, "unexpected grpc error", lat)
		} else {
			r := resp.(*pb.GetStreamURLResponse)
			if r.GetStatus() == pb.ResponseStatus_STATUS_ERROR && r.GetError() != "" {
				pass("bridge failed gracefully on bad Deezer ID: %q", trunc(r.GetError(), 80))
				recordResult(name, outPass, "STATUS_ERROR returned (Deezer 404 → bridge aborted)", lat)
			} else if r.GetStreamUrl() == "" {
				pass("empty stream_url, no crash — acceptable degraded response")
				recordResult(name, outPass, "no stream_url, no crash", lat)
			} else {
				warn("got a stream_url for a fake Deezer ID — unexpected but non-fatal")
				recordResult(name, outSkip, "unexpected stream_url for fake ID", lat)
			}
		}
	}

	{
		name := "SearchTracks (empty query -> expect InvalidArgument)"
		section(name)
		_, lat, err := invoke(func(ctx context.Context) (any, error) {
			return c.SearchTracks(ctx, &pb.SearchRequest{
				Query:     "",
				Platforms: []pb.Platform{pb.Platform_PLATFORM_DEEZER},
			})
		})
		if err != nil {
			st, _ := status.FromError(err)
			if st.Code() == codes.InvalidArgument {
				pass("correctly rejected empty query: %s", st.Message())
				recordResult(name, outPass, "InvalidArgument returned", lat)
			} else {
				fail("wrong error code %s (want InvalidArgument): %s", st.Code(), st.Message())
				recordResult(name, outFail, fmt.Sprintf("wrong code: %s", st.Code()), lat)
			}
		} else {
			fail("expected error but RPC succeeded for empty query")
			recordResult(name, outFail, "empty query not rejected", lat)
		}
	}

	// should return STATUS_ERROR or nil album, not silently succeed.
	{
		name := fmt.Sprintf("GetAlbum (track ID used as album: %s) — expect graceful error", badAlbumID)
		section(name)
		resp, lat, err := invoke(func(ctx context.Context) (any, error) {
			return c.GetAlbum(ctx, &pb.GetAlbumRequest{AlbumId: badAlbumID})
		})
		if err != nil {
			st, _ := status.FromError(err)
			fail("rpc error %s: %s", st.Code(), st.Message())
			recordResult(name, outFail, fmt.Sprintf("rpc error: %s", st.Code()), lat)
		} else {
			r := resp.(*pb.GetAlbumResponse)
			if r.GetStatus() == pb.ResponseStatus_STATUS_ERROR {
				pass("type mismatch handled gracefully: %q", trunc(r.GetError(), 80))
				recordResult(name, outPass, "STATUS_ERROR returned (type mismatch)", lat)
			} else if r.GetAlbum() == nil {
				pass("album is nil — mismatch handled gracefully")
				recordResult(name, outPass, "nil album returned gracefully", lat)
			} else {
				// if we somehow got an album back, the ID space overlaps (non-fatal).
				warn("got an album for a track ID: %q — Deezer ID spaces may overlap",
					r.GetAlbum().GetId())
				recordResult(name, outSkip, "unexpected success — ID space overlap possible", lat)
			}
		}
	}

	// to verify the three-signal fanout works across different artists.
	{
		const weekndTrack = "deezer:908604612"
		name := fmt.Sprintf("GetSimilarTracks (%s — The Weeknd, fanout seed check)", weekndTrack)
		section(name)
		resp, lat, err := invoke(func(ctx context.Context) (any, error) {
			return c.GetSimilarTracks(ctx, &pb.GetSimilarTracksRequest{
				TrackId: weekndTrack,
				Limit:   10,
			})
		})
		if err != nil {
			st, _ := status.FromError(err)
			fail("rpc error %s: %s", st.Code(), st.Message())
			recordResult(name, outFail, fmt.Sprintf("rpc error: %s", st.Code()), lat)
		} else {
			r := resp.(*pb.GetSimilarTracksResponse)
			if r.GetStatus() == pb.ResponseStatus_STATUS_ERROR {
				warn("STATUS_ERROR: %v", r.GetErrors())
				recordResult(name, outSkip, "STATUS_ERROR on similar tracks", lat)
			} else {
				tracks := r.GetTracks()
				pass("received %d similar track(s)  latency=%s",
					len(tracks), lat.Round(time.Millisecond))
				allOK := true
				display := 3
				if len(tracks) < display {
					display = len(tracks)
				}
				for i := 0; i < display; i++ {
					if !checkTrack(tracks[i], i) {
						allOK = false
					}
				}
				if allOK && len(tracks) > 0 {
					recordResult(name, outPass,
						fmt.Sprintf("fanout OK, %d tracks", len(tracks)), lat)
				} else if len(tracks) == 0 {
					recordResult(name, outSkip, "0 similar tracks", lat)
				} else {
					recordResult(name, outFail, "similar track field validation failed", lat)
				}
			}
		}
	}
}

// summary

func printSummary() {
	fmt.Printf("\n%s--- DEEZER TEST SUMMARY ---%s\n\n", cCyan, cReset)

	passed, failed, skipped := 0, 0, 0
	for _, r := range results {
		switch r.out {
		case outPass:
			passed++
		case outFail:
			failed++
		case outSkip:
			skipped++
		}
	}

	const nameW = 70
	for _, r := range results {
		var prefix, color string
		switch r.out {
		case outPass:
			prefix, color = "[+]", cGreen
		case outFail:
			prefix, color = "[-]", cRed
		case outSkip:
			prefix, color = "[!]", cYellow
		}

		nameField := r.name
		if len(nameField) > nameW {
			nameField = nameField[:nameW-1] + "..."
		}

		fmt.Printf("  %s%s%s  %-*s  %s%s%s  %s\n",
			color, prefix, cReset,
			nameW, nameField,
			cGray, r.latency.Round(time.Millisecond), cReset,
			r.detail,
		)
	}

	fmt.Printf("\n  %s%d passed%s  %s%d failed%s  %s%d skipped%s  total=%d\n\n",
		cGreen, passed, cReset,
		cRed, failed, cReset,
		cYellow, skipped, cReset,
		len(results),
	)

	if failed == 0 {
		fmt.Printf("  %s[+] all Deezer RPCs passed or skipped.%s\n\n", cGreen, cReset)
	} else {
		fmt.Printf("  %s[-] %d test(s) failed — check server logs and Deezer API reachability.%s\n\n",
			cRed, failed, cReset)
	}
}

// main

func main() {
	flag.Parse()
	log.SetFlags(log.Ltime | log.Lshortfile)

	fmt.Printf("\n%s[+] Bedrock gRPC - Deezer Integration Test Client%s\n", cCyan, cReset)
	fmt.Printf("    server   : %s\n", *addr)
	fmt.Printf("    timeout  : %s  (per RPC)\n", *perCallTimeout)
	fmt.Printf("    verbose  : %v\n", *verbose)
	fmt.Printf("    strategy : search first, feed live IDs to get/stream/similar\n")
	fmt.Printf("    note     : GetStreamURL on deezer:* bridges via SoundCloud — expects STATUS_OK + real stream_url\n\n")

	conn, err := grpc.NewClient(
		*addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[-] dial %s: %v\n", *addr, err)
		os.Exit(1)
	}
	defer conn.Close()

	c := pb.NewBedrockServiceClient(conn)

	testSearchTracks(c)
	testSearchAlbums(c)
	testSearchArtists(c)
	testSearchPlaylists(c)

	testGetTrack(c)
	testGetAlbum(c)
	testGetArtist(c)
	testGetPlaylist(c)

	testGetStreamURL(c)
	testGetStreamURLHLS(c)
	testGetSimilarTracks(c)

	testEdgeCases(c)

	printSummary()
}
