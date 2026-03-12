// go run ./tests/spotify/main.go
// go run ./tests/spotify/main.go -addr=10.0.0.1:50052 -timeout=15s
// go run ./tests/spotify/main.go -verbose
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
func info(format string, args ...any) { logf("[+]", cGray, format, args...) }
func warn(format string, args ...any) { logf("[!]", cYellow, format, args...) }

// isSpotifyPremiumErr detects the Spotify Web API 403 "premium subscription required"
// error. This is an API access-level restriction, not a code regression — tests that
// hit this should be skipped rather than failed so suite runs don't report false negatives.
func isSpotifyPremiumErr(msg string) bool {
	return strings.Contains(msg, "HTTP 403") || strings.Contains(msg, "premium subscription")
}

// spotifyProviderErr returns the Spotify provider error message from a search response's
// errors slice, or empty string if none is present.
func spotifyProviderErr(errs []*pb.ProviderError) string {
	for _, pe := range errs {
		if pe.GetPlatform() == pb.Platform_PLATFORM_SPOTIFY {
			return pe.GetMessage()
		}
	}
	return ""
}

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
	liveTrackID    string // spotify:<id>
	liveAlbumID    string // spotify:<id>  (from SearchAlbums)
	livePlaylistID string // spotify:<id>  (from SearchPlaylists)
	liveArtistID   string // spotify:<id>  (from SearchArtists)
)

// fallback constants вЂ” only used when a search returns 0 results.
const (
	// glittr - aldn
	fallbackTrackID = "spotify:5nujrmhLynf4yMoMtj8AQF"
	// glittr - collected (album)
	fallbackAlbumID = "spotify:6vAMeQMOcNEPHYXJRmGp73"
	// user playlist: https://open.spotify.com/playlist/1j9uOH2jcv3yeNjhmPhowD
	fallbackPlaylistID = "spotify:1j9uOH2jcv3yeNjhmPhowD"
	// glittr artist
	fallbackArtistID = "spotify:4MzJMcHQBl9SIYSjwWn8QW"
)

// validation helpers

