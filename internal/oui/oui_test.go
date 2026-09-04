package oui

import (
	"strings"
	"testing"
)

func TestLookupLongestPrefixWins(t *testing.T) {
	m := map[string]string{
		"AABBCC":    "block-24",
		"AABBCCD":   "block-28",
		"AABBCCDDE": "block-36",
	}
	tests := []struct{ hw, want string }{
		{"aa:bb:cc:dd:e0:00", "block-36"},
		{"aa:bb:cc:d0:00:00", "block-28"},
		{"aa:bb:cc:00:00:00", "block-24"},
		{"aa-bb-cc-00-00-00", "block-24"}, // dash notation
		{"aabb.cc00.0000", "block-24"},    // cisco dot notation
		{"11:22:33:44:55:66", ""},         // unregistered
		{"not-a-mac", ""},
		{"aa:bb:cc:dd:ee", ""}, // too short
	}
	for _, tt := range tests {
		if got := lookup(m, tt.hw); got != tt.want {
			t.Errorf("lookup(%q) = %q, want %q", tt.hw, got, tt.want)
		}
	}
}

func TestRandomized(t *testing.T) {
	tests := []struct {
		hw   string
		want bool
	}{
		{"02:00:00:00:00:01", true}, // locally administered
		{"a6:5e:60:aa:bb:cc", true},
		{"da:11:22:33:44:55", true},
		{"ee:11:22:33:44:55", true},
		{"00:1b:c5:09:00:01", false}, // globally unique
		{"9c:8e:cd:35:c3:67", false}, // a real camera on this LAN
		{"garbage", false},
	}
	for _, tt := range tests {
		if got := Randomized(tt.hw); got != tt.want {
			t.Errorf("Randomized(%q) = %v, want %v", tt.hw, got, tt.want)
		}
	}
}

func TestEmbeddedTable(t *testing.T) {
	if len(table) < 50000 {
		t.Fatalf("embedded table has %d prefixes; expected the full IEEE capture (50k+)", len(table))
	}
	// 00:00:00 has belonged to Xerox since the dawn of Ethernet; if
	// this fails the table is garbage, not merely stale.
	if v := Vendor("00:00:00:11:22:33"); !strings.Contains(v, "XEROX") {
		t.Errorf("Vendor(00:00:00...) = %q, want a XEROX entry", v)
	}
	for prefix, name := range table {
		for _, r := range prefix + name {
			if r < 0x20 || r > 0x7e {
				t.Fatalf("table entry %q -> %q contains non-ASCII or control rune %#x", prefix, name, r)
			}
		}
	}
}
