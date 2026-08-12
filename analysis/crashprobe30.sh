#!/bin/bash
# M0131-S30 re-measurement: does a goopg crash actually lose heap rows?
#
# S30's original evidence was refuted (docs/design/0131-0019): its kill never
# fired (`pgrep -f "bin/goopg start -D $DIR"` did not match the cgroup-wrapped
# command line) and its "missing rows" query was an anti-join goopg executes as
# an Index Scan, so it measured index reachability, not row presence. This
# probe fixes both halves:
#
#   PROVEN KILL   the pid comes from $DIR/postmaster.pid, is verified against
#                 /proc/<pid>/cmdline before the signal and against /proc
#                 after it, and the run ABORTS if either check fails. A probe
#                 that silently does not crash is worse than no probe.
#
#   HEAP-ONLY     the assertions never depend on an index. count(*) /
#   ASSERTIONS    count(distinct aid) / min / max over pgbench_accounts pin the
#                 aid set exactly (n = distinct = 500000, min 1, max 500000 =>
#                 precisely {1..500000}, no loss AND no duplication) without an
#                 anti-join. The `aid+0` anti-join is kept only as an
#                 independent cross-check of that conclusion.
#
#   ATOMICITY     sum(pgbench_accounts.abalance) must equal
#   INVARIANT     sum(pgbench_history.delta): pgbench inits abalance to 0 and
#                 every txn does UPDATE accounts SET abalance=abalance+delta
#                 plus INSERT INTO history(delta) in ONE transaction. A crash
#                 that tears a transaction, replays one half, or drops a
#                 committed update breaks this equality. This is the assertion
#                 that would actually catch the non-atomic HeapDelete+HeapInsert
#                 fallback S30 hypothesised.
#
#   INDEX PARITY  a seq-scan count and an index-driven count are compared, so a
#                 regression of the M0131-S31 HOT-chain prune fix under crash
#                 traffic shows up as a divergence rather than as silence.
#
# Usage: RUNS=3 bash analysis/crashprobe30.sh
set -u
REPO=/home/ryo/work/goopg/goopg
export PATH=$REPO/postgres/local_install/bin:$PATH
export LD_LIBRARY_PATH=$REPO/postgres/local_install/lib:${LD_LIBRARY_PATH:-}
PORT=${PORT:-5534}
SCALE=${SCALE:-5}
ROWS=$((SCALE * 100000))
LOADSEC=${LOADSEC:-45}
KILLAT=${KILLAT:-30}
RUNS=${RUNS:-3}
BASE=/tmp/crashprobe30

q() { psql -h 127.0.0.1 -p $PORT -U postgres -d postgres -Atc "$1" 2>&1; }

