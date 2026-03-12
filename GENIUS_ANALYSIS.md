# Genius Lyrics Integration Analysis

**Status**: ⚠️ Defined in Protobuf but NOT Implemented
**Date**: 2026-03-12
**Scope**: Testing strategy & implementation requirements for Genius.com lyrics support

---

## Current Status Summary

### What's Implemented ✅
- **LrcLib Integration** (`bedrock_server/lrclib.go`): Fully functional synced lyrics client
  - Exact match via `/api/get` endpoint
  - Fallback search via `/api/search` endpoint
  - Levenshtein distance-based similarity scoring (0-1.0)
  - LRC format parsing with millisecond precision
  - Comprehensive unit tests (`lrclib_test.go`)

- **Protobuf Definitions** (`proto/bedrock_service.proto`):
  - `LyricsSource` enum with `LYRICS_SOURCE_GENIUS` (value=2)
  - `LyricsType` enum supporting `LYRICS_TYPE_PLAIN` and `LYRICS_TYPE_SYNCED`
  - `LyricsRequest` and `LyricsResponse` messages
  - `GetLyrics` RPC endpoint (line 511)

- **GetLyrics RPC Handler** (`bedrock_server/main.go` lines 817-856):
  - Accepts `track_id`, `title`, `artist`, `duration_s`, `preferred_source`
  - Fetches track metadata if track_id provided
  - Validates title/artist resolution
  - Currently **only calls `s.lyrics.getLyrics()`** (the LrcLib client)
  - Returns appropriate error/partial responses

### What's Missing ❌
1. **No Genius Client Implementation**
   - No `genius.go` file
   - No Genius API HTTP client
   - No search/parsing logic

2. **No Fan-Out Logic in GetLyrics**
   - Proto defines `preferred_source` field but it's ignored
   - Only LrcLib is called; Genius never invoked
   - Should support parallel fetching with fallback

3. **No Genius Tests**
   - No unit tests for Genius client
   - No mock response fixtures
   - No integration test path

4. **No Environment Configuration**
   - No GENIUS_ACCESS_TOKEN env var handling
   - No rate-limit management

---

## Genius API Integration Requirements

### API Overview
**Genius** (genius.com) provides a free public API for searching and fetching song lyrics.

#### Authentication
- **Type**: Bearer token (requires registration at genius.com/api-clients)
- **Header**: `Authorization: Bearer <ACCESS_TOKEN>`
- **Free Tier**:
  - Rate limit: ~200 requests/min (IP-based)
  - No SLA (best effort)
  - Free to use for non-commercial projects

#### Key Endpoints

**1. Search Endpoint**
```
GET https://api.genius.com/search?q={query}
```
- **Query**: Free-text search (e.g., "artist:song" or just "title")
- **Response**: Array of hits with top match first
- **Rate Limited**: Yes
- **Returns**:
  - Hit metadata: `title`, `primary_artist.name`, `url`
  - Each hit has a `.url` pointing to the song page (for scraping)

**2. Get Song Details** (if you want metadata, not typically needed for lyrics)
```
GET https://api.genius.com/songs/{id}
```
- Returns structured metadata, but **NOT the lyrics text**
- Lyrics must be scraped from HTML page

#### Known Limitations
1. **No Direct Lyrics Endpoint**: Genius API does NOT return lyrics directly
   - You must scrape `hit.url` (the song page HTML)
   - Rate-limiting: Each song page scrape counts as one HTTP request
   - HTML structure can change; scraping requires robust parsing

