#!/bin/bash
# M0131-S30.9 post-mortem: run the lost-row workload, then leave the server up
# and ask the surviving data WHERE the loss lives.
#
# The gate (`analysis/lostrows-concurrent-insert.sh`) only tells us how many
# committed INSERTs vanished. This variant answers the question that actually
# splits the search space: are the lost rows *overwritten in place* (their line
# pointer now holds a different id) or did the page *revert* to an earlier,
# shorter image (line pointers never spent)?
#
#   OVERWRITTEN  a lost id's index entry still resolves to a ctid, and the row
#                living at that ctid has a different id. Two writers computed
#                the same free offset on the same page.
#   REVERTED     the lost id has no index entry either, and the block holding
#                its neighbours simply stops early (fewer tuples than a full
#                page). A stale page image was written over a fuller one.
#
# Usage: [CLIENTS=8 CHUNKS=100 CHUNKROWS=100 KEEP=1] bash analysis/lostrows-postmortem.sh
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
W=${W:-/tmp/lostrows-pm}

q() { psql -h 127.0.0.1 -p $PORT -U postgres -d postgres -Atc "$1" 2>&1; }

rm -rf $W && mkdir -p $W
$REPO/bin/goopg init -D $W/data --no-sync >$W/init.log 2>&1 || { echo "INIT_FAIL"; tail -5 $W/init.log; exit 1; }
GOOPG_CG_UNIT=lostrowspm $REPO/scripts/goopg-test-run.sh \
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
# Clients only: a bare `wait` also waits on the backgrounded server job, which
# never exits (harness defect fixed 2026-08-12).
wait "${CLIENT_PIDS[@]}"

ROWS=$(q 'select count(*) from lr')
echo "rows=$ROWS want=$WANT"

# --- the two competing signatures -------------------------------------------
# Lost ids: absent from a heap-only (index-defeating) scan.
q "create table lost as
     select g as id from generate_series(1,$WANT) g
      where not exists (select 1 from lr b where b.id+0 = g)" >/dev/null
echo "lost=$(q 'select count(*) from lost')"

# Of the lost ids, how many are still REACHABLE through the primary key?
# A lost-but-indexed id means its heap line pointer was reused by another row
# (OVERWRITTEN); a lost-and-unindexed id means the whole insert evaporated.
echo "lost_but_indexed=$(q 'select count(*) from lost l where exists (select 1 from lr b where b.id = l.id)')"

# Per-block tuple census around the loss: does the block holding the
# immediately-preceding surviving id stop early?
echo "--- ctid of the ids surrounding the first lost run (is there an offset hole?) ---"
LO=$(q 'select min(id) from lost')
q "select id, ctid from lr where id+0 between $LO - 6 and $LO + 12 order by id"
echo "--- tuples-per-block distribution (a REVERTED/short page loses its tail) ---"
q "select tuples, count(*) as blocks from (
     select ltrim(split_part(ctid::text, ',', 1), '(') as blk, count(*) as tuples
       from lr group by 1) t group by 1 order by 1::int limit 40"

echo "--- first 20 lost ids ---"
q "select string_agg(id::text, ',') from (select id from lost order by id limit 20) t"

if [ "${KEEP:-0}" = "1" ]; then
    echo "server left running on port $PORT (datadir $W/data)"
else
    $REPO/bin/goopg stop -D $W/data >/dev/null 2>&1
fi
