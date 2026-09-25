#!/usr/bin/env bash
# M0145-0008n: per-query live-route attribution on a private TPC-DS clone.
set -u
R=/home/ryo/work/goopg/goopg; cd $R
source bench/tpcds/env_tpcds.sh >/dev/null
export PATH=$R/postgres/local_install/bin:$PATH LD_LIBRARY_PATH=$R/postgres/local_install/lib
CORPUS=${1:-sf025}; case $CORPUS in sf025) SRC=$SF025_GOOPG_DATA; DB=${SF025_GOOPG_DB:-postgres};; sf1) SRC=$TPCDS_PGDATA; DB=${TPCDS_GOOPG_DB:-postgres};; esac
[ -f "$SRC/postmaster.pid" ] && { echo "source live"; exit 3; }
W=$R/tmp/m0145-0008n; CL=$W/clone-$CORPUS; LOG=$W/srv-$CORPUS.log; PORT=5591
rm -rf $CL; cp -a $SRC $CL; rm -f $CL/postmaster.pid
go build -o $W/goopg ./cmd/goopg || exit 4
: > $LOG
(GOOPG_NLI_CENSUS=1 GOOPG_CG_UNIT=m0145-0008n GOMEMLIMIT=12GiB scripts/goopg-test-run.sh $W/goopg start -D $CL --listen 127.0.0.1:$PORT --hba $CL/pg_hba.conf >> $LOG 2>&1 &)
for i in $(seq 1 120); do pg_isready -h 127.0.0.1 -p $PORT -q && break; sleep 1; done
out=$W/tpcds-$CORPUS-perq.tsv; : > $out
for q in $(seq 1 99); do
  f=$TPCDS_QUERY_DIR/query$q.sql; [ -f $f ] || continue
  before=$(wc -l < $LOG)
  { echo "SET statement_timeout='120s';"; echo -n "EXPLAIN "; cat $f; } > $W/q.sql
  timeout 150 psql -h 127.0.0.1 -p $PORT -U postgres -d $DB -X -q -f $W/q.sql >/dev/null 2>&1
  sleep 0.2
  seg=$(tail -n +$((before+1)) $LOG)
  rw=$(grep -c 'NLICENSUS route=rewrite' <<<"$seg"); ph=$(grep -c 'SUBLINKCENSUS route=jointree-posthoc' <<<"$seg"); pu=$(grep -c 'SUBLINKCENSUS route=jointree-pullup' <<<"$seg")
  printf "Q%s\tnli_rewrite=%s\tposthoc=%s\tpullup=%s\n" $q $rw $ph $pu >> $out
done
timeout 60 $W/goopg stop -D $CL >/dev/null 2>&1; systemctl --user stop m0145-0008n.scope 2>/dev/null
rm -rf $CL
awk -F'\t' '$2!="nli_rewrite=0" || $3!="posthoc=0"' $out