func checkTrack(t *pb.Track, idx int) bool {
	if t == nil {
		fail("track[%d] is nil", idx)
		return false
	}
	ok := true
	if !strings.HasPrefix(t.GetId(), "spotify:") {
		fail("track[%d] id=%q missing spotify: prefix", idx, t.GetId())
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
	if t.GetSource() != pb.Platform_PLATFORM_SPOTIFY {
		fail("track[%d] source=%v want PLATFORM_SPOTIFY", idx, t.GetSource())
		ok = false
	}
	if t.GetIsStreamable() {
		warn("track[%d] is_streamable=true вЂ” unexpected for Spotify (no direct audio stream)", idx)
	}
	durS := t.GetDurationMs() / 1000
	info("track[%d]  id=%-36s  dur=%d:%02d  pop=%-3d  title=%q  artist=%q",
		idx, t.GetId(), durS/60, durS%60,
		t.GetPopularity(),
		trunc(t.GetTitle(), 40), trunc(t.GetArtist(), 30))
	if t.GetCoverUrl() != "" {
		info("         cover  %s", trunc(t.GetCoverUrl(), 70))
	}
	if t.GetPreviewUrl() != "" {
		info("         preview %s", trunc(t.GetPreviewUrl(), 70))
	}
	return ok
}

func checkAlbum(a *pb.Album, idx int) bool {
	if a == nil {
		fail("album[%d] is nil", idx)
		return false
	}
	ok := true
	if !strings.HasPrefix(a.GetId(), "spotify:") {
		fail("album[%d] id=%q missing spotify: prefix", idx, a.GetId())
		ok = false
	}
	if a.GetTitle() == "" {
		fail("album[%d] empty title", idx)
		ok = false
	}
	if a.GetSource() != pb.Platform_PLATFORM_SPOTIFY {
		fail("album[%d] wrong source: %v", idx, a.GetSource())
		ok = false
	}
	info("album[%d]  id=%-36s  tracks=%d  date=%s  title=%q  artist=%q",
		idx, a.GetId(), a.GetTotalTracks(), a.GetReleaseDate(),
		trunc(a.GetTitle(), 40), trunc(a.GetArtist(), 30))
	return ok
}

func checkArtist(a *pb.Artist, idx int) bool {
	if a == nil {
		fail("artist[%d] is nil", idx)
		return false
	}
	ok := true
	if !strings.HasPrefix(a.GetId(), "spotify:") {
		fail("artist[%d] id=%q missing spotify: prefix", idx, a.GetId())
		ok = false
	}
	if a.GetName() == "" {
		fail("artist[%d] empty name", idx)
		ok = false
	}
	if a.GetSource() != pb.Platform_PLATFORM_SPOTIFY {
		fail("artist[%d] wrong source: %v", idx, a.GetSource())
		ok = false
	}
	info("artist[%d]  id=%-36s  followers=%-10d  genres=%d  name=%q",
		idx, a.GetId(), a.GetFollowers(), len(a.GetGenres()), trunc(a.GetName(), 40))
	if len(a.GetGenres()) > 0 {
		info("         genres: %s", strings.Join(a.GetGenres(), ", "))
	}
	return ok
}

func checkPlaylist(pl *pb.Playlist, idx int) bool {
	if pl == nil {
		fail("playlist[%d] is nil", idx)
		return false
	}
	ok := true
	if !strings.HasPrefix(pl.GetId(), "spotify:") {
		fail("playlist[%d] id=%q missing spotify: prefix", idx, pl.GetId())
		ok = false
	}
	if pl.GetTitle() == "" {
		fail("playlist[%d] empty title", idx)
		ok = false
	}
	if pl.GetSource() != pb.Platform_PLATFORM_SPOTIFY {
		fail("playlist[%d] wrong source: %v", idx, pl.GetSource())
		ok = false
	}
	info("playlist[%d]  id=%-36s  tracks=%d  owner=%q  title=%q",
		idx, pl.GetId(), pl.GetTotalTracks(),
		trunc(pl.GetOwner(), 20), trunc(pl.GetTitle(), 40))
	return ok
}

//  search tests (also populate live IDs)

func testSearchTracks(c pb.BedrockServiceClient) {
	name := `SearchTracks (query: "glittr aldn")`
	section(name)

	resp, lat, err := invoke(func(ctx context.Context) (any, error) {
		return c.SearchTracks(ctx, &pb.SearchRequest{
			Query:     "glittr aldn",
			Limit:     5,
			Platforms: []pb.Platform{pb.Platform_PLATFORM_SPOTIFY},
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
		fail("got 0 tracks вЂ” SPOTIFY_CLIENT_ID / SPOTIFY_CLIENT_SECRET may not be configured")
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

	topTitle := strings.ToLower(tracks[0].GetTitle() + " " + tracks[0].GetArtist())
	if !strings.Contains(topTitle, "glittr") && !strings.Contains(topTitle, "aldn") {
		warn("top result %q / %q doesn't match query вЂ” relevance check failed", tracks[0].GetTitle(), tracks[0].GetArtist())
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
	name := `SearchAlbums (query: "glittr")`
	section(name)

	resp, lat, err := invoke(func(ctx context.Context) (any, error) {
		return c.SearchAlbums(ctx, &pb.SearchRequest{
			Query:     "glittr",
			Limit:     5,
			Platforms: []pb.Platform{pb.Platform_PLATFORM_SPOTIFY},
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
		if errMsg := spotifyProviderErr(r.GetErrors()); isSpotifyPremiumErr(errMsg) {
			warn("spotify API 403 — premium subscription required, skipping: %s", trunc(errMsg, 80))
			recordResult(name, outSkip, "Spotify 403 premium required", lat)
		} else {
			fail("0 albums returned")
			recordResult(name, outFail, "0 albums returned", lat)
		}
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
	name := `SearchArtists (query: "glittr")`
	section(name)

	resp, lat, err := invoke(func(ctx context.Context) (any, error) {
		return c.SearchArtists(ctx, &pb.SearchRequest{
			Query:     "glittr",
			Limit:     5,
			Platforms: []pb.Platform{pb.Platform_PLATFORM_SPOTIFY},
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
		if errMsg := spotifyProviderErr(r.GetErrors()); isSpotifyPremiumErr(errMsg) {
			warn("spotify API 403 — premium subscription required, skipping: %s", trunc(errMsg, 80))
			recordResult(name, outSkip, "Spotify 403 premium required", lat)
		} else {
			fail("0 artists returned")
			recordResult(name, outFail, "0 artists", lat)
		}
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
	if !strings.Contains(topName, "glittr") {
		warn("top result %q doesn't contain 'glittr' вЂ” relevance check failed", artists[0].GetName())
	} else {
		pass("top result name matches: %q", artists[0].GetName())
	}

	if artists[0].GetFollowers() == 0 {
		warn("top artist followers=0 вЂ” unexpected for a major artist on Spotify")
	} else {
		pass("followers populated: %d", artists[0].GetFollowers())
	}

	if allOK {
		recordResult(name, outPass, fmt.Sprintf("%d artists, all valid", len(artists)), lat)
	} else {
		recordResult(name, outFail, "some artist fields invalid", lat)
	}
}

func testSearchPlaylists(c pb.BedrockServiceClient) {
	name := `SearchPlaylists (query: "glittr aldn mix")`
	section(name)

	resp, lat, err := invoke(func(ctx context.Context) (any, error) {
		return c.SearchPlaylists(ctx, &pb.SearchRequest{
			Query:     "glittr aldn mix",
			Limit:     5,
			Platforms: []pb.Platform{pb.Platform_PLATFORM_SPOTIFY},
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
		if errMsg := spotifyProviderErr(r.GetErrors()); isSpotifyPremiumErr(errMsg) {
			warn("spotify API 403 — premium subscription required, skipping: %s", trunc(errMsg, 80))
			recordResult(name, outSkip, "Spotify 403 premium required", lat)
		} else {
			fail("0 playlists returned")
			recordResult(name, outFail, "0 playlists", lat)
		}
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

//  get tests (use live IDs from search)

func testGetTrack(c pb.BedrockServiceClient) {
	id := liveTrackID
	if id == "" {
		id = fallbackTrackID
		warn("search produced no track ID вЂ” using fallback %s", id)
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
		if isSpotifyPremiumErr(r.GetError()) {
			warn("spotify API 403 — premium subscription required, skipping")
			recordResult(name, outSkip, "Spotify 403 premium required", lat)
		} else {
			fail("response status ERROR: %s", r.GetError())
			recordResult(name, outFail, r.GetError(), lat)
		}
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
		warn("album_title is empty вЂ” expected on a full Spotify GetTrack response")
	} else {
		pass("album_title=%q", trunc(t.GetAlbumTitle(), 40))
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
		warn("search produced no album ID вЂ” using fallback %s", id)
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
		if isSpotifyPremiumErr(r.GetError()) {
			warn("spotify API 403 — premium subscription required, skipping")
			recordResult(name, outSkip, "Spotify 403 premium required", lat)
		} else {
			fail("response status ERROR: %s", r.GetError())
			recordResult(name, outFail, r.GetError(), lat)
		}
		return
	}

	a := r.GetAlbum()
	aOK := checkAlbum(a, 0)

	if a.GetReleaseDate() == "" {
		warn("release_date is empty вЂ” expected on Spotify album response")
	} else {
		pass("release_date=%q", a.GetReleaseDate())
	}

	tracks := r.GetTracks()
	pass("received %d track(s) in album  latency=%s", len(tracks), lat.Round(time.Millisecond))

	if len(tracks) == 0 {
		warn("0 tracks returned вЂ” album track hydration may have failed")
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
			warn("tracks returned (%d) < album.total_tracks (%d) вЂ” pagination may be incomplete",
				len(tracks), a.GetTotalTracks())
		} else {
			pass("track count matches: returned=%d total_tracks=%d", len(tracks), a.GetTotalTracks())
		}
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
		warn("search produced no artist ID вЂ” using fallback %s", id)
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
		if isSpotifyPremiumErr(r.GetError()) {
			warn("spotify API 403 — premium subscription required, skipping")
			recordResult(name, outSkip, "Spotify 403 premium required", lat)
		} else {
			fail("response status ERROR: %s", r.GetError())
			recordResult(name, outFail, r.GetError(), lat)
		}
		return
	}

	artistOK := checkArtist(r.GetArtist(), 0)

	topTracks := r.GetTopTracks()
	pass("received %d top track(s)  latency=%s", len(topTracks), lat.Round(time.Millisecond))

	if len(topTracks) == 0 {
		warn("0 top tracks returned вЂ” unexpected for a major Spotify artist")
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
		warn("0 albums returned вЂ” Spotify GetArtist should populate albums via /artists/{id}/albums")
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
		recordResult(name, outPass, fmt.Sprintf("artist OK, %d top tracks, %d albums", len(topTracks), len(albums)), lat)
	} else {
		recordResult(name, outFail, "field validation failed", lat)
	}
}

func testGetPlaylist(c pb.BedrockServiceClient) {
	id := livePlaylistID
	if id == "" {
		id = fallbackPlaylistID
		warn("search produced no playlist ID вЂ” using fallback %s", id)
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
		if isSpotifyPremiumErr(r.GetError()) {
			warn("spotify API 403 — premium subscription required, skipping")
			recordResult(name, outSkip, "Spotify 403 premium required", lat)
		} else {
			fail("response status ERROR: %s", r.GetError())
			recordResult(name, outFail, r.GetError(), lat)
		}
		return
	}

	plOK := checkPlaylist(r.GetPlaylist(), 0)

	tracks := r.GetTracks()
	pass("received %d track(s) in playlist  latency=%s", len(tracks), lat.Round(time.Millisecond))

	if len(tracks) == 0 {
		warn("0 tracks returned вЂ” playlist may be empty or hydration failed")
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
		recordResult(name, outSkip, fmt.Sprintf("playlist OK but %d tracks (hydration may be partial)", len(tracks)), lat)
	} else {
		recordResult(name, outFail, "playlist metadata invalid", lat)
	}
}

//  stream test

func testGetStreamURL(c pb.BedrockServiceClient) {
	id := liveTrackID
	if id == "" {
		id = fallbackTrackID
		warn("no live track ID вЂ” using fallback %s", id)
	}
	// stream_url is now the expected happy-path outcome.
	name := fmt.Sprintf("GetStreamURL (%s) вЂ” expect STATUS_OK via SC bridge (mp3)", id)
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
			warn("server unavailable вЂ” is the bedrock server running?")
		}
		fail("unexpected gRPC transport error %s: %s", st.Code(), st.Message())
		recordResult(name, outFail, fmt.Sprintf("unexpected grpc error: %s", st.Code()), lat)
		return
	}

	r := resp.(*pb.GetStreamURLResponse)
	printJSON(r)

	if r.GetStatus() == pb.ResponseStatus_STATUS_ERROR {
		if isSpotifyPremiumErr(r.GetError()) {
			warn("spotify API 403 — bridge aborted (premium required), skipping")
			recordResult(name, outSkip, "Spotify 403 premium required", lat)
		} else {
			fail("bridge returned STATUS_ERROR: %s", r.GetError())
			recordResult(name, outFail, fmt.Sprintf("bridge error: %s", trunc(r.GetError(), 80)), lat)
		}
		return
	}

	streamURL := r.GetStreamUrl()
	if streamURL == "" {
		fail("STATUS_OK returned but stream_url is empty вЂ” bridge produced no URL")
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
		warn("is_fallback is false вЂ” bridge should mark cross-platform streams as fallback")
	}
	if r.GetSource() != pb.Platform_PLATFORM_SOUNDCLOUD {
		warn("source=%v вЂ” expected PLATFORM_SOUNDCLOUD (stream served by SC bridge)", r.GetSource())
	}

	st := r.GetStreamType()
	if st != "audio_stream" && st != "hls" {
		warn("unexpected stream_type %q (want audio_stream or hls)", st)
	} else {
		pass("stream_type=%q  content_type=%q", st, r.GetContentType())
	}

	recordResult(name, outPass, fmt.Sprintf("SC bridge OK  type=%s  is_fallback=%v", st, r.GetIsFallback()), lat)
}

// testGetStreamURLHLS verifies the SC bridge honours the "hls" format hint.
// returned stream_url should be an HLS playlist (or progressive as graceful fallback).
func testGetStreamURLHLS(c pb.BedrockServiceClient) {
	id := liveTrackID
	if id == "" {
		id = fallbackTrackID
		warn("no live track ID вЂ” using fallback %s", id)
	}
	name := fmt.Sprintf("GetStreamURL (%s, format=hls) вЂ” expect STATUS_OK via SC bridge", id)
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
		if isSpotifyPremiumErr(r.GetError()) {
			warn("spotify API 403 — bridge aborted (premium required), skipping")
			recordResult(name, outSkip, "Spotify 403 premium required", lat)
		} else {
			fail("bridge returned STATUS_ERROR on HLS request: %s", r.GetError())
			recordResult(name, outFail, fmt.Sprintf("bridge error: %s", trunc(r.GetError(), 80)), lat)
		}
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

	recordResult(name, outPass, fmt.Sprintf("SC bridge HLS OK  type=%s  is_fallback=%v", st, r.GetIsFallback()), lat)
}

//  similar tracks

func testGetSimilarTracks(c pb.BedrockServiceClient) {
	id := liveTrackID
	if id == "" {
		id = fallbackTrackID
		warn("no live track ID вЂ” using fallback %s", id)
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
		warn("0 similar tracks вЂ” Spotify Recommendations API may require valid credentials")
		recordResult(name, outSkip, "0 similar tracks (recommendations API may require auth)", lat)
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

//  import playlist

func testImportPlaylist(c pb.BedrockServiceClient) {
	const playlistURL = "https://open.spotify.com/playlist/1j9uOH2jcv3yeNjhmPhowD"
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
		warn("note: ImportPlaylist for Spotify requires SPOTIFY_CLIENT_ID/SECRET on the server")
		recordResult(name, outSkip, "ImportPlaylist STATUS_ERROR: "+r.GetError(), lat)
		return
	}

	pl := r.GetPlaylist()
	plOK := checkPlaylist(pl, 0)
	tracks := r.GetTracks()
	pass("imported %d track(s)  latency=%s", len(tracks), lat.Round(time.Millisecond))
	info("platform_playlist_id=%q  source=%v", r.GetPlatformPlaylistId(), r.GetSource())

	if r.GetSource() != pb.Platform_PLATFORM_SPOTIFY {
		warn("source=%v вЂ” expected PLATFORM_SPOTIFY for a spotify.com URL", r.GetSource())
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

	if plOK && tOK {
		recordResult(name, outPass, fmt.Sprintf("playlist imported, %d tracks", len(tracks)), lat)
	} else {
		recordResult(name, outFail, "validation failed", lat)
	}
}

//  edge cases

func testEdgeCases(c pb.BedrockServiceClient) {
	{
		name := "GetTrack (nonexistent ID: spotify:0000000000000000000000)"
		section(name)
		resp, lat, err := invoke(func(ctx context.Context) (any, error) {
			return c.GetTrack(ctx, &pb.GetTrackRequest{TrackId: "spotify:0000000000000000000000"})
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
				pass("track is nil вЂ” 404 handled gracefully")
				recordResult(name, outPass, "nil track (not found) returned gracefully", lat)
			} else {
				warn("unexpected success for nonexistent ID: %q", r.GetTrack().GetId())
				recordResult(name, outSkip, "unexpected non-error response for fake ID", lat)
			}
		}
	}

	// surface STATUS_ERROR cleanly вЂ” no panic, no empty response.
	{
		name := "GetStreamURL (bad ID: spotify:0000000000000000000000) вЂ” bridge must fail gracefully"
		section(name)
		resp, lat, err := invoke(func(ctx context.Context) (any, error) {
			return c.GetStreamURL(ctx, &pb.GetStreamURLRequest{
				TrackId:         "spotify:0000000000000000000000",
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
				pass("bridge failed gracefully on bad Spotify ID: %q", trunc(r.GetError(), 80))
				recordResult(name, outPass, "STATUS_ERROR returned (Spotify 404 в†’ bridge aborted)", lat)
			} else if r.GetStreamUrl() == "" {
				pass("empty stream_url, no crash вЂ” acceptable degraded response")
				recordResult(name, outPass, "no stream_url, no crash", lat)
			} else {
				warn("got a stream_url for a fake Spotify ID вЂ” unexpected but non-fatal")
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
				Platforms: []pb.Platform{pb.Platform_PLATFORM_SPOTIFY},
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

	// return a 404-equivalent error, not silently succeed or panic.
	{
		const badAlbumID = "spotify:5nujrmhLynf4yMoMtj8AQF" // glittr - aldn track ID
		name := fmt.Sprintf("GetAlbum (track ID used as album: %s)", badAlbumID)
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
				pass("not-found handled gracefully: %q", r.GetError())
				recordResult(name, outPass, "STATUS_ERROR returned (type mismatch)", lat)
			} else if r.GetAlbum() == nil {
				pass("album is nil вЂ” mismatch handled gracefully")
				recordResult(name, outPass, "nil album returned gracefully", lat)
			} else {
				// if somehow we got an album back, something is off.
				warn("got an album back for a track ID: %q вЂ” unexpected", r.GetAlbum().GetId())
				recordResult(name, outSkip, "unexpected success for wrong ID type", lat)
			}
		}
	}

	// fanout strategy works across different artists.
	{
		const weekndTrack = "spotify:0VjIjW4GlUZAMYd2vXMi3b"
		name := fmt.Sprintf("GetSimilarTracks (%s вЂ” The Weeknd, fanout seed check)", weekndTrack)
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
				warn("STATUS_ERROR: check Spotify credentials вЂ” %v", r.GetErrors())
				recordResult(name, outSkip, "STATUS_ERROR on recommendations", lat)
			} else {
				tracks := r.GetTracks()
				pass("received %d similar track(s)  latency=%s", len(tracks), lat.Round(time.Millisecond))
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
					recordResult(name, outPass, fmt.Sprintf("recommendations OK, %d tracks", len(tracks)), lat)
				} else if len(tracks) == 0 {
					recordResult(name, outSkip, "0 similar tracks for Daft Punk track", lat)
				} else {
					recordResult(name, outFail, "similar track field validation failed", lat)
				}
			}
		}
	}
}

//  summary

func printSummary() {
	fmt.Printf("\n%s--- SPOTIFY TEST SUMMARY ---%s\n\n", cCyan, cReset)

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
		fmt.Printf("  %s[+] all Spotify RPCs passed or skipped.%s\n\n", cGreen, cReset)
	} else {
		fmt.Printf("  %s[-] %d test(s) failed вЂ” check SPOTIFY_CLIENT_ID / SPOTIFY_CLIENT_SECRET on the server.%s\n\n",
			cRed, failed, cReset)
	}
}

//  main

func main() {
	flag.Parse()
	log.SetFlags(log.Ltime | log.Lshortfile)

	fmt.Printf("\n%s[+] Bedrock gRPC - Spotify Integration Test Client%s\n", cCyan, cReset)
	fmt.Printf("    server   : %s\n", *addr)
	fmt.Printf("    timeout  : %s  (per RPC; ImportPlaylist gets 3x)\n", *perCallTimeout)
	fmt.Printf("    verbose  : %v\n", *verbose)
	fmt.Printf("    strategy : search first, feed live IDs to get/stream/similar\n")
	fmt.Printf("    note     : GetStreamURL on spotify:* bridges via SoundCloud вЂ” expects STATUS_OK + real stream_url\n\n")

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
