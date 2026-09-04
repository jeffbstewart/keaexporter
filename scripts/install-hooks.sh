#!/bin/sh
# Install scripts/presubmit.sh as the git pre-commit hook.  Re-run after
# a fresh clone.
set -eu
cd "$(git rev-parse --show-toplevel)"
hook=.git/hooks/pre-commit
printf '#!/bin/sh\nexec sh scripts/presubmit.sh\n' > "$hook"
chmod +x "$hook"
echo "installed pre-commit hook -> scripts/presubmit.sh"