fail=0
for run in $(seq 1 $RUNS); do
    echo "================ RUN $run/$RUNS ================"
    W=$BASE/run$run
    DIR=$W/data
    rm -rf $W && mkdir -p $W
    $REPO/bin/goopg init -D $DIR --no-sync >$W/init.log 2>&1 || { echo "INIT_FAIL"; tail -5 $W/init.log; exit 1; }
    GOOPG_CG_UNIT=cp30-$run-a $REPO/scripts/goopg-test-run.sh \
        $REPO/bin/goopg start -D $DIR --listen 127.0.0.1:$PORT >$W/server.log 2>&1 &
    for i in $(seq 90); do q 'select 1' >/dev/null 2>&1 && break; sleep 1; done
    q 'select 1' >/dev/null 2>&1 || { echo "SERVER_NEVER_UP"; tail -20 $W/server.log; exit 1; }

    pgbench -h 127.0.0.1 -p $PORT -U postgres -i -s $SCALE postgres >$W/pgbench_init.log 2>&1 \
        || { echo "PGBENCH_INIT_FAIL"; tail -5 $W/pgbench_init.log; exit 1; }
    echo "loaded scale=$SCALE rows=$ROWS"

    pgbench -h 127.0.0.1 -p $PORT -U postgres -c 16 -j 4 -T $LOADSEC postgres >$W/pgbench_run.log 2>&1 &
    PB=$!
    sleep $KILLAT

    # ---- PROVEN KILL ------------------------------------------------------
    PIDFILE=$DIR/postmaster.pid
    [ -r "$PIDFILE" ] || { echo "KILL_ABORT: no $PIDFILE"; exit 1; }
    PID=$(head -1 "$PIDFILE" | tr -d '[:space:]')
    case "$PID" in ''|*[!0-9]*) echo "KILL_ABORT: bad pid '$PID'"; exit 1;; esac
    CMD=$(tr '\0' ' ' <"/proc/$PID/cmdline" 2>/dev/null)
    case "$CMD" in
    *goopg*start*) : ;;
    *) echo "KILL_ABORT: pid $PID is not a goopg server (cmdline: '$CMD')"; exit 1 ;;
    esac
    echo "killing pid=$PID cmdline='$CMD'"
    kill -9 "$PID" 2>/dev/null
    for i in $(seq 30); do [ -d "/proc/$PID" ] || break; sleep 0.2; done
    if [ -d "/proc/$PID" ]; then echo "KILL_ABORT: pid $PID survived SIGKILL"; exit 1; fi
    if q 'select 1' >/dev/null 2>&1; then echo "KILL_ABORT: server still answering after kill"; exit 1; fi
    echo "KILL_PROVEN pid=$PID gone, port dead"
    wait $PB 2>/dev/null
    sleep 2

    # ---- snapshot the crashed WAL (M0131-S30.1b) --------------------------
    # Recovery appends over the bytes past its stop point, so the evidence for
    # an early end-of-WAL is destroyed by the very restart that reports it.
    # Copy pg_wal (and pg_control) aside BEFORE the restart. SNAPWAL=0 opts out.
    if [ "${SNAPWAL:-1}" = "1" ]; then
        cp -a "$DIR/pg_wal" "$W/pg_wal_crash" 2>/dev/null
        cp -a "$DIR/global" "$W/global_crash" 2>/dev/null
        echo "WAL_SNAPSHOT $W/pg_wal_crash"
    fi

    # ---- restart ----------------------------------------------------------
    GOOPG_CG_UNIT=cp30-$run-b $REPO/scripts/goopg-test-run.sh \
        $REPO/bin/goopg start -D $DIR --listen 127.0.0.1:$PORT >$W/restart.log 2>&1 &
    for i in $(seq 90); do q 'select 1' >/dev/null 2>&1 && break; sleep 1; done
    q 'select 1' >/dev/null 2>&1 || { echo "RESTART_FAIL"; tail -30 $W/restart.log; fail=1; continue; }
    echo "RESTART_OK"

    # ---- heap-only assertions --------------------------------------------
    read -r N DISTINCT MN MX <<<"$(q "select count(*)||' '||count(distinct aid)||' '||min(aid)||' '||max(aid) from pgbench_accounts")"
    ABAL=$(q "select coalesce(sum(abalance),0) from pgbench_accounts")
    DELTA=$(q "select coalesce(sum(delta),0) from pgbench_history")
    IDXN=$(q "select count(*) from pgbench_accounts where aid between 1 and $ROWS")
    ANTI=$(q "select count(*) from generate_series(1,$ROWS) g left join (select aid+0 as a from pgbench_accounts) s on s.a = g.g where s.a is null")

    echo "count=$N distinct=$DISTINCT min=$MN max=$MX"
    echo "sum(abalance)=$ABAL sum(history.delta)=$DELTA"
    echo "idx_count=$IDXN heap_anti_missing=$ANTI"

    bad=0
    [ "$N" = "$ROWS" ] || { echo "FAIL: count(*)=$N want $ROWS"; bad=1; }
    [ "$DISTINCT" = "$ROWS" ] || { echo "FAIL: count(distinct aid)=$DISTINCT want $ROWS"; bad=1; }
    [ "$MN" = "1" ] || { echo "FAIL: min(aid)=$MN want 1"; bad=1; }
    [ "$MX" = "$ROWS" ] || { echo "FAIL: max(aid)=$MX want $ROWS"; bad=1; }
    [ "$ABAL" = "$DELTA" ] || { echo "FAIL: atomicity sum(abalance)=$ABAL != sum(delta)=$DELTA"; bad=1; }
    [ "$ANTI" = "0" ] || { echo "FAIL: heap-only anti-join reports $ANTI missing aids"; bad=1; }
    [ "$IDXN" = "$ROWS" ] || { echo "FAIL: index-driven count=$IDXN want $ROWS (index reachability)"; bad=1; }
    [ $bad -eq 0 ] && echo "RUN $run: PASS" || { echo "RUN $run: FAIL"; fail=1; }

    $REPO/bin/goopg stop -D $DIR >/dev/null 2>&1
    sleep 2
done
echo "================================================"
[ $fail -eq 0 ] && echo "OVERALL: PASS ($RUNS runs)" || echo "OVERALL: FAIL"
exit $fail
