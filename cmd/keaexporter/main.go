// Command keaexporter bridges Kea's control channel to Prometheus.
// Kea has no native /metrics in any version: its statistics live
// behind the control socket as JSON. This sidecar shares the
// kea-runtime dir, and on every /metrics scrape dials the socket, runs
// statistic-get-all + status-get, and translates the answers
// mechanically:
//
//	pkt4-ack-sent                  -> kea_pkt4_ack_sent
//	subnet[1].assigned-addresses   -> kea_subnet_assigned_addresses{subnet="1"}
//	subnet[1].pool[0].total-...    -> kea_subnet_pool_total_addresses{subnet="1",pool="0"}
//
// plus kea_up (did the socket answer -- the hung-but-present liveness
// signal status-get provides) and kea_uptime_seconds /
// kea_time_since_reload_seconds from status-get. All translated
// series are declared untyped: Kea mixes counters and gauges in one
// namespace and rate() works on untyped just fine. Zero dependencies,
// stdlib only, FROM scratch (see Dockerfile).
//
// A SECOND listener (-status-port, default 9548) serves a human lease
// table at "/": lease4-get-all over the same socket, which works only
// when kea loads the lease_cmds hook. The two listeners are separate on
// purpose: the metrics port (default 9547) stays private on the
// monitoring network, while the status port can be published to the LAN
// behind a reverse proxy. /healthz on the status listener answers
// without touching the socket, so a proxy's health checks never load
// kea.
//
// OPTIONAL HA support (auto-detected). When kea loads the ha hook,
// status-get carries a high-availability block; keaexporter then also
// exports kea_ha_healthy / kea_ha_serving / kea_ha_partner_in_touch and
// kea_ha_local_state{role,state}, shows a banner on the status page when
// the pair is unhealthy (serving from backup, partner-down, syncing),
// and answers /ready (on the metrics port) with 200 only when the local
// server is serving-or-synced -- a k8s readiness gate that makes a
// rolling restart wait for HA sync before it takes the partner down. A
// single-instance server has no HA block, so all of this stays inert;
// nothing here is specific to any deployment.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jeffbstewart/keaexporter/internal/oui"
)

func main() {
	fs := flag.NewFlagSet("keaexporter", flag.ContinueOnError)
	socket := fs.String("socket", "/var/run/kea/kea4-ctrl-socket", "kea-dhcp4 control socket path")
	port := fs.Int("port", 9547, "metrics listener port")
	statusPort := fs.Int("status-port", 9548, "lease status page port (0 disables)")
	timeout := fs.Duration("timeout", 5*time.Second, "per-scrape deadline for the socket conversation")
	if err := fs.Parse(os.Args[1:]); err != nil {
		if err == flag.ErrHelp {
			os.Exit(0)
		}
		os.Exit(2) // flag already printed the message and usage
	}

	e := &exporter{socket: *socket, timeout: *timeout}
	http.HandleFunc("/metrics", e.metrics)
	http.HandleFunc("/ready", e.ready)
	http.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "keaexporter -- see /metrics")
	})
	if *statusPort != 0 {
		sm := http.NewServeMux()
		sm.HandleFunc("/", e.status)
		sm.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprintln(w, "ok")
		})
		go func() {
			log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", *statusPort), sm))
		}()
	}
	log.Printf("keaexporter: translating %s, metrics :%d, status :%d", *socket, *port, *statusPort)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", *port), nil))
}

type exporter struct {
	socket  string
	timeout time.Duration
}

type response struct {
	Result    int                        `json:"result"`
	Text      string                     `json:"text"`
	Arguments map[string]json.RawMessage `json:"arguments"`
}

