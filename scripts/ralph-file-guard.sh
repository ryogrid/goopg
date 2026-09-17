#!/usr/bin/env bash
# ralph-file-guard.sh — Claude Code PreToolUse hook for
# Edit|Write|MultiEdit|NotebookEdit|mcp__serena__.* .
# When RALPH_LOOP=1, denies edits to CLAUDE.md, the AGENT.md
# PLAN-PARITY-HARNESS section, the .ralph/fix_plan.md '## Current Priority'
# banner, and the harness mechanism files (guards, .githooks, settings.json,
# .ralph/PROMPT.md, ref-cluster / gate-stamp plumbing, ~/.ralph/). Serena
# write tools (replace_content, create_text_file, *_symbol, replace_in_files,
# execute_shell_command -> ralph-bash-guard.sh) are covered too. Logic lives in ralph_protected_regions.py
# (shared with .githooks/pre-commit). No-op for interactive sessions.
[ "${RALPH_LOOP:-}" = "1" ] || exit 0
exec python3 "$(dirname "$0")/ralph_protected_regions.py" file-guard
