# Genius Lyrics Testing Specification

**Version**: 1.0
**Status**: Ready for Test Implementation
**Created**: 2026-03-12

---

## Overview

This document provides detailed test specifications for the Genius lyrics integration in Bedrock. It complements `GENIUS_ANALYSIS.md` with concrete test case implementations, fixtures, and validation criteria.

---

## Test Organization

```
bedrock_server/
├── genius.go                    (implementation)
├── genius_test.go               (unit tests)
├── testdata/genius/
│   ├── search_response.json     (mocked API response)
│   ├── valid_song.html          (mocked HTML scrape)
│   ├── malformed.html           (error case)
│   └── unicode_lyrics.html      (edge case)
└── lrclib_test.go               (existing, keep as-is)
```

---

## Unit Test Structure

### Test File: genius_test.go

```go
package main

import (
    "context"
    "encoding/json"
    "io"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
    "time"

    pb "example/grpc/bedrock"
)

// Mock fixtures
type mockGeniusServer struct {
    *httptest.Server
    searchResponses map[string]*geniusSearchResponse
    pageResponses   map[string]string
    requestCount    int
}

type geniusSearchResponse struct {
    Response struct {
        Hits []struct {
            Result struct {
                ID               int    `json:"id"`
                Title            string `json:"title"`
                PrimaryArtist    struct {
                    Name string `json:"name"`
                } `json:"primary_artist"`
                URL string `json:"url"`
            } `json:"result"`
        } `json:"hits"`
    } `json:"response"`
}
```

---

## Detailed Test Cases

### 1. Initialization Tests

#### Test: `TestNewGeniusClient_ValidToken`
```go
func TestNewGeniusClient_ValidToken(t *testing.T) {
    client := newGeniusClient("valid_token_12345")
    if client == nil {
        t.Fatal("expected non-nil client")
    }
    if client.accessToken != "valid_token_12345" {
        t.Errorf("token not set: got %q", client.accessToken)
    }
    if client.http == nil {
        t.Fatal("HTTP client not initialized")
    }
    if client.http.Timeout != 5*time.Second {
        t.Errorf("expected timeout 5s, got %v", client.http.Timeout)
    }
}
```

#### Test: `TestNewGeniusClient_EmptyToken`
```go
func TestNewGeniusClient_EmptyToken(t *testing.T) {
    client := newGeniusClient("")
    if client == nil {
        t.Fatal("expected non-nil client (graceful degradation)")
    }
    if client.accessToken != "" {
        t.Errorf("expected empty token, got %q", client.accessToken)
    }
    // Should be able to initialize but fail on first API call
}
```

---

### 2. Search Functionality Tests

#### Test: `TestSearchSong_ExactMatch`
```go
func TestSearchSong_ExactMatch(t *testing.T) {
    server := setupMockGeniusServer()
    defer server.Close()

    // Register mock response
    response := `{
        "response": {
            "hits": [{
                "result": {
                    "id": 378367,
                    "title": "Imagine",
                    "primary_artist": {"name": "John Lennon"},
                    "url": "https://genius.com/John-lennon-imagine-lyrics"
                }
            }]
        }
    }`
    server.registerSearchResponse("john lennon imagine", response)

    client := newGeniusClient("token")
    client.http = server.Client()

    url, sim, err := client.searchSong(context.Background(), "Imagine", "John Lennon")

    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if url == "" {
        t.Fatal("expected non-empty URL")
    }
    if !strings.Contains(url, "imagine-lyrics") {
        t.Errorf("URL mismatch: %q", url)
    }
    if sim < 0.9 {
        t.Errorf("expected similarity ~1.0, got %f", sim)
    }
}
```

#### Test: `TestSearchSong_PartialMatch`
```go
func TestSearchSong_PartialMatch(t *testing.T) {
    // Test that "Shape of You" -> "Shape Of You" still matches
    // Similarity should be > 0.85 but < 1.0

    title := "Shape of You"
    response := `{
        "response": {
            "hits": [{
                "result": {
                    "title": "Shape Of You",
                    "primary_artist": {"name": "Ed Sheeran"},
                    "url": "https://genius.com/Ed-sheeran-shape-of-you-lyrics"
                }
            }]
        }
    }`
    // ... setup and verify
}
```

