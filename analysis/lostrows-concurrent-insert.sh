#!/bin/bash
# M0131-S30.9: do concurrent, plainly-committed INSERTs lose rows on a live
# goopg server — with NO crash anywhere?
#
# This probe exists because S30.8 was filed as a crash-RECOVERY defect ("a
# crash tears transactions"). It is not: `analysis/crashprobe30.sh`'s atomicity
# invariant also fails when nothing ever crashes, so the crash probe was
# measuring a live-server data-loss bug through a recovery-shaped lens.
#
# The probe is deliberately the simplest workload that shows it:
#   * a fresh cluster (so no earlier corruption can be blamed),
#   * ONE table with a primary key,
#   * N single-statement INSERT transactions per client, each inserting a
#     disjoint, fully deterministic id range — no updates, no deletes, no
#     contention on any row, nothing for MVCC to resolve,
#   * every client's psql exit status and error output is checked, so a lost
#     row cannot be a swallowed client-side failure.
#
# Two assertions, both heap-only vs index-driven so the two failure modes stay
# distinguishable:
#   ROWS      seq-scan count(*) must equal CLIENTS*CHUNKS*CHUNKROWS. A shortfall
#             is a committed INSERT whose heap tuple is not there at all.
#   REACHABLE an index-driven probe (NOT EXISTS ... WHERE id = g) over the id
#             range must find every id. A shortfall here with a full heap is a
#             missing btree entry — the form that makes a later
#             `UPDATE ... WHERE id = ?` silently match zero rows.
#
# Usage: [CLIENTS=8 CHUNKS=100 CHUNKROWS=100] bash analysis/lostrows-concurrent-insert.sh
set -u
REPO=$(cd "$(dirname "$0")/.." && pwd)
export PATH=$REPO/postgres/local_install/bin:$PATH
export LD_LIBRARY_PATH=$REPO/postgres/local_install/lib:${LD_LIBRARY_PATH:-}
PORT=${PORT:-5533}
CLIENTS=${CLIENTS:-8}
CHUNKS=${CHUNKS:-100}
CHUNKROWS=${CHUNKROWS:-100}
PERCLIENT=$((CHUNKS * CHUNKROWS))
WANT=$((CLIENTS * PERCLIENT))
W=${W:-/tmp/lostrows}

q() { psql -h 127.0.0.1 -p $PORT -U postgres -d postgres -Atc "$1" 2>&1; }

rm -rf $W && mkdir -p $W
$REPO/bin/goopg init -D $W/data --no-sync >$W/init.log 2>&1 || { echo "INIT_FAIL"; tail -5 $W/init.log; exit 1; }
GOOPG_CG_UNIT=lostrows $REPO/scripts/goopg-test-run.sh \
    $REPO/bin/goopg start -D $W/data --listen 127.0.0.1:$PORT >$W/server.log 2>&1 &
for i in $(seq 90); do q 'select 1' >/dev/null 2>&1 && break; sleep 1; done
q 'select 1' >/dev/null 2>&1 || { echo "SERVER_NEVER_UP"; tail -20 $W/server.log; exit 1; }

q 'create table lr(id int primary key, pad char(84))' >/dev/null

for c in $(seq 0 $((CLIENTS - 1))); do
    {
        for chunk in $(seq 0 $((CHUNKS - 1))); do
            lo=$((c * PERCLIENT + chunk * CHUNKROWS + 1))
            hi=$((lo + CHUNKROWS - 1))
            echo "insert into lr select g,'x' from generate_series($lo,$hi) g;"
        done | psql -h 127.0.0.1 -p $PORT -U postgres -d postgres -q -v ON_ERROR_STOP=1 >$W/client_$c.log 2>&1
        echo "$?" >$W/client_$c.rc
    } &
done
wait

bad=0
for c in $(seq 0 $((CLIENTS - 1))); do
    rc=$(cat $W/client_$c.rc 2>/dev/null)
    [ "$rc" = "0" ] || { echo "FAIL: client $c exited $rc"; tail -3 $W/client_$c.log; bad=1; }
done
ERRS=$(cat $W/client_*.log | grep -ci error)
[ "$ERRS" = "0" ] || { echo "FAIL: $ERRS client-side errors (a lost row must not be an error the client saw)"; bad=1; }

ROWS=$(q 'select count(*) from lr')
# id+0 defeats the index, so this counts heap presence only.
UNREACH=$(q "select count(*) from generate_series(1,$WANT) g where not exists (select 1 from lr b where b.id = g)")
MISSING=$(q "select count(*) from generate_series(1,$WANT) g where not exists (select 1 from lr b where b.id+0 = g)")

echo "rows=$ROWS want=$WANT  heap_missing=$MISSING  index_unreachable=$UNREACH"
[ "$ROWS" = "$WANT" ] || { echo "FAIL: committed INSERTs lost from the heap ($ROWS of $WANT)"; bad=1; }
[ "$MISSING" = "0" ] || { echo "FAIL: $MISSING ids absent from the heap"; bad=1; }
[ "$UNREACH" = "0" ] || { echo "FAIL: $UNREACH ids present in the heap but unreachable through the primary key"; bad=1; }

$REPO/bin/goopg stop -D $W/data >/dev/null 2>&1
[ $bad -eq 0 ] && { echo "OVERALL: PASS"; exit 0; } || { echo "OVERALL: FAIL"; exit 1; }
