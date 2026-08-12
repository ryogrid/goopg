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

CLIENT_PIDS=()
for c in $(seq 0 $((CLIENTS - 1))); do
    {
        for chunk in $(seq 0 $((CHUNKS - 1))); do
            lo=$((c * PERCLIENT + chunk * CHUNKROWS + 1))
            hi=$((lo + CHUNKROWS - 1))
            echo "insert into lr select g,'x' from generate_series($lo,$hi) g;"
        done | psql -h 127.0.0.1 -p $PORT -U postgres -d postgres -q -v ON_ERROR_STOP=1 >$W/client_$c.log 2>&1
        echo "$?" >$W/client_$c.rc
    } &
    CLIENT_PIDS+=($!)
done
# Wait for the CLIENTS ONLY. A bare `wait` also waits on the backgrounded
# server job, which never exits — that defect (fixed 2026-08-12) made this
# probe look like it hung for hours in its measurement queries when the load
# had in fact finished in under a minute, and cost two loops of wall clock.
wait "${CLIENT_PIDS[@]}"

bad=0
for c in $(seq 0 $((CLIENTS - 1))); do
    rc=$(cat $W/client_$c.rc 2>/dev/null)
    [ "$rc" = "0" ] || { echo "FAIL: client $c exited $rc"; tail -3 $W/client_$c.log; bad=1; }
done
ERRS=$(cat $W/client_*.log | grep -ci error)
[ "$ERRS" = "0" ] || { echo "FAIL: $ERRS client-side errors (a lost row must not be an error the client saw)"; bad=1; }

ROWS=$(q 'select count(*) from lr')

# The two shortfalls are computed by DUMPING the ids and diffing them in the
# shell, not with a server-side `NOT EXISTS (… WHERE b.id+0 = g)` anti-join.
# The anti-join formulation was retired on 2026-08-12 for two reasons found
# while chasing this bug: at WANT=80000 it does not finish (a post-mortem run
# sat in it for 35 min), and its answer is unverified — it reported exactly
# `6328` on two separate runs whose `count(*)` shortfalls differed (4078 vs
# 6328), so at least one of those numbers came from the anti-join and not from
# the data. The dump+diff below is O(n log n), always terminates, and every
# number it prints is independently checkable from the files it leaves in $W.
#
#   heap_missing        ids absent from a seq scan (`id+0` defeats the index):
#                       the committed INSERT's heap tuple is not there at all.
#   index_unreachable   ids absent from an index-driven scan while present in
#                       the heap: the btree entry is missing, which is what
#                       makes a later `UPDATE … WHERE id = ?` match zero rows.
#   heap_dupes          extra heap rows beyond the distinct ids — a PRIMARY KEY
#                       violation that survived COMMIT. Counted because the
#                       old metric could not distinguish "rows lost" from
#                       "rows lost AND duplicated".
q "select id+0 from lr" | sort -n | uniq >$W/heap_ids.txt
q "select id from lr where id > 0" | sort -n | uniq >$W/index_ids.txt
seq 1 $WANT >$W/want_ids.txt
MISSING=$(comm -23 $W/want_ids.txt $W/heap_ids.txt | tee $W/heap_missing.txt | wc -l)
UNREACH=$(comm -23 $W/want_ids.txt $W/index_ids.txt | tee $W/index_missing.txt | wc -l)
DUPES=$((ROWS - $(wc -l <$W/heap_ids.txt)))

echo "rows=$ROWS want=$WANT  heap_missing=$MISSING  index_unreachable=$UNREACH  heap_dupes=$DUPES"
[ "$DUPES" = "0" ] || { echo "FAIL: $DUPES duplicate heap rows survived a PRIMARY KEY"; bad=1; }
[ "$ROWS" = "$WANT" ] || { echo "FAIL: committed INSERTs lost from the heap ($ROWS of $WANT)"; bad=1; }
[ "$MISSING" = "0" ] || { echo "FAIL: $MISSING ids absent from the heap"; bad=1; }
[ "$UNREACH" = "0" ] || { echo "FAIL: $UNREACH ids present in the heap but unreachable through the primary key"; bad=1; }

$REPO/bin/goopg stop -D $W/data >/dev/null 2>&1
[ $bad -eq 0 ] && { echo "OVERALL: PASS"; exit 0; } || { echo "OVERALL: FAIL"; exit 1; }