#### Test: `TestSearchSong_NotFound`
```go
func TestSearchSong_NotFound(t *testing.T) {
    server := setupMockGeniusServer()
    server.registerSearchResponse("xyzabc nonexistent", `{"response": {"hits": []}}`)
    defer server.Close()

    client := newGeniusClient("token")
    client.http = server.Client()

    url, sim, err := client.searchSong(context.Background(), "xyzabc", "nonexistent")

    // Should not error; just return empty URL
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if url != "" {
        t.Errorf("expected empty URL for non-existent song, got %q", url)
    }
    if sim != 0.0 {
        t.Errorf("expected sim=0.0, got %f", sim)
    }
}
```

#### Test: `TestSearchSong_Timeout`
```go
func TestSearchSong_Timeout(t *testing.T) {
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        time.Sleep(10 * time.Second) // longer than 5s timeout
        w.WriteHeader(http.StatusOK)
    }))
    defer server.Close()

    client := newGeniusClient("token")
    client.http = server.Client()

    ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
    defer cancel()

    url, sim, err := client.searchSong(ctx, "Title", "Artist")

    if err == nil {
        t.Fatal("expected timeout error")
    }
    if url != "" {
        t.Errorf("expected empty URL on timeout, got %q", url)
    }
}
```

#### Test: `TestSearchSong_Unauthorized`
```go
func TestSearchSong_Unauthorized(t *testing.T) {
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusUnauthorized)
        w.Write([]byte(`{"response": {"error": "invalid_access_token"}}`))
    }))
    defer server.Close()

    client := newGeniusClient("invalid_token")
    client.http = server.Client()

    url, sim, err := client.searchSong(context.Background(), "Title", "Artist")

    if err == nil {
        t.Fatal("expected 401 error")
    }
    if !strings.Contains(err.Error(), "401") {
        t.Errorf("expected 401 in error message, got: %v", err)
    }
}
```

#### Test: `TestSearchSong_RateLimit`
```go
func TestSearchSong_RateLimit(t *testing.T) {
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Retry-After", "60")
        w.WriteHeader(http.StatusTooManyRequests)
        w.Write([]byte(`{"response": {"error": "rate_limited"}}`))
    }))
    defer server.Close()

    client := newGeniusClient("token")
    client.http = server.Client()

    url, sim, err := client.searchSong(context.Background(), "Title", "Artist")

    if err == nil {
        t.Fatal("expected rate limit error")
    }
    if !strings.Contains(err.Error(), "429") && !strings.Contains(err.Error(), "rate") {
        t.Errorf("expected rate limit error message, got: %v", err)
    }
}
```

---

### 3. HTML Parsing Tests

#### Test: `TestParseGeniusHTML_ValidSong`
```go
func TestParseGeniusHTML_ValidSong(t *testing.T) {
    html := loadFixture("valid_song.html")

    lyrics, err := parseGeniusHTML(html)

    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if lyrics == "" {
        t.Fatal("expected non-empty lyrics")
    }
    if !strings.Contains(strings.ToLower(lyrics), "imagine") {
        t.Errorf("expected 'imagine' in lyrics, got: %s", lyrics[:100])
    }
}
```

#### Test: `TestParseGeniusHTML_HTMLEntities`
```go
func TestParseGeniusHTML_HTMLEntities(t *testing.T) {
    html := `
    <div data-lyrics-container="true">
        <p>Line 1&amp;Line 2</p>
        <p>Test&nbsp;space</p>
        <p>&quot;Quoted&quot;</p>
    </div>
    `

    lyrics, err := parseGeniusHTML(html)

    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if !strings.Contains(lyrics, "&") {
        t.Errorf("ampersand not decoded properly")
    }
    if strings.Contains(lyrics, "&amp;") {
        t.Errorf("HTML entity not decoded: %s", lyrics)
    }
}
```

#### Test: `TestParseGeniusHTML_MissingLyricsDiv`
```go
func TestParseGeniusHTML_MissingLyricsDiv(t *testing.T) {
    html := loadFixture("malformed.html") // no data-lyrics-container

    lyrics, err := parseGeniusHTML(html)

    if err == nil {
        t.Fatal("expected error for missing lyrics div")
    }
    if lyrics != "" {
        t.Errorf("expected empty lyrics, got: %s", lyrics)
    }
}
```

