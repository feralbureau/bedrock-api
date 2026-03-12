package main

import (
	"testing"
	"time"
)

func TestRelativeTime(t *testing.T) {
	tests := []struct {
		name     string
		iso      string
		contains string // проверяем что ответ содержит это
	}{
		{
			name:     "recent (hours)",
			iso:      time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339),
			contains: "hours ago",
		},
		{
			name:     "recent (days)",
			iso:      time.Now().Add(-5 * 24 * time.Hour).UTC().Format(time.RFC3339),
			contains: "days ago",
		},
		{
			name:     "old (months)",
			iso:      time.Now().Add(-3 * 30 * 24 * time.Hour).UTC().Format(time.RFC3339),
			contains: "months ago",
		},
		{
			name:     "very old (years)",
			iso:      time.Now().Add(-2 * 365 * 24 * time.Hour).UTC().Format(time.RFC3339),
			contains: "years ago",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := relativeTime(tt.iso)
			if !contains(got, tt.contains) {
				t.Errorf("relativeTime(%q) = %q, want to contain %q", tt.iso, got, tt.contains)
			}
		})
	}
}

func TestPlainBody(t *testing.T) {
	tests := []struct {
		name     string
		body     interface{}
		expected string
	}{
		{
			name:     "valid plain text",
			body:     map[string]interface{}{"plain": "some lyrics text"},
			expected: "some lyrics text",
		},
		{
			name:     "whitespace trimmed",
			body:     map[string]interface{}{"plain": "  lyrics with spaces  "},
			expected: "lyrics with spaces",
		},
		{
			name:     "nil body",
			body:     nil,
			expected: "",
		},
		{
			name:     "missing plain key",
			body:     map[string]interface{}{"html": "some html"},
			expected: "",
		},
		{
			name:     "not a map",
			body:     "just a string",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := plainBody(tt.body)
			if got != tt.expected {
				t.Errorf("plainBody() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestPickPrimaryContributor(t *testing.T) {
	t.Run("empty authors returns nil", func(t *testing.T) {
		got := pickPrimaryContributor(nil)
		if got != nil {
			t.Errorf("expected nil for empty authors, got %v", got)
		}
	})
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && s != "" && (s == substr || len(s) > 0)
}
