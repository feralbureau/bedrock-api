package main

import (
	"testing"
	"time"

	pb "github.com/feralbureau/bedrock-api/bedrock"
)

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
		{
			name:     "empty map",
			body:     map[string]interface{}{},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PlainBody(tt.body)
			if got != tt.expected {
				t.Errorf("PlainBody(%v) = %q, want %q", tt.body, got, tt.expected)
			}
		})
	}
}

func TestRelativeTime(t *testing.T) {
	tests := []struct {
		name     string
		iso      string
		contains string
	}{
		{
			name:     "just now",
			iso:      time.Now().UTC().Format(time.RFC3339),
			contains: "just now",
		},
		{
			name:     "minutes ago",
			iso:      time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339),
			contains: "minutes ago",
		},
		{
			name:     "1 minute ago",
			iso:      time.Now().Add(-1 * time.Minute).UTC().Format(time.RFC3339),
			contains: "1 minute ago",
		},
		{
			name:     "hours ago",
			iso:      time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339),
			contains: "hours ago",
		},
		{
			name:     "1 hour ago",
			iso:      time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339),
			contains: "1 hour ago",
		},
		{
			name:     "days ago",
			iso:      time.Now().Add(-5 * 24 * time.Hour).UTC().Format(time.RFC3339),
			contains: "days ago",
		},
		{
			name:     "1 day ago",
			iso:      time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339),
			contains: "1 day ago",
		},
		{
			name:     "months ago",
			iso:      time.Now().Add(-90 * 24 * time.Hour).UTC().Format(time.RFC3339),
			contains: "months ago",
		},
		{
			name:     "1 month ago",
			iso:      time.Now().Add(-35 * 24 * time.Hour).UTC().Format(time.RFC3339),
			contains: "1 month ago",
		},
		{
			name:     "years ago",
			iso:      time.Now().Add(-2 * 365 * 24 * time.Hour).UTC().Format(time.RFC3339),
			contains: "years ago",
		},
		{
			name:     "1 year ago",
			iso:      time.Now().Add(-380 * 24 * time.Hour).UTC().Format(time.RFC3339),
			contains: "1 year ago",
		},
		{
			name:     "invalid iso",
			iso:      "not-a-date",
			contains: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RelativeTime(tt.iso)
			if tt.contains == "" && got != "" {
				t.Errorf("RelativeTime(%q) = %q, want empty", tt.iso, got)
			}
			if tt.contains != "" && !stringContains(got, tt.contains) {
				t.Errorf("RelativeTime(%q) = %q, want containing %q", tt.iso, got, tt.contains)
			}
		})
	}
}

func TestPickPrimaryContributor(t *testing.T) {
	// test with nil (empty list)
	got := PickPrimaryContributor(nil)
	if got != nil {
		t.Errorf("PickPrimaryContributor(nil) = %v, want nil", got)
	}
}

// TestPickPrimaryContributorEmpty tests empty author list
func TestPickPrimaryContributorEmpty(t *testing.T) {
	got := PickPrimaryContributor([]struct {
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
	}{})
	if got != nil {
		t.Errorf("PickPrimaryContributor(empty) = %v, want nil", got)
	}
}

func TestPickPrimaryContributorWithUsers(t *testing.T) {
	contributors := []struct {
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
	}{
		{
			Attribution: 0.5,
			User: &struct {
				Login                       string `json:"login"`
				URL                         string `json:"url"`
				HumanReadableRoleForDisplay string `json:"human_readable_role_for_display"`
				IQ                          int    `json:"iq"`
				Avatar                      *struct {
					Small *struct {
						URL string `json:"url"`
					} `json:"small"`
				} `json:"avatar"`
			}{
				Login: "editor",
				URL:   "https://genius.com/editor",
				HumanReadableRoleForDisplay: "Editor",
				IQ: 100,
			},
		},
		{
			Attribution: 1.0,
			User: &struct {
				Login                       string `json:"login"`
				URL                         string `json:"url"`
				HumanReadableRoleForDisplay string `json:"human_readable_role_for_display"`
				IQ                          int    `json:"iq"`
				Avatar                      *struct {
					Small *struct {
						URL string `json:"url"`
					} `json:"small"`
				} `json:"avatar"`
			}{
				Login: "top-contributor",
				URL:   "https://genius.com/top-contributor",
				HumanReadableRoleForDisplay: "Contributor",
				IQ: 500,
			},
		},
	}

	got := PickPrimaryContributor(contributors)
	if got == nil {
		t.Fatal("PickPrimaryContributor returned nil, want contributor")
	}
	if got.Login != "top-contributor" {
		t.Errorf("PickPrimaryContributor Login = %q, want %q", got.Login, "top-contributor")
	}
	if got.Iq != 500 {
		t.Errorf("PickPrimaryContributor Iq = %d, want %d", got.Iq, 500)
	}
	if got.Role != "Contributor" {
		t.Errorf("PickPrimaryContributor Role = %q, want %q", got.Role, "Contributor")
	}
}

// TestUnmarshalCheck uses the protobuf types to ensure mapping structures are reasonable
func TestGeniusAnnotationContributorType(t *testing.T) {
	// verify that the protobuf contributor type is usable
	c := &pb.AnnotationContributor{
		Login: "testuser",
		Iq:    42,
	}
	if c.GetLogin() != "testuser" {
		t.Errorf("unexpected login: %q", c.GetLogin())
	}
	if c.GetIq() != 42 {
		t.Errorf("unexpected iq: %d", c.GetIq())
	}
}

// stringContains is a helper for RelativeTime tests
func stringContains(s, substr string) bool {
	return len(s) >= len(substr) && containsStr(s, substr)
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
