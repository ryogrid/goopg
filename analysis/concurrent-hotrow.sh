#!/bin/bash
# M0131-S32.1 PROBE: minimal deterministic repro of the concurrent hot-row
# mis-update that survives the S32 chain-walk fix.
#
# Why this exists.  The gate for S32.1 is `analysis/atomicity-nocrash-control.sh`
# (pgbench TPC-B, scale 5, 16 clients, 30-45 s).  That harness proves the defect
# but is far too coarse to localise it: a TPC-B transaction touches four tables
# behind BEGIN/COMMIT, so an under-applied `pgbench_tellers` and a ~10x
# OVER-applied `pgbench_branches` cannot be attributed to a single code path.
#
# This probe strips the workload to its essence — C sessions each applying N
# blind increments to a FEW rows of ONE table, so a given row is updated
# thousands of times concurrently, exactly like pgbench_branches (5 rows) and
# pgbench_tellers (50 rows).  The invariant is arithmetic and exact:
#
#     sum(v) MUST equal C*N   (every increment applied exactly once)
#
#   sum(v) <  C*N  => lost updates   (the pgbench_tellers direction)
#   sum(v) >  C*N  => over-application, i.e. one statement incremented the same
#                     logical row more than once (the pgbench_branches
#                     direction — the duplicate-live-index-entry hypothesis)
#
# ROWS is a knob because the two directions in the pgbench arm differ only in
# how narrow the table is: run it at 5 (branches-like) and 50 (tellers-like).
#
# Usage: ROWS=5 CLIENTS=8 N=200 bash analysis/concurrent-hotrow.sh
set -u
REPO=/home/ryo/work/goopg/goopg
export PATH=$REPO/postgres/local_install/bin:$PATH
export LD_LIBRARY_PATH=$REPO/postgres/local_install/lib:${LD_LIBRARY_PATH:-}
PORT=${PORT:-5535}
ROWS=${ROWS:-5}
CLIENTS=${CLIENTS:-8}
N=${N:-200}
TXN=${TXN:-0}          # 1 = wrap each UPDATE in BEGIN/COMMIT (pgbench-like)
# MULTI=1 (S32.3) closes the last structural gap to the pgbench harness: the hot
# UPDATE stops being the ONLY statement in its transaction.  TPC-B updates a
# WIDE table (accounts, 500k rows — never contended), reads it back, updates the
# hot narrow table, then INSERTs a history row, all inside one BEGIN/COMMIT.  So
# by the time the hot UPDATE runs, the transaction already owns an XID, has
# written tuples, and is several command-ids in — none of which the single
# statement repro exercises.  MULTI implies TXN.  The invariant becomes
#     sum(t.v) == count(h)   (one history row per committed hot increment)
# which is exactly what atomicity-nocrash-control.sh checks as
# sum(bbalance) == sum(history.delta).
MULTI=${MULTI:-0}
[ "$MULTI" = "1" ] && TXN=1
WIDE=${WIDE:-2000}     # rows in the uncontended wide table (accounts analogue)
W=${W:-/tmp/hotrow32}

q() { psql -h 127.0.0.1 -p $PORT -U postgres -d postgres -Atc "$1" 2>&1; }

# Preflight: an orphaned server from an earlier probe listening on $PORT would
# silently absorb the whole run (the fresh server fails to bind, every psql
# still connects, and the numbers come from the WRONG cluster).  This bit once.
if ss -ltn 2>/dev/null | grep -q "127.0.0.1:$PORT "; then
    echo "PORT_BUSY: something already listens on 127.0.0.1:$PORT — stop it first"
    exit 1
fi

rm -rf $W && mkdir -p $W
$REPO/bin/goopg init -D $W/data --no-sync >$W/init.log 2>&1 || { echo "INIT_FAIL"; tail -5 $W/init.log; exit 1; }
GOOPG_CG_UNIT=hotrow32 $REPO/scripts/goopg-test-run.sh \
    $REPO/bin/goopg start -D $W/data --listen 127.0.0.1:$PORT >$W/server.log 2>&1 &
for i in $(seq 90); do q 'select 1' >/dev/null 2>&1 && break; sleep 1; done
q 'select 1' >/dev/null 2>&1 || { echo "SERVER_NEVER_UP"; tail -20 $W/server.log; exit 1; }

# NOIDX=1 drops the primary key so the same UPDATE is driven by a SeqScan
# instead of an IndexScan — the two update operators are separate code paths
# (updateOp index arm vs seq arm), so this splits the search space in half.
if [ "${NOIDX:-0}" = "1" ]; then
    q "create table t(id int, v bigint not null default 0)" >/dev/null
else
    q "create table t(id int primary key, v bigint not null default 0)" >/dev/null
