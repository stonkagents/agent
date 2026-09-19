//go:build windows

package main

import (
	"testing"
)

func TestReleasesURLForTracker(t *testing.T) {
	cases := map[string]string{
		"https://tracker.dev.stonkagents.com/api/": "https://releases.dev.stonkagents.com/",
		"https://TRACKER.stg.stonkagents.com":      "https://releases.stg.stonkagents.com/",
		"https://tracker.dev.stonkagents.com":      "https://releases.dev.stonkagents.com/",
		"https://tracker.stonkagents.com":          "https://releases.stonkagents.com/",
		"http://localhost:7842":                    "",
		"https://api.example.com":                  "",
		"":                                         "",
		"::bad url":                                "",
	}
	for in, want := range cases {
		if got := releasesURLForTracker(in); got != want {
			t.Errorf("releasesURLForTracker(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveEnvironment(t *testing.T) {
	cases := []struct{ envVar, baked, tracker, want string }{
		{"", "", "https://tracker.stonkagents.com", "prd"},
		{"", "", "https://tracker.dev.stonkagents.com", "dev"},
		{"", "", "https://tracker.stg.stonkagents.com", "stg"},
		{"", "", "https://tracker.dev.stonkagents.com", "dev"},
		{"", "", "https://tracker.stonkagents.com", "prd"},
		{"", "dev", "https://tracker.stonkagents.com", "dev"},
		{"stg", "dev", "https://tracker.stonkagents.com", "stg"},
		{"bogus", "", "https://tracker.dev.stonkagents.com", "prd"},
		{"", "", "", "prd"},
	}
	for _, c := range cases {
		if got := resolveEnvironment(c.envVar, c.baked, c.tracker); got != c.want {
			t.Errorf("resolveEnvironment(%q, %q, %q) = %q, want %q", c.envVar, c.baked, c.tracker, got, c.want)
		}
	}
}
