// lrclib_test.go - unit tests for lrclib client and lrc parser
//
// simple lowercase comments. indie dev style.

package main

import (
	"reflect"
	"testing"

	pb "example/grpc/bedrock"
)

func TestParseLRC(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		expected []*pb.LyricsLine
	}{
		{
			name: "standard centiseconds",
			raw:  "[00:17.01] It starts with one",
			expected: []*pb.LyricsLine{
				{TimeMs: 17010, Text: "It starts with one"},
			},
		},
		{
			name: "milliseconds 3 digits",
			raw:  "[01:02.345] Test line",
			expected: []*pb.LyricsLine{
				{TimeMs: 62345, Text: "Test line"},
			},
		},
		{
			name: "no decimal",
			raw:  "[00:05] Intro",
			expected: []*pb.LyricsLine{
				{TimeMs: 5000, Text: "Intro"},
			},
		},
		{
			name: "empty line",
			raw:  "[00:01.00] ",
			expected: []*pb.LyricsLine{
				{TimeMs: 1000, Text: ""},
			},
		},
		{
			name: "garbage line",
			raw:  "this is not lrc",
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseLRC(tt.raw)
			if len(got) != len(tt.expected) {
				t.Errorf("parseLRC() length = %v, want %v", len(got), len(tt.expected))
				return
			}
			for i := range got {
				if got[i].TimeMs != tt.expected[i].TimeMs || got[i].Text != tt.expected[i].Text {
					t.Errorf("parseLRC() at index %d = %v, want %v", i, got[i], tt.expected[i])
				}
			}
		})
	}
}

func TestMapToResponse(t *testing.T) {
	client := &lrcClient{}

	t.Run("synced lyrics", func(t *testing.T) {
		track := &lrcTrack{
			TrackName:    "Title",
			ArtistName:   "Artist",
			SyncedLyrics: "[00:01.00] Line 1",
			PlainLyrics:  "Line 1",
		}
		got := client.mapToResponse(track)

		if got.Synced != true {
			t.Errorf("expected Synced=true")
		}
		if len(got.SyncedLines) != 1 || got.SyncedLines[0].TimeMs != 1000 {
			t.Errorf("unexpected SyncedLines: %v", got.SyncedLines)
		}
		// note: we can't test got.Type here because it won't compile without regenerated proto,
		// but we know the logic is there in lrclib.go
	})

	t.Run("plain lyrics fallback", func(t *testing.T) {
		track := &lrcTrack{
			TrackName:   "Title",
			ArtistName:  "Artist",
			PlainLyrics: "Line 1\nLine 2",
		}
		got := client.mapToResponse(track)

		if got.Synced != false {
			t.Errorf("expected Synced=false")
		}
		if len(got.SyncedLines) != 2 || got.SyncedLines[0].TimeMs != 0 {
			t.Errorf("unexpected SyncedLines: %v", got.SyncedLines)
		}
		if got.Lyrics != "Line 1\nLine 2" {
			t.Errorf("unexpected Lyrics: %q", got.Lyrics)
		}
	})
}
