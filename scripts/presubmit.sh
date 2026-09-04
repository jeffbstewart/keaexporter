#!/bin/sh
# All presubmit checks for keaexporter: 7-bit ASCII (with the vendored
# IEEE CSV exemption), the OUI table drift + strict-UTF-8 gate, gofmt,
# go vet, tests.  Run before committing; scripts/install-hooks.sh wires
# it as the pre-commit hook.  CI runs the same script
# (.github/workflows/ci.yml).
set -eu
cd "$(git rev-parse --show-toplevel)"

sh scripts/check-ascii.sh

# Vendored IEEE CSVs <-> generated internal/oui/table.txt sync, plus the
# strict-UTF-8 validation that makes the check-ascii exemption sound.
# Offline: reads only the committed files (CI has no egress to IEEE).
go run ./cmd/ouigen -verify
echo "ouigen -verify: OK"

unformatted=$(gofmt -l .)
if [ -n "$unformatted" ]; then
  echo "gofmt: these files need formatting:" >&2
  echo "$unformatted" >&2
  exit 1
fi
echo "gofmt: OK"

go vet ./...
echo "go vet: OK"

go test ./...
echo "presubmit: OK"