2. **No Synced Lyrics**: Genius only provides plain-text lyrics (matches LrcLib's fallback behavior)

3. **Rate Limiting**:
   - Search: ~200 req/min
   - HTML scraping: Subject to Genius's anti-bot measures (User-Agent required, may be throttled)

4. **Terms of Service**:
   - Lyrics must be credited to Genius.com
   - Scraping is implicitly allowed but not officially documented
   - Commercial use requires explicit permission

---

## Proposed Implementation Architecture

### File Structure
```
bedrock_server/
├── lrclib.go           (existing: synced lyrics)
├── lrclib_test.go      (existing: unit tests)
├── genius.go           (NEW: plain-text lyrics)
├── genius_test.go      (NEW: unit tests + fixtures)
└── main.go             (UPDATE: fan-out logic + env handling)
```

### genius.go Interface Design

```go
package main

import (
    "context"
    pb "example/grpc/bedrock"
)

// geniusClient holds API token + HTTP client + HTML parser config
type geniusClient struct {
    accessToken string
    http        *http.Client
    // parser state for robustness against HTML changes
}

// getLyrics: search → scrape → parse → return
// Input: title, artist, (optional) duration for deduplication
// Output: *pb.LyricsResponse or error
func (c *geniusClient) getLyrics(ctx context.Context, title, artist string) (*pb.LyricsResponse, error)

// searchSong: calls /search endpoint
// Returns: URL of best match or error
func (c *geniusClient) searchSong(ctx context.Context, title, artist string) (string, float32, error)

// scrapeLyrics: fetches HTML from URL, parses lyrics
// Returns: plain-text lyrics or error
func (c *geniusClient) scrapeLyrics(ctx context.Context, songURL string) (string, error)

// parseGeniusHTML: extracts lyrics <div> from page HTML
// Uses CSS selector or structural parsing
func parseGeniusHTML(html string) (string, error)
```

### Updated main.go GetLyrics Logic

**Current**:
```go
resp, err := s.lyrics.getLyrics(ctx, title, artist, durationS)
```

**Updated** (pseudo-code):
```go
preferred := req.GetPreferredSource()

// Phase 1: Primary provider (LrcLib or Genius)
var resp *pb.LyricsResponse
var err error

if preferred == pb.LyricsSource_LYRICS_SOURCE_LRCLIB {
    // user wants LrcLib first
    resp, err = s.lyrics.getLyrics(ctx, title, artist, durationS)
    if err == nil && resp.GetLyrics() != "" {
        return resp, nil
    }
    // fallback to Genius
    resp, err = s.genius.getLyrics(ctx, title, artist)
} else if preferred == pb.LyricsSource_LYRICS_SOURCE_GENIUS {
    // user wants Genius first
    resp, err = s.genius.getLyrics(ctx, title, artist)
    if err == nil && resp.GetLyrics() != "" {
        return resp, nil
    }
    // fallback to LrcLib
    resp, err = s.lyrics.getLyrics(ctx, title, artist, durationS)
} else {
    // UNSPECIFIED: fan-out in parallel, return best
    type result struct { resp *pb.LyricsResponse; err error }
    lrcResult := make(chan result, 1)
    geniusResult := make(chan result, 1)

    go func() {
        resp, err := s.lyrics.getLyrics(ctx, title, artist, durationS)
        lrcResult <- result{resp, err}
    }()
    go func() {
        resp, err := s.genius.getLyrics(ctx, title, artist)
        geniusResult <- result{resp, err}
    }()

    lrc := <-lrcResult
    genius := <-geniusResult

    // pick best (prefer synced > higher similarity > any result)
    resp = pickBestLyrics(lrc.resp, genius.resp)
}
```

---

## Test Plan / Checklist

### Phase 0: Setup
- [ ] Register Genius API app at genius.com/api-clients (get ACCESS_TOKEN)
- [ ] Add `GENIUS_ACCESS_TOKEN` to `.env`
- [ ] Create fixtures directory: `bedrock_server/testdata/genius/`

### Phase 1: Unit Tests (genius_test.go)

#### 1.1: Genius API Client Initialization
```
✓ Test newGeniusClient() with valid token
✓ Test newGeniusClient() with empty token (should fail gracefully)
✓ Test HTTP client timeout config (5s default)
```

#### 1.2: Search Endpoint
```
✓ Test searchSong("Imagine", "John Lennon") → valid URL + similarity > 0.7
✓ Test searchSong() with "artist - title" format
✓ Test searchSong() with partial match (e.g., "imagine" vs "imagine - john lennon")
✓ Test searchSong() with non-existent song → empty result
✓ Test searchSong() rate limit handling (if >200 req/min detected)
✓ Test searchSong() with special characters/unicode
✓ Test searchSong() context cancellation (timeout)
✓ Test searchSong() HTTP error (500, 401, etc.)
```

#### 1.3: HTML Parsing
```
✓ Test parseGeniusHTML() with valid Genius page (fixture in testdata/)
✓ Test parseGeniusHTML() with lyrics div containing stanzas
✓ Test parseGeniusHTML() with featured artists in lyrics
✓ Test parseGeniusHTML() with "Embed" sections (should skip)
✓ Test parseGeniusHTML() with malformed HTML (missing div)
✓ Test parseGeniusHTML() with JavaScript-rendered lyrics (edge case: skip)
```

#### 1.4: Similarity Scoring
```
✓ Test similarity scoring: perfect match = 1.0
✓ Test similarity scoring: "my song" vs "My Song" = 1.0 (case insensitive)
✓ Test similarity scoring: "imagine" vs "imagine - john lennon" > 0.8
✓ Test similarity scoring: random song vs random != 1.0
```

#### 1.5: Integration with LyricsResponse
```
✓ Test mapToResponse() sets source = LYRICS_SOURCE_GENIUS
✓ Test mapToResponse() sets type = LYRICS_TYPE_PLAIN
✓ Test mapToResponse() sets synced = false
✓ Test mapToResponse() populates resolved_title/resolved_artist from search result
✓ Test mapToResponse() sets similarity score
```

### Phase 2: Integration Tests

#### 2.1: GetLyrics RPC (prefer_source = GENIUS)
```
✓ Test GetLyrics(title="Imagine", artist="John Lennon", preferred_source=GENIUS)
  → LyricsResponse with plain-text lyrics
✓ Test GetLyrics with track_id from Spotify
  → Resolves metadata, fetches Genius lyrics
✓ Test GetLyrics(nonexistent song, preferred_source=GENIUS)
  → STATUS_OK with empty lyrics (or fallback to LrcLib)
```

#### 2.2: Fan-Out Logic (prefer_source = UNSPECIFIED)
```
✓ Test GetLyrics(UNSPECIFIED) when both LrcLib AND Genius return results
  → Prefers synced (from LrcLib) if available
  → Falls back to Genius plain-text
✓ Test GetLyrics(UNSPECIFIED) when only LrcLib fails
  → Returns Genius result
✓ Test GetLyrics(UNSPECIFIED) when only Genius fails
  → Returns LrcLib result
✓ Test GetLyrics(UNSPECIFIED) when both fail
  → STATUS_ERROR with descriptive error message
✓ Test concurrent execution (both providers queried in parallel)
```

#### 2.3: Fallback Chain
```
✓ Test GetLyrics(prefer_source=LRCLIB, song only on Genius)
  → Falls back to Genius
✓ Test GetLyrics(prefer_source=GENIUS, song only on LrcLib)
  → Falls back to LrcLib
```

### Phase 3: Error Handling & Edge Cases

#### 3.1: Network & API Errors
```
✓ Test timeout (5s HTTP client timeout)
✓ Test 401 Unauthorized (invalid/expired token)
✓ Test 429 Too Many Requests (rate limit)
✓ Test 500 Server Error (Genius API down)
✓ Test connection refused (no internet)
✓ Test context.Done() during search (client cancel)
```

#### 3.2: Data Quality
```
✓ Test lyrics with BOM (Byte Order Mark) removal
✓ Test lyrics with HTML entities (&amp;, &nbsp;, etc.) properly decoded
✓ Test very long lyrics (>50KB) — truncate or warn?
✓ Test lyrics with explicit content markers [EXPLICIT] — should include or strip?
✓ Test lyrics with [Intro], [Verse 1], etc. labels — keep or strip?
```

#### 3.3: Caching & Performance
```
✓ Test that searches are not cached (to detect new songs added to Genius)
  OR Test cache hit for repeated requests (if caching added)
✓ Test that scraping doesn't hammer Genius (add request deduplication?)
✓ Measure latency: full search+scrape cycle should be <3s (p95)
```

### Phase 4: Service Status & Observability

#### 4.1: Health Probes (GetServiceStatus RPC)
```
✓ Test that GetServiceStatus includes "genius" dependency
✓ Test genius health = OK when API reachable + returning results
✓ Test genius health = DEGRADED when API slow or rate-limited
✓ Test genius health = DOWN when unreachable
✓ Test latency_ms reported accurately (search + scrape time)
```

#### 4.2: Logging
```
✓ Test that search queries logged at DEBUG level (for debugging)
✓ Test that errors logged at ERROR level with context
✓ Test that rate-limit hits logged at WARN level
```

### Phase 5: Load & Stress Testing

#### 5.1: Concurrent Requests
```
✓ Test 100 concurrent GetLyrics requests
  → No race conditions or connection leaks
✓ Test 100 concurrent GetLyrics with same track_id
  → Deduplication (if implemented) works correctly
✓ Test steady state: requests/sec, p50/p95/p99 latency
```

#### 5.2: Rate Limit Resilience
```
✓ Test graceful degradation when Genius rate-limited
  → Fallback to LrcLib continues working
✓ Test that we don't amplify rate-limits (reasonable backoff)
```

---

## Test Execution Strategy

### Test Data / Fixtures

**testdata/genius/**
```
├── valid_song_response.json      # Mock search API response
├── valid_song_page.html          # Real or sanitized Genius song page
├── malformed_page.html           # Missing lyrics div
├── unicode_lyrics.html           # International character test
└── rate_limit_response.json      # 429 response
```

### Mock Strategy

```go
// Option A: HTTP mocking (table-driven)
type geniusSearchTest struct {
    query    string
    wantURL  string
    wantSim  float32
    httpResp *http.Response
    httpErr  error
}

// Option B: Interface-based injection (for advanced scenarios)
type httpDoer interface {
    Do(*http.Request) (*http.Response, error)
}
// Inject mock httpDoer into geniusClient for testing

// Option C: Live API (integration test only, requires GENIUS_ACCESS_TOKEN)
// Use real token from .env but with test_data environment variable
```

### CI/CD Integration

```yaml
# Example GitHub Actions / GitLab CI config
test:
  - Run lrclib tests (existing, no env needed)
  - Run genius unit tests (mocked, no env needed)
  - If GENIUS_ACCESS_TOKEN set:
      - Run integration tests against live API
      - Test rate-limit behavior
```

---

## Implementation Roadmap

### Milestone 1: Basic Implementation (Week 1)
1. Create `genius.go` with search + scrape + parse logic
2. Write unit tests with mocked HTTP responses
3. Update `main.go` to initialize geniusClient
4. Add `GENIUS_ACCESS_TOKEN` env handling

### Milestone 2: Integration (Week 2)
1. Implement fan-out logic in GetLyrics RPC
2. Add preferred_source parameter handling
3. Write integration tests
4. Add Genius to GetServiceStatus health probes

### Milestone 3: Optimization (Week 3)
1. Add optional request deduplication cache
2. Implement exponential backoff for rate-limits
3. Performance profiling & latency tuning
4. Add structured logging

### Milestone 4: Polish (Week 4)
1. Documentation update (CLAUDE.md, README.md)
2. Load testing & stress testing
3. Error handling review
4. Final integration tests

---

## Key Decisions / Recommendations

### 1. HTML Parsing Library
- **Option A**: `golang.org/x/net/html` (stdlib)
  - Pro: No external deps, stable
  - Con: Manual DOM traversal (verbose)
  - **Recommend**: Use this for official/production

- **Option B**: `github.com/PuerkitoBio/goquery` (jQuery-like)
  - Pro: Concise CSS selectors
  - Con: Extra dependency
  - **Recommend**: Use in tests for clarity

### 2. Caching Strategy
- **No caching** (MVP): Simpler, always fresh lyrics
- **Time-based cache** (nice-to-have): Cache search results for 24h
- **Dedup only** (balance): Cache within same request window (avoid duplicate API calls for same track)
- **Recommendation**: Start with no caching; add if load becomes issue

### 3. Rate Limit Handling
- **Approach**: Check HTTP headers `X-RateLimit-*` (if Genius provides)
- **Fallback**: On 429 response, log warning and fall back to LrcLib
- **No aggressive retry**: Don't retry 429 with backoff; let client retry later
- **Recommendation**: Simple pass-through; let load balancer/client handle backoff

### 4. Similarity Scoring
- **Reuse LrcLib's stringSimilarity()**: Works well for Genius too
- **Fix**: Genius API returns full song URL; extract title from there
- **Bonus**: Artist check optional (many songs share titles, artist match helps)

### 5. Error Handling in GetLyrics
```go
// Preferred: Clean up and return useful errors
if lrcErr != nil && geniusErr != nil {
    return &pb.LyricsResponse{
        Status: pb.ResponseStatus_STATUS_ERROR,
        Error:  fmt.Sprintf("lrclib: %v, genius: %v", lrcErr, geniusErr),
        Type:   pb.LyricsType_LYRICS_TYPE_NONE,
    }, nil  // NOT error, return protobuf response with STATUS_ERROR
}
```

---

## Code Review Checklist

When reviewing Genius implementation, verify:

- [ ] **Environment Config**
  - Token loaded from `GENIUS_ACCESS_TOKEN` env
  - Graceful degradation if token missing (genius endpoint disabled)
  - Token not logged in debug output

- [ ] **HTTP Client**
  - 5s timeout set (or configurable)
  - User-Agent header set (Genius may rate-limit bots)
  - Proper context handling (cancellation, deadlines)

- [ ] **API Calls**
  - Search uses clean query format (title + artist)
  - Error responses handled (401, 429, 500, etc.)
  - URL extraction robust (handle Genius URL format changes)

- [ ] **HTML Parsing**
  - Lyrics div identified reliably
  - HTML entities decoded properly
  - Handles missing/malformed HTML gracefully

- [ ] **Integration**
  - Both LrcLib and Genius initialized in main()
  - preferred_source parameter respected
  - Fan-out runs both in parallel (not sequential)
  - Fallback logic works both ways

- [ ] **Testing**
  - Unit tests use fixtures (no live API calls)
  - Mock HTTP responses for error cases
  - Integration tests optional/gated by GENIUS_ACCESS_TOKEN env
  - Benchmark added for similarity scoring

- [ ] **Logging**
  - Search queries logged (debug level)
  - Errors logged with context
  - Rate-limit events logged (warn level)
  - No token leakage in logs

---

## Success Criteria

Implementation is **COMPLETE** when:

1. ✅ GetLyrics RPC can fetch plain-text lyrics from Genius
2. ✅ LyricsResponse.source correctly set to LYRICS_SOURCE_GENIUS
3. ✅ preferred_source parameter honored (LrcLib, Genius, or both)
4. ✅ Fallback chain works in both directions
5. ✅ All error cases handled gracefully (no crashes)
6. ✅ 40+ unit tests pass (genius_test.go)
7. ✅ 10+ integration tests pass (if GENIUS_ACCESS_TOKEN set)
8. ✅ Service latency p95 < 3s for most songs
9. ✅ GetServiceStatus reports accurate Genius health
10. ✅ README updated with Genius setup instructions

---

## References

- **Genius API**: https://docs.genius.com/ (public docs)
- **LrcLib Integration** (reference): `bedrock_server/lrclib.go`
- **Proto Messages** (reference): `proto/bedrock_service.proto` (lines 28-218)
- **GetLyrics RPC** (reference): `bedrock_server/main.go` (lines 817-856)

---

## Appendix: Quick Start Commands

```bash
# Register for Genius API
open https://genius.com/api-clients

# Add token to .env
echo "GENIUS_ACCESS_TOKEN=your_token_here" >> .env

# Run tests (when genius.go is ready)
go test -v ./bedrock_server -run TestGenius

# Run integration tests (requires token)
GENIUS_ACCESS_TOKEN=xxx go test -v -tags=integration ./bedrock_server

# Benchmark string similarity
go test -bench=StringSimil ./bedrock_server

# Check logs
go run ./bedrock_server 2>&1 | grep -i genius
```

---

**Document Version**: 1.0
**Status**: Ready for Implementation
**Assigned To**: [Developer]
**Next Review**: After Milestone 1 completion
