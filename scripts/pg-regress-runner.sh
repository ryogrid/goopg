#!/usr/bin/env bash
#
# pg-regress-runner.sh — run a subset of PostgreSQL's upstream regression
# tests against goopg and report parity %.
#
# D-001 infrastructure: this script is the prerequisite for unlocking the
# deferred D-001 suite in docs/test-port/postgres-oracle-port-status.csv.
# As more of goopg's SQL surface area is implemented, the pass rate reported
# here will climb toward 100%.
#
# Usage:
#   # Run a curated "quick" set of self-contained type tests:
#   scripts/pg-regress-runner.sh
#
#   # Run specific named tests:
#   scripts/pg-regress-runner.sh int2 int4 float8 text
#
#   # Run all available tests (slow, many will fail initially):
#   scripts/pg-regress-runner.sh --all
#
#   # Show diff on failure instead of just FAIL:
#   scripts/pg-regress-runner.sh --verbose int2
#
#   # Use an already-running goopg (port 65433):
#   scripts/pg-regress-runner.sh --port 65433 int2
#
# Options:
#   --all              Run all 232 regress tests (not just the quick set).
#   --port PORT        Port of a running goopg (default: auto-start on 15435).
#   --verbose / -v     Print diff on FAIL.
#   --no-setup         Skip running test_setup.sql (for iterating on an
#                      already-initialised DB).
#   --stop-on-first    Exit after first failure.
#   --out-dir DIR      Directory to write per-test diff files (default: tmp/regress-diffs/).
#
# Test selection:
#   The default "quick" set covers self-contained type/operator tests that
#   do NOT require extensions, tablespaces, or complex server configuration.
#   Run with --all to see the full picture (expect many failures initially).
#
# Output format:
#   PASS  int2      (486 lines)
#   FAIL  int4      (diff in tmp/regress-diffs/int4.diff)
#   ...
#   ================================================
#   pg-regress-runner: 12/20 PASS (60.0% parity)
#
# Exit codes:
#   0  all selected tests passed
#   1  at least one test failed
#   2  operational failure
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
PG_BIN="${REPO_ROOT}/postgres/local_install/bin"
PG_LIB="${REPO_ROOT}/postgres/local_install/lib"
REGRESS_SQL="${REPO_ROOT}/postgres/src/test/regress/sql"
REGRESS_EXP="${REPO_ROOT}/postgres/src/test/regress/expected"

export PATH="${PG_BIN}:${PATH}"
export LD_LIBRARY_PATH="${PG_LIB}${LD_LIBRARY_PATH:+:${LD_LIBRARY_PATH}}"

# -------------------------------------------------------------------------- #
# Defaults
# -------------------------------------------------------------------------- #
PORT=15435
AUTO_START=1
RUN_ALL=0
VERBOSE=0
RUN_SETUP=1
STOP_ON_FIRST=0
OUT_DIR="${REPO_ROOT}/tmp/regress-diffs"
NAMED_TESTS=()

# Quick set: self-contained type/operator tests that don't require extensions,
# shared objects, tablespaces, or complex pre-existing state beyond test_setup.
QUICK_TESTS=(
    int2 int4 int8
    float4 float8
    numeric
    boolean
    char varchar
    text
    bit bitstring
    date time timetz
    timestamp timestamptz
    interval
    abstime reltime tinterval
    inet
    macaddr macaddr8
    money
    point lseg box path polygon circle
    geometry
    uuid
    json jsonb
    arrays
    multirangetypes
    rangetypes
    enum
    regex
    comments
    expressions
    select
    select_distinct
    select_having
    subselect
    union
    case
    limit
    aggregates
    groupingsets
    window
    join
    strings
    numerology
    type_sanity
)

# -------------------------------------------------------------------------- #
# Argument parsing
# -------------------------------------------------------------------------- #
while [[ $# -gt 0 ]]; do
    case "$1" in
        --all)           RUN_ALL=1; shift ;;
        --port)          PORT="$2"; AUTO_START=0; shift 2 ;;
        -v|--verbose)    VERBOSE=1; shift ;;
        --no-setup)      RUN_SETUP=0; shift ;;
        --stop-on-first) STOP_ON_FIRST=1; shift ;;
        --out-dir)       OUT_DIR="$2"; shift 2 ;;
        --) shift; NAMED_TESTS+=("$@"); break ;;
        -*) echo "Unknown option: $1" >&2; exit 2 ;;
        *)  NAMED_TESTS+=("$1"); shift ;;
    esac
