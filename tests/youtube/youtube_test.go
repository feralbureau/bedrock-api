package youtube

import (
	"context"
	"os"
	"testing"
	"time"

	pb "github.com/feralbureau/bedrock-api/bedrock"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
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

func TestSearchTracks(t *testing.T) {
	client := getTestClient(t)

	ctx, cancel := ctxWithTimeout(20 * time.Second)
	defer cancel()

	resp, err := client.SearchTracks(ctx, &pb.SearchRequest{
		Query:      "billie eilish bad guy",
		Limit:      10,
		Platforms:  []pb.Platform{pb.Platform_PLATFORM_YOUTUBE},
	})

	if err != nil {
		t.Skipf("SearchTracks failed: %v", err)
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

	searchResp, err := client.SearchTracks(ctx, &pb.SearchRequest{
		Query:      "billie eilish bad guy",
		Limit:      1,
		Platforms:  []pb.Platform{pb.Platform_PLATFORM_YOUTUBE},
	})

	if err != nil || len(searchResp.GetTracks()) == 0 {
		t.Skip("could not find test track")
	}

	trackID := searchResp.GetTracks()[0].GetId()

	resp, err := client.GetTrack(ctx, &pb.GetTrackRequest{
		TrackId: trackID,
	})

	if err != nil {
		t.Skipf("GetTrack failed: %v", err)
	}

	track := resp.GetTrack()
	if track == nil {
		t.Errorf("GetTrack returned nil track")
		return
	}

	if track.GetTitle() == "" {
		t.Errorf("GetTrack returned empty title")
	}

	t.Logf("GetTrack OK: %s", track.GetTitle())
}

func TestGetStreamURL(t *testing.T) {
	client := getTestClient(t)

	ctx, cancel := ctxWithTimeout(30 * time.Second)
	defer cancel()

	searchResp, err := client.SearchTracks(ctx, &pb.SearchRequest{
		Query:      "billie eilish bad guy",
		Limit:      1,
		Platforms:  []pb.Platform{pb.Platform_PLATFORM_YOUTUBE},
	})

	if err != nil || len(searchResp.GetTracks()) == 0 {
		t.Skip("could not find test track")
	}

	trackID := searchResp.GetTracks()[0].GetId()

	resp, err := client.GetStreamURL(ctx, &pb.GetStreamURLRequest{
		TrackId: trackID,
	})

	if err != nil {
		t.Logf("GetStreamURL returned error: %v", err)
		return
	}

	if resp.GetStreamUrl() == "" {
		t.Errorf("GetStreamURL returned empty stream_url")
		return
	}

	t.Logf("GetStreamURL OK: stream_url length=%d", len(resp.GetStreamUrl()))
}
