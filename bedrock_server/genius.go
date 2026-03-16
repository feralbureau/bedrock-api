// genius.go - genius api client: plain-text lyrics + lyric annotations.
// uses go-genius for search/metadata, direct http for referents (pagination),
// and genius internal webapp api for annotation timestamps + creator info.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	genius "github.com/tsirysndr/go-genius"
	"golang.org/x/net/html"

	pb "github.com/feralbureau/bedrock-api/bedrock"
)

const geniusBrowserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// geniusClient wraps the go-genius client and an http client for scraping / direct api calls
type geniusClient struct {
	api   *genius.Client
	http  *http.Client
	token string
}

// newGeniusClient creates a geniusClient from GENIUS_ACCESS_TOKEN env var.
// returns nil if no token — callers must guard.
func newGeniusClient() *geniusClient {
	token := os.Getenv("GENIUS_ACCESS_TOKEN")
	if token == "" {
		return nil
	}
	return &geniusClient{
		api:   genius.NewClient(token),
		token: token,
		http: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// getLyrics searches genius for title+artist and returns plain lyrics.
func (g *geniusClient) getLyrics(ctx context.Context, title, artist string) (*pb.LyricsResponse, error) {
	q := fmt.Sprintf("%s %s", artist, title)
	hits, err := g.api.Search.Get(q)
	if err != nil {
		return nil, fmt.Errorf("genius search: %w", err)
	}

	songURL, resolvedTitle, resolvedArtist, sim, _ := g.pickBestHit(hits, title, artist)
	if songURL == "" {
		return &pb.LyricsResponse{
			Type:   pb.LyricsType_LYRICS_TYPE_NONE,
			Status: pb.ResponseStatus_STATUS_OK,
		}, nil
	}

	lyrics, err := g.scrapeLyrics(ctx, songURL)
	if err != nil {
		return nil, fmt.Errorf("genius scrape: %w", err)
	}
	if lyrics == "" {
		return &pb.LyricsResponse{
			Type:   pb.LyricsType_LYRICS_TYPE_NONE,
			Status: pb.ResponseStatus_STATUS_OK,
		}, nil
	}

	var lines []*pb.LyricsLine
	for _, l := range strings.Split(lyrics, "\n") {
		lines = append(lines, &pb.LyricsLine{TimeMs: 0, Text: l})
	}

	return &pb.LyricsResponse{
		Source:         pb.LyricsSource_LYRICS_SOURCE_GENIUS,
		Lyrics:         lyrics,
		SyncedLines:    lines,
		Synced:         false,
		Type:           pb.LyricsType_LYRICS_TYPE_PLAIN,
		ResolvedTitle:  resolvedTitle,
		ResolvedArtist: resolvedArtist,
		Similarity:     float32(sim),
		Status:         pb.ResponseStatus_STATUS_OK,
	}, nil
}

// getAnnotations fetches genius community annotations for a song.
// flow: search → referents api → enrich annotations in parallel via webapp api.
func (g *geniusClient) getAnnotations(ctx context.Context, title, artist string, limit int) (*pb.AnnotationsResponse, error) {
	q := fmt.Sprintf("%s %s", artist, title)
	hits, err := g.api.Search.Get(q)
	if err != nil {
		return nil, fmt.Errorf("genius search: %w", err)
	}

	songURL, _, _, _, songID := g.pickBestHit(hits, title, artist)
	if songURL == "" || songID == 0 {
		return &pb.AnnotationsResponse{Status: pb.ResponseStatus_STATUS_OK}, nil
	}

	referents, err := g.fetchReferents(ctx, songID, limit)
	if err != nil {
		return nil, fmt.Errorf("genius referents: %w", err)
	}

	// collect candidates that have non-empty bodies
	type pending struct {
		ref geniusReferent
		ann geniusAnnotationItem
	}
	candidates := make([]pending, 0, len(referents))
	for _, ref := range referents {
		if len(ref.Annotations) == 0 {
			continue
		}
		ann := ref.Annotations[0]
		if plainBody(ann.Body) == "" {
			continue
		}
		candidates = append(candidates, pending{ref: ref, ann: ann})
	}

	// enrich each annotation in parallel via the webapp api (gets created_at unix + created_by)
	details := make([]*geniusAnnotationDetail, len(candidates))
	var wg sync.WaitGroup
	var mu sync.Mutex
	wg.Add(len(candidates))
	for i, c := range candidates {
		i, annID := i, c.ann.ID
		go func() {
			defer wg.Done()
			d, _ := g.fetchAnnotationDetail(ctx, annID)
			mu.Lock()
			details[i] = d
			mu.Unlock()
		}()
	}
	wg.Wait()

	annotations := make([]*pb.LyricAnnotation, 0, len(candidates))
	for i, c := range candidates {
		body := plainBody(c.ann.Body)

		createdAt := ""
		createdAgo := ""
		contributor := pickPrimaryContributor(c.ann.Authors)

		if d := details[i]; d != nil {
			if d.CreatedAt > 0 {
				t := time.Unix(d.CreatedAt, 0).UTC()
				createdAt = t.Format(time.RFC3339)
				createdAgo = relativeTime(createdAt)
			}
			// prefer created_by (the original author) over authors attribution list
			if d.CreatedBy != nil {
				av := ""
				if d.CreatedBy.Avatar != nil && d.CreatedBy.Avatar.Small != nil {
					av = d.CreatedBy.Avatar.Small.URL
				}
				contributor = &pb.AnnotationContributor{
					Login:     d.CreatedBy.Login,
					Url:       d.CreatedBy.URL,
					AvatarUrl: av,
					Role:      d.CreatedBy.HumanReadableRoleForDisplay,
					Iq:        int32(d.CreatedBy.IQ),
				}
			}
		}

		annotations = append(annotations, &pb.LyricAnnotation{
			Id:           int64(c.ann.ID),
			Url:          c.ann.URL,
			Fragment:     strings.TrimSpace(c.ref.Fragment),
			Body:         body,
			VotesTotal:   int32(c.ann.VotesTotal),
			Verified:     c.ann.Verified,
			Pinned:       c.ann.Pinned,
			CommentCount: int32(c.ann.CommentCount),
			Contributor:  contributor,
			CreatedAt:    createdAt,
			CreatedAgo:   createdAgo,
		})
	}

	return &pb.AnnotationsResponse{
		Annotations:   annotations,
		GeniusSongId:  int64(songID),
		GeniusSongUrl: songURL,
		Status:        pb.ResponseStatus_STATUS_OK,
	}, nil
}

// geniusAnnotationDetail is the shape returned by genius.com/api/annotations/<id>
// (the internal webapp api returns created_at unix + created_by — not in the official api)
type geniusAnnotationDetail struct {
	CreatedAt int64 `json:"created_at"`
	CreatedBy *struct {
		Login                       string `json:"login"`
		URL                         string `json:"url"`
		HumanReadableRoleForDisplay string `json:"human_readable_role_for_display"`
		IQ                          int    `json:"iq"`
		Avatar                      *struct {
			Small *struct {
				URL string `json:"url"`
			} `json:"small"`
		} `json:"avatar"`
	} `json:"created_by"`
}

// fetchAnnotationDetail calls the genius webapp api for a single annotation.
func (g *geniusClient) fetchAnnotationDetail(ctx context.Context, annID int) (*geniusAnnotationDetail, error) {
	url := fmt.Sprintf("https://genius.com/api/annotations/%d", annID)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", geniusBrowserUA)

	resp, err := g.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("[genius] close body after annotation detail: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("annotation detail returned %d", resp.StatusCode)
	}

	var out struct {
		Response struct {
			Annotation geniusAnnotationDetail `json:"annotation"`
		} `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out.Response.Annotation, nil
}

// geniusReferent is the raw shape from the referents api
type geniusReferent struct {
	ID          int                    `json:"id"`
	Fragment    string                 `json:"fragment"`
	Annotations []geniusAnnotationItem `json:"annotations"`
}

type geniusAnnotationItem struct {
	ID           int         `json:"id"`
	Body         interface{} `json:"body"`
	CommentCount int         `json:"comment_count"`
	Pinned       bool        `json:"pinned"`
	URL          string      `json:"url"`
	Verified     bool        `json:"verified"`
	VotesTotal   int         `json:"votes_total"`
	Authors      []struct {
		Attribution float64 `json:"attribution"`
		User        *struct {
			Login                       string `json:"login"`
			URL                         string `json:"url"`
			HumanReadableRoleForDisplay string `json:"human_readable_role_for_display"`
			IQ                          int    `json:"iq"`
			Avatar                      *struct {
				Small *struct {
					URL string `json:"url"`
				} `json:"small"`
			} `json:"avatar"`
		} `json:"user"`
	} `json:"authors"`
}

// fetchReferents calls the genius /referents endpoint for a song.
func (g *geniusClient) fetchReferents(ctx context.Context, songID int, limit int) ([]geniusReferent, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}

	url := fmt.Sprintf("https://api.genius.com/referents?song_id=%d&text_format=plain&per_page=%d", songID, limit)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+g.token)

	resp, err := g.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("[genius] close body after referents: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("referents api returned %d", resp.StatusCode)
	}

	var out struct {
		Response struct {
			Referents []geniusReferent `json:"referents"`
		} `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Response.Referents, nil
}

// plainBody extracts plain text from the genius body interface ({"plain": "..."} map)
func plainBody(body interface{}) string {
	if body == nil {
		return ""
	}
	if m, ok := body.(map[string]interface{}); ok {
		if plain, ok := m["plain"].(string); ok {
			return strings.TrimSpace(plain)
		}
	}
	return ""
}

// pickPrimaryContributor selects the author with the highest attribution score
func pickPrimaryContributor(authors []struct {
	Attribution float64 `json:"attribution"`
	User        *struct {
		Login                       string `json:"login"`
		URL                         string `json:"url"`
		HumanReadableRoleForDisplay string `json:"human_readable_role_for_display"`
		IQ                          int    `json:"iq"`
		Avatar                      *struct {
			Small *struct {
				URL string `json:"url"`
			} `json:"small"`
		} `json:"avatar"`
	} `json:"user"`
}) *pb.AnnotationContributor {
	best := -1
	bestAttr := -1.0
	for i, a := range authors {
		if a.Attribution > bestAttr {
			bestAttr = a.Attribution
			best = i
		}
	}
	if best < 0 || authors[best].User == nil {
		return nil
	}
	u := authors[best].User
	av := ""
	if u.Avatar != nil && u.Avatar.Small != nil {
		av = u.Avatar.Small.URL
	}
	return &pb.AnnotationContributor{
		Login:     u.Login,
		Url:       u.URL,
		AvatarUrl: av,
		Role:      u.HumanReadableRoleForDisplay,
		Iq:        int32(u.IQ),
	}
}

// relativeTime converts an iso8601 utc string to a human-readable "N ago" string.
func relativeTime(iso string) string {
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		m := int(d.Minutes())
		if m == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", m)
	case d < 24*time.Hour:
		h := int(d.Hours())
		if h == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", h)
	case d < 30*24*time.Hour:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	case d < 365*24*time.Hour:
		months := int(d.Hours() / 24 / 30)
		if months == 1 {
			return "1 month ago"
		}
		return fmt.Sprintf("%d months ago", months)
	default:
		years := int(d.Hours() / 24 / 365)
		if years == 1 {
			return "1 year ago"
		}
		return fmt.Sprintf("%d years ago", years)
	}
}

// pickBestHit scores search hits by string similarity and returns the winner.
func (g *geniusClient) pickBestHit(hits []genius.Hit, reqTitle, reqArtist string) (songURL, title, artist string, sim float64, songID int) {
	bestSim := 0.0
	for _, h := range hits {
		if h.Result == nil {
			continue
		}
		r := h.Result
		artistName := ""
		if r.PrimaryArtist != nil {
			artistName = r.PrimaryArtist.Name
		}
		simTitle := stringSimilarity(reqTitle, r.Title)
		simArtist := stringSimilarity(reqArtist, artistName)
		score := (simTitle + simArtist) / 2.0
		if score > bestSim {
			bestSim = score
			songURL = r.URL
			title = r.Title
			artist = artistName
			songID = r.ID
		}
	}
	return songURL, title, artist, bestSim, songID
}

// scrapeLyrics fetches the genius song page and extracts text from
// div[data-lyrics-container="true"] elements.
func (g *geniusClient) scrapeLyrics(ctx context.Context, pageURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", pageURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", geniusBrowserUA)

	resp, err := g.http.Do(req)
	if err != nil {
		return "", err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("[genius] close body after scrape lyrics: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("genius page returned %d", resp.StatusCode)
	}

	doc, err := html.Parse(resp.Body)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	extractLyricsContainers(doc, &sb)
	return strings.TrimSpace(sb.String()), nil
}

func extractLyricsContainers(n *html.Node, sb *strings.Builder) {
	if n.Type == html.ElementNode && n.Data == "div" {
		for _, a := range n.Attr {
			if a.Key == "data-lyrics-container" && a.Val == "true" {
				extractNodeText(n, sb)
				sb.WriteByte('\n')
				return
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		extractLyricsContainers(c, sb)
	}
}

func extractNodeText(n *html.Node, sb *strings.Builder) {
	if n.Type == html.TextNode {
		sb.WriteString(n.Data)
		return
	}
	if n.Type == html.ElementNode {
		if n.Data == "br" {
			sb.WriteByte('\n')
			return
		}
		for _, a := range n.Attr {
			if a.Key == "data-exclude-from-selection" && a.Val == "true" {
				return
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		extractNodeText(c, sb)
	}
}
