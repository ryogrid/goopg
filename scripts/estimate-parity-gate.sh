#!/usr/bin/env bash
# The estimate-accuracy (EA) parity ratchet — the gate four TODO_ALL items
# (C-05, C-10a, C-20a, C-21) cite and none of them could run.
#
# Ledger `take3-ea-ratchet-never-ran`: the previously cited instrument,
# `scripts/tpch-estimate-audit-arm.sh`, is invoked by no Makefile target, no
# hook, no precommit script and no ci/batch stage, and its default pinned PG
# baseline does not exist in the tree, so a default-flag run exits before it
# measures anything. It is also TPC-H-only, joinrel-granular (a base relation
# estimating rows=1 is not a candidate even in principle), and TPC-H has no
# LIMIT, so the NLI+Memoize shape that produces the aggregate over-estimates
# never arises in its corpus.
#
# THIS script measures est-vs-actual over the TPC-DS SF0.25 corpus with a
# PG-relative bar. See scripts/estimate-parity/parity.py for why the bar has
# to be PG-relative (an absolute bar fails Q47, where PG 18.3 emits the same
# rows=1, and a loose absolute bar passes Q99's 8007x).
#
#   make ea-ratchet                    # capture + ratchet against the pinned baseline
#   make ea-ratchet-repin              # capture + rewrite the baseline
#   EA_CAPTURE=<file> make ea-ratchet  # re-score an existing capture, no server
#
# ISOLATION. This script NEVER touches bench/tpcds/runtime_goopg/data-sf025 or
# port 65437: those belong to the standing SF0.25 gate and to peer agents, and
# two writers on one goopg data directory have damaged a bench cluster's WAL
# before. It runs against its OWN clone (EA_DATA) on its OWN port (EA_PORT),
# under the mandatory cgroup cap.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${REPO_ROOT}" || exit 2
# shellcheck source=/dev/null
source "${REPO_ROOT}/bench/tpcds/env_tpcds.sh"

EA_PORT="${EA_PORT:-5534}"
EA_DATA="${EA_DATA:-${REPO_ROOT}/tmp/c20a/data-sf025}"
EA_SRC_DATA="${EA_SRC_DATA:-${SF025_GOOPG_DATA}}"
EA_BIN="${EA_BIN:-${REPO_ROOT}/tmp/c20a/goopg-ea}"
EA_OUT="${EA_OUT:-${REPO_ROOT}/tmp/c20a}"
EA_CG_UNIT="${EA_CG_UNIT:-goopg-ea-ratchet}"
EA_QDIR="${EA_QDIR:-${TPCDS_DATA_DIR}/queries}"
EA_PGDIR="${EA_PGDIR:-${REPO_ROOT}/bench/tpcds/plans-pg}"
EA_BASELINE="${EA_BASELINE:-${REPO_ROOT}/analysis/planner-refactor-take3/c20a-estimator-census-20260922/ea-baseline.txt}"
EA_TIMEOUT="${EA_TIMEOUT:-300}"
EA_NQ="${EA_NQ:-99}"
# The planner is BLIND without an ANALYZE, and a blind planner would make
# every number this gate produces a measurement of the default selectivities
# rather than of the estimators. The seed is pinned so two runs sample the
# same rows.
export GOOPG_ANALYZE_SEED="${GOOPG_ANALYZE_SEED:-20260905}"
# env_tpcds.sh above already exported GOMEMLIMIT/GOGC; these override it on
# purpose. Its `GOGC=off` is the TIMING regime — it exists so the SF0.25 sweep
# does not measure garbage collection. This gate measures ROW COUNTS, for
# which GOGC=off is pure risk: one heavy query leaves the heap parked at
# GOMEMLIMIT and every later query runs against a thrashing or SIGKILLed
# server (CLAUDE.md, "sweep-tail collapse"; TPC-H Q21 needed exactly this
# pair to complete). Correctness over speed here.
export GOMEMLIMIT=12GiB
export GOGC=100

