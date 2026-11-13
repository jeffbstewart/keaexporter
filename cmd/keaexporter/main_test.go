package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	page, err := renderStatus(ls, now, "")
	if err != nil {
		t.Fatalf("renderStatus: %v", err)
	}
	return string(page)
}

func TestRenderStatusSortsAndEscapes(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	ls := []lease{
		// Deliberately out of order, with a numeric-vs-lexical trap
		// (.2.9 sorts after .2.100 lexically) and a hostname that
		// must not survive as markup.
		{IPAddress: "192.0.2.100", HWAddress: "aa:aa:aa:aa:aa:01", Hostname: "later", CLTT: now.Unix(), ValidLft: 7200, State: 0},
		{IPAddress: "192.0.2.9", HWAddress: "aa:aa:aa:aa:aa:02", Hostname: "<script>x</script>", CLTT: now.Unix(), ValidLft: 7200, State: 0},
		{IPAddress: "192.0.2.50", HWAddress: "aa:aa:aa:aa:aa:03", Hostname: "gone", CLTT: now.Add(-3 * time.Hour).Unix(), ValidLft: 7200, State: 2},
	}
	page := mustRender(t, ls, now)

	i9 := strings.Index(page, "192.0.2.9<")
	i50 := strings.Index(page, "192.0.2.50<")
	i100 := strings.Index(page, "192.0.2.100<")
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
		{IPAddress: "192.0.2.1", HWAddress: "00:00:00:12:34:56", CLTT: now.Unix(), ValidLft: 7200},
		{IPAddress: "192.0.2.2", HWAddress: "02:12:34:56:78:9a", CLTT: now.Unix(), ValidLft: 7200},
		{IPAddress: "192.0.2.3", HWAddress: "08:ff:ff:00:00:01", CLTT: now.Unix(), ValidLft: 7200},
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

func TestParseHANonHA(t *testing.T) {
	// A single-instance server has no high-availability block -> optional
	// HA support stays inert.
	if _, ok := parseHA(map[string]json.RawMessage{"pid": mustRaw(t, "123")}); ok {
		t.Error("parseHA reported HA present with no high-availability block")
	}
}

func TestParseHAHotStandby(t *testing.T) {
	// Verbatim shape from status-get on kea 2.6.5 (a primary whose
	// partner is unreachable -> waiting, empty scopes).
	status := map[string]json.RawMessage{
		"high-availability": mustRaw(t, `[{
			"ha-mode": "hot-standby",
			"ha-servers": {
				"local":  {"role": "primary", "scopes": [], "server-name": "kea-primary", "state": "waiting"},
				"remote": {"in-touch": false, "last-state": "", "role": "standby", "server-name": "kea-standby"}
			}
		}]`),
	}
	h, ok := parseHA(status)
	if !ok {
		t.Fatal("parseHA did not find the HA block")
	}
	if h.LocalName != "kea-primary" || h.LocalRole != "primary" || h.LocalState != "waiting" {
		t.Errorf("local parsed wrong: %+v", h)
	}
	if h.RemoteName != "kea-standby" || h.InTouch {
		t.Errorf("remote parsed wrong: %+v", h)
	}
	if h.Serving {
		t.Error("empty scopes must not be marked serving")
	}
	if h.healthy() || h.ready() {
		t.Error("waiting must be neither healthy nor ready")
	}
	if h.banner() == "" {
		t.Error("waiting should produce a banner")
	}
}

func TestHAStatePredicates(t *testing.T) {
	tests := []struct {
		h              haState
		healthy, ready bool
		bannerHas      string // substring the banner must contain ("" = no banner)
	}{
		{haState{LocalState: "hot-standby", InTouch: true}, true, true, ""},
		{haState{LocalName: "kea-standby", RemoteName: "kea-primary", LocalRole: "standby", LocalState: "partner-down", Serving: true}, false, true, "SERVING FROM BACKUP"},
		{haState{LocalName: "kea-primary", RemoteName: "kea-standby", LocalRole: "primary", LocalState: "partner-down", Serving: true}, false, true, "PARTNER DOWN"},
		{haState{LocalName: "kea-primary", RemoteName: "kea-standby", LocalState: "syncing"}, false, false, "SYNCING"},
		{haState{LocalName: "kea-primary", RemoteName: "kea-standby", LocalState: "hot-standby", InTouch: false}, false, true, "PARTNER UNREACHABLE"},
	}
	for i, tt := range tests {
		if got := tt.h.healthy(); got != tt.healthy {
			t.Errorf("case %d healthy()=%v want %v", i, got, tt.healthy)
		}
		if got := tt.h.ready(); got != tt.ready {
			t.Errorf("case %d ready()=%v want %v", i, got, tt.ready)
		}
		b := tt.h.banner()
		if tt.bannerHas == "" && b != "" {
			t.Errorf("case %d expected no banner, got %q", i, b)
		}
		if tt.bannerHas != "" && !strings.Contains(b, tt.bannerHas) {
			t.Errorf("case %d banner %q lacks %q", i, b, tt.bannerHas)
		}
	}
}

func TestRenderStatusBanner(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	page, err := renderStatus(nil, now, "SERVING FROM BACKUP: kea-standby")
	if err != nil {
		t.Fatalf("renderStatus: %v", err)
	}
	// Match the element, not the always-present .ha-banner CSS rule.
	if s := string(page); !strings.Contains(s, `class="ha-banner"`) || !strings.Contains(s, "SERVING FROM BACKUP: kea-standby") {
		t.Errorf("banner not rendered:\n%s", s)
	}
	page2, _ := renderStatus(nil, now, "")
	if strings.Contains(string(page2), `class="ha-banner"`) {
		t.Errorf("empty banner should not render the element:\n%s", string(page2))
	}
}

func TestLeaseCounts(t *testing.T) {
	ls := []lease{{State: 0}, {State: 0}, {State: 1}, {State: 2}, {State: 2}, {State: 2}, {State: 7}}
	got := leaseCounts(ls)
	want := map[string]int{"active": 2, "declined": 1, "expired-reclaimed": 3, "other": 1}
	if len(got) != len(want) {
		t.Fatalf("leaseCounts = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("leaseCounts[%q] = %d, want %d", k, got[k], v)
		}
	}
}

// Every named state is written even at zero, so two servers' series
// always line up; "other" appears only when a lease has such a state.
func TestWriteLeaseCounts(t *testing.T) {
	b := &strings.Builder{}
	writeLeaseCounts(b, nil)
	for _, want := range []string{
		"# TYPE kea_leases gauge\n",
		`kea_leases{state="active"} 0` + "\n",
		`kea_leases{state="declined"} 0` + "\n",
		`kea_leases{state="expired-reclaimed"} 0` + "\n",
	} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("empty table: output lacks %q:\n%s", want, b.String())
		}
	}
	if strings.Contains(b.String(), `state="other"`) {
		t.Errorf("empty table wrote an \"other\" series:\n%s", b.String())
	}

	b.Reset()
	writeLeaseCounts(b, []lease{{State: 0}, {State: 0}, {State: 9}})
	for _, want := range []string{`kea_leases{state="active"} 2` + "\n", `kea_leases{state="other"} 1` + "\n"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, b.String())
		}
	}
}

