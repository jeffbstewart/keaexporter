# keaexporter -- stdlib-only static binary on scratch. No CA roots
# needed: the only I/O is the kea control socket and the plaintext
# metrics/status listeners.
#
# CI (.github/workflows/ci.yml) builds and pushes this to
# ghcr.io/jeffbstewart/keaexporter on push to main and on version tags.
# To build locally from the repo root:  docker build -t keaexporter:dev .
#
# NO USER directive on purpose: kea creates its control socket
# root-owned mode 770, so a sidecar reaches it either as uid 0 (via the
# owner bits, running with cap_drop ALL and no DAC_OVERRIDE) or as a uid
# in kea's socket group. The image defaults to uid 0; set the run user
# in the orchestrator if kea's socket group is known.
#
# The builder is pinned by digest (golang:1.26): it controls the output
# binary, so pin it like a dependency.
FROM golang:1.26@sha256:dc2521c2a906db43073b8b4d99f491b6341cf15610b6ebbab187c45153f9959e AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /keaexporter ./cmd/keaexporter

FROM scratch
COPY --from=build /keaexporter /keaexporter
ENTRYPOINT ["/keaexporter"]
