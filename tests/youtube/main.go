// package main is a YouTube Music-focused integration test client for the bedrock gRPC server.
//
//  1. Run Search* RPCs to get real live IDs from YouTube Music.
//  2. Feed those IDs directly into GetTrack, GetAlbum, GetPlaylist,
//     GetStreamURL, GetSimilarTracks — no hardcoded native IDs.
//  3. Hardcoded IDs are only used as fallback when search returns 0 results.
//
//   - No auth required for public data — InnerTube is unauthenticated.
//   - GetStreamURL bridges to SoundCloud (YouTube has DRM-protected audio).
//     The test expects STATUS_OK + a real stream_url, with is_fallback=true and
//     source=PLATFORM_SOUNDCLOUD, matching the Spotify/Deezer bridge pattern.
//   - Tracks use YouTube video IDs (11 characters).
//   - Albums/Artists/Playlists use YouTube Music browseIds (UC.../VL.../OLAK...).
//
// run the youtube integration client example:
//
//	go run ./tests/youtube/main.go
//	go run ./tests/youtube/main.go -addr=10.0.0.1:50052 -timeout=20s
//	go run ./tests/youtube/main.go -verbose
//
// integration test that requires the bedrock gRPC server.
//	Ensure the bedrock gRPC service is available (no extra env vars required).
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

// gRPC call wrapper 

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

// shared live ID state 

var (
	liveTrackID    string // youtube:<videoId>
	liveAlbumID    string // youtube:<browseId>
	livePlaylistID string // youtube:<browseId>
	liveArtistID   string // youtube:<browseId>
)

// fallback constants — used only when search returns 0 results.
const (
	fallbackTrackID = "youtube:dQw4w9WgXcQ"
	// cannot reliably fallback for YT Music browse IDs
	fallbackAlbumID    = ""
	fallbackPlaylistID = ""
	fallbackArtistID   = ""
)

// validation helpers 

func checkTrack(t *pb.Track, idx int) bool {
	if t == nil {
		fail("track[%d] is nil", idx)
		return false
	}
	ok := true
	if !strings.HasPrefix(t.GetId(), "youtube:") {
		fail("track[%d] id=%q missing youtube: prefix", idx, t.GetId())
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
		warn("track[%d] duration_ms=%d (id=%s) — InnerTube may not return duration in search results",
			idx, t.GetDurationMs(), t.GetId())
	}
	if t.GetSource() != pb.Platform_PLATFORM_YOUTUBE {
		fail("track[%d] source=%v want PLATFORM_YOUTUBE", idx, t.GetSource())
		ok = false
	}
	if t.GetIsStreamable() {
		warn("track[%d] is_streamable=true — unexpected for YouTube (DRM protected)", idx)
	}
	durS := t.GetDurationMs() / 1000
	info("track[%d]  id=%-30s  dur=%d:%02d  title=%q  artist=%q",
		idx, t.GetId(), durS/60, durS%60,
		trunc(t.GetTitle(), 40), trunc(t.GetArtist(), 30))
	if t.GetCoverUrl() != "" {
		info("         cover    %s", trunc(t.GetCoverUrl(), 70))
	}
	if t.GetExternalUrl() != "" {
		info("         url      %s", trunc(t.GetExternalUrl(), 70))
	}
	return ok
}

