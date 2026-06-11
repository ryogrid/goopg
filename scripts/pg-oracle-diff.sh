#!/usr/bin/env bash
#
# pg-oracle-diff.sh — run SQL against both goopg and vanilla PG 18.3, then
# diff the normalised output.  Any divergence is a goopg compatibility bug.
#
# Usage:
#   # Against running servers (goopg default 65433, PG default 5433):
#   scripts/pg-oracle-diff.sh path/to/query.sql [query2.sql ...]
#   scripts/pg-oracle-diff.sh --sql "SELECT 1+1"
#
#   # Auto-start fresh throwaway servers, teardown on exit:
#   scripts/pg-oracle-diff.sh --auto-start path/to/query.sql
#
#   # Point at specific ports (mix --auto-start with existing servers):
#   scripts/pg-oracle-diff.sh --goopg-port 65433 --pg-port 5433 query.sql
#
#   # Quiet summary only:
#   scripts/pg-oracle-diff.sh -q path/to/query.sql
#
# Options:
#   --auto-start        Start fresh goopg + PG instances, teardown on exit.
#   --goopg-port PORT   Port for goopg (default: 65433, or 0 for --auto-start).
#   --pg-port PORT      Port for vanilla PG 18.3 (default: 5433).
#   --db DB             Database name (default: postgres).
#   --user USER         Database user (default: postgres).
#   --sql SQL           Inline SQL to run (instead of a file).
#   -q / --quiet        Print only PASS/FAIL summary line per file.
#   --stop-on-first     Exit after first diff.
#
# Normalisation applied before diff:
#   - Strip "Time: N.NNN ms" lines (psql timing).
#   - Strip blank lines at the very start/end of output.
#   - Trim trailing whitespace per line.
#   - Normalise sequences of spaces inside aligned result tables to a
#     single canonical form (collapse multi-space runs in table rows so
#     minor column-width differences don't count as failures — structural
#     check only).
#   - Strip psql prompt lines (lines matching "^[a-z_]*[=#>]+ ").
#
# Exit codes:
#   0  all queries matched
#   1  at least one diff found
#   2  operational failure (server not reachable, missing binary, etc.)
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
PG_BIN="${REPO_ROOT}/postgres/local_install/bin"
PG_LIB="${REPO_ROOT}/postgres/local_install/lib"

export PATH="${PG_BIN}:${PATH}"
export LD_LIBRARY_PATH="${PG_LIB}${LD_LIBRARY_PATH:+:${LD_LIBRARY_PATH}}"

# -------------------------------------------------------------------------- #
# Defaults
# -------------------------------------------------------------------------- #
GOOPG_PORT=65433
PG_PORT=5433
DB=postgres
USER_=postgres
AUTO_START=0
QUIET=0
STOP_ON_FIRST=0
INLINE_SQL=""
SQL_FILES=()

# -------------------------------------------------------------------------- #
# Argument parsing
# -------------------------------------------------------------------------- #
while [[ $# -gt 0 ]]; do
    case "$1" in
        --auto-start)   AUTO_START=1; shift ;;
        --goopg-port)   GOOPG_PORT="$2"; shift 2 ;;
        --pg-port)      PG_PORT="$2"; shift 2 ;;
        --db)           DB="$2"; shift 2 ;;
        --user)         USER_="$2"; shift 2 ;;
        --sql)          INLINE_SQL="$2"; shift 2 ;;
        -q|--quiet)     QUIET=1; shift ;;
        --stop-on-first) STOP_ON_FIRST=1; shift ;;
        --) shift; SQL_FILES+=("$@"); break ;;
        -*) echo "Unknown option: $1" >&2; exit 2 ;;
        *)  SQL_FILES+=("$1"); shift ;;
    esac
done

