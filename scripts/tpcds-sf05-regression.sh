#!/usr/bin/env bash
# tpcds-sf05-regression.sh — fast TPC-DS regression gate on a half-size dataset.
#
# Motivation (2026-07-27): a full SF=1 goopg-vs-PG sweep costs 4-5 hours, almost
# all of it spent waiting out 16 known 600 s timeouts. This gate cuts the loop:
#
#   * dataset ~= SF 0.5 (see SAMPLING below), so completing queries run ~2x faster
#   * PostgreSQL executes each query ONCE via EXPLAIN (ANALYZE, TIMING OFF),
#     yielding the plan AND the authoritative row count in a single pass; the
#     result is cached in an oracle file and reused by every later goopg run
#   * the recurring cost is therefore ONE goopg pass against the cached oracle
#
# SAMPLING — why this is "SF 0.5相当" and not dsdgen output:
#   dsdgen's scale parameter is OPT_INT (integer GB; DSGen r_params.c:63), so a
#   true fractional scale cannot be generated. Instead the 7 fact tables of the
#   existing SF=1 TSVs are halved by KEY PARITY, dimensions kept whole:
#       store_sales / store_returns    ss_/sr_ticket_number % 2 == 0
#       catalog_sales / catalog_returns cs_/cr_order_number % 2 == 0
#       web_sales / web_returns        ws_/wr_order_number % 2 == 0
#       inventory                      inv_item_sk % 2 == 0
#   Parity on the SHARED key keeps every sales<->returns pair intact (a kept
#   ticket keeps all its line items and all its returns), so join semantics are
#   realistic. Correctness needs no official scale: PostgreSQL runs on the SAME
#   sampled data, and its row counts are the ground truth.
#
# QUERIES are the existing SF=1 PG-fixed files (tpcds-data/queries/query*.sql):
# reusing them keeps per-query knowledge comparable across scales. Skip policy
# matches the SF=1 sweep: Q36/Q70/Q86 are dsqgen artefacts that fail on PG too;
# additionally any query that errors on PG during oracle capture is excluded.
#
# Determinism: the goopg gate always runs S-cold (fresh server, no same-session
# ANALYZE). goopg loses TableStats.RowCount on restart regardless (design doc
# tpcds-round2-fixes §7.1), so S-cold is the reproducible state.
#
# Known hazards this script codifies (all bitten on 2026-07-27):
#   * sweep-tail collapse: GOGC=off + a 600 s timeout query leaves the heap at
#     GOMEMLIMIT and every later query thrashes GC -> goopg restarts after any
#     goopg TIMEOUT (RESTART_AFTER_TIMEOUT=1 default)
#   * orphaned PG backends: `timeout N psql` kills the CLIENT; the server keeps
#     executing -> after a PG-side timeout, backends older than the timeout are
#     reaped, with the victim set materialised BEFORE pg_terminate_backend (SQL
#     gives no WHERE evaluation order; the naive form killed a healthy backend)
#   * contamination: refuses to run while the SF=1 sweep harness is active
#     (override with FORCE=1)
#
# Usage:
#   scripts/tpcds-sf05-regression.sh build-data    # sample SF=1 TSVs -> SF0.5 TSVs
#   scripts/tpcds-sf05-regression.sh load-pg       # create+load PG db 'tpcds05' (:65438)
#   scripts/tpcds-sf05-regression.sh oracle        # PG plain run -> oracle.txt (rows+ck)
#   scripts/tpcds-sf05-regression.sh load-goopg    # init+load goopg cluster on :65437
#   scripts/tpcds-sf05-regression.sh sweep         # goopg run vs oracle (the recurring gate)
#   scripts/tpcds-sf05-regression.sh plans         # EXPLAIN-only plan capture + diff (~20 s)
#   scripts/tpcds-sf05-regression.sh delta [OLD [NEW]]  # named per-query status/runtime
#                                                  # delta between two archived reports
#                                                  # (defaults: the two newest); runs
#                                                  # nothing, costs nothing
#   scripts/tpcds-sf05-regression.sh all           # everything above, in order
#   scripts/tpcds-sf05-regression.sh status
#
# Env:
#   SF05_PORT=65437         goopg port (see bench/tpcds/env_tpcds.sh port map)
#   SF05_PG_DB=tpcds05      PostgreSQL database name
#   ORACLE_TIMEOUT=600      per-query timeout for PG oracle capture (one-time)
#   TIMEOUT_SEC=300         per-query timeout for the goopg sweep
#   RESTART_AFTER_TIMEOUT=1 bounce goopg after each goopg TIMEOUT
#   FORCE=1                 run even while the SF=1 sweep harness is active
#   QUERIES="35 46"         restrict oracle/sweep to a subset (SOLO probe mode);
#                           the sweep report is stamped "SUBSET PROBE" and is
#                           NOT a gate result. With 'oracle' it additionally
#                           requires SF05_ORACLE (it would truncate the fixture)
#   SF05_ORACLE=<path>      read/write the oracle fixture elsewhere
#   SF05_RESULTS_DIR=<dir>  redirect run artefacts (reports/plans); the oracle
#                           fixture stays where SF05_ORACLE points
#   SF05_NO_PLANS=1         skip the plan-shape channel appended to a sweep
#   SF05_NO_DELTA=1         skip the status-delta channel appended to a sweep
#   SF05_SWEEP_BASELINE=<p> diff the sweep's per-query status/runtime vector
#                           against <p> instead of the newest sweep-*.txt in the
#                           results dir; `none` skips the delta
#   PLAN_TIMEOUT=180        per-query timeout for the EXPLAIN-only plan capture
#   SF05_PLANS_BASELINE=<p> diff the capture against <p> instead of the newest
#                           plans-*.txt in the results dir; `none` skips the diff
#   SF05_NO_BUILD=1         keep the binary already at GOOPG_BIN instead of
#                           rebuilding from the tree (bisect probes); the report
#                           says its provenance is unknown — see sf05_ensure_bin
#   GOOPG_BIN=<path>        build/run a private binary instead of the SHARED
#                           tmp/goopg-bench-bin (which the nightly also owns)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${REPO_ROOT}"
# shellcheck source=/dev/null
source "${REPO_ROOT}/bench/tpcds/env_tpcds.sh"
# planner_flags_body — the generated provenance stamp (M0127-P5.9-q); see
# sf05_planner_flags_line below.
# shellcheck source=/dev/null
source "${SCRIPT_DIR}/planner-flags.sh"

# Dirs/ports come from env_tpcds.sh: TPCDS_TOOLS, SF05_DATA_DIR,
# SF05_GOOPG_DATA, SF05_PORT (65437), SF05_PG_DB, SF05_LOG.
SRC_DATA_DIR="${TPCDS_DATA_DIR}"                   # SF=1 TSVs (input)
QDIR="${TPCDS_QUERY_DIR}"                          # SF=1 PG-fixed queries (reused)
OUTDIR="${SF05_RESULTS_DIR}"                       # env-overridable (env_tpcds.sh)
# The oracle is a GIT-TRACKED FIXTURE, not a run artefact: it stays in the
# canonical results dir even when SF05_RESULTS_DIR is redirected to a scratch
# dir for a one-off probe. Only 'oracle' (re-capture) writes it.
ORACLE="${SF05_ORACLE:-${TPCDS_RUNTIME_DIR}/tpcds-results-sf05/oracle.txt}"  # q|status|rows|ck|secs
ORACLE_TIMEOUT="${ORACLE_TIMEOUT:-600}"
# Value checksum (M0124-0005). Both engines' results go through the SAME parser
# and the SAME normalisation, so "same ck" means "same answer" and nothing else.
CKSUM="${SCRIPT_DIR}/tpcds-result-checksum.py"
TIMEOUT_SEC="${TIMEOUT_SEC:-300}"
CG_UNIT="goopg-tpcds-sf05"