// fakeKea answers each control command with a canned JSON response
// on a unix socket, one command per connection as kea does.
func fakeKea(t *testing.T, answers map[string]string) *exporter {
	t.Helper()
	dir, err := os.MkdirTemp("", "kea")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Skipf("unix sockets unavailable here: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			var req struct {
				Command string `json:"command"`
			}
			if json.NewDecoder(conn).Decode(&req) == nil {
				ans, ok := answers[req.Command]
				if !ok {
					ans = `{"result":1,"text":"unexpected command"}`
				}
				io.WriteString(conn, ans)
			}
			conn.Close()
		}
	}()
	return &exporter{socket: path, timeout: 5 * time.Second}
}

func scrape(t *testing.T, e *exporter) string {
	t.Helper()
	rec := httptest.NewRecorder()
	e.metrics(rec, httptest.NewRequest("GET", "/metrics", nil))
	return rec.Body.String()
}

const (
	okStats  = `{"result":0,"arguments":{"pkt4-ack-sent":[[3,"2026-01-01 00:00:00.000000"]]}}`
	okStatus = `{"result":0,"arguments":{"uptime":10,"reload":10}}`
)

func TestMetricsLeaseCounts(t *testing.T) {
	e := fakeKea(t, map[string]string{
		"statistic-get-all": okStats,
		"status-get":        okStatus,
		"lease4-get-all":    `{"result":0,"arguments":{"leases":[{"ip-address":"192.0.2.10","state":0},{"ip-address":"192.0.2.11","state":0},{"ip-address":"192.0.2.12","state":1}]}}`,
	})
	out := scrape(t, e)
	for _, want := range []string{"kea_up 1\n", `kea_leases{state="active"} 2` + "\n", `kea_leases{state="declined"} 1` + "\n", "kea_pkt4_ack_sent 3\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("scrape lacks %q:\n%s", want, out)
		}
	}
}

// Result 3 is kea's "no leases" answer: a real, empty table, which is
// exactly the number a standby that failed to sync must report.
func TestMetricsEmptyLeaseTable(t *testing.T) {
	e := fakeKea(t, map[string]string{
		"statistic-get-all": okStats,
		"status-get":        okStatus,
		"lease4-get-all":    `{"result":3,"text":"0 IPv4 lease(s) found."}`,
	})
	if out := scrape(t, e); !strings.Contains(out, `kea_leases{state="active"} 0`+"\n") {
		t.Errorf("an empty table must still report zero:\n%s", out)
	}
}

// Without the lease_cmds hook the count is absent, not zero -- a zero
// would read as an empty table -- and the server is still up.
func TestMetricsWithoutLeaseCmds(t *testing.T) {
	e := fakeKea(t, map[string]string{
		"statistic-get-all": okStats,
		"status-get":        okStatus,
		"lease4-get-all":    `{"result":2,"text":"'lease4-get-all' command not supported."}`,
	})
	out := scrape(t, e)
	if strings.Contains(out, "kea_leases") {
		t.Errorf("no lease_cmds hook, but kea_leases was written:\n%s", out)
	}
	if !strings.Contains(out, "kea_up 1\n") {
		t.Errorf("a missing hook must not read as kea down:\n%s", out)
	}
}
