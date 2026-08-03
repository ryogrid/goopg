#!/usr/bin/env bash
# M0125-0002 commit 7 — TPC-H PLANNING-TIME A/B, one arm per binary.
#
# WHY THIS AND NOT THE 22-QUERY EXECUTION POWER RUN
#
# D4 item 3 asks for a timed 22-query power run per commit, to catch a
# regression caused by a moved plan. Commit 7 moved NO plan: TPC-H is 22/22
# byte-identical A/B *and* byte-identical against `post-mhj-retire`, i.e. the
# CUMULATIVE diff across commits 1-7 is empty too, and the SF0.5 EXPLAIN A/B is
# 96/96 once the Q85 instrument flake is controlled for (see README).
#
# An execution power run therefore re-executes an unchanged plan set, and its
# noise floor (round-5 §3: 2-8 % moves unattributable) is wider than any effect
# it could attribute. What the eight conversions DID change is the planning
# code path: a hand-written type switch became a generic driver that builds an
# []exprSlot per node visited. That cost is real, it is invisible to a plan
# diff, and an execution run would bury it under 20-minute scans. This measures
# it head-on — EXPLAIN only, repeated, median reported.
#
# Usage:  capture-plantime.sh <arm-name> <goopg-binary> [reps]
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
REPO_ROOT="$PWD"

ARM="${1:?usage: capture-plantime.sh <arm> <binary> [reps]}"
BIN="${2:?usage: capture-plantime.sh <arm> <binary> [reps]}"
REPS="${3:-5}"
PGDATA="${REPO_ROOT}/bench/tpch/runtime_goopg/data"
PG_PORT=65433
CG_UNIT="goopg-tpch-c7pt"
OUT="analysis/m0125-0002-c7-plans-20260803/plantime-${ARM}.txt"
QDIR="${REPO_ROOT}/tmp/c7-tpch-queries"

export PATH="${REPO_ROOT}/postgres/local_install/bin:$PATH"

stop_server() {
    "${BIN}" stop -D "${PGDATA}" >/dev/null 2>&1 || true
    systemctl --user stop "${CG_UNIT}.scope" >/dev/null 2>&1 || true
    systemctl --user reset-failed "${CG_UNIT}.scope" >/dev/null 2>&1 || true
}
trap stop_server EXIT

stop_server
hba_arg=()
[[ -f "${PGDATA}/pg_hba.conf" ]] && hba_arg=(--hba "${PGDATA}/pg_hba.conf")
GOOPG_CG_UNIT="${CG_UNIT}" "${REPO_ROOT}/scripts/goopg-test-run.sh" \
    "${BIN}" start -D "${PGDATA}" --listen "127.0.0.1:${PG_PORT}" "${hba_arg[@]}" \
    > "analysis/m0125-0002-c7-plans-20260803/plantime-${ARM}.server.log" 2>&1 &
for _ in $(seq 1 180); do
    pg_isready -h 127.0.0.1 -p "${PG_PORT}" -U tpch >/dev/null 2>&1 && break
    sleep 1
done
pg_isready -h 127.0.0.1 -p "${PG_PORT}" -U tpch >/dev/null 2>&1 || {
    echo "FATAL: goopg (tpch) never became ready"; exit 1; }

{
    echo "# arm:     ${ARM}"
    echo "# binary:  ${BIN}  sha=$(sha256sum "${BIN}" | cut -c1-16)"
    echo "# reps:    ${REPS}   (median of ${REPS} EXPLAINs per query, ms)"
} > "${OUT}"

# All 22 EXPLAINs run in ONE psql session with \timing on. The per-query
# `date +%s%N` form this script first used reported a flat 4 ms for every
# query, which is psql's fork+connect floor, not planning: at SF=1 goopg plans
# a TPC-H query in well under a millisecond and the client overhead swamps it.
# \timing measures the statement round-trip inside an established session, so
# what is left is parse+plan+EXPLAIN-render. REPS sweeps are run and the median
# per query reported; the first sweep is discarded as warm-up (catalog and
# relation-size lookups the later ones do not pay).
{
    echo '\timing on'
    echo '\pset pager off'
    for _ in $(seq 1 $((REPS + 1))); do
        for q in $(seq 1 22); do
            f="${QDIR}/q${q}.sql"
            [[ -f "${f}" ]] || continue
            echo "\\echo MARK q${q}"
            echo "EXPLAIN"
            # tpch.Queries() carries no trailing semicolon; without one psql
            # never terminates the statement, every \echo flushes at scan time
            # and the whole sweep arrives as ONE query with ONE Time: line.
            cat "${f}"
            echo ";"
        done
    done
} > /tmp/c7_pt_session.sql

timeout 900 psql -h 127.0.0.1 -p "${PG_PORT}" -U tpch -d tpch -X -q \
    -f /tmp/c7_pt_session.sql > /tmp/c7_pt_session.out 2>&1
echo "session rc=$?"

python3 - "$OUT" "$REPS" <<'PYEOF' >> "${OUT}"
import re, sys
out, reps = sys.argv[1], int(sys.argv[2])
txt = open('/tmp/c7_pt_session.out').read().splitlines()
cur, times = None, {}
for line in txt:
    m = re.match(r'MARK q(\d+)', line.strip())
    if m:
        cur = int(m.group(1)); continue
    m = re.match(r'Time: ([0-9.]+) ms', line.strip())
    if m and cur is not None:
        times.setdefault(cur, []).append(float(m.group(1)))
        cur = None
for q in sorted(times):
    v = times[q][1:] or times[q]          # drop warm-up sweep
    v = sorted(v)
    med = v[len(v)//2]
    print('q%-3d %8.2f ms   n=%d  raw=%s' % (q, med, len(v),
          ' '.join('%.2f' % x for x in v)))
PYEOF
echo "arm ${ARM} done -> ${OUT}"