PG_SKIP="36 70 86"   # dsqgen artefacts; fail on upstream PG too

GOOPG_PSQL="psql -h ${TPCDS_HOST} -p ${SF05_PORT} -U ${TPCDS_SUPERUSER} -d postgres"
PG_PSQL="psql -h ${TPCDS_HOST} -p ${TPCDS_PG_PORT} -U ${TPCDS_PG_USER} -d ${SF05_PG_DB}"
PG_ADMIN="psql -h ${TPCDS_HOST} -p ${TPCDS_PG_PORT} -U ${TPCDS_PG_USER} -d postgres"

# The 25 real tables (mirrors tpcds-load.sh's filter).
TABLES="call_center catalog_page catalog_returns catalog_sales customer \
customer_address customer_demographics date_dim household_demographics \
income_band inventory item promotion reason ship_mode store store_returns \
store_sales time_dim warehouse web_page web_returns web_sales web_site"

# table -> parity-sampling key column (facts only; dims copied whole)
sample_key() {
    case "$1" in
    store_sales)      echo ss_ticket_number ;;
    store_returns)    echo sr_ticket_number ;;
    catalog_sales)    echo cs_order_number ;;
    catalog_returns)  echo cr_order_number ;;
    web_sales)        echo ws_order_number ;;
    web_returns)      echo wr_order_number ;;
    inventory)        echo inv_item_sk ;;
    *)                echo "" ;;
    esac
}

log() { echo "[$(date +%H:%M:%S)] $*"; }
die() { echo "FATAL: $*" >&2; exit 1; }

# query_list — which queries 'oracle' and 'sweep' iterate over.
#
# Default is the full 1..99 gate. QUERIES restricts it to a subset (comma-,
# space- or newline-separated, e.g. QUERIES=35 or QUERIES="35 45,46"), which is
# what makes a SOLO probe possible: M0124-0004 needed Q35 alone on a fresh
# server at a 900 s budget, and before this existed the only way to reach it was
# a ~1 h full sweep whose 34 preceding queries also perturbed the server's heap.
# A subset run is a PROBE, not a gate result — cmd_sweep stamps the report so a
# later reader cannot mistake a 1-query summary for a passing gate.
query_list() {
    if [[ -n "${QUERIES:-}" ]]; then
        tr ',' ' ' <<<"${QUERIES}" | tr -s '[:space:]' '\n' | grep -E '^[0-9]+$' || true
    else
        seq 1 99
    fi
}

# guard_sf1_sweep — refuse to run while another benchmark owns the host.
#
# The original check covered only the sibling SF=1 harness. On 2026-07-29
# (M0124-0004) that proved to be the smaller half of the problem: the **nightly
# CI batch** (`ci/batch/run-nightly.sh`) fires at 00:00 and its TPC-H stage runs
# a capped goopg server on :65434 for HOURS — measured at 112% CPU and 7.5 GiB
# RSS, 5 h into the run. It shares nothing with the TPC-DS clusters by port or
# data dir, so nothing stopped a "solo, fresh server" TPC-DS probe from landing
# on top of it, and two SF0.5 sweeps (2026-07-29 00:47 and 03:38) plus the
# M0124-0004 Q35 probes were all taken against that background before anyone
# noticed. Timings taken that way are not comparable to a quiet-host baseline,
# which is the one property every M0124/M0125 measurement depends on.
#
# The nightly is detected by its driver scripts rather than by its port, so a
# lane change cannot silently re-open the hole.
guard_sf1_sweep() {
    [[ "${FORCE:-0}" == "1" ]] && return 0
    local procs
    procs=$(bench_foreign_procs)   # ancestor-filtered; see env_tpcds.sh
    if grep -q '[b]ash scripts/tpcds-bench-compare.sh' <<<"${procs}"; then
        die "the SF=1 sweep harness is running — this would contaminate its timings (FORCE=1 to override)"
    fi
    if grep -qE 'ci/batch/(run-nightly\.sh|stages/)' <<<"${procs}"; then
        die "the nightly CI batch is running (ci/batch) — its TPC-H stage saturates CPU/RAM for hours and would contaminate these timings (FORCE=1 to override)"
    fi
}

# Ordinal (1-based) of column $2 in table $1, parsed from tpcds.sql so nothing
# is hardcoded from memory. Skips the trailing `primary key (...)` line.
col_index() {
    awk -v t="$1" -v c="$2" '
        $1=="create" && $2=="table" && $3==t { intab=1; n=0; next }
        intab && /^\)/ { exit }
        intab && $1=="primary" { next }
        intab && $1 ~ /^[a-z_0-9]+$/ { n++; if ($1==c) { print n; exit } }
    ' "${TPCDS_TOOLS}/tpcds.sql"
}

# ---------------------------------------------------------------- build-data
cmd_build_data() {
    guard_sf1_sweep
    [[ -d "${SRC_DATA_DIR}" ]] || die "SF=1 TSVs missing — run scripts/tpcds-setup.sh first"
    [[ -f "${TPCDS_TOOLS}/tpcds.sql" ]] || die "tpcds.sql missing — run scripts/tpcds-setup.sh first"
    mkdir -p "${SF05_DATA_DIR}"
    log "Sampling SF=1 TSVs -> ${SF05_DATA_DIR} (facts: key%2==0, dims: full copy)"
    local t src dst key idx in_rows out_rows
    for t in ${TABLES}; do
        src="${SRC_DATA_DIR}/${t}.tsv"
        dst="${SF05_DATA_DIR}/${t}.tsv"
        [[ -f "$src" ]] || { log "  ${t}: MISSING source tsv"; continue; }
        key=$(sample_key "$t")
        if [[ -z "$key" ]]; then
            cp -f "$src" "$dst"
            printf "  %-24s full copy (%s rows)\n" "$t" "$(wc -l < "$dst")"
        else
            idx=$(col_index "$t" "$key")
            [[ -n "$idx" ]] || die "column ${key} not found in ${t} (tpcds.sql parse failed)"
            awk -F'\t' -v c="$idx" '($c + 0) % 2 == 0' "$src" > "$dst"
            in_rows=$(wc -l < "$src"); out_rows=$(wc -l < "$dst")
            [[ "$out_rows" -gt 0 ]] || die "${t}: sampling produced 0 rows (key=${key} idx=${idx})"
            printf "  %-24s %s -> %s rows (%s%%, key=%s col %s)\n" \
                "$t" "$in_rows" "$out_rows" "$(( out_rows * 100 / in_rows ))" "$key" "$idx"
        fi
    done
    log "build-data done"
}

