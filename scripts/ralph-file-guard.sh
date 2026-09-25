#!/usr/bin/env bash
# ralph-file-guard.sh — Claude Code PreToolUse hook for
# Edit|Write|MultiEdit|NotebookEdit|mcp__serena__.* .
# When RALPH_LOOP=1, denies edits to CLAUDE.md, the AGENT.md
# PLAN-PARITY-HARNESS section, the .ralph/fix_plan.md '## Current Priority'
# banner, and the harness mechanism files (guards, .githooks, settings.json,
# .ralph/PROMPT.md, .ralph/gate-exceptions.md, ref-cluster / gate-stamp
# plumbing, ~/.ralph/). Serena
# write tools (replace_content, create_text_file, *_symbol, replace_in_files,
# execute_shell_command -> ralph-bash-guard.sh) are covered too. Logic lives in ralph_protected_regions.py
# (shared with .githooks/pre-commit). No-op for interactive sessions.
#
# Every denial is appended to ci/logs/ralph-guard-denials.log (timestamp, tool,
# rule, first 200 chars of the path/target) so a denial the loop never reports
# is still visible to an audit. A log-write failure never fails the hook.
[ "${RALPH_LOOP:-}" = "1" ] || exit 0

DIR="$(cd "$(dirname "$0")" && pwd)"
input="$(cat)"
out="$(printf '%s' "$input" | python3 "$DIR/ralph_protected_regions.py" file-guard)"
rc=$?

if printf '%s' "$out" | grep -q '"permissionDecision" *: *"deny"'; then
  # Best-effort audit line; never let it change the hook's decision.
  {
    info="$(printf '%s' "$input" | python3 -c 'import json,sys
try:
    d = json.load(sys.stdin); ti = d.get("tool_input") or {}
    print(d.get("cwd") or "")
    print("%s %s" % (d.get("tool_name", "?"),
                     ti.get("file_path") or ti.get("notebook_path")
                     or ti.get("relative_path") or ti.get("command") or ""))
except Exception:
    print(""); print("?")' 2>/dev/null)"
    hook_cwd="$(printf '%s\n' "$info" | sed -n 1p)"
    subj="$(printf '%s\n' "$info" | sed -n 2p)"
    root="${CLAUDE_PROJECT_DIR:-}"
    [ -n "$root" ] || root="$(git -C "${hook_cwd:-.}" rev-parse --show-toplevel 2>/dev/null)" || true
    if [ -n "$root" ] && mkdir -p "$root/ci/logs" 2>/dev/null; then
      printf '%s tool=%s rule=%s subject=%s\n' \
        "$(date -Iseconds 2>/dev/null || echo unknown-time)" \
        "file-guard" "protected-region" \
        "$(printf '%s' "$subj" | tr '\n\t' '  ' | cut -c1-200)" \
        >>"$root/ci/logs/ralph-guard-denials.log" 2>/dev/null || true
    fi
  } >/dev/null 2>&1 || true
fi

[ -z "$out" ] || printf '%s\n' "$out"
exit "$rc"
