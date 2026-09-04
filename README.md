# keaexporter

A dependency-free Prometheus exporter and lease-status page for
[Kea DHCP](https://www.isc.org/kea/). Kea has no native `/metrics`
endpoint in any version -- its statistics live behind the control
socket as JSON. keaexporter is a sidecar that shares Kea's runtime
directory and, on every scrape, dials the socket, runs
`statistic-get-all` + `status-get`, and translates the answers into
Prometheus text.

## What it exports

- `kea_up` -- `1` when kea-dhcp4 answered on the control socket (the
  hung-but-present liveness signal `status-get` provides).
- `kea_uptime_seconds`, `kea_time_since_reload_seconds` -- from
  `status-get`.
- Every numeric Kea statistic, translated mechanically:

      pkt4-ack-sent                  -> kea_pkt4_ack_sent
      subnet[1].assigned-addresses   -> kea_subnet_assigned_addresses{subnet="1"}
      subnet[1].pool[0].total-...    -> kea_subnet_pool_total_addresses{subnet="1",pool="0"}

  Translated series are declared `untyped`: Kea mixes counters and
  gauges in one namespace, and `rate()` works on untyped just fine.

## Lease status page

A second listener (`-status-port`, default 9548) serves a human lease
table at `/`: `lease4-get-all` over the same socket (which requires
Kea's `lease_cmds` hook). Each lease's MAC is annotated with its
IEEE-registered vendor; locally-administered (randomized) MACs are
labeled as such. `/healthz` answers without touching the socket, so a
reverse proxy's health checks never load Kea.

## Usage

    keaexporter [flags]

| Flag | Default | Meaning |
| --- | --- | --- |
| `-socket` | `/var/run/kea/kea4-ctrl-socket` | kea-dhcp4 control socket path |
| `-port` | `9547` | metrics listener port |
| `-status-port` | `9548` | lease status page port (0 disables) |
| `-timeout` | `5s` | per-scrape deadline for the socket conversation |

Point it at the same control socket kea-dhcp4 writes (share the runtime
directory into the container). The metrics port is meant to stay
private on a monitoring network; the status port can be published to
the LAN behind a reverse proxy.

## Build

Pure Go standard library -- no third-party module dependencies.

    go build ./cmd/keaexporter
    go test ./...

Container image (stdlib static binary on `scratch`):

    docker build -t keaexporter:dev .

CI publishes `ghcr.io/jeffbstewart/keaexporter` on pushes to `main` and
on version tags.

## MAC vendor data

The status page's vendor column uses the IEEE MAC-prefix registries,
vendored under `third_party/ieee-oui/` and compiled into
`internal/oui/table.txt` (embedded in the binary). The data is IEEE's
public listing -- see `third_party/ieee-oui/README.md` for its
provenance and redistribution basis. Refresh it with `go run
./cmd/ouifetch && go run ./cmd/ouigen` (a manual, occasional step; CI
never fetches from IEEE).

## Development

    sh scripts/install-hooks.sh   # wire the presubmit as the pre-commit hook
    sh scripts/presubmit.sh       # 7-bit ASCII, OUI table verify, gofmt, vet, tests

All authored text is 7-bit ASCII with LF line endings; the only
exemption is the vendored IEEE CSVs (validated as strict UTF-8 by
`cmd/ouigen -verify`).

## License

Apache License 2.0 -- see [LICENSE](LICENSE). The vendored IEEE data
under `third_party/ieee-oui/` is not covered by that license; it is
IEEE's public listing (see that directory's README).
