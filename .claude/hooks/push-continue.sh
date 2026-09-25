#!/usr/bin/env bash
# PostToolUse hook (Bash). After a `git push`, if the project root holds a
# `.continue` sentinel file, arm a one-shot continue marker and tell the agent
# to keep going (the equivalent of the user typing "continue" + Enter).
set -u

input=$(cat)
cmd=$(printf '%s' "$input" | jq -r '.tool_input.command // ""')

# NOTE: settings.json carries TWO `if` entries for this hook — `Bash(git *)`
# and `Bash(rtk git *)`. The RTK PreToolUse rewrite turns `git push` into
# `rtk git push`, which no longer matches `Bash(git *)` (that silently skipped
# the R59 push 1856259e4). Both spellings still contain the "git push"
# substring matched below; the script re-verifies and exits silent otherwise.
case "$cmd" in
  *"git push"*) ;;
  *) exit 0 ;;
esac

root="${CLAUDE_PROJECT_DIR:-$PWD}"
[ -f "$root/.continue" ] || exit 0

# One-shot arm for the Stop hook (cleared by continue-on-stop.sh on next stop).
mkdir -p "$root/.claude"
: > "$root/.claude/.continue-pending"

# Best-effort arm log (proves PostToolUse fired; never breaks the hook).
printf '%s armed root=%s cmd=%.120s\n' "$(date +%FT%T%z)" "$root" "$cmd" >> /tmp/push-continue.log 2>/dev/null || true

jq -nc '{hookSpecificOutput:{hookEventName:"PostToolUse",additionalContext:"continue"}}'