if [[ -z "$INLINE_SQL" && ${#SQL_FILES[@]} -eq 0 ]]; then
    echo "pg-oracle-diff: no SQL file or --sql given" >&2
    echo "Usage: scripts/pg-oracle-diff.sh [options] <file.sql> [...]" >&2
    exit 2
fi

# -------------------------------------------------------------------------- #
# Auto-start helpers
# -------------------------------------------------------------------------- #
GOOPG_DATADIR="${REPO_ROOT}/tmp/oracle-diff-goopg-data"
PG_DATADIR="${REPO_ROOT}/tmp/oracle-diff-pg-data"
GOOPG_LOG="${REPO_ROOT}/tmp/oracle-diff-goopg.log"
PG_LOG="${REPO_ROOT}/tmp/oracle-diff-pg.log"

cleanup_servers() {
    [[ "$AUTO_START" -eq 0 ]] && return 0
    local gpid pgpid
    if [[ -f "${GOOPG_DATADIR}/postmaster.pid" ]]; then
        gpid="$(head -1 "${GOOPG_DATADIR}/postmaster.pid" 2>/dev/null || true)"
        [[ -n "$gpid" ]] && kill -KILL "$gpid" 2>/dev/null || true
    fi
    if [[ -f "${PG_DATADIR}/postmaster.pid" ]]; then
        pgpid="$(head -1 "${PG_DATADIR}/postmaster.pid" 2>/dev/null || true)"
        [[ -n "$pgpid" ]] && kill -KILL "$pgpid" 2>/dev/null || true
    fi
    # Release cgroup scope if present
    if command -v systemctl >/dev/null 2>&1; then
        systemctl --user stop "oracle-diff-goopg.scope" 2>/dev/null || true
        systemctl --user reset-failed "oracle-diff-goopg.scope" 2>/dev/null || true
    fi
    rm -rf "${GOOPG_DATADIR}" "${PG_DATADIR}"
}
trap cleanup_servers EXIT INT TERM

start_servers() {
    # Pick free ports if caller left the defaults
    GOOPG_PORT=15433
    PG_PORT=15434

    mkdir -p "$(dirname "${GOOPG_LOG}")"
    rm -rf "${GOOPG_DATADIR}" "${PG_DATADIR}"

    echo "pg-oracle-diff: building goopg..."
    (cd "${REPO_ROOT}" && go build -o bin/goopg ./cmd/goopg 2>&1)

    echo "pg-oracle-diff: initialising data dirs..."
    "${REPO_ROOT}/bin/goopg" init -D "${GOOPG_DATADIR}"
    initdb -D "${PG_DATADIR}" --no-sync -q

    echo "pg-oracle-diff: starting goopg on :${GOOPG_PORT}..."
    GOOPG_CG_UNIT="oracle-diff-goopg" \
        "${REPO_ROOT}/scripts/goopg-test-run.sh" \
        "${REPO_ROOT}/bin/goopg" start \
        -D "${GOOPG_DATADIR}" --listen "127.0.0.1:${GOOPG_PORT}" \
        >"${GOOPG_LOG}" 2>&1 &

    echo "pg-oracle-diff: starting vanilla PG 18.3 on :${PG_PORT}..."
    pg_ctl start -D "${PG_DATADIR}" -o "-p ${PG_PORT} -k /tmp" \
        -l "${PG_LOG}" -w -t 30

    echo "pg-oracle-diff: waiting for goopg readiness..."
    timeout 30 bash -c \
        "until pg_isready -h 127.0.0.1 -p ${GOOPG_PORT} -U ${USER_} -q; do sleep 1; done"

    echo "pg-oracle-diff: both servers ready."
}

if [[ "$AUTO_START" -eq 1 ]]; then
    start_servers
fi

# -------------------------------------------------------------------------- #
# Connectivity check
# -------------------------------------------------------------------------- #
check_server() {
    local name="$1" port="$2"
    if ! pg_isready -h 127.0.0.1 -p "$port" -U "$USER_" -q 2>/dev/null; then
        echo "pg-oracle-diff: $name not reachable on port $port (use --auto-start or start manually)" >&2
        exit 2
    fi
}

check_server "goopg" "${GOOPG_PORT}"
check_server "vanilla PG 18.3" "${PG_PORT}"

# -------------------------------------------------------------------------- #
# Normalisation pipeline
# Strip timing lines, trailing whitespace, collapse multi-space table cell
# separators (so minor column-width differences don't flag as mismatches),
# strip psql prompt lines, remove leading/trailing blank lines.
# -------------------------------------------------------------------------- #
normalise() {
    sed \
        -e '/^Time: /d' \
        -e 's/[[:space:]]*$//' \
        -e '/^[a-z_A-Z]*[=#>!]* *$/d' \
    | awk '
        BEGIN { buf = ""; first_nonblank = 0 }
        /^[[:space:]]*$/ { if (first_nonblank) buf = buf "\n"; next }
        {
            first_nonblank = 1
            if (buf != "") { print buf; buf = "" }
            # Collapse runs of 2+ spaces between word characters (table alignment)
            gsub(/  +/, "  ")
            print
        }
    '
}

# -------------------------------------------------------------------------- #
# Run one SQL snippet against both servers and compare
# -------------------------------------------------------------------------- #
run_and_diff() {
    local label="$1"
    local sql_arg="$2"    # either "-f path" or inline string

    local goopg_out pg_out tmpdir
    tmpdir="$(mktemp -d)"
    goopg_out="${tmpdir}/goopg.out"
    pg_out="${tmpdir}/pg.out"

    # Run against goopg
    if [[ "$sql_arg" == -f* ]]; then
        local f="${sql_arg#-f}"
        f="${f# }"
        psql -h 127.0.0.1 -p "${GOOPG_PORT}" -U "${USER_}" -d "${DB}" \
            -f "$f" --no-psqlrc 2>&1 | normalise > "${goopg_out}" || true
        psql -h 127.0.0.1 -p "${PG_PORT}"    -U "${USER_}" -d "${DB}" \
            -f "$f" --no-psqlrc 2>&1 | normalise > "${pg_out}" || true
    else
        psql -h 127.0.0.1 -p "${GOOPG_PORT}" -U "${USER_}" -d "${DB}" \
            -c "$sql_arg" --no-psqlrc 2>&1 | normalise > "${goopg_out}" || true
        psql -h 127.0.0.1 -p "${PG_PORT}"    -U "${USER_}" -d "${DB}" \
            -c "$sql_arg" --no-psqlrc 2>&1 | normalise > "${pg_out}" || true
    fi

    local diff_out
    if diff_out="$(diff --unified=3 "${pg_out}" "${goopg_out}" 2>&1)"; then
        [[ "$QUIET" -eq 0 ]] && echo "PASS  ${label}"
        rm -rf "${tmpdir}"
        return 0
    else
        echo "FAIL  ${label}"
        if [[ "$QUIET" -eq 0 ]]; then
            echo "--- PG 18.3 (expected)"
            echo "+++ goopg (actual)"
            echo "${diff_out}"
        fi
        rm -rf "${tmpdir}"
        return 1
    fi
}

# -------------------------------------------------------------------------- #
# Main loop
# -------------------------------------------------------------------------- #
pass=0
fail=0
total=0

if [[ -n "$INLINE_SQL" ]]; then
    total=$((total + 1))
    if run_and_diff "(inline SQL)" "$INLINE_SQL"; then
        pass=$((pass + 1))
    else
        fail=$((fail + 1))
        [[ "$STOP_ON_FIRST" -eq 1 ]] && { echo "pg-oracle-diff: stopping on first diff"; exit 1; }
    fi
fi

for f in "${SQL_FILES[@]}"; do
    total=$((total + 1))
    label="$(basename "$f")"
    if run_and_diff "$label" "-f ${f}"; then
        pass=$((pass + 1))
    else
        fail=$((fail + 1))
        [[ "$STOP_ON_FIRST" -eq 1 ]] && { echo "pg-oracle-diff: stopping on first diff"; exit 1; }
    fi
done

# -------------------------------------------------------------------------- #
# Summary
# -------------------------------------------------------------------------- #
echo "=================================================================="
if [[ $fail -eq 0 ]]; then
    echo "pg-oracle-diff: PASS  ${pass}/${total} queries matched"
    exit 0
else
    echo "pg-oracle-diff: FAIL  ${pass}/${total} passed, ${fail}/${total} diffed"
    exit 1
fi