// exchange runs one control-channel command and returns kea's whole
// response. Each command uses a fresh connection: the protocol is a
// single JSON value each way, and reconnecting per command sidesteps
// any framing ambiguity.
func (e *exporter) exchange(name string) (response, error) {
	var resp response
	conn, err := net.DialTimeout("unix", e.socket, e.timeout)
	if err != nil {
		return resp, err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(e.timeout)); err != nil {
		return resp, err
	}
	if err := json.NewEncoder(conn).Encode(map[string]string{"command": name}); err != nil {
		return resp, fmt.Errorf("send %s: %w", name, err)
	}
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return resp, fmt.Errorf("read %s: %w", name, err)
	}
	return resp, nil
}

// command is exchange for callers that need arguments from a
// must-succeed command.
func (e *exporter) command(name string) (map[string]json.RawMessage, error) {
	resp, err := e.exchange(name)
	if err != nil {
		return nil, err
	}
	if resp.Result != 0 {
		return nil, fmt.Errorf("%s: result %d %q", name, resp.Result, resp.Text)
	}
	return resp.Arguments, nil
}

// lease is the slice of kea's lease4 object the status page shows.
type lease struct {
	IPAddress string `json:"ip-address"`
	HWAddress string `json:"hw-address"`
	Hostname  string `json:"hostname"`
	CLTT      int64  `json:"cltt"`
	ValidLft  int64  `json:"valid-lft"`
	State     int    `json:"state"`
}

// leases runs lease4-get-all (registered by the lease_cmds hook). Kea
// answers an empty pool with result 3 ("empty"), not 0 -- that is a
// valid no-leases answer, not an error.
func (e *exporter) leases() ([]lease, error) {
	resp, err := e.exchange("lease4-get-all")
	if err != nil {
		return nil, err
	}
	switch resp.Result {
	case 0:
	case 3:
		return nil, nil
	default:
		return nil, fmt.Errorf("lease4-get-all: result %d %q", resp.Result, resp.Text)
	}
	var out []lease
	if err := json.Unmarshal(resp.Arguments["leases"], &out); err != nil {
		return nil, fmt.Errorf("lease4-get-all: %w", err)
	}
	return out, nil
}

// haState is the local view of Kea's High Availability relationship,
// present ONLY when the ha hook is loaded -- a single-instance server
// has none, and everything below is skipped (HA support is optional).
// Every field comes straight from status-get; nothing here is specific
// to any deployment.
type haState struct {
	Mode        string // ha-mode, e.g. "hot-standby" or "load-balancing"
	LocalName   string
	LocalRole   string // primary | standby | secondary | backup
	LocalState  string // hot-standby | partner-down | waiting | syncing | ...
	Serving     bool   // this server is answering clients (non-empty scopes)
	RemoteName  string
	RemoteState string
	InTouch     bool // this server can reach its partner
}

// parseHA pulls the local HA view out of a status-get Arguments map.
// ok is false when there is no high-availability block (no ha hook),
// which is the normal single-instance case.
func parseHA(status map[string]json.RawMessage) (haState, bool) {
	raw, ok := status["high-availability"]
	if !ok {
		return haState{}, false
	}
	// high-availability is an array (one per relationship); a hot-standby
	// or load-balancing pair has exactly one.
	var rels []struct {
		HAMode    string `json:"ha-mode"`
		HAServers struct {
			Local struct {
				ServerName string   `json:"server-name"`
				Role       string   `json:"role"`
				State      string   `json:"state"`
				Scopes     []string `json:"scopes"`
			} `json:"local"`
			Remote struct {
				ServerName string `json:"server-name"`
				LastState  string `json:"last-state"`
				InTouch    bool   `json:"in-touch"`
			} `json:"remote"`
		} `json:"ha-servers"`
	}
	if err := json.Unmarshal(raw, &rels); err != nil || len(rels) == 0 {
		return haState{}, false
	}
	r := rels[0]
	return haState{
		Mode:        r.HAMode,
		LocalName:   r.HAServers.Local.ServerName,
		LocalRole:   r.HAServers.Local.Role,
		LocalState:  r.HAServers.Local.State,
		Serving:     len(r.HAServers.Local.Scopes) > 0,
		RemoteName:  r.HAServers.Remote.ServerName,
		RemoteState: r.HAServers.Remote.LastState,
		InTouch:     r.HAServers.Remote.InTouch,
	}, true
}