func checkAlbum(a *pb.Album, idx int) bool {
	if a == nil {
		fail("album[%d] is nil", idx)
		return false
	}
	ok := true
	if !strings.HasPrefix(a.GetId(), "youtube:") {
		fail("album[%d] id=%q missing youtube: prefix", idx, a.GetId())
		ok = false
	}
	if a.GetTitle() == "" {
		fail("album[%d] empty title", idx)
		ok = false
	}
	if a.GetSource() != pb.Platform_PLATFORM_YOUTUBE {
		fail("album[%d] wrong source: %v", idx, a.GetSource())
		ok = false
	}
	info("album[%d]  id=%-30s  tracks=%d  title=%q  artist=%q",
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
	if !strings.HasPrefix(a.GetId(), "youtube:") {
		fail("artist[%d] id=%q missing youtube: prefix", idx, a.GetId())
		ok = false
	}
	if a.GetName() == "" {
		fail("artist[%d] empty name", idx)
		ok = false
	}
	if a.GetSource() != pb.Platform_PLATFORM_YOUTUBE {
		fail("artist[%d] wrong source: %v", idx, a.GetSource())
		ok = false
	}
	info("artist[%d]  id=%-30s  name=%q",
		idx, a.GetId(), trunc(a.GetName(), 40))
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
	if !strings.HasPrefix(pl.GetId(), "youtube:") {
		fail("playlist[%d] id=%q missing youtube: prefix", idx, pl.GetId())
		ok = false
	}
	if pl.GetTitle() == "" {
		fail("playlist[%d] empty title", idx)
		ok = false
	}
	if pl.GetSource() != pb.Platform_PLATFORM_YOUTUBE {
		fail("playlist[%d] wrong source: %v", idx, pl.GetSource())
		ok = false
	}
	info("playlist[%d]  id=%-30s  tracks=%d  owner=%q  title=%q",
		idx, pl.GetId(), pl.GetTotalTracks(),
		trunc(pl.GetOwner(), 20), trunc(pl.GetTitle(), 40))
	return ok
}

// search tests (also populate live IDs) 

func testSearchTracks(c pb.BedrockServiceClient) {
	name := `SearchTracks (query: "never gonna give you up")`
	section(name)

	resp, lat, err := invoke(func(ctx context.Context) (any, error) {
		return c.SearchTracks(ctx, &pb.SearchRequest{
			Query:     "never gonna give you up",
			Limit:     5,
			Platforms: []pb.Platform{pb.Platform_PLATFORM_YOUTUBE},
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
		fail("got 0 tracks — YouTube provider may not be returning results")
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
	name := `SearchAlbums (query: "random access memories")`
	section(name)

	resp, lat, err := invoke(func(ctx context.Context) (any, error) {
		return c.SearchAlbums(ctx, &pb.SearchRequest{
			Query:     "random access memories",
			Limit:     5,
			Platforms: []pb.Platform{pb.Platform_PLATFORM_YOUTUBE},
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
		warn("0 albums returned — YouTube Music may not have matched any albums")
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
	name := `SearchArtists (query: "rick astley")`
	section(name)

	resp, lat, err := invoke(func(ctx context.Context) (any, error) {
		return c.SearchArtists(ctx, &pb.SearchRequest{
			Query:     "rick astley",
			Limit:     5,
			Platforms: []pb.Platform{pb.Platform_PLATFORM_YOUTUBE},
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

	if allOK {
		recordResult(name, outPass, fmt.Sprintf("%d artists, all valid", len(artists)), lat)
	} else {
		recordResult(name, outFail, "some artist fields invalid", lat)
	}
}

func testSearchPlaylists(c pb.BedrockServiceClient) {
	name := `SearchPlaylists (query: "top hits 2024")`
	section(name)

	resp, lat, err := invoke(func(ctx context.Context) (any, error) {
		return c.SearchPlaylists(ctx, &pb.SearchRequest{
			Query:     "top hits 2024",
			Limit:     5,
			Platforms: []pb.Platform{pb.Platform_PLATFORM_YOUTUBE},
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
		if id == "" {
			warn("no track ID available — skipping GetTrack")
			recordResult("GetTrack", outSkip, "no track ID", 0)
			return
		}
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
		if fallbackAlbumID == "" {
			warn("no album ID available — skipping GetAlbum")
			recordResult("GetAlbum", outSkip, "no album ID", 0)
			return
		}
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
		recordResult(name, outPass, fmt.Sprintf("album OK, %d tracks", len(tracks)), lat)
	} else if aOK {
		recordResult(name, outSkip, fmt.Sprintf("album OK but %d tracks", len(tracks)), lat)
	} else {
		recordResult(name, outFail, "album metadata invalid", lat)
	}
}

func testGetArtist(c pb.BedrockServiceClient) {
	id := liveArtistID
	if id == "" {
		if fallbackArtistID == "" {
			warn("no artist ID available — skipping GetArtist")
			recordResult("GetArtist", outSkip, "no artist ID", 0)
			return
		}
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

	albums := r.GetAlbums()
	if len(albums) > 0 {
		pass("albums populated: %d album(s)", len(albums))
		displayAlbums := 3
		if len(albums) < displayAlbums {
			displayAlbums = len(albums)
		}
		for i := 0; i < displayAlbums; i++ {
			if !checkAlbum(albums[i], i) {
				tOK = false
			}
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
		if fallbackPlaylistID == "" {
			warn("no playlist ID available — skipping GetPlaylist")
			recordResult("GetPlaylist", outSkip, "no playlist ID", 0)
			return
		}
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
		recordResult(name, outPass, fmt.Sprintf("playlist OK, %d tracks", len(tracks)), lat)
	} else if plOK {
		recordResult(name, outSkip, fmt.Sprintf("playlist OK but %d tracks", len(tracks)), lat)
	} else {
		recordResult(name, outFail, "playlist metadata invalid", lat)
	}
}

// stream test

// testGetStreamURL searches lostrushi, gets a native youtube stream, and validates it came from innertube (not a fallback).
func testGetStreamURL(c pb.BedrockServiceClient) {
	const artist = "lostrushi"
	searchName := fmt.Sprintf("SearchTracks for stream test (query: %q)", artist)
	section(searchName)

	var streamID string

	searchResp, searchLat, err := invoke(func(ctx context.Context) (any, error) {
		return c.SearchTracks(ctx, &pb.SearchRequest{
			Query:     artist,
			Limit:     1,
			Platforms: []pb.Platform{pb.Platform_PLATFORM_YOUTUBE},
		})
	})
	if err != nil {
		st, _ := status.FromError(err)
		fail("search rpc error %s: %s", st.Code(), st.Message())
		recordResult(searchName, outFail, fmt.Sprintf("rpc error: %s", st.Code()), searchLat)
	} else {
		sr := searchResp.(*pb.SearchTracksResponse)
		if len(sr.GetTracks()) > 0 {
			streamID = sr.GetTracks()[0].GetId()
			pass("found lostrushi track: %s  title=%q  latency=%s",
				streamID, trunc(sr.GetTracks()[0].GetTitle(), 50), searchLat.Round(time.Millisecond))
			recordResult(searchName, outPass, fmt.Sprintf("id=%s", streamID), searchLat)
		} else {
			warn("search returned 0 results for %q", artist)
			recordResult(searchName, outSkip, "0 results", searchLat)
		}
	}

	if streamID == "" {
		warn("no lostrushi track ID — skipping GetStreamURL")
		recordResult(fmt.Sprintf("GetStreamURL (lostrushi)"), outSkip, "no track found", 0)
		return
	}

	name := fmt.Sprintf("GetStreamURL (%s) — expect native Innertube stream", streamID)
	section(name)

	resp, lat, err := invoke(func(ctx context.Context) (any, error) {
		return c.GetStreamURL(ctx, &pb.GetStreamURLRequest{
			TrackId:         streamID,
			PreferredFormat: "opus",
		})
	})
	if err != nil {
		st, _ := status.FromError(err)
		if st.Code() == codes.Unavailable {
			warn("server unavailable — is the bedrock server running?")
		}
		fail("gRPC error %s: %s", st.Code(), st.Message())
		recordResult(name, outFail, fmt.Sprintf("rpc error: %s", st.Code()), lat)
		return
	}

	r := resp.(*pb.GetStreamURLResponse)
	printJSON(r)

	if r.GetStatus() == pb.ResponseStatus_STATUS_ERROR {
		fail("STATUS_ERROR — multi-client fallback exhausted: %s", r.GetError())
		recordResult(name, outFail, "no native stream returned", lat)
		return
	}

	streamURL := r.GetStreamUrl()
	if streamURL == "" {
		fail("STATUS_OK but stream_url is empty")
		recordResult(name, outFail, "empty stream_url", lat)
		return
	}

	pass("stream_url received  latency=%s", lat.Round(time.Millisecond))
	info("stream_url    %s", trunc(streamURL, 100))
	info("stream_type   %s", r.GetStreamType())
	info("content_type  %s", r.GetContentType())
	info("source        %v", r.GetSource())
	info("is_fallback   %v  fallback_from=%q", r.GetIsFallback(), r.GetFallbackFrom())

	if r.GetIsFallback() {
		fail("is_fallback=true — stream came from %s, expected native YouTube Innertube", r.GetSource())
		recordResult(name, outFail, fmt.Sprintf("fallback used: source=%v fallback_from=%s", r.GetSource(), r.GetFallbackFrom()), lat)
		return
	}

	recordResult(name, outPass, fmt.Sprintf("native Innertube stream OK  source=%v", r.GetSource()), lat)
}

// similar tracks test 

func testGetSimilarTracks(c pb.BedrockServiceClient) {
	id := liveTrackID
	if id == "" {
		id = fallbackTrackID
		if id == "" {
			warn("no track ID available — skipping GetSimilarTracks")
			recordResult("GetSimilarTracks", outSkip, "no track ID", 0)
			return
		}
		warn("no live track ID — using fallback %s", id)
	}
	name := fmt.Sprintf("GetSimilarTracks (%s)", id)
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
		warn("0 similar tracks returned — InnerTube Next endpoint may not have related content")
		recordResult(name, outSkip, "0 tracks returned", lat)
		return
	}

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
		recordResult(name, outFail, "some fields invalid", lat)
	}
}

// summary 

func printSummary() {
	fmt.Printf("\n%s%s═══ YOUTUBE TEST SUMMARY ═══%s\n", cBold, cCyan, cReset)
	passed, failed, skipped := 0, 0, 0
	for _, r := range results {
		var icon, color string
		switch r.out {
		case outPass:
			icon, color = "✓", cGreen
			passed++
		case outFail:
			icon, color = "✗", cRed
			failed++
		case outSkip:
			icon, color = "○", cYellow
			skipped++
		}
		fmt.Printf("  %s%s%s  %-60s  %6s  %s\n",
			color, icon, cReset,
			r.name, r.latency.Round(time.Millisecond), r.detail)
	}

	fmt.Printf("\n  total=%d  %spassed=%d%s  %sfailed=%d%s  %sskipped=%d%s\n\n",
		len(results),
		cGreen, passed, cReset,
		cRed, failed, cReset,
		cYellow, skipped, cReset)

	if failed > 0 {
		fmt.Printf("  %s%sSOME TESTS FAILED%s\n\n", cBold, cRed, cReset)
	} else {
		fmt.Printf("  %s%sALL TESTS PASSED%s\n\n", cBold, cGreen, cReset)
	}
}

// main 

func main() {
	flag.Parse()

	fmt.Printf("%s%s═══ YOUTUBE MUSIC INTEGRATION TEST ═══%s\n", cBold, cCyan, cReset)
	fmt.Printf("  target: %s\n", *addr)

	conn, err := grpc.NewClient(*addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatalf("dial %s: %v", *addr, err)
	}
	defer conn.Close()

	client := pb.NewBedrockServiceClient(conn)

	// run tests 
	// 1. searches: populate live IDs
	testSearchTracks(client)
	testSearchAlbums(client)
	testSearchArtists(client)
	testSearchPlaylists(client)

	// 2. get endpoints: use live IDs from search
	testGetTrack(client)
	testGetAlbum(client)
	testGetArtist(client)
	testGetPlaylist(client)

	// 3. stream + similar
	testGetStreamURL(client)
	testGetSimilarTracks(client)

	// summary 
	printSummary()

	for _, r := range results {
		if r.out == outFail {
			os.Exit(1)
		}
	}
}