fi
for r in $(seq 1 $ROWS); do q "insert into t values ($r, 0)" >/dev/null; done
if [ "$MULTI" = "1" ]; then
    q "create table w(id int primary key, v bigint not null default 0)" >/dev/null
    q "insert into w select g, 0 from generate_series(1,$WIDE) g" >/dev/null
    q "create table h(hid bigserial, id int, ts timestamp)" >/dev/null
fi
echo "table t: $ROWS rows, clients=$CLIENTS updates/client=$N txn=$TXN multi=$MULTI"

# Each client k hammers row (k mod ROWS)+1 so the collision pattern is fixed.
for c in $(seq 1 $CLIENTS); do
    id=$(( (c - 1) % ROWS + 1 ))
    : >$W/c$c.sql
    for i in $(seq 1 $N); do
        if [ "$MULTI" = "1" ]; then
            # TPC-B statement order, verbatim in shape: wide UPDATE, read-back
            # SELECT, hot UPDATE, history INSERT.  The wide row id is derived
            # from the client and iteration so no two clients collide there —
            # only `t` is contended, as in pgbench.
            wid=$(( (c * 7919 + i * 31) % WIDE + 1 ))
            printf 'BEGIN;\nUPDATE w SET v = v + 1 WHERE id = %d;\nSELECT v FROM w WHERE id = %d;\nUPDATE t SET v = v + 1 WHERE id = %d;\nINSERT INTO h(id, ts) VALUES (%d, now());\nCOMMIT;\n' \
                $wid $wid $id $id >>$W/c$c.sql
        elif [ "$TXN" = "1" ]; then
            printf 'BEGIN;\nUPDATE t SET v = v + 1 WHERE id = %d;\nCOMMIT;\n' $id >>$W/c$c.sql
        else
            printf 'UPDATE t SET v = v + 1 WHERE id = %d;\n' $id >>$W/c$c.sql
        fi
    done
done
PIDS=""
for c in $(seq 1 $CLIENTS); do
    psql -h 127.0.0.1 -p $PORT -U postgres -d postgres -q -f $W/c$c.sql >$W/c$c.log 2>&1 &
    PIDS="$PIDS $!"
done
# Wait on the CLIENT pids only — a bare `wait` would also wait on the
# backgrounded server (the S30.9 harness lesson).
for p in $PIDS; do wait $p; done
echo "clients done; errors: $(cat $W/c*.log | grep -c ERROR)"

WANT=$((CLIENTS * N))
if [ "$MULTI" = "1" ]; then
    # An error anywhere in the transaction rolls back BOTH the hot UPDATE and
    # its history row, so the committed-work invariant is sum(t.v)==count(h),
    # mirroring sum(bbalance)==sum(history.delta).  CLIENTS*N is still printed
    # so a run that lost whole transactions to errors is distinguishable from
    # one that lost only the hot UPDATE.
    HN=$(q "select count(*) from h")
    WBAL=$(q "select coalesce(sum(v),0) from w")
    echo "history_rows=$HN (issued $WANT)  sum(w.v)=$WBAL"
    WANT=$HN
fi
GOT=$(q "select coalesce(sum(v),0) from t")
CNT=$(q "select count(*) from t")
IDXCNT=$(q "select count(*) from t where id between 1 and $ROWS")
echo "sum(v)=$GOT want=$WANT   count(*)=$CNT (want $ROWS) index_count=$IDXCNT"
q "select id, v from t order by id" | sed 's/^/  row /'
# On a duplicate-live-version failure the physical layout is the evidence:
# ctid tells which page/slot each surviving version sits in, xmin/xmax which
# transaction wrote it and whether anybody stamped it dead.
if [ "$(q "select count(*) from t")" != "$ROWS" ]; then
    echo "  --- physical versions (ctid, xmin, xmax) ---"
    q "select ctid||' v='||v from t order by v" | sed 's/^/  ver /' | head -20
fi

bad=0
[ "$CNT" = "$ROWS" ] || { echo "FAIL: count(*)=$CNT want $ROWS"; bad=1; }
[ "$IDXCNT" = "$ROWS" ] || { echo "FAIL: index-driven count=$IDXCNT want $ROWS"; bad=1; }
if [ "$MULTI" = "1" ]; then
    # The wide table is the accounts analogue: uncontended, so it must be exact.
    # If it diverges too, the defect is not contention-specific.
    [ "$WBAL" = "$HN" ] || { echo "FAIL: sum(w.v)=$WBAL != history_rows=$HN (UNCONTENDED table diverged)"; bad=1; }
fi
if [ "$GOT" = "$WANT" ]; then
    echo "OVERALL: PASS"
else
    if [ "$GOT" -lt "$WANT" ]; then echo "FAIL: LOST UPDATES ($((WANT - GOT)) increments missing)"
    else echo "FAIL: OVER-APPLIED ($((GOT - WANT)) extra increments)"; fi
    bad=1
fi
$REPO/bin/goopg stop -D $W/data >/dev/null 2>&1
[ $bad -eq 0 ] && exit 0 || { echo "OVERALL: FAIL"; exit 1; }
