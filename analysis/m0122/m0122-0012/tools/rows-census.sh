#!/usr/bin/env bash
# tmp/rows-census.sh <goopg-port> — EXPLAIN ANALYZE (costs off, timing off) of every TPC-DS query's first
# statement on goopg (SF0.25 clone) and on :65438 tpcds025, raw output into tmp/rowcensus/{g,p}NN.txt
set -u; cd /home/ryo/work/goopg/goopg; source bench/tpcds/env_tpcds.sh; export PATH=$PWD/postgres/local_install/bin:$PATH
GP=$1
for q in $(seq 1 99); do
  f=$TPCDS_QUERY_DIR/query$q.sql
  stmt=$( (echo "explain (analyze, costs off, timing off)"; sed -n '1,/;/p' $f) )
  [ -s tmp/rowcensus/p$q.txt ] || echo "$stmt" | timeout 300 psql -X -h 127.0.0.1 -p 65438 -U ryo -d tpcds025 > tmp/rowcensus/p$q.txt 2>&1
  echo "$stmt" | timeout 300 psql -X -h 127.0.0.1 -p $GP -U $TPCDS_SUPERUSER -d postgres > tmp/rowcensus/g$q.txt 2>&1
done
