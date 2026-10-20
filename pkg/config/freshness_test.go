package config

import (
	"strings"
	"testing"
	"time"
)

func TestFreshnessWarnings(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }

	cases := []struct {
		name string
		item LinkedItem
		want []string // substrings expected, one per warning; nil = no warnings
	}{
		{"fresh", LinkedItem{ItemID: "item1", LastSyncedAt: ago(2 * time.Hour)}, nil},
		{"never synced", LinkedItem{ItemID: "item1"}, []string{"never been synced"}},
		{"stale sync", LinkedItem{ItemID: "item1", LastSyncedAt: ago(50 * time.Hour)}, []string{"last synced 2 days ago"}},
		{"item error", LinkedItem{ItemID: "item1", LastSyncedAt: ago(time.Hour), ItemError: "ITEM_LOGIN_REQUIRED"}, []string{"ITEM_LOGIN_REQUIRED", "login --update"}},
	}

	for _, c := range cases {
		cfg := &Config{Items: []LinkedItem{c.item}}
		got := cfg.FreshnessWarnings(now, 24*time.Hour)
		if c.want == nil {
			if len(got) != 0 {
				t.Errorf("%s: expected no warnings, got %v", c.name, got)
			}
			continue
		}
		if len(got) != 1 {
			t.Errorf("%s: expected 1 warning, got %v", c.name, got)
			continue
		}
		for _, sub := range c.want {
			if !strings.Contains(got[0], sub) {
				t.Errorf("%s: warning %q missing %q", c.name, got[0], sub)
			}
		}
	}
}

func TestHumanizeAge(t *testing.T) {
	cases := map[time.Duration]string{
		30 * time.Second: "just now",
		5 * time.Minute:  "5 minutes ago",
		1 * time.Hour:    "1 hour ago",
		3 * time.Hour:    "3 hours ago",
		26 * time.Hour:   "1 day ago",
		72 * time.Hour:   "3 days ago",
	}
	for d, want := range cases {
		if got := HumanizeAge(d); got != want {
			t.Errorf("HumanizeAge(%v) = %q, want %q", d, got, want)
		}
	}
}