TABLES="call_center catalog_page catalog_returns catalog_sales customer
customer_address customer_demographics date_dim household_demographics
income_band inventory item promotion reason ship_mode store store_returns
store_sales time_dim warehouse web_page web_returns web_sales web_site"

log() { printf '[ea] %s\n' "$*" >&2; }

mkdir -p "${EA_OUT}"
CAPTURE="${EA_CAPTURE:-${EA_OUT}/ea-capture.txt}"

score() {
    local extra=()
    if [[ -n "${EA_REPIN:-}" ]]; then
        extra=(--write-baseline "${EA_BASELINE}")
    elif [[ -f "${EA_BASELINE}" ]]; then
        extra=(--baseline "${EA_BASELINE}")
    else
        log "NOTE: no baseline at ${EA_BASELINE} — reporting only, not ratcheting."
    fi
    python3 "${REPO_ROOT}/scripts/estimate-parity/parity.py" \
        "${CAPTURE}" "${EA_PGDIR}" --json "${EA_OUT}/ea-findings.json" "${extra[@]}"
}

PSQL=(psql -h 127.0.0.1 -p "${EA_PORT}" -U postgres -d postgres -X -q -A -t
      -v ON_ERROR_STOP=0)

# M0146-0009d: verify_server — is the thing answering on EA_PORT THIS gate's
# goopg? pg_isready only proves "something speaks the wire protocol"; a
# foreign postgres squatting on the default port twice let the gate score an
# all-ERROR capture and print a vacuous PASS (2026-09-28). Identity here is
# three independent facts: the pidfile INSIDE EA_DATA names a live pid whose
# cmdline is a goopg `start -D <EA_DATA>` (a stale pidfile naming a recycled
# pid fails the cmdline check), the pidfile's recorded listen address covers
# EA_PORT, and the wire-level version() carries goopg's own string (a real
# PostgreSQL never emits it).
verify_server() {
    local pidfile="${EA_DATA}/postmaster.pid" pid cmdline canon line4
    [[ -f "${pidfile}" ]] || return 1
    pid="$(sed -n '1p' "${pidfile}")"
    [[ "${pid}" =~ ^[0-9]+$ ]] || return 1
    [[ -r "/proc/${pid}/cmdline" ]] || return 1
    cmdline="$(tr '\0' ' ' <"/proc/${pid}/cmdline")"
    # `start` is the subcommand every invocation of this gate passes; a real
    # postgres postmaster's cmdline (`postgres -D …`) never carries it.
    [[ "${cmdline}" == *"start"* ]] || return 1
    canon="$(realpath "${EA_DATA}" 2>/dev/null || echo "${EA_DATA}")"
    case " ${cmdline} " in
        *" -D ${EA_DATA} "*|*" -D=${EA_DATA} "*|*" -D ${canon} "*|*" -D=${canon} "*) ;;
        *) return 1 ;;
    esac
    line4="$(sed -n '4p' "${pidfile}")"
    [[ "${line4}" == *:"${EA_PORT}" ]] || return 1
    "${PSQL[@]}" -c "SELECT version()" 2>/dev/null | grep -qi goopg
}

# Test/debug seam: check the port's server identity and nothing else.
if [[ -n "${EA_VERIFY_ONLY:-}" ]]; then
    if verify_server; then
        log "verify OK: :${EA_PORT} is a goopg serving ${EA_DATA}"
        exit 0
    fi
    log "verify FAILED: :${EA_PORT} is not this gate's goopg on ${EA_DATA}"
    exit 2
fi

# Re-score an existing capture without touching a server at all. This is the
# mode a reviewer uses on a committed capture, and the mode that makes the
# gate cheap enough to run against a code change that cannot move a row count.
if [[ -n "${EA_CAPTURE:-}" && -s "${CAPTURE}" && -z "${EA_FORCE_CAPTURE:-}" ]]; then
    log "scoring existing capture ${CAPTURE} (no server started)"
    score
    exit $?
