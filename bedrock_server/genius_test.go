// genius_test.go - unit tests for genius lyrics cleanup
//
// simple lowercase comments. indie dev style.

package main

import (
	"testing"
)

func TestCleanupGeniusLyrics(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		expected string
	}{
		{
			name: "remove tags",
			raw:  "[Verse 1]\nLine 1\n[Chorus]\nLine 2",
			expected: "Line 1\nLine 2",
		},
		{
			name: "keep spaces",
			raw:  "Line 1\n\nLine 2",
			expected: "Line 1\nLine 2",
		},
		{
			name: "trim lines",
			raw:  "  Line 1  \n  Line 2  ",
			expected: "Line 1\nLine 2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cleanupGeniusLyrics(tt.raw)
			if got != tt.expected {
				t.Errorf("cleanupGeniusLyrics() = %q, want %q", got, tt.expected)
			}
		})
	}
}
