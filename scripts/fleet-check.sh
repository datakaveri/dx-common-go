#!/usr/bin/env bash
#
# fleet-check.sh — build (and optionally test) every service that consumes this
# library, against the working tree.
#
# WHY THIS EXISTS
#
# Every service commits `replace github.com/datakaveri/dx-common-go => ../dx-common-go`,
# so HEAD of this library is what ~24 modules compile against simultaneously.
# That makes a breaking change here a fleet-wide breakage at the same commit —
# and there is no version skew to hide behind and no external consumer owed a
# deprecation window.
#
# The flip side is that the blast radius is fully knowable before merge. This
# script turns "a breaking change breaks 20 builds" from a hazard discovered in
# someone else's CI into a pre-merge check.
#
# It has already earned its keep: on its first run it found 15 repos that could
# not build because a dependency bump had been pushed here without running
# `go mod tidy` in the dependents.
#
# USAGE
#   scripts/fleet-check.sh              # build only (fast; the usual gate)
#   scripts/fleet-check.sh --test       # build + go test ./...
#   scripts/fleet-check.sh --tidy       # run `go mod tidy` first (after dep bumps)
#   FLEET_ROOT=/path scripts/fleet-check.sh
#
# Exits non-zero if any module fails, printing every failure rather than
# stopping at the first — one run should tell you the whole blast radius.

set -uo pipefail

FLEET_ROOT="${FLEET_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"

RUN_TESTS=0
RUN_TIDY=0
for arg in "$@"; do
  case "$arg" in
    --test) RUN_TESTS=1 ;;
    --tidy) RUN_TIDY=1 ;;
    -h|--help) sed -n '2,30p' "${BASH_SOURCE[0]}"; exit 0 ;;
    *) echo "unknown flag: $arg" >&2; exit 2 ;;
  esac
done

# Excluded from the fleet:
#   dx-common-go      — this library; its own CI covers it
#   files-connect-go  — debris: a 2-file stub (go.mod + main.go), no .git, absent
#                       from scripts/clone-repos.sh, importing a package removed
#                       in the postgres reorg. Superseded by dx-files-connect-api-go.
is_excluded() {
  case "$1" in
    dx-common-go|files-connect-go) return 0 ;;
    *) return 1 ;;
  esac
}

printf '\033[1mfleet-check\033[0m  root=%s  tests=%s  tidy=%s\n\n' \
  "$FLEET_ROOT" "$RUN_TESTS" "$RUN_TIDY"

failed=()
checked=0

for dir in "$FLEET_ROOT"/*/; do
  name="$(basename "$dir")"
  [ -f "$dir/go.mod" ] || continue
  is_excluded "$name" && continue

  checked=$((checked + 1))
  printf '  %-30s ' "$name"

  if [ "$RUN_TIDY" -eq 1 ]; then
    if ! out=$(cd "$dir" && go mod tidy 2>&1); then
      printf '\033[31mTIDY FAILED\033[0m\n%s\n' "$out"
      failed+=("$name (tidy)")
      continue
    fi
  fi

  if ! out=$(cd "$dir" && go build ./... 2>&1); then
    printf '\033[31mBUILD FAILED\033[0m\n%s\n' "$out"
    failed+=("$name (build)")
    continue
  fi

  if [ "$RUN_TESTS" -eq 1 ]; then
    if ! out=$(cd "$dir" && go test ./... 2>&1); then
      printf '\033[31mTESTS FAILED\033[0m\n%s\n' "$(echo "$out" | grep -E '^(---|FAIL|\s+.*\.go:)' | head -30)"
      failed+=("$name (test)")
      continue
    fi
  fi

  printf '\033[32mok\033[0m\n'
done

echo
if [ ${#failed[@]} -eq 0 ]; then
  printf '\033[32m✓ %d modules green\033[0m\n' "$checked"
  exit 0
fi

printf '\033[31m✗ %d of %d modules failed:\033[0m\n' "${#failed[@]}" "$checked"
printf '    %s\n' "${failed[@]}"
echo
echo "If these are 'updates to go.mod needed', a dependency changed in this"
echo "library and the dependents are stale — re-run with --tidy, then commit"
echo "the go.mod/go.sum changes in each affected repo."
exit 1
