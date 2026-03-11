// proxy_bench_test.go - benchmark for proxy rewrite functions
//
// simple lowercase comments. indie dev style.

package main

import (
	"fmt"
	"testing"

	pb "example/grpc/bedrock"
)

func BenchmarkRewriteTrack(b *testing.B) {
	track := &pb.Track{
		Id:         "spotify:4Z8W4fKeB5YxbusRsdQVPb",
		PlatformId: "4Z8W4fKeB5YxbusRsdQVPb",
		Title:      "Title",
		Artist:     "Artist",
		CoverUrl:   "https://i.scdn.co/image/ab67616d0000b273b7d1",
		Source:     pb.Platform_PLATFORM_SPOTIFY,
	}
	host := "localhost:8080"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rewriteTrack(track, host)
	}
}

func BenchmarkRewriteBulk(b *testing.B) {
	tracks := make([]*pb.Track, 300)
	for i := range tracks {
		tracks[i] = &pb.Track{
			Id:         fmt.Sprintf("spotify:id%d", i),
			PlatformId: fmt.Sprintf("id%d", i),
			Title:      "Title",
			Artist:     "Artist",
			CoverUrl:   "https://i.scdn.co/image/ab67616d0000b273b7d1",
			Source:     pb.Platform_PLATFORM_SPOTIFY,
		}
	}
	host := "localhost:8080"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, t := range tracks {
			rewriteTrack(t, host)
		}
	}
}
