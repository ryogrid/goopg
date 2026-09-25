#!/usr/bin/env bash
# ralph-agent-adapter-test.sh - self-test for the OpenCode/Codex/Devin backend
# adapters in ralph_loop.sh (build_agent_command, normalize_agent_output)
# using canned CLI outputs, so no agent API calls are made.
# Usage: scripts/ralph-agent-adapter-test.sh   (exit 0 = all cases pass)
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
ENGINE="${RALPH_HOME:-$HOME/.ralph}/ralph_loop.sh"
pass=0 fail=0

ok() { pass=$((pass + 1)); echo "ok: $1"; }
ng() { fail=$((fail + 1)); echo "FAIL: $1"; }

if [[ ! -f "$ENGINE" ]]; then
    echo "FAIL: Ralph engine not found: $ENGINE (set RALPH_HOME to override)"
    exit 1
fi

TMP_OUT="$(mktemp)"

# Everything below runs in a subshell: sourcing the engine exports
# RALPH_LOOP=1 and overrides globals, neither of which may leak out.
# Per-case counters live in the subshell; the parent derives totals by
# counting result lines, so subshell scoping cannot skew the summary.
(
    source "$ENGINE"

    # Redirect engine state files to a temp dir (never touch the real .ralph/)
    TD="$(mktemp -d)"
    trap 'rm -rf "$TD"' EXIT
    RALPH_DIR="$TD"
    SESSION_FILE="$TD/.claude_session_id"
    # State paths are resolved when the engine is sourced (relative to the
    # real .ralph/), so re-point every file a helper under test may write.
    CLAUDE_SESSION_FILE="$TD/.claude_session_id"
    OPENCODE_SESSION_FILE="$TD/.opencode_session_id"
    CODEX_SESSION_FILE="$TD/.codex_thread_id"
    DEVIN_SESSION_FILE="$TD/.devin_session_id"
    RALPH_SESSION_FILE="$TD/.ralph_session"
    RALPH_SESSION_HISTORY_FILE="$TD/.ralph_session_history"
    EXIT_SIGNALS_FILE="$TD/.exit_signals"
    RESPONSE_ANALYSIS_FILE="$TD/.response_analysis"
    LOG_DIR="$TD/logs"
    mkdir -p "$LOG_DIR"

    printf 'prompt body\n' > "$TD/PROMPT.md"

    # --- backend validation ---
    AGENT_BACKEND=claude; validate_agent_backend >/dev/null 2>&1 && ok "backend claude accepted" || ng "backend claude accepted"
    AGENT_BACKEND=opencode; validate_agent_backend >/dev/null 2>&1 && ok "backend opencode accepted" || ng "backend opencode accepted"
    AGENT_BACKEND=codex; validate_agent_backend >/dev/null 2>&1 && ok "backend codex accepted" || ng "backend codex accepted"
    AGENT_BACKEND=devin; validate_agent_backend >/dev/null 2>&1 && ok "backend devin accepted" || ng "backend devin accepted"
    AGENT_BACKEND=bogus; validate_agent_backend >/dev/null 2>&1 && ng "backend bogus rejected" || ok "backend bogus rejected"

    # --- session file mapping (backend-specific files) ---
    [[ "$(agent_session_file opencode)" == "$OPENCODE_SESSION_FILE" ]] && ok "opencode session file" || ng "opencode session file"
    [[ "$(agent_session_file codex)" == "$CODEX_SESSION_FILE" ]] && ok "codex session file" || ng "codex session file"
    [[ "$(agent_session_file claude)" == "$CLAUDE_SESSION_FILE" ]] && ok "claude session file" || ng "claude session file"
    [[ "$(agent_session_file devin)" == "$DEVIN_SESSION_FILE" ]] && ok "devin session file" || ng "devin session file"

    # --- opencode command shape ---
    AGENT_BACKEND=opencode
    build_agent_command "$TD/PROMPT.md" "CTX" "ses_1" "" >/dev/null 2>&1
    [[ "${AGENT_CMD_ARGS[0]}" == "opencode" && "${AGENT_CMD_ARGS[1]}" == "run" ]] && ok "opencode argv head" || ng "opencode argv head"
    [[ " ${AGENT_CMD_ARGS[*]} " == *" --format json "* ]] && ok "opencode json format" || ng "opencode json format"
    [[ " ${AGENT_CMD_ARGS[*]} " == *" --auto "* ]] && ok "opencode auto flag" || ng "opencode auto flag"
    [[ " ${AGENT_CMD_ARGS[*]} " == *" --session ses_1 "* ]] && ok "opencode session resume" || ng "opencode session resume"
    last_idx=$((${#AGENT_CMD_ARGS[@]} - 1))
    [[ "${AGENT_CMD_ARGS[$last_idx]}" == "CTX"* ]] && ok "opencode context prepend" || ng "opencode context prepend"

    # --- codex command shape (resume and fresh) ---
    AGENT_BACKEND=codex
    build_agent_command "$TD/PROMPT.md" "CTX" "thr_1" "$TD/last.txt" >/dev/null 2>&1
    [[ "${AGENT_CMD_ARGS[0]}" == "codex" && "${AGENT_CMD_ARGS[1]}" == "exec" ]] && ok "codex argv head" || ng "codex argv head"
    [[ " ${AGENT_CMD_ARGS[*]} " == *" -o $TD/last.txt "* ]] && ok "codex last-message file" || ng "codex last-message file"
    [[ " ${AGENT_CMD_ARGS[*]} " == *" resume thr_1 "* ]] && ok "codex explicit resume" || ng "codex explicit resume"
    [[ " ${AGENT_CMD_ARGS[*]} " != *" --last "* ]] && ok "codex never uses --last" || ng "codex never uses --last"
    build_agent_command "$TD/PROMPT.md" "" "" "$TD/last.txt" >/dev/null 2>&1
    [[ " ${AGENT_CMD_ARGS[*]} " != *" resume "* ]] && ok "codex fresh start" || ng "codex fresh start"
    CODEX_SANDBOX="" build_agent_command "$TD/PROMPT.md" "" "" "$TD/last.txt" >/dev/null 2>&1
    [[ " ${AGENT_CMD_ARGS[*]} " == *" -s workspace-write "* ]] && ok "codex sandbox default" || ng "codex sandbox default"

    # --- devin command shape (resume and fresh; dangerous, never sandboxed) ---
    AGENT_BACKEND=devin
    export DEVIN_SANDBOX=1 DEVIN_PERMISSION_MODE=auto
    DEVIN_MODEL="" DEVIN_SKIP_TRUST=false
    build_agent_command "$TD/PROMPT.md" "CTX" "sess-one" "$TD/devin_export.json" >/dev/null 2>&1
    [[ "${AGENT_CMD_ARGS[0]}" == "devin" ]] && ok "devin argv head" || ng "devin argv head"
    [[ " ${AGENT_CMD_ARGS[*]} " == *" --permission-mode dangerous "* ]] && ok "devin dangerous mode" || ng "devin dangerous mode"
    [[ " ${AGENT_CMD_ARGS[*]} " == *" --export $TD/devin_export.json "* ]] && ok "devin export file" || ng "devin export file"
    [[ " ${AGENT_CMD_ARGS[*]} " == *" -r sess-one "* ]] && ok "devin explicit resume" || ng "devin explicit resume"
    [[ " ${AGENT_CMD_ARGS[*]} " != *" --sandbox "* ]] && ok "devin no sandbox flag" || ng "devin no sandbox flag"
    [[ " ${AGENT_CMD_ARGS[*]} " != *" -c "* && " ${AGENT_CMD_ARGS[*]} " != *" --continue "* ]] && ok "devin never continues" || ng "devin never continues"
    [[ -z "${DEVIN_SANDBOX+x}" && -z "${DEVIN_PERMISSION_MODE+x}" ]] && ok "devin sandbox env unset" || ng "devin sandbox env unset"
    [[ " ${AGENT_CMD_ARGS[*]} " != *" --respect-workspace-trust "* ]] && ok "devin trust check kept" || ng "devin trust check kept"
    last_idx=$((${#AGENT_CMD_ARGS[@]} - 1))
    prev_idx=$((last_idx - 1))
    [[ "${AGENT_CMD_ARGS[$last_idx]}" == "-p" && "${AGENT_CMD_ARGS[$((prev_idx - 1))]}" == "--prompt-file" ]] && ok "devin prompt-file print mode" || ng "devin prompt-file print mode"
    devin_prompt="${AGENT_CMD_ARGS[$prev_idx]}"
    [[ "$(head -1 "$devin_prompt" 2>/dev/null)" == "CTX" ]] && grep -q 'prompt body' "$devin_prompt" 2>/dev/null && ok "devin context prepend" || ng "devin context prepend"
    DEVIN_MODEL="sonnet" DEVIN_SKIP_TRUST=true build_agent_command "$TD/PROMPT.md" "" "" "$TD/devin_export.json" >/dev/null 2>&1
    [[ " ${AGENT_CMD_ARGS[*]} " != *" -r "* ]] && ok "devin fresh start" || ng "devin fresh start"
    [[ " ${AGENT_CMD_ARGS[*]} " == *" --model sonnet "* ]] && ok "devin model override" || ng "devin model override"
    [[ " ${AGENT_CMD_ARGS[*]} " == *" --respect-workspace-trust false "* ]] && ok "devin skip trust opt-in" || ng "devin skip trust opt-in"
    [[ "$(cat "${AGENT_CMD_ARGS[$(( ${#AGENT_CMD_ARGS[@]} - 2 ))]}")" == "prompt body" ]] && ok "devin no context no prepend" || ng "devin no context no prepend"
    build_agent_command "$TD/PROMPT.md" "" "" "" >/dev/null 2>&1 && ng "devin requires export file" || ok "devin requires export file"

    # --- reset_session clears every backend's session file ---
    for f in "$CLAUDE_SESSION_FILE" "$OPENCODE_SESSION_FILE" "$CODEX_SESSION_FILE" "$DEVIN_SESSION_FILE"; do printf 'id\n' > "$f"; done
    reset_session "test_reset" >/dev/null 2>&1
    [[ ! -e "$DEVIN_SESSION_FILE" ]] && ok "reset clears devin session" || ng "reset clears devin session"
    [[ ! -e "$CODEX_SESSION_FILE" && ! -e "$OPENCODE_SESSION_FILE" ]] && ok "reset clears other sessions" || ng "reset clears other sessions"

    # --- claude path mirrors into the shared array ---
    AGENT_BACKEND=claude
    build_agent_command "$TD/PROMPT.md" "CTX" "" "" >/dev/null 2>&1
    [[ "${AGENT_CMD_ARGS[*]}" == "${CLAUDE_CMD_ARGS[*]}" ]] && ok "claude array mirror" || ng "claude array mirror"

    # --- normalize: opencode event stream ---
    cat > "$TD/opencode.json" <<'EOF'
{"type":"step_start","sessionID":"ses_test","part":{"type":"step-start"}}
{"type":"text","sessionID":"ses_test","part":{"type":"text","text":"All done\n"}}
{"type":"text","sessionID":"ses_test","part":{"type":"text","text":"---RALPH_STATUS---\nSTATUS: COMPLETE\nEXIT_SIGNAL: true\n---END_RALPH_STATUS---\n"}}
EOF
    normalize_agent_output opencode "$TD/opencode.json" "" "$TD/opencode.canon.json" 0
    [[ "$(jq -r .sessionId "$TD/opencode.canon.json")" == "ses_test" ]] && ok "opencode session extraction" || ng "opencode session extraction"
    jq -e '.result | contains("---RALPH_STATUS---")' "$TD/opencode.canon.json" >/dev/null && ok "opencode text concat" || ng "opencode text concat"
    [[ "$(jq -r .is_error "$TD/opencode.canon.json")" == "false" ]] && ok "opencode no-error flag" || ng "opencode no-error flag"

    # --- normalize: codex -o file wins, JSONL fallback, error flag ---
    cat > "$TD/codex.jsonl" <<'EOF'
{"type":"thread.started","thread_id":"thr_test"}
{"type":"item.completed","item":{"id":"i1","type":"agent_message","text":"fallback text"}}
EOF
    printf 'final via -o' > "$TD/last.txt"
    normalize_agent_output codex "$TD/codex.jsonl" "$TD/last.txt" "$TD/codex.canon.json" 0
    [[ "$(jq -r .result "$TD/codex.canon.json")" == "final via -o" ]] && ok "codex -o priority" || ng "codex -o priority"
    [[ "$(jq -r .sessionId "$TD/codex.canon.json")" == "thr_test" ]] && ok "codex thread extraction" || ng "codex thread extraction"
    normalize_agent_output codex "$TD/codex.jsonl" "$TD/missing.txt" "$TD/codex.canon2.json" 1
    [[ "$(jq -r .result "$TD/codex.canon2.json")" == "fallback text" ]] && ok "codex message fallback" || ng "codex message fallback"
    [[ "$(jq -r .is_error "$TD/codex.canon2.json")" == "true" ]] && ok "codex error flag" || ng "codex error flag"

    # --- normalize: error events, empty output, multi-message join ---
    printf '%s\n' '{"type":"text","sessionID":"ses_e","part":{"type":"text","text":"partial"}}' '{"type":"error","message":"boom"}' > "$TD/opencode-err.json"
    normalize_agent_output opencode "$TD/opencode-err.json" "" "$TD/opencode-err.canon.json" 0
    [[ "$(jq -r .is_error "$TD/opencode-err.canon.json")" == "true" ]] && ok "opencode error event" || ng "opencode error event"
    printf '%s\n' '{"type":"thread.started","thread_id":"ses_e"}' > "$TD/empty.jsonl"
    normalize_agent_output codex "$TD/empty.jsonl" "$TD/missing.txt" "$TD/empty.canon.json" 0
    # Session established but no text: kept as success-shape; the analysis
    # pipeline records it as a no-progress iteration (session is still valid).
    [[ "$(jq -r .is_error "$TD/empty.canon.json")" == "false" ]] && ok "empty text keeps session" || ng "empty text keeps session"
    printf '%s\n' '{"type":"turn.started"}' > "$TD/nothing.jsonl"
    normalize_agent_output codex "$TD/nothing.jsonl" "$TD/missing.txt" "$TD/nothing.canon.json" 0
    [[ "$(jq -r .is_error "$TD/nothing.canon.json")" == "true" ]] && ok "empty output is error" || ng "empty output is error"
    printf '%s\n' '{"type":"thread.started","thread_id":"thr_m"}' '{"type":"item.completed","item":{"id":"a","type":"agent_message","text":"line one"}}' '{"type":"item.completed","item":{"id":"b","type":"agent_message","text":"line two"}}' > "$TD/multi.jsonl"
    normalize_agent_output codex "$TD/multi.jsonl" "$TD/missing.txt" "$TD/multi.canon.json" 0
    [[ "$(jq -r .result "$TD/multi.canon.json")" == "$(printf 'line one\nline two')" ]] && ok "codex message join" || ng "codex message join"

    # --- normalize: devin export (cumulative; this turn only), fallbacks ---
    cat > "$TD/devin_export2.json" <<'EOF'
{"schema_version":"ATIF-v1.7","session_id":"calm-otter","steps":[
 {"step_id":1,"source":"system","message":"sys"},
 {"step_id":2,"source":"user","message":"first prompt"},
 {"step_id":3,"source":"agent","message":"OLD TURN"},
 {"step_id":4,"source":"user","message":"second prompt"},
 {"step_id":5,"source":"agent","message":"working on it"},
 {"step_id":6,"source":"agent","message":"done\n---RALPH_STATUS---\nSTATUS: COMPLETE\nEXIT_SIGNAL: true\n---END_RALPH_STATUS---"}]}
EOF
    printf 'working on itdone\n' > "$TD/devin_raw.txt"
    normalize_agent_output devin "$TD/devin_raw.txt" "$TD/devin_export2.json" "$TD/devin.canon.json" 0
    [[ "$(jq -r .sessionId "$TD/devin.canon.json")" == "calm-otter" ]] && ok "devin session extraction" || ng "devin session extraction"
    jq -e '.result | contains("OLD TURN") | not' "$TD/devin.canon.json" >/dev/null && ok "devin current turn only" || ng "devin current turn only"
    jq -e '.result | test("(^|\n)---RALPH_STATUS---\n")' "$TD/devin.canon.json" >/dev/null && ok "devin status block on own line" || ng "devin status block on own line"
    [[ "$(jq -r .is_error "$TD/devin.canon.json")" == "false" ]] && ok "devin no-error flag" || ng "devin no-error flag"
    printf 'plain answer\nwarning: something harmless\n' > "$TD/devin_raw2.txt"
    normalize_agent_output devin "$TD/devin_raw2.txt" "$TD/missing.json" "$TD/devin.canon2.json" 0
    [[ "$(jq -r .result "$TD/devin.canon2.json")" == "plain answer" ]] && ok "devin stdout fallback" || ng "devin stdout fallback"
    printf "I'll do it.\nwarning: rejected a tool call that requires confirmation. Running in non-interactive mode.\n" > "$TD/devin_raw3.txt"
    normalize_agent_output devin "$TD/devin_raw3.txt" "$TD/missing.json" "$TD/devin.canon3.json" 0
    [[ "$(jq -r .is_error "$TD/devin.canon3.json")" == "true" ]] && ok "devin rejection is error" || ng "devin rejection is error"
    printf "Note: the CLI prints 'warning: rejected a tool call that requires confirmation' on denial.\n" > "$TD/devin_raw3b.txt"
    normalize_agent_output devin "$TD/devin_raw3b.txt" "$TD/missing.json" "$TD/devin.canon3b.json" 0
    [[ "$(jq -r .is_error "$TD/devin.canon3b.json")" == "false" ]] && ok "devin quoted warning not error" || ng "devin quoted warning not error"
    normalize_agent_output devin "$TD/devin_raw2.txt" "$TD/missing.json" "$TD/devin.canon4.json" 1
    [[ "$(jq -r .is_error "$TD/devin.canon4.json")" == "true" ]] && ok "devin exit code error" || ng "devin exit code error"
    normalize_agent_output devin "$TD/devin_raw2.txt" "$TD/devin_export2.json" "$TD/devin.canon5.json" 0
    analyze_response "$TD/devin.canon5.json" 1 "$TD/.response_analysis_devin" >/dev/null 2>&1
    [[ "$(jq -r .analysis.exit_signal "$TD/.response_analysis_devin" 2>/dev/null)" == "true" ]] && ok "devin e2e exit signal" || ng "devin e2e exit signal"

    # --- end-to-end: canonical JSON feeds analyze_response ---
    cat > "$TD/e2e.json" <<'EOF'
{"type":"text","sessionID":"ses_e2e","part":{"type":"text","text":"work finished\n---RALPH_STATUS---\nSTATUS: COMPLETE\nTASKS_COMPLETED_THIS_LOOP: 1\nFILES_MODIFIED: 0\nTESTS_STATUS: NOT_RUN\nWORK_TYPE: DOCUMENTATION\nEXIT_SIGNAL: true\nRECOMMENDATION: proceed\n---END_RALPH_STATUS---\n"}}
EOF
    normalize_agent_output opencode "$TD/e2e.json" "" "$TD/e2e.canon.json" 0
    analyze_response "$TD/e2e.canon.json" 1 "$TD/.response_analysis" >/dev/null 2>&1
    [[ "$(jq -r .analysis.exit_signal "$TD/.response_analysis" 2>/dev/null)" == "true" ]] && ok "e2e exit signal" || ng "e2e exit signal"
) >"$TMP_OUT" 2>&1
sub_status=$?

pass=$(grep -c '^ok: ' "$TMP_OUT" || true)
fail=$(grep -c '^FAIL: ' "$TMP_OUT" || true)
cat "$TMP_OUT"
rm -f "$TMP_OUT"
echo "pass=$pass fail=$fail"
[[ $fail -eq 0 && $sub_status -eq 0 ]]
