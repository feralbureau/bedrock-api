package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	innertube "github.com/wslyyy/youtube-go"
)

func main() {
	httpClient := &http.Client{Timeout: 12 * time.Second}
	ctx := innertube.ClientContext{
		ClientName:    "WEB_REMIX",
		ClientVersion: "1.20260213.01.00",
		ClientID:      67,
		APIKey:        "AIzaSyC9XL3ZjWddXya6X74dJoCTL-WEYFDNX30",
		UserAgent:     innertube.USER_AGENT_WEB,
		Referer:       innertube.REFERER_YOUTUBE_MUSIC,
	}
	it := &innertube.InnerTube{Adaptor: innertube.NewInnerTubeAdaptor(ctx, httpClient)}

	// rick astley top 3 tracks
	videoIDs := []string{"lYBUbBu4W08", "i_Q88T1HI_w", "OFMthc9YkOw"}
	data, err := it.MusicGetQueue(&videoIDs, nil)
	if err != nil {
		log.Fatalf("MusicGetQueue error: %v", err)
	}
	writeJSON("yt_queue_resp.json", data)
	log.Println("done — check yt_queue_resp.json")
}

func writeJSON(name string, v any) {
	f, err := os.Create(name)
	if err != nil {
		log.Fatalf("create %s: %v", name, err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		log.Fatalf("encode %s: %v", name, err)
	}
}
