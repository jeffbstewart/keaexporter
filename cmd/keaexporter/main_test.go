package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func mustRaw(t *testing.T, v string) json.RawMessage {
	t.Helper()
	if !json.Valid([]byte(v)) {
		t.Fatalf("invalid test JSON: %s", v)
	}
	return json.RawMessage(v)
}

func TestTranslate(t *testing.T) {
	stats := map[string]json.RawMessage{
		"pkt4-ack-sent":                           mustRaw(t, `[[42, "2026-08-09 10:00:00.000000"]]`),
		"subnet[1].assigned-addresses":            mustRaw(t, `[[17, "2026-08-09 10:00:00.000000"], [16, "2026-08-09 09:59:00.000000"]]`),
		"subnet[1].total-addresses":               mustRaw(t, `[[254, "2026-08-09 10:00:00.000000"]]`),
		"subnet[1].pool[0].total-addresses":       mustRaw(t, `[[254, "2026-08-09 10:00:00.000000"]]`),
		"subnet[1].cumulative-assigned-addresses": mustRaw(t, `[[99, "2026-08-09 10:00:00.000000"]]`),
	}
	fams := translate(stats)

	tests := []struct {
		family string
		labels string
		want   float64
	}{
		{"kea_pkt4_ack_sent", "", 42},
		{"kea_subnet_assigned_addresses", `{subnet="1"}`, 17}, // newest sample wins
		{"kea_subnet_total_addresses", `{subnet="1"}`, 254},
		{"kea_subnet_pool_total_addresses", `{subnet="1",pool="0"}`, 254},
		{"kea_subnet_cumulative_assigned_addresses", `{subnet="1"}`, 99},
	}
	for _, tt := range tests {
		ss, ok := fams[tt.family]
		if !ok {
			t.Errorf("missing family %s (have %v)", tt.family, keys(fams))
			continue
		}
		found := false
		for _, s := range ss {
			if s.labels == tt.labels {
				found = true
				if s.value != tt.want {
					t.Errorf("%s%s = %g, want %g", tt.family, tt.labels, s.value, tt.want)
				}
			}
		}
		if !found {
			t.Errorf("%s: no series with labels %q (have %v)", tt.family, tt.labels, ss)
		}
	}
}

func TestTranslateSkipsNonNumeric(t *testing.T) {
	stats := map[string]json.RawMessage{
		"weird-string-stat": mustRaw(t, `[["hello", "2026-08-09 10:00:00.000000"]]`),
		"empty-stat":        mustRaw(t, `[]`),
	}
	if fams := translate(stats); len(fams) != 0 {
		t.Errorf("expected non-numeric stats to be skipped, got %v", keys(fams))
	}
}

func keys(m map[string][]series) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func mustRender(t *testing.T, ls []lease, now time.Time) string {
	t.Helper()
	page, err := renderStatus(ls, now)
	if err != nil {
		t.Fatalf("renderStatus: %v", err)
	}
	return string(page)
}

func TestRenderStatusSortsAndEscapes(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	ls := []lease{
		// Deliberately out of order, with a numeric-vs-lexical trap
		// (.20.9 sorts after .20.100 lexically) and a hostname that
		// must not survive as markup.
		{IPAddress: "172.16.20.100", HWAddress: "aa:aa:aa:aa:aa:01", Hostname: "later", CLTT: now.Unix(), ValidLft: 7200, State: 0},
		{IPAddress: "172.16.20.9", HWAddress: "aa:aa:aa:aa:aa:02", Hostname: "<script>x</script>", CLTT: now.Unix(), ValidLft: 7200, State: 0},
		{IPAddress: "172.16.20.50", HWAddress: "aa:aa:aa:aa:aa:03", Hostname: "gone", CLTT: now.Add(-3 * time.Hour).Unix(), ValidLft: 7200, State: 2},
	}
	page := mustRender(t, ls, now)

	i9 := strings.Index(page, "172.16.20.9<")
	i50 := strings.Index(page, "172.16.20.50<")
	i100 := strings.Index(page, "172.16.20.100<")
	if i9 == -1 || i50 == -1 || i100 == -1 || !(i9 < i50 && i50 < i100) {
		t.Errorf("rows not in numeric IP order (indexes %d %d %d):\n%s", i9, i50, i100, page)
	}
	if strings.Contains(page, "<script>") {
		t.Errorf("hostname markup not escaped:\n%s", page)
	}
	if !strings.Contains(page, "2 active / 3 leases") {
		t.Errorf("wrong counts (want 2 active / 3 leases):\n%s", page)
	}
	if !strings.Contains(page, "(expired)") {
		t.Errorf("expired lease not marked expired:\n%s", page)
	}
	if !strings.Contains(page, "expired-reclaimed") {
		t.Errorf("state 2 not named expired-reclaimed:\n%s", page)
	}
}

func TestRenderStatusVendorColumn(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	ls := []lease{
		// 00:00:00 is Xerox's for all time; 02:... has the locally-
		// administered bit (randomized); 08:ff:ff is globally-unique
		// form but absent from the registry (verified at fixture time).
		{IPAddress: "172.16.20.1", HWAddress: "00:00:00:12:34:56", CLTT: now.Unix(), ValidLft: 7200},
		{IPAddress: "172.16.20.2", HWAddress: "02:12:34:56:78:9a", CLTT: now.Unix(), ValidLft: 7200},
		{IPAddress: "172.16.20.3", HWAddress: "08:ff:ff:00:00:01", CLTT: now.Unix(), ValidLft: 7200},
	}
	page := mustRender(t, ls, now)
	if !strings.Contains(page, "XEROX") {
		t.Errorf("registered MAC did not resolve to XEROX:\n%s", page)
	}
	if !strings.Contains(page, "(randomized)") {
		t.Errorf("locally-administered MAC not labeled (randomized):\n%s", page)
	}
	if !strings.Contains(page, "<td>08:ff:ff:00:00:01</td><td></td>") {
		t.Errorf("unregistered MAC should have an empty vendor cell:\n%s", page)
	}
}

func TestRenderStatusEmpty(t *testing.T) {
	page := mustRender(t, nil, time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC))
	if !strings.Contains(page, "0 active / 0 leases") {
		t.Errorf("empty pool should render zero counts:\n%s", page)
	}
}
