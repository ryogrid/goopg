#!/usr/bin/env bash
# M0146-0035 reproduction harness: online clones of a busy throwaway source.
set -u
cd /home/ryo/work/goopg/goopg
export PATH=$PWD/postgres/local_install/bin:$PATH LD_LIBRARY_PATH=$PWD/postgres/local_install/lib
BIN=tmp/zz-goopg SRC=tmp/m35-src SP=5535 CP=5536 N=${N:-6} XMODE=${XMODE:-fetch} CHURN=${CHURN:-psql}
source scripts/lib/tpch-private-clone.sh
waitup(){ local j; for j in $(seq 1 90); do pg_isready -h 127.0.0.1 -p $1 >/dev/null 2>&1 && return 0; sleep 1; done; return 1; }
q(){ psql -h 127.0.0.1 -p $1 -U postgres -d postgres -Atc "select string_agg(datname,',' order by datname) from pg_database" 2>&1 | tr '\n' ' '; psql -h 127.0.0.1 -p $1 -U postgres -d postgres -Atc "select count(*) from pg_roles where rolname='tpch'" 2>&1 | tr '\n' ' '; }
rm -rf $SRC; $BIN init -D $SRC >/dev/null 2>&1 || { echo init failed; exit 1; }
(GOOPG_CG_UNIT=zz-m35src scripts/goopg-test-run.sh $BIN start -D $SRC --listen 127.0.0.1:$SP > tmp/m35-src.log 2>&1 &)
waitup $SP || { echo src down; exit 1; }
psql -h 127.0.0.1 -p $SP -U postgres -d postgres -c "CREATE ROLE tpch LOGIN SUPERUSER" -c "CREATE DATABASE tpch OWNER tpch" >/dev/null
psql -h 127.0.0.1 -p $SP -U tpch -d tpch -c "CREATE TABLE w(a int, b text)" >/dev/null
# busy writer + periodic checkpoints
if [ "$CHURN" = pgbench ]; then
  # CLOG page-0 churn: four clients committing single-row inserts back to back.
  echo "insert into w values (1, 'x');" > tmp/m35-churn.sql
  ( pgbench -h 127.0.0.1 -p $SP -U tpch -n -c 4 -T 3600 -f tmp/m35-churn.sql tpch >/dev/null 2>&1 ) & WPID=$!
else
  ( for w in $(seq 1 100000); do psql -h 127.0.0.1 -p $SP -U tpch -d tpch -qAtc "insert into w select g, md5(g::text) from generate_series(1,200) g" >/dev/null 2>&1 || break; done ) & WPID=$!
fi
( while kill -0 $WPID 2>/dev/null; do $BIN checkpoint -D $SRC >/dev/null 2>&1; sleep 3; done ) & CPID=$!
sleep 5
for k in $(seq 1 $N); do
  i=$k; D=tmp/m35-clone-$k; rm -rf $D
  if [ "$XMODE" = stream ]; then
    pg_basebackup -h 127.0.0.1 -p $SP -U postgres -D $D -X stream >/dev/null 2>&1 || { echo "clone $k failed"; continue; }
  else
    TPCH_CLONE_MODE=online tpch_private_clone_snapshot $SRC $D 127.0.0.1 $SP >/dev/null 2>&1 || { echo "clone $k failed"; continue; }
  fi
  i=$k
  (GOOPG_CG_UNIT=zz-m35c scripts/goopg-test-run.sh $BIN start -D $D --listen 127.0.0.1:$CP > tmp/m35-clone-$i.log 2>&1 &)
  waitup $CP || { echo "clone $i no start"; systemctl --user stop zz-m35c.scope; continue; }
  a=$(q $CP); $BIN stop -D $D >/dev/null 2>&1; systemctl --user stop zz-m35c.scope 2>/dev/null; sleep 1
  (GOOPG_CG_UNIT=zz-m35c scripts/goopg-test-run.sh $BIN start -D $D --listen 127.0.0.1:$CP >> tmp/m35-clone-$i.log 2>&1 &)
  waitup $CP; b=$(q $CP); $BIN stop -D $D >/dev/null 2>&1; systemctl --user stop zz-m35c.scope 2>/dev/null; sleep 1
  echo "clone $k: start1=[$a] start2=[$b]"
done
pkill -P $WPID 2>/dev/null; kill $WPID $CPID 2>/dev/null; wait $WPID 2>/dev/null
$BIN stop -D $SRC >/dev/null 2>&1; systemctl --user stop zz-m35src.scope 2>/dev/null
echo "src restart check:"; (GOOPG_CG_UNIT=zz-m35src scripts/goopg-test-run.sh $BIN start -D $SRC --listen 127.0.0.1:$SP >> tmp/m35-src.log 2>&1 &); waitup $SP; q $SP; echo; psql -h 127.0.0.1 -p $SP -U tpch -d tpch -Atc "select 'committed churn rows: ' || count(*) from w" 2>&1; $BIN stop -D $SRC >/dev/null 2>&1; systemctl --user stop zz-m35src.scope 2>/dev/null
