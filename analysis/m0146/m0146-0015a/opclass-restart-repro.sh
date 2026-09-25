#!/bin/bash
# usage: restart-repro.sh <binary> <datadir> <port>
EXTRA=${EXTRA:-select 1}; bin=$1; dd=$2; port=$3; B=postgres/local_install/bin
$bin stop -D $dd >/dev/null 2>&1; rm -rf $dd; $bin init -D $dd >/dev/null 2>&1
(GOOPG_CG_UNIT=rr-$port scripts/goopg-test-run.sh $bin start -D $dd --listen 127.0.0.1:$port > $dd.log 2>&1 &); sleep 4
$B/psql -h 127.0.0.1 -p $port -U postgres -d postgres -qAt -c "create table t1 (a int4)" -c "insert into t1 select g from generate_series(1,1000) g" -c "create index t1_a on t1 using btree (a ${OPC:-})" -c "$EXTRA" -c "select 'before', count(*) from t1 where a < 50"
$bin stop -D $dd >/dev/null; sleep 1
(GOOPG_CG_UNIT=rr-$port scripts/goopg-test-run.sh $bin start -D $dd --listen 127.0.0.1:$port > $dd.log2 2>&1 &); sleep 4
$B/psql -h 127.0.0.1 -p $port -U postgres -d postgres -qAt -c "select 'after', count(*), min(a) from t1 where a < 50" -c "select 'after-minmax', min(a), max(a) from t1"
$bin stop -D $dd >/dev/null