#### Test: `TestParseGeniusHTML_UnicodeChars`
```go
func TestParseGeniusHTML_UnicodeChars(t *testing.T) {
    html := loadFixture("unicode_lyrics.html")

    lyrics, err := parseGeniusHTML(html)

    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    // Verify unicode chars preserved (not mangled)
    if !strings.Contains(lyrics, "é") && !strings.Contains(lyrics, "中") {
        // At least check we didn't lose data
        if len(lyrics) == 0 {
            t.Fatal("unicode characters lost")
        }
    }
}
```

---

### 4. Scraping Tests

#### Test: `TestScrapeLyrics_ValidURL`
```go
func TestScrapeLyrics_ValidURL(t *testing.T) {
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "text/html; charset=utf-8")
        html := loadFixture("valid_song.html")
        w.Write([]byte(html))
    }))
    defer server.Close()

    client := newGeniusClient("token")
    client.http = server.Client()

    lyrics, err := client.scrapeLyrics(context.Background(), server.URL)

    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if lyrics == "" {
        t.Fatal("expected non-empty lyrics")
    }
}
```

#### Test: `TestScrapeLyrics_InvalidURL`
```go
func TestScrapeLyrics_InvalidURL(t *testing.T) {
    client := newGeniusClient("token")

    lyrics, err := client.scrapeLyrics(context.Background(), "http://invalid.local:99999/")

    if err == nil {
        t.Fatal("expected connection error")
    }
    if lyrics != "" {
        t.Errorf("expected empty lyrics on error, got: %s", lyrics)
    }
}
```

#### Test: `TestScrapeLyrics_NotFound404`
```go
func TestScrapeLyrics_NotFound404(t *testing.T) {
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusNotFound)
    }))
    defer server.Close()

    client := newGeniusClient("token")
    client.http = server.Client()

    lyrics, err := client.scrapeLyrics(context.Background(), server.URL)

    if err == nil {
        t.Fatal("expected 404 error")
    }
    if lyrics != "" {
        t.Errorf("expected empty lyrics on 404, got: %s", lyrics)
    }
}
```

---

### 5. Integration Tests (GetLyrics RPC)

#### Test: `TestGetLyrics_GeniusPreferred_Success`
```go
func TestGetLyrics_GeniusPreferred_Success(t *testing.T) {
    // This would be an integration test using the real gRPC service
    // or a mock server setup

    ctx := context.Background()
    req := &pb.LyricsRequest{
        Title:            "Imagine",
        Artist:           "John Lennon",
        PreferredSource:  pb.LyricsSource_LYRICS_SOURCE_GENIUS,
    }

    resp, err := testServer.GetLyrics(ctx, req)

    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if resp.GetStatus() != pb.ResponseStatus_STATUS_OK {
        t.Errorf("expected STATUS_OK, got %v: %s", resp.GetStatus(), resp.GetError())
    }
    if resp.GetLyrics() == "" {
        t.Fatal("expected non-empty lyrics")
    }
    if resp.GetSource() != pb.LyricsSource_LYRICS_SOURCE_GENIUS {
        t.Errorf("expected GENIUS source, got %v", resp.GetSource())
    }
    if resp.GetType() != pb.LyricsType_LYRICS_TYPE_PLAIN {
        t.Errorf("expected PLAIN type, got %v", resp.GetType())
    }
    if resp.GetSynced() != false {
        t.Errorf("expected synced=false for Genius, got %v", resp.GetSynced())
    }
}
```

#### Test: `TestGetLyrics_GeniusPreferred_FallbackToLrcLib`
```go
func TestGetLyrics_GeniusPreferred_FallbackToLrcLib(t *testing.T) {
    // Mock Genius to return empty, LrcLib should provide synced lyrics

    ctx := context.Background()
    req := &pb.LyricsRequest{
        Title:            "Imagine",
        Artist:           "John Lennon",
        PreferredSource:  pb.LyricsSource_LYRICS_SOURCE_GENIUS,
    }

    resp, err := testServer.GetLyrics(ctx, req)

    // Even though GENIUS preferred, fallback to LrcLib should work
    if resp.GetLyrics() == "" {
        t.Fatal("expected lyrics from fallback (LrcLib)")
    }
}
```

