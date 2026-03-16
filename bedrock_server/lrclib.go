// lrclib.go - lrclib api client for fetching synced lyrics
//
// simple lowercase comments. indie dev style.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	pb "github.com/feralbureau/bedrock-api/bedrock"
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

	sim := 1.0

	// fallback to search if not found
	if track == nil {
		track, sim, err = c.fetchSearch(ctx, title, artist, durationS)
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

	return c.mapToResponse(track, float32(sim)), nil
}

// fetchExact calls /api/get for exact metadata match
func (c *lrcClient) fetchExact(ctx context.Context, title, artist string, durationS int) (*lrcTrack, error) {
	u := fmt.Sprintf("https://lrclib.net/api/get?artist_name=%s&track_name=%s",
		url.QueryEscape(artist), url.QueryEscape(title))
	if durationS > 0 {
		u += fmt.Sprintf("&duration=%d", durationS)
	}

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, nil // return nil, nil so it falls back to search instead of failing outright
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("[lrclib] close body after fetchExact: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, nil // if not 200 (like 404 or 400), just return nil to fallback to search
	}

	var track lrcTrack
	if err := json.NewDecoder(resp.Body).Decode(&track); err != nil {
		return nil, nil
	}

	return &track, nil
}

// fetchSearch calls /api/search and picks best result based on string similarity
func (c *lrcClient) fetchSearch(ctx context.Context, title, artist string, durationS int) (*lrcTrack, float64, error) {
	// first pass: search by artist and title
	q := fmt.Sprintf("%s %s", artist, title)
	track, sim, err := c.doSearchWithQuery(ctx, q, title, artist, durationS, true)
	if err != nil {
		return nil, 0, err
	}
	if track != nil {
		return track, sim, nil
	}

	// second pass: fallback to searching just by title
	if artist != "" {
		track, sim, err = c.doSearchWithQuery(ctx, title, title, artist, durationS, false)
		if err != nil {
			return nil, 0, err
		}
		if track != nil {
			return track, sim, nil
		}
	}

	return nil, 0, nil
}

func (c *lrcClient) doSearchWithQuery(ctx context.Context, query, reqTitle, reqArtist string, durationS int, isFullSearch bool) (*lrcTrack, float64, error) {
	u := fmt.Sprintf("https://lrclib.net/api/search?q=%s", url.QueryEscape(query))

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, 0, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("[lrclib] close body after search: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("lrclib search returned %d", resp.StatusCode)
	}

	var results []lrcTrack
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return nil, 0, err
	}

	if len(results) == 0 {
		return nil, 0, nil
	}

	var bestTrack *lrcTrack
	bestSim := 0.0

	for i := range results {
		t := &results[i]

		cleanTrack := t.TrackName
		if t.ArtistName != "" {
			prefix1 := t.ArtistName + " - "
			prefix2 := t.ArtistName + " — " // mdash
			lowerTrack := strings.ToLower(t.TrackName)
			lowerPrefix1 := strings.ToLower(prefix1)
			lowerPrefix2 := strings.ToLower(prefix2)
			if strings.HasPrefix(lowerTrack, lowerPrefix1) {
				cleanTrack = t.TrackName[len(prefix1):]
			} else if strings.HasPrefix(lowerTrack, lowerPrefix2) {
				cleanTrack = t.TrackName[len(prefix2):]
			}
		}

		simTitle := stringSimilarity(reqTitle, cleanTrack)
		simOrig := stringSimilarity(reqTitle, t.TrackName)
		if simOrig > simTitle {
			simTitle = simOrig
		}

		// if the requested title is fully contained in the track name, bump to 0.8
		if len(reqTitle) > 3 && strings.Contains(strings.ToLower(t.TrackName), strings.ToLower(reqTitle)) {
			if simTitle < 0.8 {
				simTitle = 0.8
			}
		}

		simArtist := 0.0
		if reqArtist != "" {
			simArtist = stringSimilarity(reqArtist, t.ArtistName)
		}

		var sim float64
		if isFullSearch {
			sim = (simTitle + simArtist) / 2.0
		} else {
			// fallback by title: title similarity is primary, artist is bonus
			sim = (simTitle*0.8 + simArtist*0.2)
		}

		durationMatch := false
		if durationS > 0 {
			diff := math.Abs(t.Duration - float64(durationS))
			if diff <= 5 {
				durationMatch = true
				sim += 0.1 // bonus for matching duration
			} else {
				sim -= 0.3 // strong penalty for mismatching duration
			}
		} else {
			durationMatch = true // ignore duration check if not provided
		}

		if sim > bestSim {
			if isFullSearch {
				// regular search: need decent text match.
				// if duration matches, text match can be slightly lower.
				if sim >= 0.85 || (sim >= 0.65 && durationMatch) {
					bestSim = sim
					bestTrack = t
				}
			} else {
				// fallback search (title only): duration must match if duration was requested. text must match reasonably well.
				if durationMatch && simTitle >= 0.85 {
					bestSim = sim
					bestTrack = t
				}
			}
		}
	}

	if bestSim > 1.0 {
		bestSim = 1.0
	}

	if bestTrack != nil {
		return bestTrack, bestSim, nil
	}

	return nil, 0, nil
}

func stringSimilarity(a, b string) float64 {
	a = strings.ToLower(strings.TrimSpace(a))
	b = strings.ToLower(strings.TrimSpace(b))
	if a == b {
		return 1.0
	}
	if len(a) == 0 || len(b) == 0 {
		return 0.0
	}

	// ensure a is the longer string to minimize space usage
	if len(a) < len(b) {
		a, b = b, a
	}

	n, m := len(a), len(b)
	// iterative levenshtein with O(m) space to reduce allocations from O(n) to O(1)
	row := make([]int, m+1)
	for j := 0; j <= m; j++ {
		row[j] = j
	}

	for i := 1; i <= n; i++ {
		prev := i - 1
		row[0] = i
		for j := 1; j <= m; j++ {
			old := row[j]
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			// find min of (top, left, top-left)
			min := row[j] + 1
			if row[j-1]+1 < min {
				min = row[j-1] + 1
			}
			if prev+cost < min {
				min = prev + cost
			}
			row[j] = min
			prev = old
		}
	}

	return 1.0 - float64(row[m])/float64(n)
}

// mapToResponse converts lrc data to grpc response
func (c *lrcClient) mapToResponse(track *lrcTrack, similarity float32) *pb.LyricsResponse {
	res := &pb.LyricsResponse{
		Source:         pb.LyricsSource_LYRICS_SOURCE_LRCLIB,
		ResolvedTitle:  track.TrackName,
		ResolvedArtist: track.ArtistName,
		Similarity:     similarity,
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
