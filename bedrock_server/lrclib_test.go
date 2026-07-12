package main

import (
	"math"
	"testing"

	pb "github.com/feralbureau/bedrock-api/bedrock"
)

func TestParseLRC(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		wantLen  int
		wantMs   int32
		wantText string
	}{
		{
			name:     "standard centiseconds",
			raw:      "[00:17.01] It starts with one",
			wantLen:  1,
			wantMs:   17010,
			wantText: "It starts with one",
		},
		{
			name:     "milliseconds 3 digits",
			raw:      "[01:02.345] Test line",
			wantLen:  1,
			wantMs:   62345,
			wantText: "Test line",
		},
		{
			name:     "no decimal",
			raw:      "[00:05] Intro",
			wantLen:  1,
			wantMs:   5000,
			wantText: "Intro",
		},
		{
			name:     "multiple lines",
			raw:      "[00:12.34] line one\n[00:56.78] line two\n[01:23.45] line three",
			wantLen:  3,
			wantMs:   83450,
			wantText: "line three",
		},
		{
			name:     "empty line skipped",
			raw:      "[00:01.00] first\n\n[00:02.00] third",
			wantLen:  2,
			wantMs:   2000,
			wantText: "third",
		},
		{
			name:     "garbage line skipped",
			raw:      "[00:01.00] real\nthis is not lrc\n[00:02.00] also real",
			wantLen:  2,
			wantMs:   2000,
			wantText: "also real",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseLRC(tt.raw)
			if len(got) != tt.wantLen {
				t.Errorf("parseLRC(%q) returned %d lines, want %d", tt.raw, len(got), tt.wantLen)
				return
			}
			if tt.wantLen > 0 {
				last := got[len(got)-1]
				if last.TimeMs != tt.wantMs {
					t.Errorf("parseLRC(%q) last.TimeMs = %d, want %d", tt.raw, last.TimeMs, tt.wantMs)
				}
				if last.Text != tt.wantText {
					t.Errorf("parseLRC(%q) last.Text = %q, want %q", tt.raw, last.Text, tt.wantText)
				}
			}
		})
	}
}

func TestParseLRCWithMetadataTags(t *testing.T) {
	raw := "[ti:Title]\n[ar:Artist]\n[00:01.00] first line\n[00:02.00] second line"
	got := parseLRC(raw)
	if len(got) != 2 {
		t.Errorf("parseLRC should skip metadata tags, got %d lines", len(got))
	}
}

func TestStringSimilarity(t *testing.T) {
	tests := []struct {
		name   string
		a, b   string
		minSim float64
		maxSim float64
	}{
		{
			name:   "exact match",
			a:      "hello world",
			b:      "hello world",
			minSim: 1.0,
			maxSim: 1.0,
		},
		{
			name:   "case insensitive",
			a:      "Hello World",
			b:      "hello world",
			minSim: 1.0,
			maxSim: 1.0,
		},
		{
			name:   "one char different",
			a:      "hello",
			b:      "hallo",
			minSim: 0.7,
			maxSim: 1.0,
		},
		{
			name:   "completely different",
			a:      "abc",
			b:      "xyz",
			minSim: 0.0,
			maxSim: 0.4,
		},
		{
			name:   "empty both",
			a:      "",
			b:      "",
			minSim: 1.0,
			maxSim: 1.0,
		},
		{
			name:   "one empty",
			a:      "hello",
			b:      "",
			minSim: 0.0,
			maxSim: 0.0,
		},
		{
			name:   "trimmed",
			a:      "  hello  ",
			b:      "hello",
			minSim: 1.0,
			maxSim: 1.0,
		},
		{
			name:   "partial match",
			a:      "hello world!",
			b:      "hello",
			minSim: 0.4,
			maxSim: 0.7,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stringSimilarity(tt.a, tt.b)
			if got < tt.minSim || got > tt.maxSim {
				t.Errorf("stringSimilarity(%q, %q) = %.4f, want in [%.4f, %.4f]",
					tt.a, tt.b, got, tt.minSim, tt.maxSim)
			}
		})
	}
}

// roundTo rounds to n decimal places for float comparison
func roundTo(f float64, decimals int) float64 {
	pow := math.Pow(10, float64(decimals))
	return math.Round(f*pow) / pow
}

func TestStringSimilaritySymmetry(t *testing.T) {
	// property: similarity should be symmetric
	a := "hello beautiful world"
	b := "hello world"
	s1 := stringSimilarity(a, b)
	s2 := stringSimilarity(b, a)
	if roundTo(s1, 6) != roundTo(s2, 6) {
		t.Errorf("stringSimilarity not symmetric: a->b = %.6f, b->a = %.6f", s1, s2)
	}
}

func TestLyricsLineType(t *testing.T) {
	// verify that parsed lines are of the expected protobuf type
	lines := parseLRC("[00:01.50] test line")
	if len(lines) != 1 {
		t.Fatal("expected 1 line")
	}
	if lines[0].Text != "test line" {
		t.Errorf("text = %q, want %q", lines[0].Text, "test line")
	}
	if lines[0].TimeMs != 1500 {
		t.Errorf("timeMs = %d, want %d", lines[0].TimeMs, 1500)
	}

	// check it's a proper protobuf message
	var _ *pb.LyricsLine = lines[0]
}
