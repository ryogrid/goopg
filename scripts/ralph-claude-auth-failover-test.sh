#!/usr/bin/env bash
# ralph-claude-auth-failover-test.sh - self-test for the Claude API-token
# failover in ralph_loop.sh ("CLAUDE API-TOKEN FAILOVER"): token validation,
# mode selection, per-mode resume ids, budget/invalid/429 classification, the
# retry window, and the credential environment bracket. Canned outputs only —
# no agent API calls are made.
# Usage: scripts/ralph-claude-auth-failover-test.sh   (exit 0 = all cases pass)
set -uo pipefail

ENGINE="${RALPH_HOME:-$HOME/.ralph}/ralph_loop.sh"

if [[ ! -f "$ENGINE" ]]; then
    echo "FAIL: Ralph engine not found: $ENGINE (set RALPH_HOME to override)"
    exit 1
fi

TMP_OUT="$(mktemp)"

# Runs in a subshell: sourcing the engine overrides globals (see
# ralph-agent-adapter-test.sh).
(
    ok() { echo "ok: $1"; }
    ng() { echo "FAIL: $1"; }
    check() { local name=$1; shift; if "$@"; then ok "$name"; else ng "$name"; fi; }

    unset ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN CLAUDE_CODE_OAUTH_TOKEN \
          CLAUDE_CODE_USE_BEDROCK CLAUDE_CODE_USE_VERTEX CLAUDE_CODE_USE_FOUNDRY
    source "$ENGINE"

    TD="$(mktemp -d)"
    trap 'rm -rf "$TD"' EXIT
    RALPH_DIR="$TD"
    LOG_DIR="$TD/logs"
    mkdir -p "$LOG_DIR"
    PROGRESS_FILE="$TD/progress.json"
    CLAUDE_SESSION_FILE_SUBSCRIPTION="$TD/.claude_session_id"
    CLAUDE_SESSION_FILE_API="$TD/.claude_session_id.api"
    CLAUDE_SESSION_FILE="$CLAUDE_SESSION_FILE_SUBSCRIPTION"
    CLAUDE_AUTH_STATE_FILE="$TD/.claude_auth_state.json"
    RALPH_SESSION_FILE="$TD/.ralph_session"
    RALPH_SESSION_HISTORY_FILE="$TD/.ralph_session_history"
    EXIT_SIGNALS_FILE="$TD/.exit_signals"
    RESPONSE_ANALYSIS_FILE="$TD/.response_analysis"
    send_notification() { :; }
    AGENT_BACKEND=claude
    CLAUDE_API_FAILOVER=true
    CLAUDE_API_RETRY_HOURS=24
    CLAUDE_API_SHORT_RETRY_HOURS=1
    CLAUDE_CODE_CMD=claude
    unset ANTHROPIC_BASE_URL
    CLAUDE_API_TOKEN_VAR=TEST_FAILOVER_TOKEN
    KEY_A="sk-ant-api03-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
    KEY_B="sk-ant-api03-BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"

    # --- token validation ------------------------------------------------------
    check "console key accepted" claude_api_token_valid "$KEY_A"
    check "usr key accepted" claude_api_token_valid "sk-ant-usr-1AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
    check "admin key rejected" eval '! claude_api_token_valid "sk-ant-admin01-AAAAAAAAAAAAAAAAAAAAAAAAAAAA"'
    check "missing secret rejected" eval '! claude_api_token_valid "sk-ant-api03-"'
    check "oauth token rejected" eval '! claude_api_token_valid "sk-ant-oat01-AAAAAAAAAAAAAAAAAAAAAAAAAAAA"'
    check "garbage rejected" eval '! claude_api_token_valid "hello"'

    # --- init: unset / empty / malformed / bad var name / cloud / disabled ------
    unset TEST_FAILOVER_TOKEN
    init_claude_api_failover 2>/dev/null
    check "unset token -> inactive" [ "$CLAUDE_API_FAILOVER_ACTIVE" = false ]
    export TEST_FAILOVER_TOKEN=""
    init_claude_api_failover 2>/dev/null
    check "empty token -> inactive" [ "$CLAUDE_API_FAILOVER_ACTIVE" = false ]
    export TEST_FAILOVER_TOKEN="not-a-key"
    init_claude_api_failover 2>/dev/null
    check "malformed token -> inactive" [ "$CLAUDE_API_FAILOVER_ACTIVE" = false ]
    check "malformed token un-exported" eval '! env | grep -q "^TEST_FAILOVER_TOKEN="'
    CLAUDE_API_TOKEN_VAR='BAD-NAME'
    init_claude_api_failover 2>/dev/null
    check "invalid var name -> inactive" [ "$CLAUDE_API_FAILOVER_ACTIVE" = false ]
    CLAUDE_API_TOKEN_VAR=TEST_FAILOVER_TOKEN
    export TEST_FAILOVER_TOKEN="$KEY_A"
    export CLAUDE_CODE_USE_BEDROCK=1
    init_claude_api_failover 2>/dev/null
    check "bedrock set -> inactive" [ "$CLAUDE_API_FAILOVER_ACTIVE" = false ]
    unset CLAUDE_CODE_USE_BEDROCK
    export TEST_FAILOVER_TOKEN="$KEY_A"
    CLAUDE_API_FAILOVER=false
    init_claude_api_failover 2>/dev/null
    check "CLAUDE_API_FAILOVER=false -> inactive" [ "$CLAUDE_API_FAILOVER_ACTIVE" = false ]
    CLAUDE_API_FAILOVER=true
    AGENT_BACKEND=codex
    init_claude_api_failover 2>/dev/null
    check "non-claude backend -> inactive" [ "$CLAUDE_API_FAILOVER_ACTIVE" = false ]
    AGENT_BACKEND=claude
    export ANTHROPIC_BASE_URL=http://127.0.0.1:8787
    init_claude_api_failover 2>/dev/null
    check "ANTHROPIC_BASE_URL set -> inactive" [ "$CLAUDE_API_FAILOVER_ACTIVE" = false ]
    unset ANTHROPIC_BASE_URL
    CLAUDE_CODE_CMD="$HOME/.ralph/.claude-with-dseek.sh"
    init_claude_api_failover 2>/dev/null
    check "wrapper CLAUDE_CODE_CMD -> inactive" [ "$CLAUDE_API_FAILOVER_ACTIVE" = false ]
    CLAUDE_CODE_CMD="/usr/local/bin/claude --permission-mode bypassPermissions"
    init_claude_api_failover 2>/dev/null
    check "claude with flags -> active" [ "$CLAUDE_API_FAILOVER_ACTIVE" = true ]
    CLAUDE_CODE_CMD=claude
    CLAUDE_API_TOKEN_VAR=ANTHROPIC_API_KEY
    init_claude_api_failover 2>/dev/null
    check "token var named ANTHROPIC_API_KEY -> inactive" [ "$CLAUDE_API_FAILOVER_ACTIVE" = false ]
    CLAUDE_API_TOKEN_VAR=TEST_FAILOVER_TOKEN
    CLAUDE_API_FAILOVER=yes
    CLAUDE_API_SHORT_RETRY_HOURS=08
    init_claude_api_failover 2>/dev/null
    check "CLAUDE_API_FAILOVER=yes -> active" [ "$CLAUDE_API_FAILOVER_ACTIVE" = true ]
    check "hours 08 read as decimal 8" [ "$CLAUDE_API_SHORT_RETRY_HOURS" = 8 ]
    CLAUDE_API_SHORT_RETRY_HOURS=1
    CLAUDE_API_FAILOVER=true
    CLAUDE_API_RETRY_HOURS=abc
    export TEST_FAILOVER_TOKEN="  $KEY_A
"
    init_claude_api_failover 2>/dev/null
    check "valid token (whitespace trimmed) -> active" [ "$CLAUDE_API_FAILOVER_ACTIVE" = true ]
    check "bad retry hours -> 24" [ "$CLAUDE_API_RETRY_HOURS" = 24 ]
    check "token un-exported" eval '! env | grep -q "^TEST_FAILOVER_TOKEN="'
    check "token still readable in the shell" [ -n "${TEST_FAILOVER_TOKEN:-}" ]

    # --- mode selection + per-mode resume ids ------------------------------------
    select_claude_auth_mode 2>/dev/null
    check "no block -> api mode" [ "$CLAUDE_AUTH_MODE" = api ]
    check "api mode uses the api resume id" [ "$CLAUDE_SESSION_FILE" = "$CLAUDE_SESSION_FILE_API" ]
    check "agent_session_file follows the mode" [ "$(agent_session_file claude)" = "$CLAUDE_SESSION_FILE_API" ]
    check "response_analyzer follows the mode" [ "$(session_file_for_backend)" = "$CLAUDE_SESSION_FILE_API" ]

    # --- environment bracket -------------------------------------------------------
    export ANTHROPIC_AUTH_TOKEN="outer-bearer"
    claude_auth_env_apply
    check "api env: key exported" [ "$(printenv ANTHROPIC_API_KEY)" = "$KEY_A" ]
    check "api env: bearer suspended" eval '[ -z "$(printenv ANTHROPIC_AUTH_TOKEN)" ]'
    claude_auth_env_restore
    check "restore: key removed" eval '[ -z "$(printenv ANTHROPIC_API_KEY)" ]'
    check "restore: bearer back" [ "$(printenv ANTHROPIC_AUTH_TOKEN)" = "outer-bearer" ]
    unset ANTHROPIC_AUTH_TOKEN

    # --- classification ------------------------------------------------------------
    printf '%s\n' '{"type":"result","is_error":true,"result":"Credit balance is too low","session_id":"x"}' > "$TD/credit.json"
    check "credit balance -> budget" [ "$(classify_claude_api_failure "$TD/credit.json")" = budget ]
    printf '%s\n' '{"type":"result","is_error":true,"api_error_status":400,"result":"API Error: 400 You have reached your specified API usage limits. You will regain access on 2026-11-01 at 00:00 UTC."}' > "$TD/spend.json"
    check "spend limit -> budget" [ "$(classify_claude_api_failure "$TD/spend.json")" = budget ]
    printf '%s\n' '{"type":"result","is_error":true,"api_error_status":401,"result":"Invalid API key · Fix external API key"}' > "$TD/inv.json"
    check "401 -> invalid" [ "$(classify_claude_api_failure "$TD/inv.json")" = invalid ]
    printf '%s\n' '{"type":"result","is_error":true,"api_error_status":429,"result":"API Error: 429 rate_limit_error"}' > "$TD/rl.json"
    check "plain 429 -> 429" [ "$(classify_claude_api_failure "$TD/rl.json")" = 429 ]
    printf '%s\n' 'not json' '{"type":"assistant","message":"credit balance is too low"}' '{"type":"user","tool_result":"Credit balance is too low"}' '{"type":"result","is_error":false,"result":"done"}' > "$TD/stream_ok.log"
    check "tool/assistant text never classifies" [ -z "$(classify_claude_api_failure "$TD/stream_ok.log")" ]
    printf '%s\n' 'not json' '{"type":"system","session_id":"s"}' '{"type":"result","is_error":true,"result":"Credit balance is too low"}' > "$TD/stream_err.log"
    check "stream with an error result -> budget" [ "$(classify_claude_api_failure "$TD/stream_err.log")" = budget ]
    printf '%s\n' '{"type":"result","is_error":true,"result":"You'"'"'ve hit your session limit · resets 3:10am"}' > "$TD/sub.json"
    check "subscription-limit text is not budget/invalid" [ "$(classify_claude_api_failure "$TD/sub.json")" = other ]

    # --- handle: 429 streak, then budget block ----------------------------------------
    CLAUDE_AUTH_MODE=api
    handle_claude_api_failure "$TD/rl.json" 2>/dev/null
    check "429 #1 -> transient rc 1" [ "$CLAUDE_API_FAILURE_RC" = 1 ]
    check "429 #1 -> no block" [ ! -f "$CLAUDE_AUTH_STATE_FILE" ]
    handle_claude_api_failure "$TD/rl.json" 2>/dev/null
    handle_claude_api_failure "$TD/rl.json" 2>/dev/null
    check "429 #3 -> failover rc 4" [ "$CLAUDE_API_FAILURE_RC" = 4 ]
    check "429 #3 -> rate_limit block" [ "$(jq -r .reason "$CLAUDE_AUTH_STATE_FILE")" = rate_limit ]
    check "rate_limit block uses the short window" [ "$(jq -r .retry_after_hours "$CLAUDE_AUTH_STATE_FILE")" = 1 ]
    jq '.blocked_at -= 3700' "$CLAUDE_AUTH_STATE_FILE" > "$TD/s" && mv "$TD/s" "$CLAUDE_AUTH_STATE_FILE"
    _CLAUDE_API_BLOCKED_UNTIL_MEM=0
    check "rate_limit block expires after 1h" claude_api_mode_available
    rm -f "$CLAUDE_AUTH_STATE_FILE"

    # other api-mode errors: generic path twice, then an api_error block
    printf '%s\n' '{"type":"result","is_error":true,"api_error_status":404,"result":"API Error: 404 model not found"}' > "$TD/nf.json"
    check "404 -> other" [ "$(classify_claude_api_failure "$TD/nf.json")" = other ]
    check "other #1 -> caller handles" eval '! handle_claude_api_failure "$TD/nf.json" 2>/dev/null'
    check "other #2 -> caller handles" eval '! handle_claude_api_failure "$TD/nf.json" 2>/dev/null'
    handle_claude_api_failure "$TD/nf.json" 2>/dev/null
    check "other #3 -> failover" [ "$CLAUDE_API_FAILURE_RC" = 4 ]
    check "other #3 -> api_error block" [ "$(jq -r .reason "$CLAUDE_AUTH_STATE_FILE")" = api_error ]
    rm -f "$CLAUDE_AUTH_STATE_FILE"; _CLAUDE_API_BLOCKED_UNTIL_MEM=0

    # a 429 then an unrelated error resets the 429 streak
    handle_claude_api_failure "$TD/rl.json" 2>/dev/null
    handle_claude_api_failure "$TD/rl.json" 2>/dev/null
    handle_claude_api_failure "$TD/nf.json" 2>/dev/null
    handle_claude_api_failure "$TD/rl.json" 2>/dev/null
    check "interrupted 429 streak does not block" [ ! -f "$CLAUDE_AUTH_STATE_FILE" ]
    clear_claude_api_block 2>/dev/null

    # invalid-key wording with a non-401 status is not "invalid"
    printf '%s\n' '{"type":"result","is_error":true,"api_error_status":500,"result":"authentication_error upstream"}' > "$TD/500.json"
    check "500 with auth wording -> other" [ "$(classify_claude_api_failure "$TD/500.json")" = other ]
    # a plain-text failure before any JSON
    printf 'Invalid API key · Fix external API key\n' > "$TD/plain.txt"
    check "plain-text invalid key -> invalid" [ "$(classify_claude_api_failure "$TD/plain.txt")" = invalid ]

    # the per-call --max-budget-usd cap is a normal stop: keep the session
    printf '%s\n' '{"type":"result","subtype":"error_max_budget_usd","is_error":true,"result":"Reached maximum budget ($5)","session_id":"33333333-3333-3333-3333-333333333333"}' > "$TD/mb.json"
    handle_claude_api_failure "$TD/mb.json" 2>/dev/null
    check "max_budget -> rc 1, no block" eval '[ "$CLAUDE_API_FAILURE_RC" = 1 ] && [ ! -f "$CLAUDE_AUTH_STATE_FILE" ]'
    check "max_budget keeps the session id" [ "$(cat "$CLAUDE_SESSION_FILE_API")" = 33333333-3333-3333-3333-333333333333 ]
    rm -f "$CLAUDE_SESSION_FILE_API"

    # an unwritable state file still blocks (in memory)
    SAVED_STATE="$CLAUDE_AUTH_STATE_FILE"
    CLAUDE_AUTH_STATE_FILE="$TD/no-such-dir/state.json"
    handle_claude_api_failure "$TD/credit.json" 2>/dev/null
    check "unwritable state -> still rc 4" [ "$CLAUDE_API_FAILURE_RC" = 4 ]
    check "unwritable state -> blocked in memory" eval '! claude_api_mode_available'
    CLAUDE_AUTH_STATE_FILE="$SAVED_STATE"
    _CLAUDE_API_BLOCKED_UNTIL_MEM=0

    handle_claude_api_failure "$TD/credit.json" 2>/dev/null
    check "budget -> rc 4" [ "$CLAUDE_API_FAILURE_RC" = 4 ]
    check "block keyed by fingerprint" [ "$(jq -r .fingerprint "$CLAUDE_AUTH_STATE_FILE")" = "$_CLAUDE_API_TOKEN_FP" ]
    check "key never written to disk" eval '! grep -rq "AAAAAAAAAAAAAAAAAAAA" "$TD"'
    check "non-API failure -> not handled" eval '! handle_claude_api_failure "$TD/sub.json" 2>/dev/null'
    CLAUDE_AUTH_MODE=subscription
    check "subscription mode -> never handled" eval '! handle_claude_api_failure "$TD/credit.json" 2>/dev/null'

    select_claude_auth_mode 2>/dev/null
    check "blocked -> subscription mode" [ "$CLAUDE_AUTH_MODE" = subscription ]
    check "subscription uses its own resume id" [ "$CLAUDE_SESSION_FILE" = "$CLAUDE_SESSION_FILE_SUBSCRIPTION" ]
    check "analyzer follows subscription" [ "$(session_file_for_backend)" = "$CLAUDE_SESSION_FILE_SUBSCRIPTION" ]
    export ANTHROPIC_API_KEY="sk-inherited" CLAUDE_CODE_OAUTH_TOKEN="oat-inherited"
    claude_auth_env_apply
    check "subscription env: inherited API key suspended" eval '[ -z "$(printenv ANTHROPIC_API_KEY)" ]'
    check "subscription env: oauth token kept" [ "$(printenv CLAUDE_CODE_OAUTH_TOKEN)" = oat-inherited ]
    claude_auth_env_restore
    check "subscription restore: inherited key back" [ "$(printenv ANTHROPIC_API_KEY)" = sk-inherited ]
    unset ANTHROPIC_API_KEY CLAUDE_CODE_OAUTH_TOKEN

    # --- retry window ----------------------------------------------------------------
    jq '.blocked_at -= 23*3600' "$CLAUDE_AUTH_STATE_FILE" > "$TD/s" && mv "$TD/s" "$CLAUDE_AUTH_STATE_FILE"
    check "23h after block -> still subscription" eval '! claude_api_mode_available'
    jq '.blocked_at -= 2*3600' "$CLAUDE_AUTH_STATE_FILE" > "$TD/s" && mv "$TD/s" "$CLAUDE_AUTH_STATE_FILE"
    check "25h after block -> api available" claude_api_mode_available
    select_claude_auth_mode 2>/dev/null
    check "25h after block -> api mode" [ "$CLAUDE_AUTH_MODE" = api ]
    clear_claude_api_block 2>/dev/null
    check "api success clears the block" [ ! -f "$CLAUDE_AUTH_STATE_FILE" ]

    # a changed token is tried at once
    CLAUDE_AUTH_MODE=api
    handle_claude_api_failure "$TD/inv.json" 2>/dev/null
    check "invalid -> block" [ "$(jq -r .reason "$CLAUDE_AUTH_STATE_FILE")" = invalid ]
    check "invalid -> subscription" eval '! claude_api_mode_available'
    export TEST_FAILOVER_TOKEN="$KEY_B"
    init_claude_api_failover 2>/dev/null
    check "new token value -> api available at once" claude_api_mode_available

    # --- per-mode resume ids survive each other ---------------------------------------
    printf '11111111-1111-1111-1111-111111111111' > "$CLAUDE_SESSION_FILE_SUBSCRIPTION"
    printf '22222222-2222-2222-2222-222222222222' > "$CLAUDE_SESSION_FILE_API"
    CLAUDE_USE_CONTINUE=true
    CLAUDE_SESSION_FILE="$CLAUDE_SESSION_FILE_API"
    check "api resumes its own id" [ "$(init_agent_session claude 2>/dev/null)" = 22222222-2222-2222-2222-222222222222 ]
    CLAUDE_SESSION_FILE="$CLAUDE_SESSION_FILE_SUBSCRIPTION"
    check "subscription resumes its own id" [ "$(init_agent_session claude 2>/dev/null)" = 11111111-1111-1111-1111-111111111111 ]
    reset_session test current 2>/dev/null
    check "reset_session current keeps the other id" eval '[ ! -f "$CLAUDE_SESSION_FILE_SUBSCRIPTION" ] && [ -f "$CLAUDE_SESSION_FILE_API" ]'
    printf '11111111-1111-1111-1111-111111111111' > "$CLAUDE_SESSION_FILE_SUBSCRIPTION"
    reset_session test 2>/dev/null
    check "reset_session clears both ids" eval '[ ! -f "$CLAUDE_SESSION_FILE_SUBSCRIPTION" ] && [ ! -f "$CLAUDE_SESSION_FILE_API" ]'

    # --- build_claude_command: --max-budget-usd only in api mode ----------------------
    printf 'prompt\n' > "$TD/PROMPT.md"
    CLAUDE_API_MAX_BUDGET_USD=5
    CLAUDE_AUTH_MODE=api
    build_claude_command "$TD/PROMPT.md" "" "" 2>/dev/null
    check "api mode adds --max-budget-usd" eval '[[ " ${CLAUDE_CMD_ARGS[*]} " == *" --max-budget-usd 5 "* ]]'
    CLAUDE_AUTH_MODE=subscription
    build_claude_command "$TD/PROMPT.md" "" "" 2>/dev/null
    check "subscription omits --max-budget-usd" eval '[[ " ${CLAUDE_CMD_ARGS[*]} " != *"--max-budget-usd"* ]]'
) >"$TMP_OUT" 2>&1
sub_status=$?

pass=$(grep -c '^ok: ' "$TMP_OUT" || true)
fail=$(grep -c '^FAIL: ' "$TMP_OUT" || true)
cat "$TMP_OUT"
rm -f "$TMP_OUT"
echo "pass=$pass fail=$fail"
[[ $fail -eq 0 && $sub_status -eq 0 ]]
