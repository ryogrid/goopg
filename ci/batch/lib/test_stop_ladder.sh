#!/usr/bin/env bash
# test_stop_ladder.sh — exercises every rung of common.sh's stop_goopg_server.
#
# Regression guard for the 2026-07-29 nightly wedge: stage-tpch.sh's cleanup did
# an UNTIMED `wait ${server_pid}` after `goopg stop`. A backend leaked by the
# per-query TIMEOUT (Q13) kept the graceful stop from ever returning, so the
# stage held a 16-core host for 6h45m after its sweep had already finished.
# The ladder must therefore terminate on its own no matter how wedged the server
# is — that is exactly what these four cases assert.
#
# Standalone: `bash ci/batch/lib/test_stop_ladder.sh` (no goopg build needed —
# the "server" is a sleep and the "goopg binary" is a stub).
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
# shellcheck source=/dev/null
source "${REPO_ROOT}/ci/batch/lib/common.sh"

TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
fails=0

# make_stub <name> <body> — a stand-in for the goopg binary. It is invoked as
# `<stub> stop -D <dir> [-mode immediate]`, exactly as stop_goopg_server calls it.
make_stub() {
    printf '#!/usr/bin/env bash\n%s\n' "$2" > "${TMP}/$1"
    chmod +x "${TMP}/$1"
}

check() {  # check <case> <want_rung> <got_rung> <max_elapsed_s> <elapsed_s>
    if [[ "$2" != "$3" ]]; then
        echo "FAIL[$1]: want rung '$2', got '$3'"; fails=$(( fails + 1 )); return
    fi
    if (( $5 > $4 )); then
        echo "FAIL[$1]: rung '$3' correct but took ${5}s (max ${4}s)"; fails=$(( fails + 1 )); return
    fi
    echo "ok[$1]: rung=$3 in ${5}s"
}

run_case() {  # run_case <name> <stub_body> <server_cmd> <want> <max_s>
    local name="$1" stub="$2" servercmd="$3" want="$4" max="$5"
    make_stub goopg-stub "${stub}"
    bash -c "${servercmd}" &
    local pid=$! t0 t1
    sleep 0.3
    t0=$(date +%s)
    stop_goopg_server "${TMP}/goopg-stub" "${TMP}/data" "${pid}" 3 3
    t1=$(date +%s)
    check "${name}" "${want}" "${STOP_RUNG}" "${max}" "$(( t1 - t0 ))"
}

# The server obeys the graceful stop: the stub kills it, as the control socket
# would. Must NOT escalate.
run_case graceful \
    'kill -TERM $(cat '"${TMP}"'/pid) 2>/dev/null; exit 0' \
    'echo $BASHPID > '"${TMP}"'/pid; exec sleep 300' \
    graceful 5

# The server ignores a plain stop but honours `-mode immediate` — the rung that
# exists because the graceful path is the one that blocks on a leaked backend.
run_case immediate \
    '[[ " $* " == *" -mode immediate "* ]] && kill -TERM $(cat '"${TMP}"'/pid) 2>/dev/null; exit 0' \
    'echo $BASHPID > '"${TMP}"'/pid; exec sleep 300' \
    immediate 9

# The control socket is dead entirely (the 2026-07-29 shape: stop returns but the
# process never exits). Falls through to SIGTERM.
run_case sigterm \
    'exit 0' \
    'exec sleep 300' \
    sigterm 12

# Worst case: SIGTERM is swallowed too — only SIGKILL is left. This is the case
# that guarantees a bounded exit no matter what the server is doing.
run_case sigkill \
    'exit 0' \
    'trap "" TERM; sleep 300' \
    sigkill 45

# A server that already exited must be recognised without any stop attempt, and
# without being fooled by the un-reaped zombie that `kill -0` still answers for.
make_stub goopg-stub 'echo "STUB MUST NOT BE CALLED" >&2; exit 1'
sleep 0.1 &
zpid=$!
sleep 0.5   # exited, but not yet waited on
t0=$(date +%s)
stop_goopg_server "${TMP}/goopg-stub" "${TMP}/data" "${zpid}" 3 3
t1=$(date +%s)
check already-exited already-exited "${STOP_RUNG}" 2 "$(( t1 - t0 ))"

# The whole point of the dump hook: when the graceful rung misses its budget the
# server is still alive, and that is the last moment it can be inspected. Serve a
# stand-in pprof endpoint and assert the dump lands in STOP_DUMP_FILE.
mkdir -p "${TMP}/www/debug/pprof"
echo "goroutine 1 [chan receive, 405 minutes]:" > "${TMP}/www/debug/pprof/goroutine"
( cd "${TMP}/www" && exec python3 -m http.server 6162 --bind 127.0.0.1 ) >/dev/null 2>&1 &
www_pid=$!
for _ in $(seq 1 25); do
    curl -sf --max-time 1 http://127.0.0.1:6162/debug/pprof/goroutine >/dev/null 2>&1 && break
    sleep 0.2
done
make_stub goopg-stub 'exit 0'          # stop never works -> escalate past graceful
sleep 300 &
dpid=$!
sleep 0.3
STOP_PPROF_ADDR="127.0.0.1:6162"
STOP_DUMP_FILE="${TMP}/dump.txt"
stop_goopg_server "${TMP}/goopg-stub" "${TMP}/data" "${dpid}" 2 2 >/dev/null
unset STOP_PPROF_ADDR STOP_DUMP_FILE
kill "${www_pid}" 2>/dev/null || true
if [[ -s "${TMP}/dump.txt" ]] && grep -q 'chan receive' "${TMP}/dump.txt"; then
    echo "ok[dump-capture]: goroutine dump saved before escalation"
else
    echo "FAIL[dump-capture]: no dump written to ${TMP}/dump.txt"; fails=$(( fails + 1 ))
fi

if (( fails > 0 )); then
    echo "FAILED: ${fails} case(s)"; exit 1
fi
echo "PASS: all stop-ladder rungs bounded and correct"
