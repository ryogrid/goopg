#!/usr/bin/env bash
# PostToolUse hook (Bash). After a `git push`, if the project root holds a
# `.continue` sentinel file, arm a one-shot continue marker and tell the agent
# to keep going (the equivalent of the user typing "continue" + Enter).
set -u

input=$(cat)
cmd=$(printf '%s' "$input" | jq -r '.tool_input.command // ""')

# The `if` matcher is best-effort (it still fires on commands containing $(),
# backticks or $VAR), so re-verify the actual command string.
case "$cmd" in
  *"git push"*) ;;
  *) exit 0 ;;
esac

root="${CLAUDE_PROJECT_DIR:-$PWD}"
[ -f "$root/.continue" ] || exit 0

# One-shot arm for the Stop hook (cleared by continue-on-stop.sh on next stop).
mkdir -p "$root/.claude"
: > "$root/.claude/.continue-pending"

jq -nc '{hookSpecificOutput:{hookEventName:"PostToolUse",additionalContext:"continue"}}'
