#!/usr/bin/env bash
# ralph-devin-guard-adapter-test.sh - self-test for
# scripts/ralph-devin-guard-adapter.py (Devin CLI PreToolUse hook) using
# canned Devin hook payloads, so no agent API calls are made.
# Usage: scripts/ralph-devin-guard-adapter-test.sh   (exit 0 = all cases pass)
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
ADAPTER="$HERE/ralph-devin-guard-adapter.py"
pass=0 fail=0

TD="$(mktemp -d)"; trap 'rm -rf "$TD"' EXIT
mkdir -p "$TD/.ralph" "$TD/.devin" "$TD/scripts"
cat >"$TD/AGENT.md" <<'EOF'
# AGENT
intro text
<!-- PLAN-PARITY-HARNESS:BEGIN -->
rule one: never stop reference clusters
<!-- PLAN-PARITY-HARNESS:END -->
status note: old
EOF
cat >"$TD/.ralph/fix_plan.md" <<'EOF'
# Fix plan
## Current Priority
1. do the important thing
## Notes / rules
- [ ] **M0142-0001 — task**
EOF
printf 'claude\n' >"$TD/CLAUDE.md"
printf '{}\n' >"$TD/.devin/hooks.v1.json"

# Devin hook payload: <tool_name> <tool_input JSON>
dj() { python3 -c 'import json,sys; print(json.dumps({"hook_event_name":"PreToolUse","tool_name":sys.argv[1],"tool_input":json.loads(sys.argv[2]),"tool_use_id":"call_t","session_id":"test-session"}))' "$1" "$2"; }

# Keep denial audit logs inside the temp dir (the guards log under the project root).
run() { # <loop-env> <json> -> "deny" | "allow"
  local out
  out="$(printf '%s' "$2" | RALPH_LOOP="$1" DEVIN_PROJECT_DIR="$TD" CLAUDE_PROJECT_DIR="$TD" "$ADAPTER" 2>/dev/null)"
  if printf '%s' "$out" | grep -q '"permissionDecision": *"deny"'; then echo deny; else echo allow; fi
}

check() { # <expect> <tool> <tool_input JSON> [loop-env]
  local expect="$1" json got
  json="$(dj "$2" "$3")"
  got="$(run "${4-1}" "$json")"
  if [ "$got" = "$expect" ]; then pass=$((pass + 1))
  else fail=$((fail + 1)); printf 'FAIL expect=%s got=%s RALPH_LOOP=%s\n  json: %s\n' "$expect" "$got" "${4-1}" "$json"; fi
}

# --- exec -> bash guard -------------------------------------------------------
check deny  exec '{"command":"echo x >> CLAUDE.md"}'
check deny  exec '{"command":"git reset --hard HEAD~1"}'
check deny  exec '{"command":"git commit --no-verify -m x"}'
check deny  exec '{"command":"cp /tmp/x .devin/hooks.v1.json"}'
check allow exec '{"command":"go build ./..."}'
check allow exec '{"command":"git status"}'
# exec env object is checked as a VAR=value prefix
check deny  exec '{"command":"git commit -m x","env":{"RALPH_LOOP":"0"}}'
check deny  exec '{"command":"git commit -m x","env":{"GIT_CONFIG_COUNT":"1","GIT_CONFIG_KEY_0":"core.hooksPath","GIT_CONFIG_VALUE_0":"/dev/null"}}'
check allow exec '{"command":"go test ./internal/...","env":{"GOFLAGS":"-mod=mod"}}'
check deny  exec '{"command":"echo x >> CLAUDE.md","workdir":"'"$TD"'"}'
# exec workdir inside a harness directory (bare file names would evade the bash guard)
check deny  exec '{"command":"printf x > hooks.v1.json","workdir":"'"$TD"'/.devin"}'
check deny  exec '{"command":"printf x > PROMPT.md","workdir":".ralph"}'
check deny  exec '{"command":"printf x > pre-commit","workdir":"'"$TD"'/.githooks"}'
check deny  exec '{"command":"sed -i s/a/b/ config","workdir":"'"$TD"'/.git"}'
check deny  exec '{"command":"printf x > ralph_loop.sh","workdir":"~/.ralph"}'
check deny  exec '{"command":"ls","workdir":"'"$TD"'/scripts/lib"}'
check allow exec '{"command":"go test ./...","workdir":"'"$TD"'/scripts"}'
check allow exec '{"command":"ls","workdir":"/tmp"}'

# --- write_to_process: typed text denied, control keys allowed -----------------
check deny  write_to_process '{"shell_id":"s1","text_input":"git push --force origin HEAD\n"}'
check deny  write_to_process '{"shell_id":"s1","text_input":"ls\n"}'
check deny  write_to_process '{"shell_id":"s1","text_input":"echo x >> CLAU"}'
check deny  write_to_process '{"shell_id":"s1","bytes_input":"echo x >> CLAUDE.md<CR>"}'
check allow write_to_process '{"shell_id":"s1","bytes_input":"<CR>"}'
check allow write_to_process '{"shell_id":"s1","bytes_input":"<C-c>"}'

