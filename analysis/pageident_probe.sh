#!/bin/bash
# M0131-S30.3 page-identity probe (no crash needed).
#
# Runs the same pgbench load as analysis/crashprobe30.sh with
# GOOPG_PAGEIDENT_PROBE=1 and greps the server log for the two impossible
# events the probe reports (see internal/storage/pageident_probe.go):
#
#   PAGEIDENT-REGRESS   buffer tag/content aliasing
#   PAGEIDENT-REEXTEND  a block number handed out twice
#
# Usage: bash analysis/pageident_probe.sh   (env: PORT SCALE LOADSEC CLIENTS)
set -u
REPO=/home/ryo/work/goopg/goopg
export PATH=$REPO/postgres/local_install/bin:$PATH
export LD_LIBRARY_PATH=$REPO/postgres/local_install/lib:${LD_LIBRARY_PATH:-}
PORT=${PORT:-5536}
SCALE=${SCALE:-5}
LOADSEC=${LOADSEC:-45}
CLIENTS=${CLIENTS:-16}
W=${W:-/tmp/pageident_probe}
DIR=$W/data

q() { psql -h 127.0.0.1 -p $PORT -U postgres -d postgres -Atc "$1" 2>&1; }

rm -rf $W && mkdir -p $W
$REPO/bin/goopg init -D $DIR --no-sync >$W/init.log 2>&1 || { echo "INIT_FAIL"; tail -5 $W/init.log; exit 1; }
GOOPG_PAGEIDENT_PROBE=1 GOOPG_CG_UNIT=pgident $REPO/scripts/goopg-test-run.sh \
    $REPO/bin/goopg start -D $DIR --listen 127.0.0.1:$PORT >$W/server.log 2>&1 &
for i in $(seq 90); do q 'select 1' >/dev/null 2>&1 && break; sleep 1; done
q 'select 1' >/dev/null 2>&1 || { echo "SERVER_NEVER_UP"; tail -20 $W/server.log; exit 1; }

pgbench -h 127.0.0.1 -p $PORT -U postgres -i -s $SCALE postgres >$W/pgbench_init.log 2>&1 \
    || { echo "PGBENCH_INIT_FAIL"; tail -5 $W/pgbench_init.log; exit 1; }
echo "loaded scale=$SCALE; init-phase probe hits:"
grep -c PAGEIDENT $W/server.log || true

pgbench -h 127.0.0.1 -p $PORT -U postgres -c $CLIENTS -j 4 -T $LOADSEC postgres >$W/pgbench_run.log 2>&1
tail -3 $W/pgbench_run.log

$REPO/bin/goopg stop -D $DIR >>$W/server.log 2>&1
sleep 2
echo "================ PAGEIDENT events ================"
grep -n PAGEIDENT $W/server.log | head -50
echo "total: $(grep -c PAGEIDENT $W/server.log || true)   log: $W/server.log"
