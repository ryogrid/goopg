#!/bin/bash
# M0131-S32: a single-session, single-row UPDATE loop silently stops applying.
#
# The minimal deterministic repro. ONE client, autocommit, no concurrency, no
# crash, no special config:
#
#     CREATE TABLE t(id int primary key, v bigint);  INSERT INTO t VALUES (1,0);
#     UPDATE t SET v = v + 1 WHERE id = 1;    -- x300, one session
#
# `v` advances 1,2,...,64 and then FREEZES at 64 forever while every remaining
# UPDATE still reports `UPDATE 1` and commits. The row is never lost
# (count(*) stays 1) and no error is ever raised, so the only observable is a
# wrong value. 64 is stable across runs, which is why this is read as "the page
# runs out of room for another HOT version and the fallback path then no-ops"
# rather than as a race.
#
# The probe deliberately reads `v` through `WHERE id+0=1` (seq scan) as well as
# `WHERE id=1` (index scan): after a longer pgbench-driven run the index read
# can return NOTHING while the seq read returns the (stale) row, so the two must
# never be conflated. Both are reported.
#
# Usage: bash analysis/hotstall.sh            # N=300 sequential updates
#        N=1000 PORT=5537 bash analysis/hotstall.sh
set -u
REPO=/home/ryo/work/goopg/goopg
export PATH=$REPO/postgres/local_install/bin:$PATH
export LD_LIBRARY_PATH=$REPO/postgres/local_install/lib:${LD_LIBRARY_PATH:-}
PORT=${PORT:-5537}
N=${N:-300}
STEP=${STEP:-20}
W=${W:-/tmp/hotstall}
DIR=$W/data

rm -rf $W && mkdir -p $W
$REPO/bin/goopg init -D $DIR --no-sync >$W/init.log 2>&1 || { echo "INIT_FAIL"; tail -5 $W/init.log; exit 1; }
GOOPG_CG_UNIT=hotstall $REPO/scripts/goopg-test-run.sh \
    $REPO/bin/goopg start -D $DIR --listen 127.0.0.1:$PORT >$W/server.log 2>&1 &
q() { psql -h 127.0.0.1 -p $PORT -U postgres -d postgres -Atc "$1" 2>&1; }
for i in $(seq 90); do q 'select 1' >/dev/null 2>&1 && break; sleep 1; done
q 'select 1' >/dev/null 2>&1 || { echo "SERVER_NEVER_UP"; tail -20 $W/server.log; exit 1; }

q "create table t(id int primary key, v bigint)" >/dev/null
q "insert into t values (1,0)" >/dev/null

{
    for i in $(seq 1 $N); do
        echo "UPDATE t SET v=v+1 WHERE id=1;"
        [ $((i % STEP)) -eq 0 ] && echo "SELECT $i AS n, (SELECT v FROM t WHERE id+0=1) AS seq_v, (SELECT count(*) FROM t) AS rows;"
    done
} >$W/seq.sql
psql -h 127.0.0.1 -p $PORT -U postgres -d postgres -f $W/seq.sql >$W/seq.out 2>&1
grep -E '^ *[0-9]+ \|' $W/seq.out

SEQV=$(q "select v from t where id+0=1")
IDXV=$(q "select v from t where id=1")
NROWS=$(q "select count(*) from t")
echo "updates_issued=$N seq_read='$SEQV' idx_read='$IDXV' rows=$NROWS"

bad=0
[ "$SEQV" = "$N" ] || { echo "FAIL: seq read v=$SEQV want $N ($((N - ${SEQV:-0})) committed updates silently not applied)"; bad=1; }
[ "$IDXV" = "$N" ] || { echo "FAIL: index read v='$IDXV' want $N"; bad=1; }
[ "$NROWS" = "1" ] || { echo "FAIL: count(*)=$NROWS want 1"; bad=1; }
[ $bad -eq 0 ] && echo "OVERALL: PASS" || echo "OVERALL: FAIL"

$REPO/bin/goopg stop -D $DIR >/dev/null 2>&1
exit $bad