done

# -------------------------------------------------------------------------- #
# Determine test list
# -------------------------------------------------------------------------- #
if [[ ${#NAMED_TESTS[@]} -gt 0 ]]; then
    TESTS=("${NAMED_TESTS[@]}")
elif [[ "$RUN_ALL" -eq 1 ]]; then
    # All tests with a .sql file and a corresponding .out file
    TESTS=()
    while IFS= read -r f; do
        name="$(basename "$f" .sql)"
        [[ -f "${REGRESS_EXP}/${name}.out" ]] && TESTS+=("$name")
    done < <(find "${REGRESS_SQL}" -maxdepth 1 -name '*.sql' ! -name 'test_setup.sql' | sort)
else
    TESTS=("${QUICK_TESTS[@]}")
fi

# -------------------------------------------------------------------------- #
# Server lifecycle
# -------------------------------------------------------------------------- #
GOOPG_DATADIR="${REPO_ROOT}/tmp/regress-goopg-data"
GOOPG_LOG="${REPO_ROOT}/tmp/regress-goopg.log"
SERVER_PID=""

cleanup() {
    [[ "$AUTO_START" -eq 0 ]] && return 0
    if [[ -n "$SERVER_PID" ]] && kill -0 "$SERVER_PID" 2>/dev/null; then
        kill -KILL "$SERVER_PID" 2>/dev/null || true
    fi
    if command -v systemctl >/dev/null 2>&1; then
        systemctl --user stop "goopg-regress-runner.scope" 2>/dev/null || true
        systemctl --user reset-failed "goopg-regress-runner.scope" 2>/dev/null || true
    fi
    rm -rf "${GOOPG_DATADIR}"
}
trap cleanup EXIT INT TERM

if [[ "$AUTO_START" -eq 1 ]]; then
    echo "pg-regress-runner: building goopg..."
    (cd "${REPO_ROOT}" && go build -o bin/goopg ./cmd/goopg 2>&1)

    echo "pg-regress-runner: initialising data dir..."
    rm -rf "${GOOPG_DATADIR}"
    "${REPO_ROOT}/bin/goopg" init -D "${GOOPG_DATADIR}"

    echo "pg-regress-runner: starting goopg on :${PORT}..."
    mkdir -p "$(dirname "${GOOPG_LOG}")"
    GOOPG_CG_UNIT="goopg-regress-runner" \
        "${REPO_ROOT}/scripts/goopg-test-run.sh" \
        "${REPO_ROOT}/bin/goopg" start \
        -D "${GOOPG_DATADIR}" --listen "127.0.0.1:${PORT}" \
        >"${GOOPG_LOG}" 2>&1 &

    timeout 30 bash -c \
        "until pg_isready -h 127.0.0.1 -p ${PORT} -U postgres -q; do sleep 1; done"

    SERVER_PID="$(head -1 "${GOOPG_DATADIR}/postmaster.pid" 2>/dev/null || true)"
    echo "pg-regress-runner: server ready (pid=${SERVER_PID})"
fi

# -------------------------------------------------------------------------- #
# Connectivity check
# -------------------------------------------------------------------------- #
if ! pg_isready -h 127.0.0.1 -p "${PORT}" -U postgres -q 2>/dev/null; then
    echo "pg-regress-runner: goopg not reachable on port ${PORT}" >&2
    exit 2
fi

PSQL=(psql -h 127.0.0.1 -p "${PORT}" -U postgres -d postgres --no-psqlrc -X -a -q)

# -------------------------------------------------------------------------- #
# Run test_setup.sql (best-effort: goopg may not support everything)
# -------------------------------------------------------------------------- #
if [[ "$RUN_SETUP" -eq 1 ]]; then
    echo "pg-regress-runner: running test_setup.sql (errors are expected for unimplemented features)..."
    # Strip \getenv and \set meta-commands (load external variables) which
    # are specific to pg_regress and not standard psql.
    SETUP_TMP="$(mktemp /tmp/regress-setup-XXXXXX.sql)"
    grep -vE '^\s*\\(getenv|set\s|setenv)' "${REGRESS_SQL}/test_setup.sql" > "${SETUP_TMP}" || true
    "${PSQL[@]}" -f "${SETUP_TMP}" >/dev/null 2>&1 || true
    rm -f "${SETUP_TMP}"
    echo "pg-regress-runner: setup done."
fi

# -------------------------------------------------------------------------- #
# Normalisation: applied to both actual and expected before diff.
# -------------------------------------------------------------------------- #
normalise_output() {
    sed \
        -e '/^Time: /d' \
        -e 's/[[:space:]]*$//' \
        -e '/^$/d' \
    | awk '
        {
            # Collapse internal multi-space runs to 2 spaces (column alignment)
            gsub(/  +/, "  ")
            print
        }
    '
}

# -------------------------------------------------------------------------- #
# Run one test
# Returns: 0=PASS, 1=FAIL, 2=SKIP
# -------------------------------------------------------------------------- #
mkdir -p "${OUT_DIR}"

run_test() {
    local name="$1"
    local sql_file="${REGRESS_SQL}/${name}.sql"
    local exp_file="${REGRESS_EXP}/${name}.out"
    local diff_file="${OUT_DIR}/${name}.diff"

    if [[ ! -f "$sql_file" ]]; then
        echo "SKIP  ${name}  (no .sql file)"
        return 2
    fi
    if [[ ! -f "$exp_file" ]]; then
        echo "SKIP  ${name}  (no .out file)"
        return 2
    fi

    local actual_raw actual_norm expected_norm
    actual_raw="$(mktemp)"
    actual_norm="$(mktemp)"
    expected_norm="$(mktemp)"

    # Run the .sql file; capture combined stdout+stderr
    "${PSQL[@]}" -f "${sql_file}" >"${actual_raw}" 2>&1 || true

    normalise_output < "${actual_raw}" > "${actual_norm}"
    normalise_output < "${exp_file}"   > "${expected_norm}"

    local exp_lines
    exp_lines="$(wc -l < "${expected_norm}")"

    if diff --unified=5 "${expected_norm}" "${actual_norm}" > "${diff_file}" 2>&1; then
        echo "PASS  ${name}  (${exp_lines} lines)"
        rm -f "${actual_raw}" "${actual_norm}" "${expected_norm}" "${diff_file}"
        return 0
    else
        local diff_lines
        diff_lines="$(wc -l < "${diff_file}")"
        echo "FAIL  ${name}  (diff: ${diff_lines} lines → ${diff_file})"
        if [[ "$VERBOSE" -eq 1 ]]; then
            cat "${diff_file}"
        fi
        rm -f "${actual_raw}" "${actual_norm}" "${expected_norm}"
        return 1
    fi
}

# -------------------------------------------------------------------------- #
# Main loop
# -------------------------------------------------------------------------- #
pass=0
fail=0
skip=0
total=${#TESTS[@]}

echo "pg-regress-runner: running ${total} tests against goopg:${PORT}"
echo "----------------------------------------------------------------------"

for t in "${TESTS[@]}"; do
    rc=0
    run_test "$t" || rc=$?
    case "$rc" in
        0) pass=$((pass + 1)) ;;
        1) fail=$((fail + 1)); [[ "$STOP_ON_FIRST" -eq 1 ]] && break ;;
        2) skip=$((skip + 1)) ;;
    esac
done

echo "======================================================================"
pct=0
if [[ $((pass + fail)) -gt 0 ]]; then
    pct=$(awk "BEGIN {printf \"%.1f\", ${pass}*100/(${pass}+${fail})}")
fi
echo "pg-regress-runner: ${pass}/$((pass+fail)) PASS (${pct}% parity, ${skip} skipped)"
echo "  diffs in: ${OUT_DIR}/"

if [[ $fail -gt 0 ]]; then
    exit 1
fi
exit 0