fi

# ---------------------------------------------------------------- clone
if [[ ! -d "${EA_DATA}" ]]; then
    log "cloning ${EA_SRC_DATA} -> ${EA_DATA}"
    mkdir -p "$(dirname "${EA_DATA}")"
    rsync -a --delete "${EA_SRC_DATA}/" "${EA_DATA}/" || exit 2
fi

# ---------------------------------------------------------------- binary
if [[ -z "${EA_SKIP_BUILD:-}" ]]; then
    log "building ${EA_BIN}"
    go build -o "${EA_BIN}" ./cmd/goopg || exit 2
fi
BIN_SHA="$(sha256sum "${EA_BIN}" | cut -c1-16)"

# ---------------------------------------------------------------- server
stop_server() {
    "${EA_BIN}" stop -D "${EA_DATA}" >/dev/null 2>&1
    systemctl --user stop "${EA_CG_UNIT}.scope" >/dev/null 2>&1
    systemctl --user reset-failed "${EA_CG_UNIT}.scope" >/dev/null 2>&1
}
trap stop_server EXIT

start_server() {
    if pg_isready -h 127.0.0.1 -p "${EA_PORT}" >/dev/null 2>&1; then
        # Something is already listening. Reuse it ONLY if it is this
        # gate's own goopg on EA_DATA — a foreign responder used to
        # short-circuit straight past startup and score all-ERROR captures
        # (M0146-0009d).
        if verify_server; then
            log "reusing this gate's server already on :${EA_PORT}"
            return 0
        fi
        log "port ${EA_PORT} is held by a server that is not this gate's" \
            "goopg on ${EA_DATA} — refusing to capture against it"
        return 1
    fi
    systemctl --user reset-failed "${EA_CG_UNIT}.scope" >/dev/null 2>&1
    GOOPG_CG_UNIT="${EA_CG_UNIT}" "${REPO_ROOT}/scripts/goopg-test-run.sh" \
        "${EA_BIN}" start -D "${EA_DATA}" --listen "127.0.0.1:${EA_PORT}" \
        >>"${EA_OUT}/server.log" 2>&1 &
    local child=$! i
    for i in $(seq 1 "${EA_READY_TRIES:-300}"); do
        if pg_isready -h 127.0.0.1 -p "${EA_PORT}" >/dev/null 2>&1; then
            verify_server && return 0
            # Port answers but identity fails. If our spawn is already dead
            # the responder is foreign — fail now instead of waiting out the
            # loop.
            kill -0 "${child}" 2>/dev/null || {
                log "spawned server exited but :${EA_PORT} still answers" \
                    "— a foreign server holds the port"
                return 1
            }
        fi
        sleep 2
    done
    return 1
}

log "starting goopg on 127.0.0.1:${EA_PORT} (data ${EA_DATA}, cgroup ${EA_CG_UNIT})"
if ! start_server; then
    log "server did not come up; see ${EA_OUT}/server.log"
    exit 2
fi

# M0146-0009d: corpus sanity. The verified server is a goopg on EA_DATA, but
# a stale or wrongly-seeded clone can still lack the TPC-DS tables — the
# second half of the 2026-09-28 incident. A gate that captures 99
# `relation does not exist` answers measures nothing.
CORPUSOK="$("${PSQL[@]}" -c \
    "SELECT count(*) FROM pg_class WHERE relname = 'store_sales'" \
    2>/dev/null | tr -d ' ')"
if [[ ! "${CORPUSOK}" =~ ^[0-9]+$ || "${CORPUSOK}" -eq 0 ]]; then
    log "EA_DATA has no TPC-DS corpus (store_sales absent) — refusing to capture"
    exit 2
fi

# ---------------------------------------------------------------- analyze
#
# Since M0125-0028/-0029 the per-column stats and reltuples survive a restart
# (the goopg-private sidecar), so this runs once rather than per session — but
# it must run, and a bare `ANALYZE;` with no table name is a no-op that leaves
# the planner blind while looking like it did something.
log "ANALYZE"
for t in ${TABLES}; do
    "${PSQL[@]}" -c "ANALYZE ${t}" >/dev/null 2>&1