// healthy is the clean paired steady state: in touch with the partner
// and in the mode's normal serving state.
func (h haState) healthy() bool {
	if !h.InTouch {
		return false
	}
	switch h.LocalState {
	case "hot-standby", "load-balancing":
		return true
	}
	return false
}

// ready reports whether the local server is up and serving-or-synced --
// the k8s readiness signal, so a rolling restart waits for HA sync
// before it takes the partner down. Transient startup states
// (waiting/syncing) are deliberately NOT ready.
func (h haState) ready() bool {
	switch h.LocalState {
	case "hot-standby", "load-balancing", "partner-down",
		"communication-recovery", "partner-in-maintenance":
		return true
	}
	return false
}

// banner is a short human alert for the status page; empty when healthy.
func (h haState) banner() string {
	switch {
	case h.LocalState == "partner-down" && h.LocalRole == "standby":
		return "SERVING FROM BACKUP: " + h.LocalName + " (standby) has taken over -- partner " + h.RemoteName + " is down"
	case h.LocalState == "partner-down":
		return "PARTNER DOWN: " + h.LocalName + " is serving alone, no redundancy (partner " + h.RemoteName + ")"
	case h.LocalState == "waiting" || h.LocalState == "syncing":
		return "SYNCING: " + h.LocalName + " (" + h.LocalState + ") is pairing with " + h.RemoteName
	case !h.InTouch:
		return "PARTNER UNREACHABLE: " + h.LocalName + " is not in touch with " + h.RemoteName
	case !h.healthy():
		return "HA " + h.LocalState + ": " + h.LocalName + " (" + h.LocalRole + ")"
	}
	return ""
}

