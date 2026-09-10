#!/usr/bin/env bash
# Stop hook. If a `git push` happened while the `.continue` sentinel was present
# (armed by push-continue.sh), prevent the agent from stopping — continue the
# conversation — exactly once.
set -u

input=$(cat)

# Loop protection: Claude Code re-runs Stop hooks after a block; never re-block.
if [ "$(printf '%s' "$input" | jq -r '.stop_hook_active // false')" = "true" ]; then
  exit 0
fi

root="${CLAUDE_PROJECT_DIR:-$(printf '%s' "$input" | jq -r '.cwd // ""')}"
[ -f "$root/.continue" ] || exit 0
[ -f "$root/.claude/.continue-pending" ] || exit 0

rm -f "$root/.claude/.continue-pending"   # fire once per push, not every turn

jq -nc '{decision:"block",reason:"The .continue sentinel is present and a git push just completed. Continue working instead of stopping."}'