# ------------------------------------------------------------------ load-pg
cmd_load_pg() {
    guard_sf1_sweep
    [[ -d "${SF05_DATA_DIR}" ]] || die "run build-data first"
    log "Recreating PG database ${SF05_PG_DB} on :${TPCDS_PG_PORT}"
    ${PG_ADMIN} -c "DROP DATABASE IF EXISTS ${SF05_PG_DB}" >/dev/null
    ${PG_ADMIN} -c "CREATE DATABASE ${SF05_PG_DB}" >/dev/null
    ${PG_PSQL} -q -f "${TPCDS_TOOLS}/tpcds.sql" 2>&1 | tail -2 || true
    local t cnt
    for t in ${TABLES}; do
        printf "  %-24s " "$t"
        if ${PG_PSQL} -v ON_ERROR_STOP=1 -c "COPY ${t} FROM '${SF05_DATA_DIR}/${t}.tsv'" >/dev/null; then
            cnt=$(${PG_PSQL} -t -A -c "SELECT count(*) FROM ${t}")
            echo "OK (${cnt} rows)"
        else
            echo "COPY FAILED"
        fi
    done
    log "ANALYZE (PG keeps stats persistently — one-time)"
    ${PG_PSQL} -c "ANALYZE" >/dev/null
    log "load-pg done"
}

# ------------------------------------------------------------ binary identity
#
# Why this exists (2026-07-30). The sweep report stamped `# goopg: $(git log
# --oneline -1)` — the WORKING TREE's HEAD — while `cmd_load_goopg` accepted any
# already-present binary (`[[ -x "${GOOPG_BIN}" ]] || go build`). Those two
# facts together let a report claim a commit it never executed: GOOPG_BIN is one
# shared path (tmp/goopg-bench-bin) that the nightly CI batch and every TPC-H
# bench script also build into, so the file sitting there belongs to whoever
# built it last. Since every M0125 acceptance is a row-count/checksum verdict
# read off these reports, "which binary produced this" is load-bearing evidence,
# not decoration.
#
# So: build unconditionally from the current tree (the Go build cache makes that
# ~1 s when nothing changed), and stamp what actually ran. SF05_NO_BUILD=1 keeps
# a deliberately-foreign binary (a bisect probe) usable, at the cost of the
# report saying so out loud.
sf05_bin_provenance=""          # set by sf05_ensure_bin; echoed into the report
sf05_engine_id_at_start=""      # D4a comparability key, captured before the build
sf05_bin_sha_at_start=""        # on-disk image the sweep intended to measure
sf05_report=""                  # cmd_sweep's report path (the restart guard appends)
sf05_sweep_active=0             # 1 once the sweep loop owns the server

sf05_ensure_bin() {
    local tree_sha dirty built="rebuilt from tree"
    tree_sha=$(cd "${REPO_ROOT}" && git rev-parse --short HEAD 2>/dev/null || echo unknown)
    # "Dirty" must mean "the binary is not HEAD's code": only tracked Go sources
    # and the module files can change what got compiled. Untracked analysis
    # artefacts and .ralph churn are always present in a loop's tree and would
    # make the flag fire on every run, i.e. mean nothing.
    dirty=$(cd "${REPO_ROOT}" && git status --porcelain --untracked-files=no \
                -- '*.go' go.mod go.sum 2>/dev/null | head -1)
    if [[ "${SF05_NO_BUILD:-0}" == "1" ]]; then
        [[ -x "${GOOPG_BIN}" ]] || die "SF05_NO_BUILD=1 but ${GOOPG_BIN} is not executable"
        built="PRE-EXISTING, NOT BUILT BY THIS RUN (SF05_NO_BUILD=1) — provenance unknown"
    else
        # Refuse to clobber the SHARED default while a foreign bench harness owns
        # the host: the nightly runs servers from this exact file for hours and
        # restarts them between stages, so overwriting it mid-run would swap the
        # binary under someone else's measurement. FORCE=1 does NOT waive this —
        # it waives timing contamination, which is a different question.
        if [[ "${GOOPG_BIN}" == "${REPO_ROOT}/tmp/goopg-bench-bin" ]] \
           && grep -qE 'ci/batch/(run-nightly\.sh|stages/)|[b]ash scripts/tpcds-bench-compare\.sh' \
                   <<<"$(bench_foreign_procs)"; then
            die "refusing to rebuild the SHARED ${GOOPG_BIN} while the nightly/SF=1 harness runs from it; re-run with GOOPG_BIN=${REPO_ROOT}/tmp/goopg-sf05-bin"
        fi
        mkdir -p "$(dirname "${GOOPG_BIN}")"
        log "building goopg -> ${GOOPG_BIN} (tree ${tree_sha}${dirty:+, dirty})"
        ( cd "${REPO_ROOT}" && go build -o "${GOOPG_BIN}" ./cmd/goopg ) \
            || die "goopg build failed"
    fi
    # Field names and semantics are D4a's, verbatim, so an SF0.5 header and an
    # SF=1 header can be compared line for line (helpers in env_tpcds.sh).
    # `running=` is filled in later — the postmaster does not exist yet here.
    sf05_engine_id_at_start=$(bench_engine_id)
    sf05_bin_provenance=$(printf '# goopg: %s\n# engine-id: %s\n# build: %s%s' \
        "$(cd "${REPO_ROOT}" && git log --oneline -1 2>/dev/null)" \
        "${sf05_engine_id_at_start}" \
        "${built}" \
        "${dirty:+ [tree DIRTY in Go sources — the binary is not this commit alone]}")
}

# sf05_engine_binary_line — D4a's third field, emitted once the cluster is up.
# on-disk vs running differ whenever a foreign harness rebuilt the shared path
# after this server started; the sweep header must show which image answered.
sf05_engine_binary_line() {
    local running ondisk
    running=$(bench_running_engine_sha "${SF05_GOOPG_DATA}")
    ondisk=$(bench_engine_bin_sha "${GOOPG_BIN}")
    printf '# engine-binary: running=%s on-disk=%s (%s)\n' "${running}" "${ondisk}" "${GOOPG_BIN}"
    [[ "${running}" != "${ondisk}" ]] && \
        printf '# WARNING: the serving goopg is not the on-disk build of %s — a foreign harness rebuilt it; this run measures the `running` image.\n' "${GOOPG_BIN}"
    return 0
}

# --------------------------------------------------------------- goopg server
sf05_goopg_stop() {
    "${GOOPG_BIN}" stop -D "${SF05_GOOPG_DATA}" >/dev/null 2>&1 || true
    systemctl --user stop "${CG_UNIT}.scope" >/dev/null 2>&1 || true
    systemctl --user reset-failed "${CG_UNIT}.scope" >/dev/null 2>&1 || true
}

sf05_goopg_start() {
    sf05_goopg_stop
    local hba_arg=()
    [[ -f "${SF05_GOOPG_DATA}/pg_hba.conf" ]] && hba_arg=(--hba "${SF05_GOOPG_DATA}/pg_hba.conf")
    GOOPG_CG_UNIT="${CG_UNIT}" "${REPO_ROOT}/scripts/goopg-test-run.sh" \
        "${GOOPG_BIN}" start -D "${SF05_GOOPG_DATA}" \
        --listen "127.0.0.1:${SF05_PORT}" "${hba_arg[@]}" \
        >> "${SF05_LOG}" 2>&1 &
    local i
    for i in $(seq 1 180); do
        if pg_isready -h 127.0.0.1 -p "${SF05_PORT}" -U "${PG_SUPERUSER}" >/dev/null 2>&1; then
            sf05_guard_engine_stable
            return 0
        fi
        sleep 1
    done
    die "goopg (sf05) did not become ready in 180s — see ${SF05_LOG}"
}

