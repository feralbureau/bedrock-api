package deezer

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	pb "github.com/feralbureau/bedrock-api/bedrock"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

var testConn *grpc.ClientConn

func init() {
	addr := os.Getenv("BEDROCK_TEST_ADDR")
	if addr == "" {
		addr = "localhost:50052"
	}
	conn, _ := grpc.Dial(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	testConn = conn
}

func getTestClient(t *testing.T) pb.BedrockServiceClient {
	if testConn == nil {
		t.Skip("test setup failed")
	}
	return pb.NewBedrockServiceClient(testConn)
}

func ctxWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// getAuthCtx registers a throwaway user, logs in, and returns a context with the bearer token.
func getAuthCtx(t *testing.T, baseCtx context.Context) context.Context {
	t.Helper()
	client := pb.NewBedrockServiceClient(testConn)

	email := fmt.Sprintf("testuser_%d@example.com", time.Now().UnixNano())
	password := "test-password-123"

	_, err := client.Register(baseCtx, &pb.RegisterRequest{Email: email, Password: password})
	if err != nil {
		t.Fatalf("auth setup: Register failed: %v", err)
	}

	loginResp, err := client.Login(baseCtx, &pb.LoginRequest{Email: email, Password: password})
	if err != nil {
		t.Fatalf("auth setup: Login failed: %v", err)
	}

	return metadata.AppendToOutgoingContext(baseCtx, "authorization", "Bearer "+loginResp.GetAccessToken())
}

func TestSearchTracks(t *testing.T) {
	client := getTestClient(t)

	ctx, cancel := ctxWithTimeout(20 * time.Second)
	defer cancel()
	ctx = getAuthCtx(t, ctx)

	resp, err := client.SearchTracks(ctx, &pb.SearchRequest{
		Query:     "coldplay",
		Limit:     10,
		Platforms: []pb.Platform{pb.Platform_PLATFORM_DEEZER},
	})

	if err != nil {
		t.Fatalf("SearchTracks failed: %v", err)
	}

	tracks := resp.GetTracks()
	if len(tracks) == 0 {
		t.Errorf("SearchTracks returned no results")
	}

	for _, track := range tracks {
		if track.GetId() == "" || track.GetTitle() == "" {
			t.Errorf("SearchTracks returned invalid track: id=%s title=%s", track.GetId(), track.GetTitle())
		}
	}

	t.Logf("SearchTracks OK: found %d tracks", len(tracks))
}

func TestGetTrack(t *testing.T) {
	client := getTestClient(t)

	ctx, cancel := ctxWithTimeout(20 * time.Second)
	defer cancel()
	ctx = getAuthCtx(t, ctx)

	searchResp, err := client.SearchTracks(ctx, &pb.SearchRequest{
		Query:     "coldplay",
		Limit:     1,
		Platforms: []pb.Platform{pb.Platform_PLATFORM_DEEZER},
	})

	if err != nil || len(searchResp.GetTracks()) == 0 {
		t.Fatal("could not find test track")
	}

	trackID := searchResp.GetTracks()[0].GetId()

	resp, err := client.GetTrack(ctx, &pb.GetTrackRequest{
		TrackId: trackID,
	})

	if err != nil {
		t.Fatalf("GetTrack failed: %v", err)
	}

	track := resp.GetTrack()
	if track == nil {
		t.Errorf("GetTrack returned nil track")
		return
	}

	if track.GetTitle() == "" || track.GetArtist() == "" {
		t.Errorf("GetTrack returned incomplete track: title=%s artist=%s", track.GetTitle(), track.GetArtist())
	}

	t.Logf("GetTrack OK: %s - %s", track.GetTitle(), track.GetArtist())
}

func TestSearchAlbums(t *testing.T) {
	client := getTestClient(t)

	ctx, cancel := ctxWithTimeout(20 * time.Second)
	defer cancel()
	ctx = getAuthCtx(t, ctx)

	resp, err := client.SearchAlbums(ctx, &pb.SearchRequest{
		Query:     "coldplay",
		Limit:     10,
		Platforms: []pb.Platform{pb.Platform_PLATFORM_DEEZER},
	})

	if err != nil {
		t.Fatalf("SearchAlbums failed: %v", err)
	}

	albums := resp.GetAlbums()
	if len(albums) == 0 {
		t.Errorf("SearchAlbums returned no results")
	}

	for _, album := range albums {
		if album.GetId() == "" || album.GetTitle() == "" {
			t.Errorf("SearchAlbums returned invalid album: id=%s title=%s", album.GetId(), album.GetTitle())
		}
	}

	t.Logf("SearchAlbums OK: found %d albums", len(albums))
}

func TestGetAlbum(t *testing.T) {
	client := getTestClient(t)

	ctx, cancel := ctxWithTimeout(20 * time.Second)
	defer cancel()
	ctx = getAuthCtx(t, ctx)

	searchResp, err := client.SearchAlbums(ctx, &pb.SearchRequest{
		Query:     "coldplay",
		Limit:     1,
		Platforms: []pb.Platform{pb.Platform_PLATFORM_DEEZER},
	})

	if err != nil || len(searchResp.GetAlbums()) == 0 {
		t.Fatal("could not find test album")
	}

	albumID := searchResp.GetAlbums()[0].GetId()

	resp, err := client.GetAlbum(ctx, &pb.GetAlbumRequest{
		AlbumId: albumID,
	})

	if err != nil {
		t.Fatalf("GetAlbum failed: %v", err)
	}

	album := resp.GetAlbum()
	if album == nil {
		t.Errorf("GetAlbum returned nil album")
		return
	}

	if album.GetTitle() == "" {
		t.Errorf("GetAlbum returned empty title")
	}

	t.Logf("GetAlbum OK: %s, %d tracks", album.GetTitle(), len(resp.GetTracks()))
}

func TestSearchArtists(t *testing.T) {
	client := getTestClient(t)

	ctx, cancel := ctxWithTimeout(20 * time.Second)
	defer cancel()
	ctx = getAuthCtx(t, ctx)

	resp, err := client.SearchArtists(ctx, &pb.SearchRequest{
		Query:     "coldplay",
		Limit:     10,
		Platforms: []pb.Platform{pb.Platform_PLATFORM_DEEZER},
	})

	if err != nil {
		t.Fatalf("SearchArtists failed: %v", err)
	}

	artists := resp.GetArtists()
	if len(artists) == 0 {
		t.Errorf("SearchArtists returned no results")
	}

	for _, artist := range artists {
		if artist.GetId() == "" || artist.GetName() == "" {
			t.Errorf("SearchArtists returned invalid artist: id=%s name=%s", artist.GetId(), artist.GetName())
		}
	}

	t.Logf("SearchArtists OK: found %d artists", len(artists))
}

func TestGetArtist(t *testing.T) {
	client := getTestClient(t)

	ctx, cancel := ctxWithTimeout(20 * time.Second)
	defer cancel()
	ctx = getAuthCtx(t, ctx)

	searchResp, err := client.SearchArtists(ctx, &pb.SearchRequest{
		Query:     "coldplay",
		Limit:     1,
		Platforms: []pb.Platform{pb.Platform_PLATFORM_DEEZER},
	})

	if err != nil || len(searchResp.GetArtists()) == 0 {
		t.Fatal("could not find test artist")
	}

	artistID := searchResp.GetArtists()[0].GetId()

	resp, err := client.GetArtist(ctx, &pb.GetArtistRequest{
		ArtistId: artistID,
	})

	if err != nil {
		t.Fatalf("GetArtist failed: %v", err)
	}

	artist := resp.GetArtist()
	if artist == nil {
		t.Errorf("GetArtist returned nil artist")
		return
	}

	if artist.GetName() == "" {
		t.Errorf("GetArtist returned empty name")
	}

	t.Logf("GetArtist OK: %s", artist.GetName())
}

func TestSearchPlaylists(t *testing.T) {
	client := getTestClient(t)

	ctx, cancel := ctxWithTimeout(20 * time.Second)
	defer cancel()
	ctx = getAuthCtx(t, ctx)

	resp, err := client.SearchPlaylists(ctx, &pb.SearchRequest{
		Query:     "workout",
		Limit:     10,
		Platforms: []pb.Platform{pb.Platform_PLATFORM_DEEZER},
	})

	if err != nil {
		t.Fatalf("SearchPlaylists failed: %v", err)
	}

	playlists := resp.GetPlaylists()
	if len(playlists) == 0 {
		t.Errorf("SearchPlaylists returned no results")
	}

	for _, pl := range playlists {
		if pl.GetId() == "" || pl.GetTitle() == "" {
			t.Errorf("SearchPlaylists returned invalid playlist: id=%s title=%s", pl.GetId(), pl.GetTitle())
		}
	}

	t.Logf("SearchPlaylists OK: found %d playlists", len(playlists))
}

func TestGetPlaylist(t *testing.T) {
	client := getTestClient(t)

	ctx, cancel := ctxWithTimeout(20 * time.Second)
	defer cancel()
	ctx = getAuthCtx(t, ctx)

	searchResp, err := client.SearchPlaylists(ctx, &pb.SearchRequest{
		Query:     "workout",
		Limit:     1,
		Platforms: []pb.Platform{pb.Platform_PLATFORM_DEEZER},
	})

	if err != nil || len(searchResp.GetPlaylists()) == 0 {
		t.Fatal("could not find test playlist")
	}

	playlistID := searchResp.GetPlaylists()[0].GetId()

	resp, err := client.GetPlaylist(ctx, &pb.GetPlaylistRequest{
		PlaylistId: playlistID,
	})

	if err != nil {
		t.Fatalf("GetPlaylist failed: %v", err)
	}

	playlist := resp.GetPlaylist()
	if playlist == nil {
		t.Errorf("GetPlaylist returned nil playlist")
		return
	}

	if playlist.GetTitle() == "" {
		t.Errorf("GetPlaylist returned empty title")
	}

	t.Logf("GetPlaylist OK: %s, %d tracks", playlist.GetTitle(), len(resp.GetTracks()))
}
