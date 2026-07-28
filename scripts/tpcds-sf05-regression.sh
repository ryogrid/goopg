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
#   scripts/tpcds-sf05-regression.sh oracle        # PG EXPLAIN ANALYZE -> oracle.txt + plans
#   scripts/tpcds-sf05-regression.sh load-goopg    # init+load goopg cluster on :65437
#   scripts/tpcds-sf05-regression.sh sweep         # goopg run vs oracle (the recurring gate)
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
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${REPO_ROOT}"
# shellcheck source=/dev/null
source "${REPO_ROOT}/bench/tpcds/env_tpcds.sh"

# Dirs/ports come from env_tpcds.sh: TPCDS_TOOLS, SF05_DATA_DIR,
# SF05_GOOPG_DATA, SF05_PORT (65437), SF05_PG_DB, SF05_LOG.
SRC_DATA_DIR="${TPCDS_DATA_DIR}"                   # SF=1 TSVs (input)
QDIR="${TPCDS_QUERY_DIR}"                          # SF=1 PG-fixed queries (reused)
OUTDIR="${SF05_RESULTS_DIR}"                       # env-overridable (env_tpcds.sh)
# The oracle is a GIT-TRACKED FIXTURE, not a run artefact: it stays in the
# canonical results dir even when SF05_RESULTS_DIR is redirected to a scratch
# dir for a one-off probe. Only 'oracle' (re-capture) writes it.
ORACLE="${SF05_ORACLE:-${TPCDS_RUNTIME_DIR}/tpcds-results-sf05/oracle.txt}"  # q|status|rows|secs
ORACLE_TIMEOUT="${ORACLE_TIMEOUT:-600}"
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
        pg_isready -h 127.0.0.1 -p "${SF05_PORT}" -U "${PG_SUPERUSER}" >/dev/null 2>&1 && return 0
        sleep 1
    done
    die "goopg (sf05) did not become ready in 180s — see ${SF05_LOG}"
}