done
ANOK=$("${PSQL[@]}" -c \
    "SELECT count(*) FROM pg_class WHERE reltuples > 0" 2>/dev/null | tr -d ' ')
log "  relations with reltuples > 0: ${ANOK:-?}"
# M0146-0009d: a capture over an un-analyzed corpus measures the default
# selectivities, not the estimators — refuse rather than score it.
if [[ ! "${ANOK}" =~ ^[0-9]+$ || "${ANOK}" -eq 0 ]]; then
    log "ANALYZE left no analyzed relations — refusing to capture blind"
    exit 2
fi

# ---------------------------------------------------------------- capture
#
# ONE psql PER QUERY, with a server-liveness check between them. The first
# draft ran the whole corpus in a single session; a server death on Q2 then
# cost every remaining query, and the tool reported a clean gate over a
# population of two. A per-query invocation also matches the recipe the c13a
# census used to get 99/99 on this corpus.
: >"${CAPTURE}"
FAILED=""
for q in $(seq 1 "${EA_NQ}"); do
    qf="${EA_QDIR}/query${q}.sql"
    [[ -f "${qf}" ]] || { log "missing ${qf}"; continue; }
    python3 "${REPO_ROOT}/scripts/estimate-parity/mkexplain.py" \
        "${qf}" >"${EA_OUT}/ea-q.sql" || continue
    echo "===== Q${q} =====" >>"${CAPTURE}"
    if ! timeout "${EA_TIMEOUT}" "${PSQL[@]}" -f "${EA_OUT}/ea-q.sql" \
            >>"${CAPTURE}" 2>&1; then
        FAILED="${FAILED} Q${q}"
    fi
    # `timeout N psql` kills only the CLIENT — the server keeps executing the
    # query. Check the server is still there at all before the next query is
    # charged to it, and restart it if a heavy query took it out; otherwise
    # one death silently empties the rest of the corpus.
    if ! pg_isready -h 127.0.0.1 -p "${EA_PORT}" >/dev/null 2>&1; then
        log "  server gone after Q${q} — restarting"
        stop_server
        start_server || { log "restart failed after Q${q}"; break; }
    fi
done
log "capture done: $(wc -l <"${CAPTURE}") lines; non-zero psql rc:${FAILED:- none}"

# M0146-0009d: never hand a vacuous capture to the scorer. ON_ERROR_STOP=0
# means psql exits 0 even when every statement in the file errored, so an
# all-ERROR corpus passes the per-query rc check silently — this is the
# shape the foreign-server incident took.
QBLOCKS="$(grep -c '^===== Q' "${CAPTURE}")"
# psql renders file-mode errors as `psql:<file>:<line>: ERROR:  …` — the
# marker is mid-line, so match it unanchored.
ERRBLOCKS="$(awk '/^===== Q/      { if (q) tot += err; q++; err = 0 }
                 /ERROR:|FATAL:/ { err = 1 }
                 END             { if (q) tot += err; print tot + 0 }' \
                 "${CAPTURE}")"
if [[ "${QBLOCKS}" -eq 0 || "${ERRBLOCKS}" -eq "${QBLOCKS}" ]]; then
    log "capture is empty or all-ERROR (${ERRBLOCKS}/${QBLOCKS} sections" \
        "errored) — refusing to score a vacuous capture"
    exit 2
fi

{
    echo "# ea-parity capture"
    echo "# engine-binary: ${EA_BIN} sha256/16=${BIN_SHA}"
    echo "# commit: $(git rev-parse --short HEAD)"
    echo "# date: $(date -Is)"
    echo "# port: ${EA_PORT}  data: ${EA_DATA}"
    echo "# non-zero psql rc:${FAILED:- none}"
} >"${CAPTURE}.header"

stop_server
trap - EXIT

score