# sf05_guard_engine_stable — every RESTART_AFTER_TIMEOUT bounce and every
# crash-restart re-execs ${GOOPG_BIN} as it is *then*, so a foreign harness that
# rebuilt the shared path mid-sweep silently swaps the engine under the report
# (D4a's second failure direction). Mirror the SF=1 harness's policy: a changed
# engine-id VOIDS the run and must be shouted in the artefact, not left to
# process archaeology; a changed image with identical source is a docs/tracker
# commit rebuild and voids nothing.
sf05_guard_engine_stable() {
    [[ "${sf05_sweep_active}" == "1" ]] || return 0
    local now_id now_sha msg=""
    now_id=$(bench_engine_id)
    now_sha=$(bench_engine_bin_sha "${GOOPG_BIN}")
    if [[ "${now_id}" != "${sf05_engine_id_at_start}" ]]; then
        msg="      *** SWEEP VOID: engine source changed mid-sweep (engine-id ${sf05_engine_id_at_start} -> ${now_id}); verdicts before and after this restart are not one measurement ***"
    elif [[ "${now_sha}" != "${sf05_bin_sha_at_start}" ]]; then
        msg="      (engine source unchanged; ${GOOPG_BIN} image re-built ${sf05_bin_sha_at_start} -> ${now_sha} — docs/tracker commit, not a code change)"
    fi
    if [[ -n "${msg}" ]]; then
        echo "${msg}"
        [[ -n "${sf05_report}" ]] && echo "${msg}" >> "${sf05_report}"
    fi
    return 0
}

# --------------------------------------------------------------- load-goopg
cmd_load_goopg() {
    guard_sf1_sweep
    [[ -d "${SF05_DATA_DIR}" ]] || die "run build-data first"
    sf05_ensure_bin
    log "Initialising fresh goopg cluster at ${SF05_GOOPG_DATA} (port ${SF05_PORT})"
    sf05_goopg_stop
    rm -rf "${SF05_GOOPG_DATA}"
    "${GOOPG_BIN}" init -D "${SF05_GOOPG_DATA}" >/dev/null
    sf05_goopg_start
    log "Loading schema + data"
    ${GOOPG_PSQL} -q -f "${TPCDS_TOOLS}/tpcds.sql" 2>&1 | tail -2 || true
    local t cnt
    for t in ${TABLES}; do
        printf "  %-24s " "$t"
        if ${GOOPG_PSQL} -c "COPY ${t} FROM '${SF05_DATA_DIR}/${t}.tsv'" >/dev/null 2>&1; then
            cnt=$(${GOOPG_PSQL} -t -A -c "SELECT count(*) FROM ${t}" 2>/dev/null)
            echo "OK (${cnt} rows)"
        else
            echo "COPY FAILED"
        fi
    done
    # M0125-0028/-0029: ANALYZE now resolves per-DB and the per-column stats +
    # relation size (reltuples/relpages via the goopg-private sidecar) survive
    # a restart — the S-cold premise this gate was designed under no longer
    # applies to a freshly loaded-and-ANALYZEd cluster.
    log "ANALYZE + CHECKPOINT"
    local t ok=0
    for t in ${TABLES}; do
        ${GOOPG_PSQL} -c "ANALYZE ${t}" >/dev/null 2>&1 || true
        if ${GOOPG_PSQL} -t -A -c "SELECT reltuples FROM pg_class WHERE relname = '${t}' AND reltuples > 0" 2>/dev/null | grep -q .; then
            ok=$((ok + 1))
        fi
    done
    log "  reltuples verification: ${ok}/25 tables have reltuples > 0"
    ${GOOPG_PSQL} -c "CHECKPOINT" >/dev/null 2>&1 || true
    sf05_goopg_stop
    log "load-goopg done (server stopped; the sweep starts its own fresh instance)"
}

# ------------------------------------------------------------------- oracle
# Reap PG backends left running after a client-side timeout. The victim set
# MUST be materialised before pg_terminate_backend: SQL does not guarantee
# WHERE evaluation order, and the naive single-WHERE form killed a healthy
# backend (PG Q6) on 2026-07-27.
reap_pg_orphans() {
    ${PG_ADMIN} -t -A -c "
        with victims as materialized (
            select pid from pg_stat_activity
            where backend_type='client backend'
              and pid <> pg_backend_pid()
              and datname='${SF05_PG_DB}'
              and state='active'
              and now()-query_start > interval '${ORACLE_TIMEOUT} seconds'
        )
        select count(*) from victims where pg_terminate_backend(pid)" 2>/dev/null || true
}

# query_limits — the LIMIT values written in a query file, comma-joined.
#
# Feeds the checksum tool's saturation rule (design 0124-0005 D3): a result
# block holding exactly as many rows as a LIMIT allows had rows discarded at the
# window boundary, so its row SET is only stable if the ORDER BY is a total
# order — which we cannot prove per query. Those report ck=n/a and stay
# row-count-only rather than risk a spurious CKMISMATCH on a legitimate tie.
query_limits() {
    grep -oiE 'limit +[0-9]+' "$1" 2>/dev/null \
        | grep -oE '[0-9]+' | sort -un | paste -sd, - || true
}

# result_rows_ck — "<rows> <ck>" for one psql result capture, both derived from
# the SAME parse of the SAME run. A checksum captured in a different run than
# its row count is not a fixture, it is two fixtures (design D1).
result_rows_ck() {
    local out
    out=$("${CKSUM}" "$1" --limits "$(query_limits "$2")" 2>/dev/null) || {
        echo "0 err"; return 0
    }
    echo "$(sed -n 's/.*rows=\([0-9]*\).*/\1/p' <<<"$out") $(sed -n 's/.*ck=\([^ ]*\).*/\1/p' <<<"$out")"
}

