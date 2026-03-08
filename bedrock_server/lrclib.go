// lrclib.go - lrclib api client for fetching synced lyrics
//
// simple lowercase comments. indie dev style.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	pb "example/grpc/bedrock"
)

// lrclib track metadata from api
type lrcTrack struct {
	ID           int     `json:"id"`
	Name         string  `json:"name"`
	TrackName    string  `json:"trackName"`
	ArtistName   string  `json:"artistName"`
	AlbumName    string  `json:"albumName"`
	Duration     float64 `json:"duration"`
	PlainLyrics  string  `json:"plainLyrics"`
	SyncedLyrics string  `json:"syncedLyrics"`
}

// lrclib client holds http configuration
type lrcClient struct {
	http *http.Client
}

func newLrcClient() *lrcClient {
	return &lrcClient{
		http: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// getLyrics fetches lyrics from lrclib trying exact match then search
func (c *lrcClient) getLyrics(ctx context.Context, title, artist string, durationS int) (*pb.LyricsResponse, error) {
	// try exact match first
	track, err := c.fetchExact(ctx, title, artist, durationS)
	if err != nil {
		return nil, err
	}

	// fallback to search if not found
	if track == nil {
		track, err = c.fetchSearch(ctx, title, artist, durationS)
		if err != nil {
			return nil, err
		}
	}

	// return empty if still nothing
	if track == nil {
		return &pb.LyricsResponse{
			Type:   pb.LyricsType_LYRICS_TYPE_NONE,
			Status: pb.ResponseStatus_STATUS_OK,
		}, nil
	}

	return c.mapToResponse(track), nil
}

// fetchExact calls /api/get for exact metadata match
func (c *lrcClient) fetchExact(ctx context.Context, title, artist string, durationS int) (*lrcTrack, error) {
	u := fmt.Sprintf("https://lrclib.net/api/get?artist_name=%s&track_name=%s&duration=%d",
		url.QueryEscape(artist), url.QueryEscape(title), durationS)

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lrclib get returned %d", resp.StatusCode)
	}

	var track lrcTrack
	if err := json.NewDecoder(resp.Body).Decode(&track); err != nil {
		return nil, err
	}

	return &track, nil
}

// fetchSearch calls /api/search and picks first result if duration is close
func (c *lrcClient) fetchSearch(ctx context.Context, title, artist string, durationS int) (*lrcTrack, error) {
	u := fmt.Sprintf("https://lrclib.net/api/search?q=%s",
		url.QueryEscape(fmt.Sprintf("%s %s", artist, title)))

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lrclib search returned %d", resp.StatusCode)
	}

	var results []lrcTrack
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return nil, err
	}

	if len(results) == 0 {
		return nil, nil
	}

	// pick first match with duration diff < 2s
	for _, t := range results {
		if math.Abs(t.Duration-float64(durationS)) < 2 {
			return &t, nil
		}
	}

	return nil, nil
}

// mapToResponse converts lrc data to grpc response
func (c *lrcClient) mapToResponse(track *lrcTrack) *pb.LyricsResponse {
	res := &pb.LyricsResponse{
		Source:         pb.LyricsSource_LYRICS_SOURCE_LRCLIB,
		ResolvedTitle:  track.TrackName,
		ResolvedArtist: track.ArtistName,
		Similarity:     0.9,
		Status:         pb.ResponseStatus_STATUS_OK,
	}

	if track.SyncedLyrics != "" {
		res.SyncedLines = parseLRC(track.SyncedLyrics)
		res.Synced = true
		res.Type = pb.LyricsType_LYRICS_TYPE_SYNCED
		// also populate plain field for compatibility
		res.Lyrics = track.PlainLyrics
	} else if track.PlainLyrics != "" {
		lines := strings.Split(track.PlainLyrics, "\n")
		for _, l := range lines {
			res.SyncedLines = append(res.SyncedLines, &pb.LyricsLine{
				TimeMs: 0,
				Text:   strings.TrimSpace(l),
			})
		}
		res.Lyrics = track.PlainLyrics
		res.Type = pb.LyricsType_LYRICS_TYPE_PLAIN
	} else {
		res.Type = pb.LyricsType_LYRICS_TYPE_NONE
	}

	return res
}

// lrc line regex: [mm:ss.xx] text
var lrcRegex = regexp.MustCompile(`^\[(\d+):(\d+)(?:\.(\d+))?\](.*)`)

// parseLRC converts raw lrc string to structured lines
func parseLRC(raw string) []*pb.LyricsLine {
	var lines []*pb.LyricsLine
	scanner := strings.Split(raw, "\n")

	for _, s := range scanner {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}

		matches := lrcRegex.FindStringSubmatch(s)
		if len(matches) < 3 {
			continue
		}

		min, _ := strconv.Atoi(matches[1])
		sec, _ := strconv.Atoi(matches[2])
		ms := 0
		if matches[3] != "" {
			// lrc uses hundredths usually, e.g. .34 -> 340ms
			val := matches[3]
			if len(val) == 2 {
				m, _ := strconv.Atoi(val)
				ms = m * 10
			} else if len(val) == 3 {
				ms, _ = strconv.Atoi(val)
			}
		}

		totalMs := (min*60+sec)*1000 + ms
		text := strings.TrimSpace(matches[4])

		lines = append(lines, &pb.LyricsLine{
			TimeMs: int32(totalMs),
			Text:   text,
		})
	}
	return lines
}
