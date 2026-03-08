// proxy.go - http streaming proxy for media and covers
//
// simple, lowercase comments. indie dev style.

package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	pb "example/grpc/bedrock"
)

// rewriteTrack replaces raw urls in pb.Track with proxy links
func rewriteTrack(t *pb.Track, host string) {
	if t == nil {
		return
	}
	// service name for the route: "soundcloud", "spotify", etc.
	service := strings.ToLower(strings.TrimPrefix(pb.Platform_name[int32(t.Source)], "PLATFORM_"))
	if service == "unspecified" {
		return
	}

	if t.CoverUrl != "" {
		t.CoverUrl = fmt.Sprintf("http://%s/cover/%s/%s", host, service, t.PlatformId)
	}

	for _, a := range t.Artists {
		rewriteArtist(a, host)
	}

	// note: we don't have a direct raw stream url in Track message,
	// but we can provide a proxy link that GetStreamURL would normally return.
}

// rewriteAlbum replaces raw urls in pb.Album and its child tracks
func rewriteAlbum(a *pb.Album, tracks []*pb.Track, host string) {
	if a == nil {
		return
	}
	service := strings.ToLower(strings.TrimPrefix(pb.Platform_name[int32(a.Source)], "PLATFORM_"))
	if service != "unspecified" && a.CoverUrl != "" {
		a.CoverUrl = fmt.Sprintf("http://%s/cover/%s/%s", host, service, a.PlatformId)
	}
	for _, art := range a.Artists {
		rewriteArtist(art, host)
	}
	for _, t := range tracks {
		rewriteTrack(t, host)
	}
}

// rewriteArtist replaces raw urls in pb.Artist
func rewriteArtist(a *pb.Artist, host string) {
	if a == nil {
		return
	}
	service := strings.ToLower(strings.TrimPrefix(pb.Platform_name[int32(a.Source)], "PLATFORM_"))
	if service == "unspecified" {
		return
	}

	if a.ImageUrl != "" {
		_, nativeID := parsePlatformID(a.Id)
		a.ImageUrl = fmt.Sprintf("http://%s/cover/%s/%s", host, service, nativeID)
	}
}

// rewritePlaylist replaces raw urls in pb.Playlist and its child tracks
func rewritePlaylist(p *pb.Playlist, tracks []*pb.Track, host string) {
	if p == nil {
		return
	}
	service := strings.ToLower(strings.TrimPrefix(pb.Platform_name[int32(p.Source)], "PLATFORM_"))
	if service != "unspecified" && p.CoverUrl != "" {
		p.CoverUrl = fmt.Sprintf("http://%s/cover/%s/%s", host, service, p.PlatformId)
	}
	for _, t := range tracks {
		rewriteTrack(t, host)
	}
}

var proxyClient = &http.Client{
	Timeout: 0, // no timeout for streaming
}

// proxyHandler handles both /stream and /cover routes
func proxyHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	parts := strings.Split(path, "/")

	if len(parts) < 3 {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	action := parts[0]  // "stream" or "cover"
	service := parts[1] // "soundcloud", "spotify", etc.
	id := parts[2]      // native id

	// resolution timeout - don't hang if provider is slow
	resCtx, resCancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer resCancel()

	var targetURL string
	var err error

	if action == "stream" {
		// resolveStreamURL expects namespaced id: "service:id"
		resp, err := resolveStreamURL(resCtx, service+":"+id, "")
		if err != nil {
			log.Printf("[proxy] stream resolve error: %v", err)
			http.Error(w, "failed to resolve stream", http.StatusNotFound)
			return
		}
		targetURL = resp.GetStreamUrl()
	} else if action == "cover" {
		targetURL, err = resolveCoverURL(resCtx, service, id)
		if err != nil {
			log.Printf("[proxy] cover resolve error: %v", err)
			http.Error(w, "failed to resolve cover", http.StatusNotFound)
			return
		}
	} else {
		http.Error(w, "unknown action", http.StatusNotFound)
		return
	}

	if targetURL == "" {
		http.Error(w, "empty target url", http.StatusNotFound)
		return
	}

	// use request context for streaming - no hard timeout, follows client life
	proxyReq, err := http.NewRequestWithContext(r.Context(), "GET", targetURL, nil)
	if err != nil {
		http.Error(w, "failed to create request", http.StatusInternalServerError)
		return
	}

	// forward range header for seeking
	if rangeH := r.Header.Get("Range"); rangeH != "" {
		proxyReq.Header.Set("Range", rangeH)
	}

	// some providers might need a user-agent to avoid blocks
	proxyReq.Header.Set("User-Agent", "Mozilla/5.0 (bedrock-proxy)")

	// execute request
	resp, err := proxyClient.Do(proxyReq)
	if err != nil {
		log.Printf("[proxy] fetch error: %v", err)
		http.Error(w, "failed to fetch source", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// forward essential headers
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.Header().Set("Accept-Ranges", resp.Header.Get("Accept-Ranges"))
	if etag := resp.Header.Get("ETag"); etag != "" {
		w.Header().Set("ETag", etag)
	}

	// handle content length and status
	if resp.StatusCode == http.StatusPartialContent {
		w.Header().Set("Content-Range", resp.Header.Get("Content-Range"))
		w.Header().Set("Content-Length", resp.Header.Get("Content-Length"))
		w.WriteHeader(http.StatusPartialContent)
	} else if resp.StatusCode == http.StatusOK {
		w.Header().Set("Content-Length", resp.Header.Get("Content-Length"))
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(resp.StatusCode)
	}

	// pipe the body - true streaming
	_, err = io.Copy(w, resp.Body)
	if err != nil {
		log.Printf("[proxy] copy error: %v", err)
	}
}

func startProxyServer(addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/stream/", proxyHandler)
	mux.HandleFunc("/cover/", proxyHandler)

	log.Printf("[proxy] server listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("[proxy] server failed: %v", err)
	}
}
