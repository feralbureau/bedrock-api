// genius.go - genius api client for fetching plain-text lyrics
//
// simple lowercase comments. indie dev style.

package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	pb "example/grpc/bedrock"
	"github.com/broxgit/genius"
)

// geniusClient holds genius api configuration
type geniusClient struct {
	client *genius.Client
}

func newGeniusClient(token string) *geniusClient {
	return &geniusClient{
		client: genius.NewClient(nil, token),
	}
}

// getLyrics searches for track on genius and scrapes lyrics
func (c *geniusClient) getLyrics(ctx context.Context, title, artist string) (*pb.LyricsResponse, error) {
	// search for the track
	q := fmt.Sprintf("%s %s", artist, title)
	search, err := c.client.Search(q)
	if err != nil {
		return nil, fmt.Errorf("genius search error: %v", err)
	}

	if len(search.Response.Hits) == 0 {
		return &pb.LyricsResponse{
			Type:   pb.LyricsType_LYRICS_TYPE_NONE,
			Status: pb.ResponseStatus_STATUS_OK,
		}, nil
	}

	var bestHit *genius.Hit
	bestSim := 0.0

	for _, hit := range search.Response.Hits {
		if hit.Type != "song" {
			continue
		}

		s := hit.Result
		simTitle := stringSimilarity(title, s.Title)
		simArtist := stringSimilarity(artist, s.PrimaryArtist.Name)

		// genius often has "(Ft. ...)" in titles, try cleaning it
		cleanTitle := s.Title
		if idx := strings.Index(cleanTitle, " ("); idx != -1 {
			cleanTitle = cleanTitle[:idx]
		}
		simClean := stringSimilarity(title, cleanTitle)
		if simClean > simTitle {
			simTitle = simClean
		}

		sim := (simTitle + simArtist) / 2.0

		if sim > bestSim {
			bestSim = sim
			bestHit = hit
		}
	}

	// threshold check (using 0.7 for genius as titles can be noisy with features/remixes)
	if bestHit == nil || bestSim < 0.7 {
		return &pb.LyricsResponse{
			Type:   pb.LyricsType_LYRICS_TYPE_NONE,
			Status: pb.ResponseStatus_STATUS_OK,
		}, nil
	}

	song := bestHit.Result

	// scrape lyrics from the genius page
	// the library handles the scraping from the song URL
	lyrics, err := c.client.GetLyrics(song.URL)
	if err != nil {
		log.Printf("genius scrape error for %s: %v", song.URL, err)
		return nil, fmt.Errorf("genius scrape error: %v", err)
	}

	// cleanup lyrics: remove common genius tags like [Verse 1], [Chorus], etc.
	lyrics = cleanupGeniusLyrics(lyrics)

	res := &pb.LyricsResponse{
		Source:         pb.LyricsSource_LYRICS_SOURCE_GENIUS,
		Type:           pb.LyricsType_LYRICS_TYPE_PLAIN,
		Lyrics:         lyrics,
		ResolvedTitle:  song.Title,
		ResolvedArtist: song.PrimaryArtist.Name,
		Similarity:     float32(bestSim),
		Status:         pb.ResponseStatus_STATUS_OK,
	}

	// also populate synced_lines with plain text for compatibility
	lines := strings.Split(lyrics, "\n")
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" {
			continue
		}
		res.SyncedLines = append(res.SyncedLines, &pb.LyricsLine{
			TimeMs: 0,
			Text:   trimmed,
		})
	}

	return res, nil
}

func cleanupGeniusLyrics(l string) string {
	lines := strings.Split(l, "\n")
	var out []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// skip [Verse], [Chorus], etc.
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
