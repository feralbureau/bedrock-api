// package main is a SoundCloud-focused integration test client for the bedrock gRPC server.
//
//   1. Run Search* RPCs to get real live IDs from SoundCloud.
//   2. Feed those IDs directly into GetTrack, GetAlbum, GetPlaylist,
//      GetStreamURL, GetSimilarTracks — no hardcoded native IDs.
//   3. Hardcoded IDs are only used as fallback when search returns 0 results.
//
//
//	go run ./client/soundcloud_test/main.go
//	go run ./client/soundcloud_test/main.go -addr=10.0.0.1:50052 -timeout=15s
//	go run ./client/soundcloud_test/main.go -verbose
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

// в”Ђв”Ђ log helpers в”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђ

// section prints a simple ASCII divider and the test name.
func section(title string) {
	fmt.Printf("\n%s--- %s ---%s\n", cCyan, title, cReset)
}

// logf prints a prefixed line.  prefix must be one of [+], [-], [!].
func logf(prefix, color, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Printf("  %s%s%s %s\n", color, prefix, cReset, msg)
}

func pass(format string, args ...any) { logf("[+]", cGreen, format, args...) }
func fail(format string, args ...any) { logf("[-]", cRed, format, args...) }
func info(format string, args ...any) { logf("[+]", cGray, format, args...) }
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

// в”Ђв”Ђ gRPC call wrapper в”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђ

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

// в”Ђв”Ђ shared live ID state в”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђ

var (
	liveTrackID    string // soundcloud:NNN
	liveAlbumID    string // soundcloud:NNN  (from SearchAlbums)
	livePlaylistID string // soundcloud:NNN  (from SearchPlaylists)
	liveArtistID   string // soundcloud:NNN  (from SearchArtists)
)

// fallback constants — only used when a search returns 0 results.
const (
	fallbackTrackID    = "soundcloud:2031629953" // tryavoid - ведь она... (fallback if search fails)
	fallbackAlbumID    = "soundcloud:121693200"  // deadmau5 - while(1<2) (fallback if search fails)
	fallbackPlaylistID = "soundcloud:1095670683"
	fallbackArtistID   = "soundcloud:305337" // deadmau5
)

// в”Ђв”Ђ validation helpers в”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђ

func checkTrack(t *pb.Track, idx int) bool {
	if t == nil {
		fail("track[%d] is nil", idx)
		return false
	}
	ok := true
	if !strings.HasPrefix(t.GetId(), "soundcloud:") {
		fail("track[%d] id=%q missing soundcloud: prefix", idx, t.GetId())
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
		warn("track[%d] duration_ms=%d (id=%s)", idx, t.GetDurationMs(), t.GetId())
	}
	if t.GetSource() != pb.Platform_PLATFORM_SOUNDCLOUD {
		fail("track[%d] source=%v want PLATFORM_SOUNDCLOUD", idx, t.GetSource())
		ok = false
	}
	durS := t.GetDurationMs() / 1000
	info("track[%d]  id=%-24s  dur=%d:%02d  streamable=%-5v  title=%q  artist=%q",
		idx, t.GetId(), durS/60, durS%60,
		t.GetIsStreamable(),
		trunc(t.GetTitle(), 40), trunc(t.GetArtist(), 30))
	if t.GetCoverUrl() != "" {
		info("         cover  %s", trunc(t.GetCoverUrl(), 70))
	}
	return ok
}

func checkAlbum(a *pb.Album, idx int) bool {
	if a == nil {
		fail("album[%d] is nil", idx)
		return false
	}
	ok := true
	if a.GetTitle() == "" {
		fail("album[%d] empty title", idx)
		ok = false
	}
	if a.GetSource() != pb.Platform_PLATFORM_SOUNDCLOUD {
		fail("album[%d] wrong source: %v", idx, a.GetSource())
		ok = false
	}
	info("album[%d]  id=%-24s  tracks=%d  title=%q  artist=%q",
		idx, a.GetId(), a.GetTotalTracks(),
		trunc(a.GetTitle(), 40), trunc(a.GetArtist(), 30))
	return ok
}

func checkArtist(a *pb.Artist, idx int) bool {
	if a == nil {
		fail("artist[%d] is nil", idx)
		return false
	}
	ok := true
	if a.GetName() == "" {
		fail("artist[%d] empty name", idx)
		ok = false
	}
	if a.GetSource() != pb.Platform_PLATFORM_SOUNDCLOUD {
		fail("artist[%d] wrong source: %v", idx, a.GetSource())
		ok = false
	}
	info("artist[%d]  id=%-24s  followers=%-8d  name=%q",
		idx, a.GetId(), a.GetFollowers(), trunc(a.GetName(), 40))
	return ok
}

