#!/bin/sh
# Presubmit: every tracked file is 7-bit ASCII.  Tab and LF are the only
# control characters allowed; every other printable byte must be in the
# space..tilde range.  Run from the repository root.
#
# The vendored IEEE registry CSVs (third_party/ieee-oui/*.csv) are the
# ONLY exemption: they carry non-ASCII organization names.  They are not
# unchecked -- cmd/ouigen -verify (presubmit) enforces strict UTF-8 and
# table sync on them.
set -eu

TAB=$(printf '\t')
if git ls-files -z -- ':(exclude)third_party/ieee-oui/*.csv' \
    | LC_ALL=C xargs -0 grep -n "[^${TAB} -~]" --; then
  echo "check-ascii: non-ASCII or control bytes found (listed above)" >&2
  exit 1
fi
echo "check-ascii: OK"