cmd_oracle() {
    guard_sf1_sweep
    # cmd_oracle TRUNCATES the fixture. A partial re-capture under QUERIES would
    # therefore delete the other 98 rows, silently turning the gate into a
    # 1-query no-op — so a subset capture must name its own output file.
    if [[ -n "${QUERIES:-}" && -z "${SF05_ORACLE:-}" ]]; then
        die "QUERIES= with 'oracle' would truncate the git-tracked fixture; set SF05_ORACLE=<scratch path> too"
    fi
    mkdir -p "${OUTDIR}"
    # PLAIN execution, not EXPLAIN ANALYZE (M0124-0005 / design D1). EXPLAIN
    # ANALYZE emits a plan and NO result tuples, so there is nothing to
    # checksum in that run — and rows and ck must come from the SAME run or the
    # fixture is two fixtures. The switch also changes how `rows` is derived
    # (result tuples, not top-node `actual rows=`), which is why D5 makes
    # "re-captured counts equal the pinned ones, query for query" the
    # load-bearing acceptance criterion.
    log "Capturing PG oracle (plain run + value checksum, timeout ${ORACLE_TIMEOUT}s/query) -> ${ORACLE}"
    {
        echo "# TPC-DS SF0.5 row-count + value-checksum oracle — PostgreSQL 18.3 ground truth"
        echo "# captured: $(date -Iseconds)  source: $(git -C "${REPO_ROOT}" log --oneline -1 | cut -d' ' -f1)"
        echo "# dataset: SF=1 TSVs, facts halved by key parity (see tpcds-sf05-regression.sh header)"
        echo "# format: q|status|rows|ck|secs   (secs are machine-specific; rows and ck are the fixture)"
        echo "# ck: scripts/tpcds-result-checksum.py — sha256/16 over field-stripped rows,"
        echo "#     fractional numerics canonicalised to 12 significant digits (goopg's"
        echo "#     stddev_samp differs from PG's sqrt_var in the last 1-2 digits: ledger row"
        echo "#     'tpcds-round2 stddev-precision'). Column NAMES are excluded. Rows are NOT"
        echo "#     sorted, so a wrong ORDER BY is a checksum failure, not a silent pass."
        echo "# ck=n/a: a LIMIT window saturated at its bound — the row SET is ambiguous at"
        echo "#     the boundary tie group, so the query stays row-count-only (design D3)."
        echo "# This file is GIT-TRACKED as a pinned fixture so other machines/CI can skip"
        echo "# the ~20 min PG capture. Re-run 'oracle' only when the dataset or queries change."
    } > "${ORACLE}"
    local q qf res secs start rc rows ck status zero=0 okc=0 ckc=0 nac=0
    for q in $(query_list); do
        if grep -qw "$q" <<<"${PG_SKIP}"; then
            echo "${q}|SKIP_QUERYGEN|0|n/a|0" >> "${ORACLE}"
            printf "Q%-3s SKIP (dsqgen artefact)\n" "$q"
            continue
        fi
        qf="${QDIR}/query${q}.sql"
        [[ -f "$qf" ]] || { echo "${q}|MISSING|0|n/a|0" >> "${ORACLE}"; continue; }
        res="${OUTDIR}/pg_q${q}_result.txt"
        # Millisecond resolution (take2 P0-09). bash's SECONDS is integer, so
        # 54 of the 95 OK rows in the committed fixture read `0` and the column
        # said nothing. EPOCHREALTIME is bash 5 and costs no subprocess.
        # NOTE: `secs` is informational — the fixture is `rows` and `ck` (see
        # this file's oracle header) — so this does NOT justify a re-capture on
        # its own; the resolution lands on the next capture required for another
        # reason, under design D5.
        start=${EPOCHREALTIME/./}
        timeout "${ORACLE_TIMEOUT}" ${PG_PSQL} -f "$qf" > "$res" 2>&1 && rc=0 || rc=$?
        secs=$(awk -v us=$(( ${EPOCHREALTIME/./} - start )) 'BEGIN{printf "%.3f", us/1000000}')
        ck="n/a"
        if [[ $rc -eq 124 ]]; then
            status="TIMEOUT"; rows=0
            reap_pg_orphans >/dev/null
        elif grep -qE '^(psql:[^ ]*:[0-9]+: )?(ERROR|FATAL|PANIC):' "$res"; then
            status="PG_ERROR"; rows=0
        elif [[ $rc -ne 0 ]]; then
            # M0125-0027 sibling: a psql that never connected exits non-zero
            # with no ERROR:/FATAL: prefix — it must not be captured as an OK
            # oracle cell with the error text's "row count". Only rc==0 is OK.
            status="PG_NOCONN"; rows=0
        else
            status="OK"; read -r rows ck < <(result_rows_ck "$res" "$qf")
            okc=$((okc+1)); [[ "$rows" == "0" ]] && zero=$((zero+1))
            if [[ "$ck" == "n/a" ]]; then nac=$((nac+1)); else ckc=$((ckc+1)); fi
        fi
        echo "${q}|${status}|${rows}|${ck}|${secs}" >> "${ORACLE}"
        printf "Q%-3s %-9s %6ss %8s rows  ck=%s\n" "$q" "$status" "$secs" "$rows" "$ck"
    done
    log "oracle done: ${okc} queries captured (${ckc} with a checksum, ${nac} ck=n/a); ${zero} return 0 rows"
    [[ "$zero" -gt 0 ]] && log "NOTE: 0-row oracles give a weak regression signal (0==0 passes trivially)"
    true
}

# -------------------------------------------------------------- plan channel
#
# Why the gate needs one (M0127-P5.6-g-i-b, 2026-08-05). The row-count +
# checksum verdict above is the PRIMARY bar and is not weakened by anything in
# this section — nothing here can fail the gate. But it is blind to plan shape,
# and P5.6-g-i measured how blind: commit `4b820ab8` re-ordered **74 of 99**
# TPC-DS plans while the sweep reported a verdict identical to the previous
# baseline line for line (PASS=94, the same 57 checksums, the same single Q47
# TIMEOUT). For a milestone whose whole subject is the join search, 74 plans
# moving in silence is the event we most want the artefact to record.
#
# The channel is EXPLAIN-without-ANALYZE, so it executes nothing: no timings and
# no actual rows enter the file, which is what makes the capture byte-stable.
# P5.6-g-i measured that noise floor directly — the same binary captured twice
# produced byte-identical plans for all 99 queries — so a diff here is signal.
#
# Every statement in a query file is EXPLAIN-prefixed (Q14/23/24/39 hold two),
# so the second statement of those four is never executed either. The file
# format is deliberately identical to the hand-rolled capture that preceded this
# (`analysis/leftdeep-joins/2026-08-05-p56gi-capture.sh`), so the four committed
# corpus captures from that loop remain valid baselines for
# `scripts/tpcds-plan-diff.py`.
PLAN_TIMEOUT="${PLAN_TIMEOUT:-180}"
PLAN_DIFF="${PLAN_DIFF:-${SCRIPT_DIR}/tpcds-plan-diff.py}"

# ------------------------------------------------------------ status channel
#
# Why the gate needs this one too (M0127-P5.6-f-v, 2026-08-05). The SUMMARY line
# is a set of COUNTS, and `TIMEOUT=1` is invariant to WHICH query timed out. On
# 2026-08-04/05 commit `ce027cee` traded Q72's timeout for Q47's (31 s -> 523 s)
# and the summary was byte-identical across FOUR sweeps; it took a bisect
# against a copy of the cluster to find (analysis/m0127-p56fiii/README.md,
# design 09 §5.15). The plan channel above would not have caught it either: it
# reports that a shape moved, not that a query stopped finishing.
#
# So the sweep also diffs its own per-query STATUS/RUNTIME VECTOR against the
# previous report and prints what moved BY NAME (`TIMEOUT +Q47 -Q72`). Input is
# the report itself, so no new artefact is produced and every archived report is
# a valid baseline. Non-blocking, exactly like the plan channel: performance
# never fails this gate, only correctness does.
SWEEP_DIFF="${SWEEP_DIFF:-${SCRIPT_DIR}/tpcds-sweep-diff.py}"

