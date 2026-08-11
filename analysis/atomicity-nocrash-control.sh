#!/bin/bash
# M0131-S30.8 CONTROL: does crashprobe30's atomicity invariant hold when NOTHING
# crashes?
#
# Why this script exists.  `analysis/crashprobe30.sh` only ever evaluates
#
#     sum(pgbench_accounts.abalance) == sum(pgbench_history.delta)
#
# AFTER a SIGKILL + replay, so every violation it reports has been attributed to
# WAL replay (S30.7, S30.8).  That attribution was never controlled.  Loop #141
# claimed a no-crash control also failed, and loop #142 retracted that claim
# because the harness used a bare `wait` that also waited on the backgrounded
# server (docs/design/0131-0024 §7) — so the control has still never been RUN
# correctly.  Until it is, S30.8's "replay defect" premise is unproven.
#
# What this measures, in three phases, with no crash anywhere:
#
#   LIVE       immediately after the load, on the still-running server that did
#              the writes.  A violation here is a runtime defect and rules the
#              replay path out entirely.
#   RESTART    after a CLEAN `goopg stop` + start.  A violation that appears
#              only here is a shutdown/replay defect — the S30.8 hypothesis,
#              reachable without any kill.
#   Both phases also cross-check pgbench's own client-side transaction count
#   against `count(*) from pgbench_history`, so "history rows are missing" and
#   "accounts updates are missing" can never be conflated.
#
# Harness hygiene (the S30.9 lesson — analysis/lostrows-concurrent-insert.sh):
# the server is backgrounded but is NEVER waited on; only pgbench's own pid is
# waited for, and the wait is verified by parsing pgbench's completion line
# before any query runs.  A partially-loaded table must never be measurable.
#
# CURRENT EXPECTED RESULT (2026-08-12, after the M0131-S32 chain-walk fix):
# the accounts/history arm PASSES, and the tellers/branches arm added on
# 2026-08-12 FAILS — under 16 clients the two narrow tables both diverge from
# sum(delta), in BOTH directions (one under-applied, one ~10x over-applied).
# That is the still-open concurrent half of S32, filed as M0131-S32.1; the
# single-client half is fixed and guarded by analysis/hotstall.sh and
# TestHOTUpdateChainBeyond64Versions. So `OVERALL: FAIL` here is the KNOWN
# state, not a fresh regression — read the per-table lines, not just the verdict.
#
# Usage: RUNS=2 bash analysis/atomicity-nocrash-control.sh
set -u
REPO=/home/ryo/work/goopg/goopg
export PATH=$REPO/postgres/local_install/bin:$PATH
export LD_LIBRARY_PATH=$REPO/postgres/local_install/lib:${LD_LIBRARY_PATH:-}
PORT=${PORT:-5534}
SCALE=${SCALE:-5}
ROWS=$((SCALE * 100000))
LOADSEC=${LOADSEC:-45}
CLIENTS=${CLIENTS:-16}
RUNS=${RUNS:-2}
BASE=${BASE:-/tmp/atomctl30}

q() { psql -h 127.0.0.1 -p $PORT -U postgres -d postgres -Atc "$1" 2>&1; }

# measure <label> -> echoes one line and sets globals via stdout parsing
measure() {
    local label=$1
    read -r N DISTINCT MN MX <<<"$(q "select count(*)||' '||count(distinct aid)||' '||min(aid)||' '||max(aid) from pgbench_accounts")"
    local ABAL DELTA HN IDXN TBAL BBAL
    ABAL=$(q "select coalesce(sum(abalance),0) from pgbench_accounts")
    DELTA=$(q "select coalesce(sum(delta),0) from pgbench_history")
    HN=$(q "select count(*) from pgbench_history")
    IDXN=$(q "select count(*) from pgbench_accounts where aid between 1 and $ROWS")
    # M0131-S32: TPC-B updates accounts, tellers AND branches by the same
    # :delta in one transaction, so all three sums must equal sum(delta).
    # Only accounts was checked until 2026-08-12, and it is the one table too
    # wide (500k rows at scale 5) to hit the defect — tellers (50 rows) and
    # branches (5 rows) update the SAME row thousands of times, which is
    # exactly what the truncated 64-version chain walk broke. These two are
    # therefore the sensitive arm of this control, not redundant with accounts.
    TBAL=$(q "select coalesce(sum(tbalance),0) from pgbench_tellers")
    BBAL=$(q "select coalesce(sum(bbalance),0) from pgbench_branches")
    echo "  [$label] count=$N distinct=$DISTINCT min=$MN max=$MX idx_count=$IDXN"
    echo "  [$label] sum(abalance)=$ABAL sum(history.delta)=$DELTA history_rows=$HN (pgbench processed=$PROCESSED)"
    echo "  [$label] sum(tbalance)=$TBAL sum(bbalance)=$BBAL"
    local bad=0
    [ "$N" = "$ROWS" ] || { echo "  FAIL[$label]: count(*)=$N want $ROWS"; bad=1; }
    [ "$DISTINCT" = "$ROWS" ] || { echo "  FAIL[$label]: count(distinct aid)=$DISTINCT want $ROWS"; bad=1; }
    [ "$IDXN" = "$ROWS" ] || { echo "  FAIL[$label]: index-driven count=$IDXN want $ROWS"; bad=1; }
    [ "$ABAL" = "$DELTA" ] || { echo "  FAIL[$label]: ATOMICITY sum(abalance)=$ABAL != sum(delta)=$DELTA"; bad=1; }
    [ "$TBAL" = "$DELTA" ] || { echo "  FAIL[$label]: ATOMICITY sum(tbalance)=$TBAL != sum(delta)=$DELTA"; bad=1; }
    [ "$BBAL" = "$DELTA" ] || { echo "  FAIL[$label]: ATOMICITY sum(bbalance)=$BBAL != sum(delta)=$DELTA"; bad=1; }
    [ "$HN" = "$PROCESSED" ] || { echo "  FAIL[$label]: history_rows=$HN != pgbench processed=$PROCESSED"; bad=1; }
    return $bad
}