#### Test: `TestGetLyrics_Unspecified_BothAvailable_SyncedPreferred`
```go
func TestGetLyrics_Unspecified_BothAvailable_SyncedPreferred(t *testing.T) {
    // When both sources available and preferred_source=UNSPECIFIED,
    // should prefer synced (LrcLib) over plain (Genius)

    ctx := context.Background()
    req := &pb.LyricsRequest{
        Title:   "Imagine",
        Artist:  "John Lennon",
        // PreferredSource: UNSPECIFIED (default)
    }

    resp, err := testServer.GetLyrics(ctx, req)

    if resp.GetSource() != pb.LyricsSource_LYRICS_SOURCE_LRCLIB {
        t.Errorf("expected LrcLib (synced) preferred, got %v", resp.GetSource())
    }
    if resp.GetSynced() != true {
        t.Errorf("expected synced lyrics from LrcLib, got %v", resp.GetSynced())
    }
}
```

#### Test: `TestGetLyrics_ContextCancellation`
```go
func TestGetLyrics_ContextCancellation(t *testing.T) {
    ctx, cancel := context.WithCancel(context.Background())
    cancel() // cancel immediately

    req := &pb.LyricsRequest{
        Title:  "Imagine",
        Artist: "John Lennon",
    }

    resp, err := testServer.GetLyrics(ctx, req)

    // Should return error or partial result, not hang
    if err == nil {
        if resp.GetStatus() != pb.ResponseStatus_STATUS_ERROR {
            t.Errorf("expected STATUS_ERROR on context cancel")
        }
    }
}
```

---

### 6. Similarity Scoring Tests

#### Test: `TestSimilarityScoring_ExactMatch`
```go
func TestSimilarityScoring_ExactMatch(t *testing.T) {
    sim := searchResultSimilarity("Imagine", "John Lennon", "Imagine", "John Lennon")
    if sim < 0.99 {
        t.Errorf("expected sim ~1.0 for exact match, got %f", sim)
    }
}
```

#### Test: `TestSimilarityScoring_CaseInsensitive`
```go
func TestSimilarityScoring_CaseInsensitive(t *testing.T) {
    sim1 := searchResultSimilarity("Imagine", "John Lennon", "IMAGINE", "JOHN LENNON")
    sim2 := searchResultSimilarity("Imagine", "John Lennon", "imagine", "john lennon")

    if sim1 < 0.99 || sim2 < 0.99 {
        t.Errorf("expected case-insensitive matching: %f, %f", sim1, sim2)
    }
}
```

#### Test: `TestSimilarityScoring_PartialMatch`
```go
func TestSimilarityScoring_PartialMatch(t *testing.T) {
    sim := searchResultSimilarity("Shape of You", "Ed Sheeran", "Shape Of You", "Ed Sheeran")
    if sim < 0.85 {
        t.Errorf("expected high similarity for minor differences, got %f", sim)
    }
}
```

---

### 7. Response Mapping Tests

#### Test: `TestMapGeniusToResponse_PlainText`
```go
func TestMapGeniusToResponse_PlainText(t *testing.T) {
    geniusResp := &geniusSearchResult{
        Title:  "Imagine",
        Artist: "John Lennon",
        URL:    "https://genius.com/...",
    }

    pbResp := mapGeniusToResponse(geniusResp, "Imagine\nAll the people...", 0.95)

    if pbResp.GetSource() != pb.LyricsSource_LYRICS_SOURCE_GENIUS {
        t.Errorf("expected GENIUS source")
    }
    if pbResp.GetType() != pb.LyricsType_LYRICS_TYPE_PLAIN {
        t.Errorf("expected PLAIN type")
    }
    if pbResp.GetSynced() != false {
        t.Errorf("expected synced=false")
    }
    if pbResp.GetSimilarity() != 0.95 {
        t.Errorf("expected similarity=0.95, got %f", pbResp.GetSimilarity())
    }
}
```

---

## Test Fixtures

### File: testdata/genius/search_response.json
```json
{
  "response": {
    "hits": [
      {
        "result": {
          "id": 378367,
          "title": "Imagine",
          "primary_artist": {
            "name": "John Lennon"
          },
          "url": "https://genius.com/John-lennon-imagine-lyrics"
        }
      }
    ]
  }
}
```

