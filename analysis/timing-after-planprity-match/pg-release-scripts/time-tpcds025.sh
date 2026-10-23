#!/usr/bin/env bash
# TPC-DS SF0.25 timing on the private non-cassert PG — mirrors scripts/tpcds-sf025-regression.sh `sweep`
# (the goopg column's harness): per sweep a FRESH server, then Q1..Q99 once each, sequentially,
# `timeout 300 psql -h 127.0.0.1 -p PORT -U <u> -d <db> -f queryN.sql > file 2>&1` (no -X), ms via EPOCHREALTIME,
# rows+checksum judged against the git-tracked PG oracle with the gate's own tpcds-result-checksum.py.
# Sweep 0 is a discarded warm-up (OS page cache hot, as goopg's hourly sweeps kept it); sweeps 1..3 are used (median).
set -uo pipefail
source "$(dirname "$0")/common.sh"
SWEEPS=${SWEEPS:-3}
QDIR=$REPO/bench/tpcds/runtime_goopg/tpcds-data/queries
ORACLE=$REPO/bench/tpcds/runtime_goopg/tpcds-results-sf025/oracle.txt
CKSUM=$REPO/scripts/tpcds-result-checksum.py
OUT=$B/results/tpcds025; mkdir -p $OUT
PSQL="$CLI/psql -h 127.0.0.1 -p $TPCDS_PORT -U ryo -d tpcds025"
trap 'pg_stop $TPCDS_DATA' EXIT
pg_stop $TPCDS_DATA 2>/dev/null
limits() { grep -oiE 'limit +[0-9]+' "$1" 2>/dev/null | grep -oE '[0-9]+' | sort -un | paste -sd, - || true; }
for s in $(seq 0 $SWEEPS); do
  for _ in $(seq 1 40); do [[ $(pgrep -fc 'tpcds-sf025-regression.sh|tpcds-bench-compare.sh|run-nightly.sh') -eq 0 ]] && break; sleep 30; done
  rep=$OUT/sweep$s.txt
  echo "# sweep $s before: $(loadinfo)" > $rep
  pg_start $REL $TPCDS_DATA $TPCDS_PORT tpcds-s$s ryo >> $rep || exit 1
  for q in $(seq 1 99); do
    qf=$QDIR/query$q.sql; res=$OUT/.result.txt
    m0=${EPOCHREALTIME/./}
    timeout 300 $PSQL -f "$qf" > "$res" 2>&1 && rc=0 || rc=$?
    ms=$(awk -v us=$(( ${EPOCHREALTIME/./} - m0 )) 'BEGIN{printf "%.0f", us/1000}')
    o=$(grep -E "^${q}\|" $ORACLE); orows=$(cut -d'|' -f3 <<<"$o"); ock=$(cut -d'|' -f4 <<<"$o")
    if [[ $rc -eq 124 ]]; then v=TIMEOUT
    elif [[ $rc -ne 0 ]] || grep -qE '^(psql:[^ ]*:[0-9]+: )?(ERROR|FATAL|PANIC):|connection to server was lost' "$res"; then v=ERROR
    else
      out=$(python3 -I $CKSUM "$res" --limits "$(limits "$qf")" 2>/dev/null)
      rows=$(sed -n 's/.*rows=\([0-9]*\).*/\1/p' <<<"$out"); ck=$(sed -n 's/.*ck=\([^ ]*\).*/\1/p' <<<"$out")
      if [[ "$rows" != "$orows" ]]; then v="MISMATCH(rows=$rows)"
      elif [[ "$ock" != "n/a" && "$ck" != "$ock" ]]; then v="CKMISMATCH"
      else v=PASS; fi
    fi
    printf "Q%-3s %-10s %6sms rows=%s\n" $q "$v" $ms "$orows" >> $rep
  done
  echo "# sweep $s after: $(loadinfo)" >> $rep
  pg_stop $TPCDS_DATA
  echo "sweep $s: $(grep -c ' PASS ' $rep) PASS, total $(awk '/ms rows/{sub("ms","",$3); t+=$3} END{print t}' $rep) ms"
done