fail=0
for run in $(seq 1 $RUNS); do
    echo "================ RUN $run/$RUNS (NO CRASH) ================"
    W=$BASE/run$run
    DIR=$W/data
    rm -rf $W && mkdir -p $W
    $REPO/bin/goopg init -D $DIR --no-sync >$W/init.log 2>&1 || { echo "INIT_FAIL"; tail -5 $W/init.log; exit 1; }
    GOOPG_CG_UNIT=atomctl-$run-a $REPO/scripts/goopg-test-run.sh \
        $REPO/bin/goopg start -D $DIR --listen 127.0.0.1:$PORT >$W/server.log 2>&1 &
    for i in $(seq 90); do q 'select 1' >/dev/null 2>&1 && break; sleep 1; done
    q 'select 1' >/dev/null 2>&1 || { echo "SERVER_NEVER_UP"; tail -20 $W/server.log; exit 1; }

    pgbench -h 127.0.0.1 -p $PORT -U postgres -i -s $SCALE postgres >$W/pgbench_init.log 2>&1 \
        || { echo "PGBENCH_INIT_FAIL"; tail -5 $W/pgbench_init.log; exit 1; }
    echo "loaded scale=$SCALE rows=$ROWS"

    pgbench -h 127.0.0.1 -p $PORT -U postgres -c $CLIENTS -j 4 -T $LOADSEC postgres >$W/pgbench_run.log 2>&1 &
    PB=$!
    # NEVER a bare `wait` here: it would also wait on the backgrounded server
    # (S30.9 / docs/design/0131-0024 §7).  Wait on the client pid only.
    wait $PB
    PROCESSED=$(sed -n 's/^number of transactions actually processed: \([0-9]*\).*/\1/p' $W/pgbench_run.log | head -1)
    case "${PROCESSED:-}" in
    ''|*[!0-9]*) echo "LOAD_ABORT: pgbench did not report a transaction count"; tail -20 $W/pgbench_run.log; exit 1 ;;
    esac
    echo "pgbench finished: $PROCESSED transactions"

    bad=0
    measure LIVE || bad=1

    # ---- clean stop + restart (no kill anywhere) --------------------------
    $REPO/bin/goopg stop -D $DIR >$W/stop.log 2>&1 || { echo "STOP_FAIL"; tail -20 $W/stop.log; fail=1; continue; }
    for i in $(seq 60); do q 'select 1' >/dev/null 2>&1 || break; sleep 1; done
    GOOPG_CG_UNIT=atomctl-$run-b $REPO/scripts/goopg-test-run.sh \
        $REPO/bin/goopg start -D $DIR --listen 127.0.0.1:$PORT >$W/restart.log 2>&1 &
    for i in $(seq 90); do q 'select 1' >/dev/null 2>&1 && break; sleep 1; done
    q 'select 1' >/dev/null 2>&1 || { echo "RESTART_FAIL"; tail -30 $W/restart.log; fail=1; continue; }
    echo "RESTART_OK (clean shutdown, no crash)"
    measure RESTART || bad=1

    [ $bad -eq 0 ] && echo "RUN $run: PASS" || { echo "RUN $run: FAIL"; fail=1; }
    $REPO/bin/goopg stop -D $DIR >/dev/null 2>&1
    sleep 2
done
echo "================================================"
[ $fail -eq 0 ] && echo "OVERALL: PASS ($RUNS runs, no crash)" || echo "OVERALL: FAIL"
exit $fail
