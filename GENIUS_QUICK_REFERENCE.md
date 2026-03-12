# Genius Lyrics Integration — Quick Reference

**Last Updated**: 2026-03-12
**Status**: 📋 Analysis Complete | ⏳ Implementation Pending

---

## 📊 Current Status

| Component | Status | Details |
|-----------|--------|---------|
| **LrcLib** | ✅ Complete | Synced lyrics working; 80+ unit tests |
| **Genius (Protobuf)** | ✅ Defined | Proto messages ready; not wired to RPC |
| **Genius (Implementation)** | ❌ Missing | No `genius.go` file; not in GetLyrics |
| **Fan-out Logic** | ⚠️ Partial | Only calls LrcLib; ignores `preferred_source` |
| **Tests** | ❌ Missing | No genius tests; no fixtures |

---

## 🎯 Key Findings

### The Problem
GetLyrics RPC (main.go:817) only calls:
```go
resp, err := s.lyrics.getLyrics(ctx, title, artist, durationS)
```

This is hardcoded to LrcLib. Even though:
- ✅ Proto defines `LYRICS_SOURCE_GENIUS`
- ✅ Proto defines `preferred_source` field
- ✅ README says "Genius support in progress"
- ❌ No actual Genius client exists
- ❌ No fan-out to both sources

### Genius API Quick Facts
- **Free Tier**: 200 req/min rate limit
- **Auth**: Bearer token (requires genius.com registration)
- **Search**: `/search` returns URLs to song pages
- **Lyrics**: Must be **scraped from HTML** (no direct lyrics endpoint)
- **Type**: Plain-text only (no sync timestamps)

---

## 🏗️ Implementation Architecture

### Files to Create
```
bedrock_server/genius.go          (~300 LOC)
bedrock_server/genius_test.go     (~400 LOC)
bedrock_server/testdata/genius/   (fixtures)
```

### genius.go Minimal API
```go
type geniusClient struct {
    accessToken string
    http        *http.Client
}

func newGeniusClient(token string) *geniusClient
func (c *geniusClient) getLyrics(ctx context.Context, title, artist string) (*pb.LyricsResponse, error)
```

### GetLyrics Update (fan-out)
```go
// Current: Only LrcLib
resp, err := s.lyrics.getLyrics(...)

// New: Both sources (parallel)
lrcResp := <-lrcChan
geniusResp := <-genuiusChan
resp = pickBest(lrcResp, geniusResp)  // synced > higher similarity
```

---

## 📋 Test Checklist (25 tests)

### Unit Tests (mocked HTTP)
- ✓ 2 initialization tests
- ✓ 6 search tests (match, partial, not found, timeout, 401, 429)
- ✓ 5 HTML parsing tests (valid, entities, unicode, missing div, malformed)
- ✓ 3 scraping tests (valid, invalid URL, 404)
- ✓ 3 similarity tests (exact, case-insensitive, partial)
- ✓ 1 response mapping test

### Integration Tests (live API or mocked)
- ✓ 5 RPC tests (preferred_source=GENIUS, LRCLIB, UNSPECIFIED, fallback, error)

**Total**: 25 tests, 100% coverage target

---

## 🚀 Implementation Timeline

| Milestone | Duration | Deliverables |
|-----------|----------|---------------|
| **Phase 1** | Week 1 | genius.go + unit tests |
| **Phase 2** | Week 2 | Fan-out logic + integration tests |
| **Phase 3** | Week 3 | Performance tuning + caching (optional) |
| **Phase 4** | Week 4 | Documentation + final testing |

---

## 🔑 Key Decisions

### 1. HTML Parsing
- **Recommendation**: Use stdlib `golang.org/x/net/html` + manual DOM traversal
- **Why**: No external dependencies; stable
- **Fallback**: `goquery` library if complexity grows

### 2. Error Handling
```go
// On Genius error, fall back to LrcLib (not vice versa by default)
if preferred == GENIUS {
    try Genius
    if fails → try LrcLib
} else {
    try LrcLib (prefers synced)
    if fails → try Genius
}
```

### 3. Caching
- **MVP**: No caching (always fresh)
- **Future**: Time-based (24h) or dedup-only within request window
- **Why**: Lyrics providers rarely updated; dedup is enough

### 4. Rate Limiting
- **Approach**: No aggressive retry; pass errors to client
- **Graceful Degradation**: Fall back to LrcLib on 429
- **Logging**: Warn on rate-limit hits

---

## 📚 Documentation Deliverables