// ready is the k8s readiness endpoint. With HA it returns 200 only when
// the local server is serving/synced (so a rollout gates on HA sync);
// without HA it returns 200 when the control socket answers. 503
// otherwise. Served on the metrics listener, which is always up.
func (e *exporter) ready(w http.ResponseWriter, _ *http.Request) {
	status, err := e.command("status-get")
	if err != nil {
		http.Error(w, "kea did not answer: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	if h, ok := parseHA(status); ok && !h.ready() {
		http.Error(w, "HA not ready: "+h.LocalState, http.StatusServiceUnavailable)
		return
	}
	fmt.Fprintln(w, "ready")
}

// statKeyRe splits "subnet[1].pool[0].assigned-addresses" into its
// optional subnet id, optional pool id, and the bare statistic name.
var statKeyRe = regexp.MustCompile(`^(?:subnet\[(\d+)\]\.)?(?:pool\[(\d+)\]\.)?([^\[\]]+)$`)

type series struct {
	labels string // rendered {...} block, may be empty
	value  float64
}

// translate turns statistic-get-all arguments into metric families.
// Each statistic's value is a list of [value, timestamp] samples,
// newest first; only the newest matters here.
func translate(stats map[string]json.RawMessage) map[string][]series {
	families := map[string][]series{}
	for key, raw := range stats {
		m := statKeyRe.FindStringSubmatch(key)
		if m == nil {
			continue
		}
		// Samples are [value, "timestamp"] pairs -- mixed types, so
		// decode elements lazily and only commit to float64 for the
		// newest sample's value. Non-numeric statistics are skipped.
		var samples [][]json.RawMessage
		if err := json.Unmarshal(raw, &samples); err != nil || len(samples) == 0 || len(samples[0]) == 0 {
			continue
		}
		var v float64
		if err := json.Unmarshal(samples[0][0], &v); err != nil {
			continue
		}
		name := "kea_" + metricName(m[3])
		var labels []string
		if m[1] != "" {
			labels = append(labels, fmt.Sprintf("subnet=%q", m[1]))
		}
		if m[2] != "" {
			name = strings.Replace(name, "kea_", "kea_subnet_pool_", 1)
			labels = append(labels, fmt.Sprintf("pool=%q", m[2]))
		} else if m[1] != "" {
			name = strings.Replace(name, "kea_", "kea_subnet_", 1)
		}
		var lb string
		if len(labels) > 0 {
			lb = "{" + strings.Join(labels, ",") + "}"
		}
		families[name] = append(families[name], series{labels: lb, value: v})
	}
	return families
}

var unsafeChars = regexp.MustCompile(`[^a-zA-Z0-9_]`)

func metricName(stat string) string {
	return unsafeChars.ReplaceAllString(stat, "_")
}

func (e *exporter) metrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	b := &strings.Builder{}

	stats, statsErr := e.command("statistic-get-all")
	status, statusErr := e.command("status-get")

	up := 0
	if statsErr == nil && statusErr == nil {
		up = 1
	}
	if statsErr != nil {
		log.Printf("statistic-get-all: %v", statsErr)
	}
	if statusErr != nil {
		log.Printf("status-get: %v", statusErr)
	}
	fmt.Fprintf(b, "# HELP kea_up 1 when kea-dhcp4 answered on the control socket.\n")
	fmt.Fprintf(b, "# TYPE kea_up gauge\n")
	fmt.Fprintf(b, "kea_up %d\n", up)

	if statusErr == nil {
		for stat, metric := range map[string]string{
			"uptime": "kea_uptime_seconds",
			"reload": "kea_time_since_reload_seconds",
		} {
			var v float64
			if raw, ok := status[stat]; ok && json.Unmarshal(raw, &v) == nil {
				fmt.Fprintf(b, "# TYPE %s gauge\n%s %g\n", metric, metric, v)
			}
		}
	}

	// HA metrics, only when the ha hook is loaded (optional -- a
	// single-instance server has no high-availability block).
	if statusErr == nil {
		if h, ok := parseHA(status); ok {
			b2i := func(x bool) int {
				if x {
					return 1
				}
				return 0
			}
			fmt.Fprintf(b, "# HELP kea_ha_healthy 1 when the local HA server is in a clean paired steady state.\n")
			fmt.Fprintf(b, "# TYPE kea_ha_healthy gauge\nkea_ha_healthy %d\n", b2i(h.healthy()))
			fmt.Fprintf(b, "# HELP kea_ha_serving 1 when this server is currently answering DHCP clients.\n")
			fmt.Fprintf(b, "# TYPE kea_ha_serving gauge\nkea_ha_serving %d\n", b2i(h.Serving))
			fmt.Fprintf(b, "# HELP kea_ha_partner_in_touch 1 when the local server can reach its HA partner.\n")
			fmt.Fprintf(b, "# TYPE kea_ha_partner_in_touch gauge\nkea_ha_partner_in_touch %d\n", b2i(h.InTouch))
			fmt.Fprintf(b, "# HELP kea_ha_local_state 1 for the current local HA role and state.\n")
			fmt.Fprintf(b, "# TYPE kea_ha_local_state gauge\nkea_ha_local_state{role=%q,state=%q} 1\n", h.LocalRole, h.LocalState)
		}
	}

	if statsErr == nil {
		families := translate(stats)
		names := make([]string, 0, len(families))
		for name := range families {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			fmt.Fprintf(b, "# TYPE %s untyped\n", name)
			ss := families[name]
			sort.Slice(ss, func(i, j int) bool { return ss[i].labels < ss[j].labels })
			for _, s := range ss {
				fmt.Fprintf(b, "%s%s %g\n", name, s.labels, s.value)
			}
		}
	}

	io.WriteString(w, b.String())
}

var stateNames = map[int]string{0: "active", 1: "declined", 2: "expired-reclaimed"}