# --- edit / write -> file guard -------------------------------------------------
check deny  edit  '{"file_path":"'"$TD"'/CLAUDE.md","old_string":"claude","new_string":"x"}'
check deny  edit  '{"file_path":"'"$TD"'/AGENT.md","old_string":"rule one: never","new_string":"rule one: rarely"}'
check allow edit  '{"file_path":"'"$TD"'/AGENT.md","old_string":"status note: old","new_string":"status note: new"}'
check deny  edit  '{"file_path":"'"$TD"'/.ralph/fix_plan.md","old_string":"1. do the important thing","new_string":"1. do my thing"}'
check allow edit  '{"file_path":"'"$TD"'/.ralph/fix_plan.md","old_string":"- [ ] **M0142-0001","new_string":"- [x] **M0142-0001"}'
check deny  edit  '{"file_path":"AGENT.md","old_string":"rule one: never","new_string":"rule one: rarely"}'
check deny  write '{"file_path":"'"$TD"'/.devin/hooks.v1.json","content":"{}"}'
check deny  write '{"file_path":"'"$TD"'/scripts/ralph-devin-guard-adapter.py","content":""}'
check allow write '{"file_path":"'"$TD"'/notes.md","content":"hello"}'

# --- notebook_edit -> file guard ------------------------------------------------
check deny  notebook_edit '{"notebook_path":"'"$TD"'/CLAUDE.md","cell_number":0,"new_source":"x"}'
check allow notebook_edit '{"notebook_path":"'"$TD"'/nb.ipynb","cell_number":0,"new_source":"x"}'

# --- mcp_call_tool (serena) -> file guard ----------------------------------------
check deny  mcp_call_tool '{"server_name":"serena","tool_name":"create_text_file","arguments":{"relative_path":"CLAUDE.md","content":"x"}}'
check deny  mcp_call_tool '{"server_name":"serena","tool_name":"execute_shell_command","arguments":{"command":"git reset --hard"}}'
check allow mcp_call_tool '{"server_name":"serena","tool_name":"find_symbol","arguments":{"name_path":"x"}}'
check allow mcp_call_tool '{"server_name":"headroom","tool_name":"headroom_stats","arguments":{}}'
# arguments delivered as a JSON string, or unreadable
check deny  mcp_call_tool '{"server_name":"serena","tool_name":"create_text_file","arguments":"{\"relative_path\":\"CLAUDE.md\",\"content\":\"x\"}"}'
check deny  mcp_call_tool '{"server_name":"serena","tool_name":"create_text_file","arguments":"not json"}'
check allow mcp_call_tool '{"server_name":"serena","tool_name":"find_symbol","arguments":"not json"}'

# --- inactive outside the loop / unknown shapes fail open ------------------------
check allow exec '{"command":"git reset --hard"}' 0
check allow edit '{"file_path":"'"$TD"'/CLAUDE.md","old_string":"claude","new_string":"x"}' ""
check allow read '{"file_path":"'"$TD"'/CLAUDE.md"}'
check allow exec '{"cmd":"git reset --hard"}'
# guard infrastructure failure blocks the call (fail closed)
BROKEN="$TD/broken"; mkdir -p "$BROKEN"
cp "$ADAPTER" "$BROKEN/ralph-devin-guard-adapter.py"
printf '#!/bin/sh\nexit 3\n' > "$BROKEN/ralph-bash-guard.sh"; chmod +x "$BROKEN/ralph-bash-guard.sh"
out="$(dj exec '{"command":"ls"}' | RALPH_LOOP=1 DEVIN_PROJECT_DIR="$TD" CLAUDE_PROJECT_DIR="$TD" python3 "$BROKEN/ralph-devin-guard-adapter.py" 2>/dev/null)"
if printf '%s' "$out" | grep -q '"permissionDecision": *"deny"'; then pass=$((pass + 1)); else fail=$((fail + 1)); echo "FAIL guard failure should deny: $out"; fi
# the hook command itself denies when the adapter cannot run
hookcmd="$(jq -r '.PreToolUse[0].hooks[0].command' "$HERE/../.devin/hooks.v1.json")"
out="$(printf '{}' | RALPH_LOOP=1 DEVIN_PROJECT_DIR="$TD/missing" bash -c "$hookcmd" 2>/dev/null)"
if printf '%s' "$out" | grep -q '"permissionDecision": *"deny"'; then pass=$((pass + 1)); else fail=$((fail + 1)); echo "FAIL missing adapter should deny: $out"; fi
out="$(printf '{}' | RALPH_LOOP=0 DEVIN_PROJECT_DIR="$TD/missing" bash -c "$hookcmd" 2>/dev/null)"
if [ -z "$out" ]; then pass=$((pass + 1)); else fail=$((fail + 1)); echo "FAIL missing adapter outside loop should be silent: $out"; fi

got="$(printf 'not json' | RALPH_LOOP=1 DEVIN_PROJECT_DIR="$TD" CLAUDE_PROJECT_DIR="$TD" "$ADAPTER" 2>/dev/null; echo "rc=$?")"
if [ "$got" = "rc=0" ]; then pass=$((pass + 1)); else fail=$((fail + 1)); echo "FAIL malformed input: $got"; fi

echo "ralph-devin-guard-adapter-test: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
