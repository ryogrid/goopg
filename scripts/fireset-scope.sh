#!/usr/bin/env bash
# fireset-scope.sh — the M0145-0021a fire-set-gate scope predicate.
#
# Owner scope call 2026-09-22 (recorded in .ralph/fix_plan.md M0145-0021a and
# AGENT.md G9): a commit needs a tmp/gate-stamps/tpcds-fireset.json PASS when
# it stages a non-test .go file
#   - under internal/optimizer/ or internal/planner/, or
#   - anywhere under internal/ or cmd/ whose path matches *cost*|*stat*|*selfuncs*.
# internal/executor/ is deliberately OUT: it cannot move plan election, and its
# timeout class is covered by the sf025 sweep's own TIMEOUT counter.
#
# This script is the SINGLE SOURCE for that predicate — .githooks/commit-msg
# calls it on the staged file list. Keep the patterns in sync with the G9 text.
#
# Usage:
#   scripts/fireset-scope.sh <path> [path...]   # exit 0 = in scope, 1 = not
#   git diff --cached --name-only | scripts/fireset-scope.sh --stdin
set -u

is_test_path() { case "$1" in *_test.go|*/testdata/*) return 0 ;; esac; return 1; }

fireset_in_scope() { # <path> -> 0 if the path is in the fire-set scope
    local f="$1"
    is_test_path "$f" && return 1
    # internal/executor/ is out per G9 even when its filename matches *stat* —
    # pgstat_*/extstats_*/sys_pg_statistic_ext* live there and cannot move plan
    # election (their timeout class is the sweep's own TIMEOUT counter).
    # internal/testport/ is out for the same reason commit-msg excludes it
    # from gated_go.
    case "$f" in
        internal/executor/*|internal/testport/*) return 1 ;;
        internal/optimizer/*.go|internal/planner/*.go) return 0 ;;
    esac
    case "$f" in
        internal/*.go|cmd/*.go)
            # Same substring convention as commit-msg's arm_needed; the
            # *stat* arm is deliberately broad (over-inclusive is the safe
            # direction for a gate).
            case "$f" in *cost*|*stat*|*selfuncs*) return 0 ;; esac ;;
    esac
    return 1
}

if [[ "${1:-}" == "--stdin" ]]; then
    shift
    while IFS= read -r p; do
        [[ -n "$p" ]] && fireset_in_scope "$p" && { echo "IN-SCOPE: $p"; exit 0; }
    done
    exit 1
fi

[[ $# -ge 1 ]] || { echo "usage: $0 <path> [path...] | --stdin" >&2; exit 2; }
for p in "$@"; do
    fireset_in_scope "$p" && { echo "IN-SCOPE: $p"; exit 0; }
done
exit 1