# sf05_sweep_baseline — the report this run is diffed against, chosen BEFORE the
# new one is created so it can never select itself (same rule, and the same
# hazard, as sf05_plan_baseline). Unlike the plan baseline it skips SUBSET
# PROBES: a probe covers a handful of queries and is stamped "NOT a gate
# result", so diffing a full sweep against one would compare 3 queries and stay
# silent about the other 96. The newest FULL report is the last comparable gate
# run. SF05_SWEEP_BASELINE overrides it; `none` suppresses.
sf05_sweep_baseline() {
    if [[ -n "${SF05_SWEEP_BASELINE:-}" ]]; then
        [[ "${SF05_SWEEP_BASELINE}" == "none" ]] || echo "${SF05_SWEEP_BASELINE}"
        return 0
    fi
    local f
    for f in $(ls -t "${OUTDIR}"/sweep-*.txt 2>/dev/null); do
        grep -q '^# SUBSET PROBE' "$f" || { echo "$f"; return 0; }
    done
    # Only probes exist (or nothing does): fall back to the newest, whatever it
    # is — the diff itself labels a probe baseline out loud.
    ls -t "${OUTDIR}"/sweep-*.txt 2>/dev/null | head -1 || true
}

# sf05_status_delta_channel <report> <baseline> — append the named delta to the
# report. Swallows every failure: a broken delta must not turn a passing
# correctness gate red.
sf05_status_delta_channel() {
    local report="$1" baseline="$2"
    {
        echo ""
        if [[ -n "${baseline}" && -f "${baseline}" ]]; then
            python3 "${SWEEP_DIFF}" "${baseline}" "${report}" 2>&1 || true
        else
            echo "# status-delta: no previous sweep report to diff against (first run, or SF05_SWEEP_BASELINE=none)"
        fi
        echo "# The status-delta channel is NON-BLOCKING: it never changes this gate's exit status."
    } | tee -a "${report}"
}

# sf05_planner_flags_line — the arm label, shared by the sweep report and the
# plan capture. D4a made the report say which ENGINE ran; a planner A/B
# additionally needs it to say which FLAGS ran, or two arms of the same commit
# produce artefacts that are indistinguishable on their face and the comparison
# rests on the operator's memory of what was exported. Every flag is printed
# even when unset, so "off" is a positive statement in the file rather than the
# absence of a line.
#
# RELSIZE's unset label says `unset(2)`, not `unset(off)`, from 2026-07-30:
# M0125-0005 flipped defaultRelSizeFallbackStage to 2 and `=0` became the
# explicit opt-out. The old label survived the flip and made every artefact
# captured after it state the OPPOSITE of the regime it measured — the same
# defect class M0125-0011 fixed when the report could still name a binary it had
# never run.
#
# A plan diff between two captures taken under different flags is meaningless,
# which is why the capture stamps this too and not just the sweep.
#
# GOOPG_PGSHAPED_DP/_COLLAPSE joined the line at M0127-P5.9 run 3, when this
# gate was first used as an ARM of a planner A/B (09 §3 clause 4). Without them
# a flag-ON sweep is byte-indistinguishable from a flag-OFF one — precisely the
# failure this function exists to prevent, one flag generation later.
#
# M0127-P5.9-n (2026-08-06) then walked straight into the RELSIZE defect the
# paragraph above describes, one flag generation later still. The DP flip
# (`b92582fb`) made `GOOPG_PGSHAPED_DP` default-ON with `=0` as the opt-out, and
# the `unset(off)` label survived it — so every artefact captured between the
# flip and this fix (`sweep-20260806-022814.txt`, `plans-20260806-022814.txt`)
# states the OPPOSITE of the regime it measured. The label is now `unset(on)`.
# Same commit retired `GOOPG_COST_DRIVEN_JOINORDER` entirely: no code reads it
# any more (`internal/planner/bushy.go:13`), so printing it as a live flag
# invited a later loop to A/B a variable the binary cannot see. It is stamped
# `retired` rather than dropped, because dropping it would make old artefacts
# that DO carry it look like they were captured by this version of the gate.
#
# M0127-P5.9-q (2026-08-06) took the labels away from this printf. Fixing the
# same defect by hand for the second time is evidence that hand-written labels
# do not survive a flip, so the list and its unset-labels now come from
# `scripts/planner-flags.env`, GENERATED from the Go defaults
# (internal/planner/flaglabels.go) and guarded by a unit test. Adding a
# plan-shaping flag in Go now stamps it here with no edit to this file — which
# is how the six flags this function never named (EXISTS_TO_ANY, UNNEST_PREDP,
# INDEXKEY_HARVEST, NLI_COSTGATE, HASH_OUTER_JOIN, MHJ_PACKING_OFF) joined the
# line in the same commit.
sf05_planner_flags_line() {
    printf '# planner-flags: %s\n' "$(planner_flags_body)"
}

# sf05_plan_baseline — the capture this run is diffed against: the newest
# plans-*.txt already in the results dir, chosen BEFORE the new file is created
# so it can never select itself. SF05_PLANS_BASELINE overrides it (point it at
# one of the committed corpus captures to attribute a change to a specific
# commit); `none` suppresses the diff for a first capture on a fresh dir.
sf05_plan_baseline() {
    if [[ -n "${SF05_PLANS_BASELINE:-}" ]]; then
        [[ "${SF05_PLANS_BASELINE}" == "none" ]] || echo "${SF05_PLANS_BASELINE}"
        return 0
    fi
    ls -t "${OUTDIR}"/plans-*.txt 2>/dev/null | head -1 || true
}

# sf05_capture_plans <outfile> — one EXPLAIN pass over the corpus on the
# server that is currently up. The caller owns the server's lifecycle: the
# capture must run on a FRESHLY started instance so a sweep's accumulated heap
# (and any RESTART_AFTER_TIMEOUT bounce it took) cannot be confused for a
# planner input.
#
# The capture is ALWAYS the full 1..99 corpus, even under QUERIES= — unlike the
# sweep, which a subset turns into a probe. A plan file's whole purpose is to be
# diffable against every other plan file, and a subset capture would report the
# other 98 queries as `removed` against any real baseline. At 14 s for the full
# corpus there is nothing to save by narrowing it.
sf05_capture_plans() {
    local out="$1" q f rc
    [[ -n "${QUERIES:-}" ]] && log "  (plan capture ignores QUERIES= — it is always the full corpus)"
    {
        echo "# TPC-DS SF0.5 plan-shape capture — $(date -Iseconds)"
        echo "${sf05_bin_provenance}"
        sf05_planner_flags_line
        echo "# EXPLAIN only (no ANALYZE): nothing in this file was executed."
        echo "# diff with: scripts/tpcds-plan-diff.py OLD NEW [--verbose]"
    } > "${out}"
    for q in $(seq 1 99); do
        f="${QDIR}/query${q}.sql"
        [[ -f "$f" ]] || continue
        echo "===== Q${q} =====" >> "${out}"
        python3 - "$f" > "${OUTDIR}/.explain_all.sql" <<'PY'
import sys
src = open(sys.argv[1]).read()
for stmt in src.split(';'):
    if stmt.strip():
        print("EXPLAIN " + stmt.strip() + ";")
PY
        timeout "${PLAN_TIMEOUT}" ${GOOPG_PSQL} -f "${OUTDIR}/.explain_all.sql" >> "${out}" 2>&1 \
            || { rc=$?; echo "(explain rc=${rc})" >> "${out}"; }
    done
    rm -f "${OUTDIR}/.explain_all.sql"
}