### File: testdata/genius/valid_song.html
(Real or sanitized Genius song page HTML)
```html
<html>
<head><title>Imagine by John Lennon</title></head>
<body>
  <div data-lyrics-container="true">
    <div><p>Imagine there's no heaven</p></div>
    <div><p>It's easy if you try</p></div>
    <!-- ... more stanzas -->
  </div>
</body>
</html>
```

### File: testdata/genius/malformed.html
```html
<html>
<body>
  <!-- Missing data-lyrics-container div -->
  <div>Some other content</div>
</body>
</html>
```

---

## Test Execution Matrix

| Test Group | Type | Count | Mock? | Time | Status |
|---|---|---|---|---|---|
| Initialization | Unit | 2 | Yes | <100ms | ✓ Ready |
| Search | Unit | 6 | Yes | ~500ms | ✓ Ready |
| HTML Parsing | Unit | 5 | Yes | ~200ms | ✓ Ready |
| Scraping | Unit | 3 | Yes | ~300ms | ✓ Ready |
| Response Mapping | Unit | 1 | Yes | <50ms | ✓ Ready |
| Similarity | Unit | 3 | Yes | <100ms | ✓ Ready |
| Integration (RPC) | Integration | 5 | Partial | ~2s | ⚠️ Needs env |
| **Total** | | **25** | | | |

---

## Mocking Strategy

### Approach: httptest + Fixtures

```go
func setupMockGeniusServer() *mockGeniusServer {
    mux := http.NewServeMux()

    mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
        q := r.URL.Query().Get("q")
        if resp, ok := mockResponses[q]; ok {
            w.Header().Set("Content-Type", "application/json")
            json.NewEncoder(w).Encode(resp)
        } else {
            w.Header().Set("Content-Type", "application/json")
            json.NewEncoder(w).Encode(`{"response": {"hits": []}}`)
        }
    })

    server := httptest.NewServer(mux)
    return &mockGeniusServer{Server: server}
}
```

### Benefits
- ✅ No external dependencies needed
- ✅ Fast execution (all in-memory)
- ✅ Deterministic results
- ✅ Easy to test error cases
- ✅ No rate limits or credentials needed

---

## CI/CD Integration

### GitHub Actions Example

```yaml
name: Test Genius Integration

on: [push, pull_request]

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3

      - name: Set up Go
        uses: actions/setup-go@v4
        with:
          go-version: 1.23

      - name: Run unit tests (mocked)
        run: go test -v -run "^TestGenius" ./bedrock_server

      - name: Run all LrcLib tests
        run: go test -v ./bedrock_server

      - name: Run integration tests (if token available)
        if: secrets.GENIUS_ACCESS_TOKEN != ''
        env:
          GENIUS_ACCESS_TOKEN: ${{ secrets.GENIUS_ACCESS_TOKEN }}
        run: go test -v -tags=integration -run "Integration" ./bedrock_server
```

---

## Running Tests Locally

```bash
# All unit tests (no env needed)
go test -v ./bedrock_server

# Genius tests only
go test -v -run Genius ./bedrock_server

# With coverage
go test -v -cover ./bedrock_server

# Integration tests (requires GENIUS_ACCESS_TOKEN)
GENIUS_ACCESS_TOKEN=your_token go test -v -tags=integration ./bedrock_server

# Benchmark string similarity
go test -bench=StringSimilarity -benchmem ./bedrock_server
```

---

## Validation & Success Criteria

Test suite is **COMPLETE** when:

- [ ] All 25 unit tests pass
- [ ] 100% coverage for genius.go (target >90%)
- [ ] All error cases handled (401, 429, timeout, invalid HTML, etc.)
- [ ] Fixtures present and documented
- [ ] Integration tests pass with real API token (if available)
- [ ] CI/CD pipeline green on PRs
- [ ] Latency benchmarks recorded (p50/p95/p99 for search+scrape)

---

## Appendix: Test Template

```go
func TestGenius[Feature](t *testing.T) {
    // 1. Setup
    server := setupMockGeniusServer()
    defer server.Close()

    client := newGeniusClient("test_token")
    client.http = server.Client()

    // 2. Execute
    result, err := client.[method](context.Background(), ...)

    // 3. Validate
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }

    if got, want := result.Field, expectedValue; got != want {
        t.Errorf("got %v, want %v", got, want)
    }
}
```

---

**Document Version**: 1.0
**Next Update**: After first 5 tests implemented