// status serves the human lease table. Every request dials kea fresh --
// same cost profile as a /metrics scrape, and LAN-only traffic at human
// click rates needs no cache.
func (e *exporter) status(w http.ResponseWriter, _ *http.Request) {
	ls, err := e.leases()
	if err != nil {
		log.Printf("lease4-get-all: %v", err)
		http.Error(w, "kea did not answer: "+err.Error(), http.StatusBadGateway)
		return
	}
	// A banner when HA is in a non-healthy state (serving from backup,
	// partner down, syncing). Best-effort: a status-get failure or a
	// non-HA server just leaves it empty.
	banner := ""
	if status, serr := e.command("status-get"); serr == nil {
		if h, ok := parseHA(status); ok {
			banner = h.banner()
		}
	}
	page, err := renderStatus(ls, time.Now(), banner)
	if err != nil {
		log.Printf("render status: %v", err)
		http.Error(w, "render: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(page)
}

// statusTmpl escapes everything contextually (html/template), which is
// the load-bearing property: hostnames are client-controlled bytes.
var statusTmpl = template.Must(template.New("status").Parse(`<!doctype html>
<title>kea leases</title>
<meta http-equiv="refresh" content="30">
<style>body{font-family:monospace}table{border-collapse:collapse}th,td{padding:2px 12px;text-align:left;border-bottom:1px solid #ccc}.ha-banner{background:#c0392b;color:#fff;padding:8px 12px;margin:0 0 12px;font-weight:bold}</style>
{{if .Banner}}<p class="ha-banner">{{.Banner}}</p>{{end}}
<h1>{{.Active}} active / {{.Total}} leases</h1>
<p>as of {{.Now}}</p>
<table>
<tr><th>ip</th><th>hostname</th><th>mac</th><th>vendor</th><th>expires</th><th>state</th></tr>
{{range .Rows}}<tr><td>{{.IP}}</td><td>{{.Hostname}}</td><td>{{.MAC}}</td><td>{{.Vendor}}</td><td>{{.Expires}}</td><td>{{.State}}</td></tr>
{{end}}</table>
`))

type statusRow struct {
	IP, Hostname, MAC, Vendor, Expires, State string
}

type statusPage struct {
	Active, Total int
	Now           string
	Banner        string
	Rows          []statusRow
}

// renderStatus is the whole page: one row per lease, IP-sorted. banner
// is a non-empty HA alert string to show above the table, or "".
func renderStatus(ls []lease, now time.Time, banner string) ([]byte, error) {
	sort.Slice(ls, func(i, j int) bool {
		a, aerr := netip.ParseAddr(ls[i].IPAddress)
		b, berr := netip.ParseAddr(ls[j].IPAddress)
		if aerr != nil || berr != nil {
			return ls[i].IPAddress < ls[j].IPAddress
		}
		return a.Less(b)
	})
	page := statusPage{
		Total:  len(ls),
		Now:    now.Format("2006-01-02 15:04:05 MST"),
		Banner: banner,
	}
	for _, l := range ls {
		if l.State == 0 {
			page.Active++
		}
		expiry := time.Unix(l.CLTT+l.ValidLft, 0)
		left := expiry.Sub(now).Round(time.Second)
		expires := fmt.Sprintf("%s (in %s)", expiry.Format("15:04:05"), left)
		if left < 0 {
			expires = fmt.Sprintf("%s (expired)", expiry.Format("15:04:05"))
		}
		state := stateNames[l.State]
		if state == "" {
			state = fmt.Sprintf("state %d", l.State)
		}
		// IEEE lookup (internal/oui). A locally-administered address is
		// its own answer: modern phones/laptops randomize, so "no
		// vendor" there is signal, not a lookup miss.
		vendor := oui.Vendor(l.HWAddress)
		if oui.Randomized(l.HWAddress) {
			vendor = "(randomized)"
		}
		page.Rows = append(page.Rows, statusRow{
			IP:       l.IPAddress,
			Hostname: l.Hostname,
			MAC:      l.HWAddress,
			Vendor:   vendor,
			Expires:  expires,
			State:    state,
		})
	}
	b := &bytes.Buffer{}
	if err := statusTmpl.Execute(b, page); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
