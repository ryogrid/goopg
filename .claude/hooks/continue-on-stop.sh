#!/usr/bin/env bash
# Stop hook. If a `git push` happened while the `.continue` sentinel was present
# (armed by push-continue.sh), prevent the agent from stopping — continue the
# conversation — exactly once.
set -u

input=$(cat)

sha="$(printf '%s' "$input" | jq -r '.stop_hook_active // false')"
cwd_in="$(printf '%s' "$input" | jq -r '.cwd // ""')"

# Best-effort debug log (diagnosing silent exits; never breaks the hook).
clog() { printf '%s %s\n' "$(date +%FT%T%z)" "$*" >> /tmp/continue-on-stop.log 2>/dev/null || true; }

# Loop protection: Claude Code re-runs Stop hooks after a block; never re-block.
if [ "$sha" = "true" ]; then
  clog "exit=loop-protection cwd_in=$cwd_in pwd=$PWD env=${CLAUDE_PROJECT_DIR:-<unset>}"
  exit 0
fi

root="${CLAUDE_PROJECT_DIR:-$cwd_in}"
# Fallback: Stop inputs have arrived without either CLAUDE_PROJECT_DIR or .cwd
# (the silent R59-stop of 2026-09-11 ran 25ms with both sentinels present but
# blocked nothing). If the resolved root holds no sentinel, try the hook's cwd.
if [ ! -f "$root/.continue" ] && [ -f "$PWD/.continue" ]; then
  root="$PWD"
fi

cont="no"; [ -f "$root/.continue" ] && cont="yes"
pend="no"; [ -f "$root/.claude/.continue-pending" ] && pend="yes"

if [ "$cont" != "yes" ] || [ "$pend" != "yes" ]; then
  clog "exit=missing root=$root pwd=$PWD env=${CLAUDE_PROJECT_DIR:-<unset>} cwd_in=$cwd_in continue=$cont pending=$pend"
  exit 0
fi

rm -f "$root/.claude/.continue-pending"   # fire once per push, not every turn
clog "decision=block root=$root"

jq -nc '{decision:"block",reason:"The .continue sentinel is present and a git push just completed. Continue working instead of stopping."}'