# cmd_plans — the channel on its own, without the ~1 h sweep in front of it.
# A plan-only pass is ~20 s (14 s of EXPLAIN plus the server bounce), which is
# what makes "did this commit move any plan?" a question a loop can afford to
# ask before it asks the expensive one.
cmd_plans() {
    guard_sf1_sweep
    [[ -d "${SF05_GOOPG_DATA}" ]] || die "run load-goopg first"
    mkdir -p "${OUTDIR}"
    sf05_ensure_bin
    sf05_bin_sha_at_start=$(bench_engine_bin_sha "${GOOPG_BIN}")
    local baseline plans
    baseline=$(sf05_plan_baseline)
    plans="${OUTDIR}/plans-$(date +%Y%m%d-%H%M%S).txt"
    log "plan-shape capture (EXPLAIN only, timeout ${PLAN_TIMEOUT}s/query) -> ${plans}"
    sf05_goopg_start
    sf05_capture_plans "${plans}"
    sf05_goopg_stop
    if [[ -n "${baseline}" && -f "${baseline}" ]]; then
        python3 "${PLAN_DIFF}" "${baseline}" "${plans}"
    else
        log "no baseline capture to diff against (first run, or SF05_PLANS_BASELINE=none)"
    fi
    log "plans: ${plans}"
}

# sf05_plan_channel <report> — the sweep's tail. Restarts the server so the
# capture is S-cold like the sweep itself, captures, diffs, and appends the
# result to the sweep report. Runs in a subshell and swallows every failure:
# a broken plan pass must not turn a passing correctness gate red.
sf05_plan_channel() {
    local report="$1" baseline plans rc=0
    baseline=$(sf05_plan_baseline)
    plans="${report%/*}/plans-${report##*/sweep-}"
    (
        sf05_goopg_start
        sf05_capture_plans "${plans}"
        sf05_goopg_stop
    ) >> "${SF05_LOG}" 2>&1 || rc=$?
    {
        echo ""
        if [[ "${rc}" -ne 0 ]]; then
            echo "=== PLAN-SHAPE: capture FAILED (rc=${rc}) — see ${SF05_LOG}; the verdict above is unaffected ==="
        elif [[ -n "${baseline}" && -f "${baseline}" ]]; then
            python3 "${PLAN_DIFF}" "${baseline}" "${plans}" 2>&1 || true
        else
            echo "# plan-shape: captured ${plans} (no baseline to diff against yet)"
        fi
        echo "# The plan channel is NON-BLOCKING: it never changes this gate's exit status."
    } | tee -a "${report}"
}

# -------------------------------------------------------------------- sweep
cmd_sweep() {
    guard_sf1_sweep
    [[ -s "${ORACLE}" ]] || die "run oracle first"
    [[ -d "${SF05_GOOPG_DATA}" ]] || die "run load-goopg first"
    mkdir -p "${OUTDIR}"
    # Chosen BEFORE the new report exists, or the status channel would diff the
    # run against itself (sf05_plan_baseline has the same ordering constraint).
    local delta_baseline
    delta_baseline=$(sf05_sweep_baseline)
    local report="${OUTDIR}/sweep-$(date +%Y%m%d-%H%M%S).txt"
    log "goopg SF0.5 sweep (timeout ${TIMEOUT_SEC}s/query, S-cold) -> ${report}"
    sf05_ensure_bin
    sf05_bin_sha_at_start=$(bench_engine_bin_sha "${GOOPG_BIN}")
    sf05_report="$report"
    sf05_goopg_start
    # Only now is the sweep the server's owner: the guard must not fire on this
    # first, intended start.
    sf05_sweep_active=1
    local q line ostatus orows ock qf out rc secs start grows gck verdict nf
    local pass=0 mismatch=0 ckmismatch=0 gerr=0 gto=0 skip=0 ckver=0 ckna=0
    local resfile="${OUTDIR}/.goopg_result.txt"
    {
        echo "# TPC-DS SF0.5 goopg sweep — $(date -Iseconds)"
        # Identity of the engine that ACTUALLY ran, not just the tree's HEAD —
        # see sf05_ensure_bin for why the difference bit us. Same three fields as
        # the SF=1 harness (design 0124-0001 D4a).
        echo "${sf05_bin_provenance}"
        sf05_engine_binary_line
        echo "# oracle: ${ORACLE} (PG 18.3 plain-run row counts + value checksums)"
        echo "# timeout: ${TIMEOUT_SEC}s"
        # The arm label — see sf05_planner_flags_line for why a flags line is
        # load-bearing evidence and why RELSIZE's unset label reads `unset(2)`.
        # Shared with the plan capture so the two artefacts of one run agree.
        sf05_planner_flags_line
        [[ "${FORCE:-0}" == "1" ]] && \
            echo "# FORCE=1 — a foreign bench/nightly harness was running: ROW COUNTS AND CHECKSUMS ARE VALID, PER-QUERY SECONDS ARE NOT"
        [[ -n "${QUERIES:-}" ]] && \
            echo "# SUBSET PROBE (QUERIES=${QUERIES}) — NOT a gate result; the summary below covers only these queries"
    } > "$report"
    for q in $(query_list); do
        line=$(grep -E "^${q}\|" "${ORACLE}" || true)
        ostatus=$(cut -d'|' -f2 <<<"$line"); orows=$(cut -d'|' -f3 <<<"$line")
        # 4-column oracles (pre-M0124-0005: q|status|rows|secs) stay readable —
        # they simply carry no checksum, so every query is row-count-only. A
        # hard failure here would strand every checkout that has not re-captured.
        nf=$(awk -F'|' '{print NF}' <<<"$line")
        ock="n/a"; [[ "${nf:-0}" -ge 5 ]] && ock=$(cut -d'|' -f4 <<<"$line")
        if [[ "$ostatus" != "OK" ]]; then
            printf "Q%-3s SKIP (oracle: %s)\n" "$q" "${ostatus:-absent}" | tee -a "$report"
            skip=$((skip+1)); continue
        fi
        qf="${QDIR}/query${q}.sql"
        start=$SECONDS
        # Millisecond arm (EX0-06): EPOCHREALTIME costs no subprocess; the
        # integer `secs` above stays the parsed field (sweep-diff.py's
        # TIMED_RE anchors `(\d+)s\b`, which an appended `(Nms)` suffix
        # does not disturb) — ms rides alongside, additively.
        mstart=${EPOCHREALTIME/./}
        # Captured to a file rather than a shell variable so the checksum tool
        # can parse the very same bytes the verdict is drawn from. The file is
        # reused per query and only kept (under a per-query name) when the
        # verdict is bad — a passing gate must not litter the results dir.
        timeout "${TIMEOUT_SEC}" ${GOOPG_PSQL} -f "$qf" > "$resfile" 2>&1 && rc=0 || rc=$?
        secs=$((SECONDS - start))
        msecs=$(awk -v us=$(( ${EPOCHREALTIME/./} - mstart )) 'BEGIN{printf "%.0f", us/1000}')
        if [[ $rc -eq 124 ]]; then
            verdict="TIMEOUT"; gto=$((gto+1))
            printf "Q%-3s TIMEOUT  %4ss (%sms) (oracle %s rows)\n" "$q" "$secs" "$msecs" "$orows" | tee -a "$report"
            if [[ "${RESTART_AFTER_TIMEOUT:-1}" == "1" ]]; then
                echo "      (restarting goopg to drop accumulated heap)" | tee -a "$report"
                sf05_goopg_start
            fi
        elif [[ $rc -ne 0 ]] || grep -qE '^(psql:[^ ]*:[0-9]+: )?(ERROR|FATAL|PANIC):|connection to server was lost|server closed the connection' "$resfile"; then
            # rc!=0 arm added for M0125-0027: connection-refused (dead server,
            # psql exit 2) carries no ERROR: prefix and previously fell through
            # to the row-count comparison below — judging error text as rows.
            verdict="ERROR"; gerr=$((gerr+1))
            printf "Q%-3s ERROR    %4ss (%sms) %s\n" "$q" "$secs" "$msecs" \
                "$(grep -E '(ERROR|FATAL|PANIC):|connection to server' "$resfile" | head -1 | cut -c1-90)" | tee -a "$report"
            # A dead server presents as a connection error on the NEXT query;
            # probe and restart so one crash doesn't cascade.
            pg_isready -h 127.0.0.1 -p "${SF05_PORT}" -U "${PG_SUPERUSER}" >/dev/null 2>&1 || sf05_goopg_start
        else
            read -r grows gck < <(result_rows_ck "$resfile" "$qf")
            if [[ "$grows" != "$orows" ]]; then
                verdict="MISMATCH"; mismatch=$((mismatch+1))
                printf "Q%-3s MISMATCH %4ss (%sms) goopg=%s oracle=%s\n" "$q" "$secs" "$msecs" "$grows" "$orows" | tee -a "$report"
                cp -f "$resfile" "${OUTDIR}/goopg_q${q}_result.txt"
            elif [[ "$ock" != "n/a" && "$gck" != "n/a" && "$gck" != "$ock" ]]; then
                # The right number of WRONG rows — the more alarming of the two
                # failures, and the one this whole column exists to catch (Q75
                # PASSed for weeks at 100 rows over a corrupt CTE).
                verdict="CKMISMATCH"; ckmismatch=$((ckmismatch+1))
                printf "Q%-3s CKMISMATCH %2ss (%sms) %8s rows  goopg ck=%s oracle ck=%s\n" \
                    "$q" "$secs" "$msecs" "$grows" "$gck" "$ock" | tee -a "$report"
                cp -f "$resfile" "${OUTDIR}/goopg_q${q}_result.txt"
            else
                verdict="PASS"; pass=$((pass+1))
                if [[ "$ock" != "n/a" && "$gck" == "$ock" ]]; then
                    ckver=$((ckver+1))
                    printf "Q%-3s PASS     %4ss (%sms) %8s rows  ck=%s\n" "$q" "$secs" "$msecs" "$grows" "$gck" | tee -a "$report"
                else
                    ckna=$((ckna+1))
                    printf "Q%-3s PASS     %4ss (%sms) %8s rows  ck=n/a\n" "$q" "$secs" "$msecs" "$grows" | tee -a "$report"
                fi
            fi
        fi
    done
    sf05_goopg_stop
    rm -f "$resfile"
    {
        echo ""
        echo "=== SUMMARY: PASS=${pass} (${ckver} ck-verified, ${ckna} ck=n/a) MISMATCH=${mismatch} CKMISMATCH=${ckmismatch} ERROR=${gerr} TIMEOUT=${gto} SKIP=${skip} ==="
        # Say out loud how much of the PASS count is value-verified. Without
        # this, "N queries PASS" reads as "N right answers" when part of it is
        # only "N right row counts" (round-2 README §13.4 item 3).
        echo "# ck=n/a queries are row-count-only: a saturated LIMIT window has no stable row set."
    } | tee -a "$report"
    # The status-delta channel (P5.6-f-v) — the counts above cannot say WHICH
    # query owns them, so the named delta goes directly under them, before the
    # (slower) plan pass. It reads the report just written; nothing it does can
    # perturb the verdict or the exit status below.
    if [[ "${SF05_NO_DELTA:-0}" == "1" ]]; then
        echo "# status-delta: skipped (SF05_NO_DELTA=1)" | tee -a "$report"
    else
        sf05_status_delta_channel "$report" "${delta_baseline}"
    fi
    # The plan-shape channel (P5.6-g-i-b) — second, NON-BLOCKING column. It runs
    # after the verdict is written, on its own fresh server, so nothing it does
    # can perturb the per-query seconds above or the exit status below.
    if [[ "${SF05_NO_PLANS:-0}" == "1" ]]; then
        echo "# plan-shape: skipped (SF05_NO_PLANS=1)" | tee -a "$report"
    else
        sf05_plan_channel "$report"
    fi
    log "sweep report: ${report}"
    # Gate semantics: correctness failures are fatal; timeouts are reported but
    # non-fatal (perf tracking, not a correctness gate). CKMISMATCH is a
    # correctness failure — the right number of wrong rows.
    [[ $((mismatch + ckmismatch + gerr)) -eq 0 ]]
}