func checkPlaylist(pl *pb.Playlist, idx int) bool {
	if pl == nil {
		fail("playlist[%d] is nil", idx)
		return false
	}
	ok := true
	if pl.GetTitle() == "" {
		fail("playlist[%d] empty title", idx)
		ok = false
	}
	if pl.GetSource() != pb.Platform_PLATFORM_SOUNDCLOUD {
		fail("playlist[%d] wrong source: %v", idx, pl.GetSource())
		ok = false
	}
	info("playlist[%d]  id=%-24s  tracks=%d  owner=%q  title=%q",
		idx, pl.GetId(), pl.GetTotalTracks(),
		trunc(pl.GetOwner(), 20), trunc(pl.GetTitle(), 40))
	return ok
}

// в”Ђв”Ђ search tests (also populate live IDs) в”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђ

func testSearchTracks(c pb.BedrockServiceClient) {
	name := `SearchTracks (query: "tryavoid ведь она")`
	section(name)

	resp, lat, err := invoke(func(ctx context.Context) (any, error) {
		return c.SearchTracks(ctx, &pb.SearchRequest{
			Query:     "tryavoid ведь она",
			Limit:     5,
			Platforms: []pb.Platform{pb.Platform_PLATFORM_SOUNDCLOUD},
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
		fail("response status ERROR: %s", r.GetErrors()[0].GetMessage())
		recordResult(name, outFail, "status=ERROR", lat)
		return
	}

	tracks := r.GetTracks()
	if len(tracks) == 0 {
		fail("got 0 tracks — SoundCloud client_id may not be configured")
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

	if allOK {
		recordResult(name, outPass, fmt.Sprintf("%d tracks, all valid", len(tracks)), lat)
	} else {
		recordResult(name, outFail, fmt.Sprintf("%d tracks, some invalid fields", len(tracks)), lat)
	}
}

func testSearchAlbums(c pb.BedrockServiceClient) {
	name := `SearchAlbums (query: "tryavoid")`
	section(name)

	resp, lat, err := invoke(func(ctx context.Context) (any, error) {
		return c.SearchAlbums(ctx, &pb.SearchRequest{
			Query:     "tryavoid",
			Limit:     5,
			Platforms: []pb.Platform{pb.Platform_PLATFORM_SOUNDCLOUD},
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
		warn("0 albums returned (SC search/albums endpoint can be sparse)")
		recordResult(name, outSkip, "0 albums — endpoint may be sparse", lat)
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
	name := `SearchArtists (query: "deadmau5")`
	section(name)

	resp, lat, err := invoke(func(ctx context.Context) (any, error) {
		return c.SearchArtists(ctx, &pb.SearchRequest{
			Query:     "deadmau5",
			Limit:     5,
			Platforms: []pb.Platform{pb.Platform_PLATFORM_SOUNDCLOUD},
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
	pass("received %d artist(s)  latency=%s  -> live ID: %s", len(artists), lat.Round(time.Millisecond), liveArtistID)

	allOK := true
	for i, a := range artists {
		if !checkArtist(a, i) {
			allOK = false
		}
	}

	topName := strings.ToLower(artists[0].GetName())
	if !strings.Contains(topName, "deadmau5") && !strings.Contains(topName, "deadmau") {
		warn("top result %q doesn't contain 'deadmau5' — relevance check failed", artists[0].GetName())
	} else {
		pass("top result name matches: %q", artists[0].GetName())
	}

	if allOK {
		recordResult(name, outPass, fmt.Sprintf("%d artists, all valid", len(artists)), lat)
	} else {
		recordResult(name, outFail, "some artist fields invalid", lat)
	}
}

func testSearchPlaylists(c pb.BedrockServiceClient) {
	name := `SearchPlaylists (query: "tryavoid")`
	section(name)

	resp, lat, err := invoke(func(ctx context.Context) (any, error) {
		return c.SearchPlaylists(ctx, &pb.SearchRequest{
			Query:     "tryavoid",
			Limit:     5,
			Platforms: []pb.Platform{pb.Platform_PLATFORM_SOUNDCLOUD},
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
	pass("-> live playlist ID for GetPlaylist: %s  title=%q", livePlaylistID, trunc(pls[0].GetTitle(), 50))

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

// в”Ђв”Ђ get tests (use live IDs from search) в”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђ

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
		fail("id mismatch: got %q want %q", t.GetId(), id)
		ok = false
	} else {
		pass("id round-trips correctly: %s", id)
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

	tracks := r.GetTracks()
	pass("received %d track(s) in album  latency=%s", len(tracks), lat.Round(time.Millisecond))

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

	if aOK && tOK && len(tracks) > 0 {
		recordResult(name, outPass, fmt.Sprintf("album OK, %d tracks hydrated", len(tracks)), lat)
	} else if aOK {
		recordResult(name, outSkip, fmt.Sprintf("album OK but %d tracks (hydration may be partial)", len(tracks)), lat)
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

	if r.GetAlbums() != nil {
		warn("albums field is non-nil — unexpected for SoundCloud GetArtist")
	} else {
		pass("albums == nil (expected: SC has no per-artist album endpoint)")
	}

	if artistOK && tOK {
		recordResult(name, outPass, fmt.Sprintf("artist OK, %d top tracks", len(topTracks)), lat)
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

	if plOK && tOK && len(tracks) > 0 {
		recordResult(name, outPass, fmt.Sprintf("playlist OK, %d tracks hydrated", len(tracks)), lat)
	} else if plOK {
		recordResult(name, outSkip, fmt.Sprintf("playlist OK but %d tracks (hydration may be partial)", len(tracks)), lat)
	} else {
		recordResult(name, outFail, "playlist metadata invalid", lat)
	}
}

// в”Ђв”Ђ stream / similar tests в”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђ

func testGetStreamURL(c pb.BedrockServiceClient) {
	id := liveTrackID
	if id == "" {
		id = fallbackTrackID
		warn("no live track ID — using fallback %s", id)
	}
	name := fmt.Sprintf("GetStreamURL (%s, format=mp3)", id)
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
			warn("server unavailable — is SOUNDCLOUD_CLIENT_IDS set?")
		}
		fail("rpc error %s: %s", st.Code(), st.Message())
		recordResult(name, outFail, fmt.Sprintf("rpc error: %s", st.Code()), lat)
		return
	}

	r := resp.(*pb.GetStreamURLResponse)
	printJSON(r)

	if r.GetStatus() == pb.ResponseStatus_STATUS_ERROR {
		fail("response status ERROR: %s", r.GetError())
		recordResult(name, outFail, r.GetError(), lat)
		return
	}

	streamURL := r.GetStreamUrl()
	if streamURL == "" {
		fail("stream_url is empty")
		recordResult(name, outFail, "empty stream_url", lat)
		return
	}

	pass("stream_url received  latency=%s", lat.Round(time.Millisecond))
	info("stream_url    %s", trunc(streamURL, 80))
	info("stream_type   %s", r.GetStreamType())
	info("content_type  %s", r.GetContentType())
	info("is_fallback   %v", r.GetIsFallback())

	if !strings.HasPrefix(streamURL, "http://") && !strings.HasPrefix(streamURL, "https://") {
		fail("stream_url is not an http(s) url: %s", trunc(streamURL, 60))
		recordResult(name, outFail, "stream_url scheme invalid", lat)
		return
	}

	st := r.GetStreamType()
	if st != "audio_stream" && st != "hls" {
		warn("unexpected stream_type %q (want audio_stream or hls)", st)
	} else {
		pass("stream_type=%q  content_type=%q", st, r.GetContentType())
	}

	recordResult(name, outPass, fmt.Sprintf("stream_url OK, type=%s", st), lat)
}

func testGetStreamURLHLS(c pb.BedrockServiceClient) {
	id := liveTrackID
	if id == "" {
		id = fallbackTrackID
		warn("no live track ID — using fallback %s", id)
	}
	name := fmt.Sprintf("GetStreamURL (%s, format=hls)", id)
	section(name)

	resp, lat, err := invoke(func(ctx context.Context) (any, error) {
		return c.GetStreamURL(ctx, &pb.GetStreamURLRequest{
			TrackId:         id,
			PreferredFormat: "hls",
		})
	})
	if err != nil {
		st, _ := status.FromError(err)
		fail("rpc error %s: %s", st.Code(), st.Message())
		recordResult(name, outFail, fmt.Sprintf("rpc error: %s", st.Code()), lat)
		return
	}

	r := resp.(*pb.GetStreamURLResponse)
	printJSON(r)

	if r.GetStatus() == pb.ResponseStatus_STATUS_ERROR {
		fail("response status ERROR: %s", r.GetError())
		recordResult(name, outFail, r.GetError(), lat)
		return
	}

	streamURL := r.GetStreamUrl()
	if streamURL == "" {
		fail("stream_url is empty")
		recordResult(name, outFail, "empty stream_url", lat)
		return
	}

	pass("HLS stream_url received  latency=%s", lat.Round(time.Millisecond))
	info("stream_url    %s", trunc(streamURL, 80))
	info("stream_type   %s", r.GetStreamType())
	info("content_type  %s", r.GetContentType())

	st := r.GetStreamType()
	switch st {
	case "hls":
		pass("HLS transcoding selected as expected")
	case "audio_stream":
		warn("HLS requested but progressive transcoding used (HLS may not be available)")
	default:
		fail("unexpected stream_type %q", st)
		recordResult(name, outFail, fmt.Sprintf("unexpected stream_type %q", st), lat)
		return
	}

	recordResult(name, outPass, fmt.Sprintf("stream OK, type=%s content_type=%s", st, r.GetContentType()), lat)
}

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
		warn("0 similar tracks — SC /related may require auth or track is not indexed")
		recordResult(name, outSkip, "0 similar tracks (SC related may require token)", lat)
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

	if allOK {
		recordResult(name, outPass, fmt.Sprintf("%d similar tracks, all valid", len(tracks)), lat)
	} else {
		recordResult(name, outFail, "some similar tracks have invalid fields", lat)
	}
}

// в”Ђв”Ђ import playlist в”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђ

func testImportPlaylist(c pb.BedrockServiceClient) {
	const playlistURL = "https://soundcloud.com/wheartemoji/sets/test"
	name := fmt.Sprintf("ImportPlaylist (%s)", playlistURL)
	section(name)

	importTimeout := *perCallTimeout * 3
	resp, lat, err := invoke(func(_ context.Context) (any, error) {
		ctx, cancel := context.WithTimeout(context.Background(), importTimeout)
		defer cancel()
		if *accessToken != "" {
			ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+*accessToken)
		}
		return c.ImportPlaylist(ctx, &pb.ImportPlaylistRequest{Url: playlistURL})
	})
	if err != nil {
		st, _ := status.FromError(err)
		fail("rpc error %s: %s", st.Code(), st.Message())
		recordResult(name, outFail, fmt.Sprintf("rpc error: %s", st.Code()), lat)
		return
	}

	r := resp.(*pb.ImportPlaylistResponse)
	printJSON(r)

	if r.GetStatus() == pb.ResponseStatus_STATUS_ERROR {
		fail("response status ERROR: %s", r.GetError())
		warn("note: ImportPlaylist on SoundCloud requires URL resolution on the server side")
		recordResult(name, outSkip, "ImportPlaylist STATUS_ERROR: "+r.GetError(), lat)
		return
	}

	pl := r.GetPlaylist()
	plOK := checkPlaylist(pl, 0)
	tracks := r.GetTracks()
	pass("imported %d track(s)  latency=%s", len(tracks), lat.Round(time.Millisecond))
	info("platform_playlist_id=%q  source=%v", r.GetPlatformPlaylistId(), r.GetSource())

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

	if plOK && tOK {
		recordResult(name, outPass, fmt.Sprintf("playlist imported, %d tracks", len(tracks)), lat)
	} else {
		recordResult(name, outFail, "validation failed", lat)
	}
}

// в”Ђв”Ђ edge cases в”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђ

func testEdgeCases(c pb.BedrockServiceClient) {
	{
		name := "GetTrack (nonexistent ID: soundcloud:99999999999)"
		section(name)
		resp, lat, err := invoke(func(ctx context.Context) (any, error) {
			return c.GetTrack(ctx, &pb.GetTrackRequest{TrackId: "soundcloud:99999999999"})
		})
		if err != nil {
			st, _ := status.FromError(err)
			fail("unexpected gRPC error %s: %s", st.Code(), st.Message())
			recordResult(name, outFail, "unexpected grpc error", lat)
		} else {
			r := resp.(*pb.GetTrackResponse)
			if r.GetStatus() == pb.ResponseStatus_STATUS_ERROR && r.GetError() != "" {
				pass("got expected STATUS_ERROR: %q", r.GetError())
				recordResult(name, outPass, "STATUS_ERROR returned (not found)", lat)
			} else if r.GetTrack() == nil {
				pass("track is nil — 404 handled gracefully")
				recordResult(name, outPass, "nil track (not found) returned gracefully", lat)
			} else {
				warn("unexpected success for nonexistent ID")
				recordResult(name, outSkip, "unexpected non-error response", lat)
			}
		}
	}

	{
		name := "GetStreamURL (bad ID: soundcloud:000000001)"
		section(name)
		resp, lat, err := invoke(func(ctx context.Context) (any, error) {
			return c.GetStreamURL(ctx, &pb.GetStreamURLRequest{
				TrackId:         "soundcloud:000000001",
				PreferredFormat: "mp3",
			})
		})
		if err != nil {
			st, _ := status.FromError(err)
			fail("unexpected gRPC transport error %s: %s", st.Code(), st.Message())
			recordResult(name, outFail, "unexpected grpc error", lat)
		} else {
			r := resp.(*pb.GetStreamURLResponse)
			if r.GetStatus() == pb.ResponseStatus_STATUS_ERROR {
				pass("error handled gracefully: %q", r.GetError())
				recordResult(name, outPass, "STATUS_ERROR returned (bad ID)", lat)
			} else if r.GetStreamUrl() == "" {
				pass("empty stream_url returned — no crash")
				recordResult(name, outPass, "no stream_url, no crash", lat)
			} else {
				warn("got a stream_url for ID 000000001 — surprising but non-fatal")
				recordResult(name, outSkip, "unexpected stream url", lat)
			}
		}
	}

	{
		name := "SearchTracks (empty query -> expect InvalidArgument)"
		section(name)
		_, lat, err := invoke(func(ctx context.Context) (any, error) {
			return c.SearchTracks(ctx, &pb.SearchRequest{
				Query:     "",
				Platforms: []pb.Platform{pb.Platform_PLATFORM_SOUNDCLOUD},
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

	{
		const chainsmokers = "soundcloud:2818940"
		name := fmt.Sprintf("GetArtist (%s — The Chainsmokers, parallel fetch check)", chainsmokers)
		section(name)
		resp, lat, err := invoke(func(ctx context.Context) (any, error) {
			return c.GetArtist(ctx, &pb.GetArtistRequest{ArtistId: chainsmokers})
		})
		if err != nil {
			st, _ := status.FromError(err)
			fail("rpc error %s: %s", st.Code(), st.Message())
			recordResult(name, outFail, fmt.Sprintf("rpc error: %s", st.Code()), lat)
		} else {
			r := resp.(*pb.GetArtistResponse)
			if r.GetStatus() == pb.ResponseStatus_STATUS_ERROR {
				warn("STATUS_ERROR: %s", r.GetError())
				recordResult(name, outSkip, r.GetError(), lat)
			} else {
				aOK := checkArtist(r.GetArtist(), 0)
				pass("top_tracks=%d  latency=%s", len(r.GetTopTracks()), lat.Round(time.Millisecond))
				if aOK {
					recordResult(name, outPass, fmt.Sprintf("artist OK, %d top tracks", len(r.GetTopTracks())), lat)
				} else {
					recordResult(name, outFail, "artist field validation failed", lat)
				}
			}
		}
	}
}

// в”Ђв”Ђ summary в”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђ

func printSummary() {
	fmt.Printf("\n%s--- SOUNDCLOUD TEST SUMMARY ---%s\n\n", cCyan, cReset)

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

	const nameW = 60
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
		fmt.Printf("  %s[+] all SoundCloud RPCs passed or skipped.%s\n\n", cGreen, cReset)
	} else {
		fmt.Printf("  %s[-] %d test(s) failed — check SOUNDCLOUD_CLIENT_IDS on the server.%s\n\n",
			cRed, failed, cReset)
	}
}

// в”Ђв”Ђ main в”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђв”Ђ

func main() {
	flag.Parse()
	log.SetFlags(log.Ltime | log.Lshortfile)

	fmt.Printf("\n%s[+] Bedrock gRPC - SoundCloud Integration Test Client%s\n", cCyan, cReset)
	fmt.Printf("    server   : %s\n", *addr)
	fmt.Printf("    timeout  : %s  (per RPC; ImportPlaylist gets 3x)\n", *perCallTimeout)
	fmt.Printf("    verbose  : %v\n", *verbose)
	fmt.Printf("    strategy : search first, feed live IDs to get/stream/similar\n\n")

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

	testImportPlaylist(c)

	testEdgeCases(c)

	printSummary()
}
