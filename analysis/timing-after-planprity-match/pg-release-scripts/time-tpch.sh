#!/usr/bin/env bash
# TPC-H SF1 timing on the private non-cassert PG — mirrors tmp/tpch-gap-gc.sh (the goopg column's harness):
# fresh server start -> runner pass 1, pass 2 (Q15 skipped) -> stop. Repeated REPS times.
# Headline (decided in advance) = rep 1 pass 2, the same statistic as goopg's single pass 2.
set -uo pipefail
source "$(dirname "$0")/common.sh"
REPS=${REPS:-3}; Q=1,2,3,4,5,6,7,8,9,10,11,12,13,14,16,17,18,19,20,21,22
OUT=$B/results/tpch; mkdir -p $OUT
trap 'pg_stop $TPCH_DATA' EXIT
pg_stop $TPCH_DATA 2>/dev/null
for r in $(seq 1 $REPS); do
  # bounded wait while a heavy Ralph harness runs (never stop it)
  for _ in $(seq 1 40); do [[ $(pgrep -fc 'tpcds-sf025-regression.sh|tpcds-bench-compare.sh|run-nightly.sh') -eq 0 ]] && break; sleep 30; done
  echo "rep$r before: $(loadinfo)" | tee -a $OUT/conditions.txt
  pg_start $REL $TPCH_DATA $TPCH_PORT tpch-r$r >> $OUT/conditions.txt || exit 1
  for pass in 1 2; do
    $REPO/tmp/tpch-acceptance-runner -host 127.0.0.1 -port $TPCH_PORT -db tpch -user tpch -password tpch -queries "$Q" > $OUT/rep$r.pass$pass.raw 2>&1
    echo "rep$r pass$pass OK=$(grep -c ': OK ' $OUT/rep$r.pass$pass.raw)" | tee -a $OUT/conditions.txt
  done
  echo "rep$r after:  $(loadinfo)" | tee -a $OUT/conditions.txt
  pg_stop $TPCH_DATA
done