| Document | Purpose | Status |
|----------|---------|--------|
| **GENIUS_ANALYSIS.md** | Complete API requirements + test plan | ✅ Ready |
| **GENIUS_TEST_SPECIFICATION.md** | Detailed test cases + fixtures | ✅ Ready |
| **GENIUS_QUICK_REFERENCE.md** | This file | ✅ Ready |

---

## 🛠️ Setup Instructions

### Step 1: Get Genius API Token
```bash
# Visit: https://genius.com/api-clients
# Create app → copy access token
echo "GENIUS_ACCESS_TOKEN=<token>" >> .env
```

### Step 2: Create genius.go
Use the architecture in GENIUS_ANALYSIS.md (Section: "Proposed Implementation")

### Step 3: Write Tests
Reference GENIUS_TEST_SPECIFICATION.md for 25 test cases

### Step 4: Update main.go
Implement fan-out logic (see GetLyrics section in GENIUS_ANALYSIS.md)

### Step 5: Run & Validate
```bash
go test -v ./bedrock_server
go test -cover ./bedrock_server  # target >90%
```

---

## ⚠️ Common Pitfalls

| Issue | Solution |
|-------|----------|
| **Rate Limited** | Fall back to LrcLib; don't retry aggressively |
| **Slow Scraping** | Each song page = 1 HTTP request; cache results if needed |
| **HTML Changes** | Use robust CSS selectors; test with multiple songs |
| **Token Leak** | Don't log token; use env vars; sanitize logs |
| **Unicode Issues** | Ensure HTML entities decoded; use UTF-8 throughout |
| **Timeout** | Set 5s HTTP timeout; respect context deadlines |

---

## 💡 Quick Start Commands

```bash
# Clone repo
cd bedrock-api

# Set up env
cp .env.example .env
# Edit .env: add GENIUS_ACCESS_TOKEN

# Test (existing LrcLib tests pass)
go test -v ./bedrock_server

# Build
go build -o bedrock ./bedrock_server

# Run
./bedrock

# After Genius implementation:
go test -v -run Genius ./bedrock_server
```

---

## 🔗 References

**Files to Review**:
- `bedrock_server/lrclib.go` — Reference for similar client pattern
- `bedrock_server/lrclib_test.go` — Reference for test structure
- `proto/bedrock_service.proto` (lines 28-33, 174-218) — Genius enum + messages
- `bedrock_server/main.go` (lines 817-856) — GetLyrics RPC handler

**External Links**:
- Genius API Docs: https://docs.genius.com/
- Go HTML Parsing: https://golang.org/x/net/html
- Testing with httptest: https://pkg.go.dev/net/http/httptest

---

## 📞 Support & Questions

**Q: Should Genius be required or optional?**
A: Optional. If `GENIUS_ACCESS_TOKEN` missing, disable Genius gracefully. LrcLib works standalone.

**Q: What if Genius rate-limits us?**
A: Return fallback to LrcLib; log warning; don't retry aggressively.

**Q: Can we cache Genius search results?**
A: Yes, but not needed for MVP. Add if performance becomes issue (25+ concurrent requests).

**Q: How do we handle Genius HTML structure changes?**
A: Use robust selectors (target `[data-lyrics-container]`). Monitor for breaks in logs.

**Q: Should GetLyrics fan-out both in parallel or sequentially?**
A: Parallel (concurrency = strength of Go). LrcLib has timeout; Genius won't block.

---

## 🎓 Learning Resources

For developers implementing this:

1. **Read GENIUS_ANALYSIS.md** (10 min) — Understand requirements
2. **Read GENIUS_TEST_SPECIFICATION.md** (15 min) — See test examples
3. **Review lrclib.go** (10 min) — Pattern to follow
4. **Start with genius.go** (stub + search, 1 hour)
5. **Add genius_test.go** (unit tests, 2 hours)
6. **Integrate main.go** (fan-out, 1 hour)

**Total**: ~4-5 hours for basic implementation

---

## ✅ Success Criteria

Implementation is **DONE** when:

1. ✅ `GetLyrics(preferred_source=GENIUS)` returns Genius plain-text lyrics
2. ✅ `GetLyrics(preferred_source=UNSPECIFIED)` prefers synced (LrcLib) if available
3. ✅ Both sources queried in parallel (no sequential blocking)
4. ✅ 25+ unit tests pass (90%+ coverage)
5. ✅ Service latency p95 < 3s
6. ✅ GetServiceStatus reports accurate Genius health
7. ✅ All error cases handled gracefully
8. ✅ README updated with Genius setup

---

**Version**: 1.0
**Status**: Ready for Development
**Next Step**: Create genius.go (reference GENIUS_ANALYSIS.md Section 3)
