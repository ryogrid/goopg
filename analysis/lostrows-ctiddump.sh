#!/bin/bash
# M0131-S30.9 evidence collector: run the lost-row workload at a scale that
# actually loses rows, then dump every surviving (id, ctid) so the loss can be
# analysed OFF the server.
#
# Why a dump rather than server-side SQL: the obvious diagnostic
# (`generate_series(...) g WHERE NOT EXISTS (SELECT 1 FROM lr WHERE id+0 = g)`)
# is an index-defeating correlated anti-join and does not finish on goopg at
# this scale — the previous post-mortem hung in it for 35 min. Every question
# we need to ask (which ids are gone, which blocks they belonged to, whether a
# block's offsets have holes or stop early) is answerable from the dump.
#
# Leaves the server RUNNING so the pool can be interrogated further; stop it
# with `bin/goopg stop -D $W/data`.
#
# Usage: [CLIENTS=8 CHUNKS=100 CHUNKROWS=100 PORT=5533] bash analysis/lostrows-ctiddump.sh
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
W=${W:-/tmp/lostrows-dump}

q() { psql -h 127.0.0.1 -p $PORT -U postgres -d postgres -Atc "$1" 2>&1; }

rm -rf $W && mkdir -p $W
$REPO/bin/goopg init -D $W/data --no-sync >$W/init.log 2>&1 || { echo "INIT_FAIL"; tail -5 $W/init.log; exit 1; }
GOOPG_CG_UNIT=lostrowsdump $REPO/scripts/goopg-test-run.sh \
    $REPO/bin/goopg start -D $W/data --listen 127.0.0.1:$PORT >$W/server.log 2>&1 &
for i in $(seq 90); do q 'select 1' >/dev/null 2>&1 && break; sleep 1; done
q 'select 1' >/dev/null 2>&1 || { echo "SERVER_NEVER_UP"; tail -20 $W/server.log; exit 1; }

q 'create table lr(id int primary key, pad char(84))' >/dev/null

START=$(date +%s)
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
echo "load seconds: $(($(date +%s) - START))"

for c in $(seq 0 $((CLIENTS - 1))); do
    rc=$(cat $W/client_$c.rc 2>/dev/null)
    [ "$rc" = "0" ] || echo "client $c exited $rc: $(tail -2 $W/client_$c.log)"
done
echo "client error lines: $(cat $W/client_*.log | grep -ci error)"

echo "count=$(q 'select count(*) from lr')  want=$WANT"
# Seq scan (id+0 defeats the index) so the dump is heap truth, not index truth.
q "select id+0, ctid from lr" > $W/heap.tsv
q "select id, ctid from lr where id > 0" > $W/index.tsv
echo "heap rows dumped: $(wc -l < $W/heap.tsv)   index rows dumped: $(wc -l < $W/index.tsv)"
echo "server left running on port $PORT (datadir $W/data)"