# --------------------------------------------------------------- load-goopg
cmd_load_goopg() {
    guard_sf1_sweep
    [[ -d "${SF05_DATA_DIR}" ]] || die "run build-data first"
    [[ -x "${GOOPG_BIN}" ]] || ( cd "${REPO_ROOT}" && go build -o "${GOOPG_BIN}" ./cmd/goopg )
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
    # ANALYZE persists pg_statistic column stats (RowCount is NOT restored on
    # restart — known gap, tpcds-round2-fixes §7.1 — the gate is S-cold anyway).
    log "ANALYZE + CHECKPOINT"
    for t in ${TABLES}; do ${GOOPG_PSQL} -c "ANALYZE ${t}" >/dev/null 2>&1 || true; done
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

explain_analyze_script() {
    awk '
        { stmt = stmt $0 "\n" }
        /;[[:space:]]*$/ { printf "EXPLAIN (ANALYZE, TIMING OFF) %s", stmt; stmt = "" }
        END { if (stmt ~ /[^[:space:]]/) printf "EXPLAIN (ANALYZE, TIMING OFF) %s;\n", stmt }
    ' "$1"
}

# Sum the TOP-node actual row counts, one per statement. The top node is the
# first line after each ---- header; `actual rows=` must be matched explicitly
# because the same line carries the ESTIMATED `rows=` first.
sum_top_actual_rows() {
    awk '
        /^-+$/ { hdr=1; next }
        hdr==1 {
            if (match($0, /actual rows=[0-9]+/)) {
                sum += substr($0, RSTART+12, RLENGTH-12)
            }
            hdr=0
        }
        END { print sum+0 }
    ' "$1"
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
    log "Capturing PG oracle (EXPLAIN ANALYZE, timeout ${ORACLE_TIMEOUT}s/query) -> ${ORACLE}"
    {
        echo "# TPC-DS SF0.5 row-count oracle — PostgreSQL 18.3 ground truth"
        echo "# captured: $(date -Iseconds)  source: $(git -C "${REPO_ROOT}" log --oneline -1 | cut -d' ' -f1)"
        echo "# dataset: SF=1 TSVs, facts halved by key parity (see tpcds-sf05-regression.sh header)"
        echo "# format: q|status|rows|secs   (secs are machine-specific; rows are the fixture)"
        echo "# This file is GIT-TRACKED as a pinned fixture so other machines/CI can skip"
        echo "# the ~20 min PG capture. Re-run 'oracle' only when the dataset or queries change."
    } > "${ORACLE}"
    local q qf plan secs start rc rows status zero=0 okc=0
    for q in $(query_list); do
        if grep -qw "$q" <<<"${PG_SKIP}"; then
            echo "${q}|SKIP_QUERYGEN|0|0" >> "${ORACLE}"
            printf "Q%-3s SKIP (dsqgen artefact)\n" "$q"
            continue
        fi
        qf="${QDIR}/query${q}.sql"
        [[ -f "$qf" ]] || { echo "${q}|MISSING|0|0" >> "${ORACLE}"; continue; }
        plan="${OUTDIR}/pg_q${q}_analyze.txt"
        explain_analyze_script "$qf" > "${OUTDIR}/.ea.sql"
        start=$SECONDS
        timeout "${ORACLE_TIMEOUT}" ${PG_PSQL} -f "${OUTDIR}/.ea.sql" > "$plan" 2>&1 && rc=0 || rc=$?
        secs=$((SECONDS - start))
        if [[ $rc -eq 124 ]]; then
            status="TIMEOUT"; rows=0
            reap_pg_orphans >/dev/null
        elif grep -qE '^(psql:[^ ]*:[0-9]+: )?(ERROR|FATAL|PANIC):' "$plan"; then
            status="PG_ERROR"; rows=0
        else
            status="OK"; rows=$(sum_top_actual_rows "$plan")
            okc=$((okc+1)); [[ "$rows" == "0" ]] && zero=$((zero+1))
        fi
        echo "${q}|${status}|${rows}|${secs}" >> "${ORACLE}"
        printf "Q%-3s %-9s %6ss %8s rows\n" "$q" "$status" "$secs" "$rows"
    done
    rm -f "${OUTDIR}/.ea.sql"
    log "oracle done: ${okc} queries captured; ${zero} return 0 rows"
    [[ "$zero" -gt 0 ]] && log "NOTE: 0-row oracles give a weak regression signal (0==0 passes trivially)"
    true
}

# -------------------------------------------------------------------- sweep
cmd_sweep() {
    guard_sf1_sweep
    [[ -s "${ORACLE}" ]] || die "run oracle first"
    [[ -d "${SF05_GOOPG_DATA}" ]] || die "run load-goopg first"
    mkdir -p "${OUTDIR}"
    local report="${OUTDIR}/sweep-$(date +%Y%m%d-%H%M%S).txt"
    log "goopg SF0.5 sweep (timeout ${TIMEOUT_SEC}s/query, S-cold) -> ${report}"
    sf05_goopg_start
    local q line ostatus orows qf out rc secs start grows verdict
    local pass=0 mismatch=0 gerr=0 gto=0 skip=0
    {
        echo "# TPC-DS SF0.5 goopg sweep — $(date -Iseconds)"
        echo "# goopg: $(git log --oneline -1)"
        echo "# oracle: ${ORACLE} (PG 18.3 EXPLAIN ANALYZE row counts)"
        echo "# timeout: ${TIMEOUT_SEC}s"
        [[ -n "${QUERIES:-}" ]] && \
            echo "# SUBSET PROBE (QUERIES=${QUERIES}) — NOT a gate result; the summary below covers only these queries"
    } > "$report"
    for q in $(query_list); do
        line=$(grep -E "^${q}\|" "${ORACLE}" || true)
        ostatus=$(cut -d'|' -f2 <<<"$line"); orows=$(cut -d'|' -f3 <<<"$line")
        if [[ "$ostatus" != "OK" ]]; then
            printf "Q%-3s SKIP (oracle: %s)\n" "$q" "${ostatus:-absent}" | tee -a "$report"
            skip=$((skip+1)); continue
        fi
        qf="${QDIR}/query${q}.sql"
        start=$SECONDS
        out=$(timeout "${TIMEOUT_SEC}" ${GOOPG_PSQL} -f "$qf" 2>&1) && rc=0 || rc=$?
        secs=$((SECONDS - start))
        if [[ $rc -eq 124 ]]; then
            verdict="TIMEOUT"; gto=$((gto+1))
            printf "Q%-3s TIMEOUT  %4ss (oracle %s rows)\n" "$q" "$secs" "$orows" | tee -a "$report"
            if [[ "${RESTART_AFTER_TIMEOUT:-1}" == "1" ]]; then
                echo "      (restarting goopg to drop accumulated heap)" | tee -a "$report"
                sf05_goopg_start
            fi
        elif grep -qE '^(psql:[^ ]*:[0-9]+: )?(ERROR|FATAL|PANIC):|connection to server was lost|server closed the connection' <<<"$out"; then
            verdict="ERROR"; gerr=$((gerr+1))
            printf "Q%-3s ERROR    %4ss %s\n" "$q" "$secs" \
                "$(grep -E '(ERROR|FATAL|PANIC):' <<<"$out" | head -1 | cut -c1-90)" | tee -a "$report"
            # A dead server presents as a connection error on the NEXT query;
            # probe and restart so one crash doesn't cascade.
            pg_isready -h 127.0.0.1 -p "${SF05_PORT}" -U "${PG_SUPERUSER}" >/dev/null 2>&1 || sf05_goopg_start
        else
            grows_expr=$(grep -oP '\(\d+ rows?\)' <<<"$out" | grep -oP '\d+' | paste -sd+ - 2>/dev/null || true)
            grows=$(( ${grows_expr:-0} ))
            if [[ "$grows" == "$orows" ]]; then
                verdict="PASS"; pass=$((pass+1))
                printf "Q%-3s PASS     %4ss %8s rows\n" "$q" "$secs" "$grows" | tee -a "$report"
            else
                verdict="MISMATCH"; mismatch=$((mismatch+1))
                printf "Q%-3s MISMATCH %4ss goopg=%s oracle=%s\n" "$q" "$secs" "$grows" "$orows" | tee -a "$report"
            fi
        fi
    done
    sf05_goopg_stop
    {
        echo ""
        echo "=== SUMMARY: PASS=${pass} MISMATCH=${mismatch} ERROR=${gerr} TIMEOUT=${gto} SKIP=${skip} ==="
    } | tee -a "$report"
    log "sweep report: ${report}"
    # Gate semantics: correctness failures are fatal; timeouts are reported but
    # non-fatal (perf tracking, not a correctness gate).
    [[ $((mismatch + gerr)) -eq 0 ]]
}

# ------------------------------------------------------------------- status
cmd_status() {
    echo "SF0.5 TSVs   : $([[ -d ${SF05_DATA_DIR} ]] && ls "${SF05_DATA_DIR}"/*.tsv 2>/dev/null | wc -l || echo 0) files (${SF05_DATA_DIR})"
    echo "PG db        : $(${PG_ADMIN} -t -A -c "select count(*) from pg_database where datname='${SF05_PG_DB}'" 2>/dev/null || echo '?') (${SF05_PG_DB} on :${TPCDS_PG_PORT})"
    echo "goopg cluster: $([[ -d ${SF05_GOOPG_DATA} ]] && echo present || echo absent) (${SF05_GOOPG_DATA}, port ${SF05_PORT})"
    echo "oracle       : $([[ -s ${ORACLE} ]] && grep -c '|OK|' "${ORACLE}" || echo 0) OK entries (${ORACLE})"
    ls -t "${OUTDIR}"/sweep-*.txt 2>/dev/null | head -3 | sed 's/^/last sweeps  : /' || true
}

case "${1:-status}" in
build-data)  cmd_build_data ;;
load-pg)     cmd_load_pg ;;
load-goopg)  cmd_load_goopg ;;
oracle)      cmd_oracle ;;
sweep)       cmd_sweep ;;
all)         cmd_build_data && cmd_load_pg && cmd_oracle && cmd_load_goopg && cmd_sweep ;;
status)      cmd_status ;;
*)           die "unknown subcommand '$1' (build-data|load-pg|load-goopg|oracle|sweep|all|status)" ;;
esac