# cmd_delta [OLD [NEW]] — the status channel on its own, over reports that
# already exist. Costs nothing and touches no server, so "what actually moved
# between those two sweeps?" is answerable at any time, including for the ~90
# reports archived before this channel existed.
cmd_delta() {
    local old="${1:-}" new="${2:-}"
    if [[ -z "${new}" ]]; then
        new=$(ls -t "${OUTDIR}"/sweep-*.txt 2>/dev/null | head -1 || true)
        [[ -n "${new}" ]] || die "no sweep reports in ${OUTDIR}"
    fi
    if [[ -z "${old}" ]]; then
        old=$(ls -t "${OUTDIR}"/sweep-*.txt 2>/dev/null | sed -n 2p || true)
        [[ -n "${old}" ]] || die "only one sweep report in ${OUTDIR} — nothing to diff against"
    fi
    python3 "${SWEEP_DIFF}" "${old}" "${new}"
}

# ------------------------------------------------------------------- status
cmd_status() {
    echo "SF0.5 TSVs   : $([[ -d ${SF05_DATA_DIR} ]] && ls "${SF05_DATA_DIR}"/*.tsv 2>/dev/null | wc -l || echo 0) files (${SF05_DATA_DIR})"
    echo "PG db        : $(${PG_ADMIN} -t -A -c "select count(*) from pg_database where datname='${SF05_PG_DB}'" 2>/dev/null || echo '?') (${SF05_PG_DB} on :${TPCDS_PG_PORT})"
    echo "goopg cluster: $([[ -d ${SF05_GOOPG_DATA} ]] && echo present || echo absent) (${SF05_GOOPG_DATA}, port ${SF05_PORT})"
    echo "oracle       : $([[ -s ${ORACLE} ]] && grep -c '|OK|' "${ORACLE}" || echo 0) OK entries (${ORACLE})"
    ls -t "${OUTDIR}"/sweep-*.txt 2>/dev/null | head -3 | sed 's/^/last sweeps  : /' || true
    ls -t "${OUTDIR}"/plans-*.txt 2>/dev/null | head -3 | sed 's/^/last plans   : /' || true
}

case "${1:-status}" in
build-data)  cmd_build_data ;;
load-pg)     cmd_load_pg ;;
load-goopg)  cmd_load_goopg ;;
oracle)      cmd_oracle ;;
sweep)       cmd_sweep ;;
plans)       cmd_plans ;;
delta)       shift; cmd_delta "${1:-}" "${2:-}" ;;
all)         cmd_build_data && cmd_load_pg && cmd_oracle && cmd_load_goopg && cmd_sweep ;;
status)      cmd_status ;;
*)           die "unknown subcommand '$1' (build-data|load-pg|load-goopg|oracle|sweep|plans|delta|all|status)" ;;
esac
