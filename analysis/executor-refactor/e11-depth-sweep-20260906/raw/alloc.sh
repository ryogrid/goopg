#!/usr/bin/env bash
# E-11 alloc arm: serial (non-parallel) Q6 + Q1 over lineitem, where the
# prefetch window is actually live (refillPrefetchWindow returns early for a
# parallel scan), profiled with pprof alloc_objects / alloc_space.
set -u
REPO=/home/ryo/work/goopg/goopg
cd "$REPO" || exit 1
SP=/tmp/claude-1000/-home-ryo-work-goopg-goopg/b8e0d095-04da-41e7-b272-805106e9cf98/scratchpad/e11b
BIN=$SP/goopg-e11b
export PATH="$REPO/postgres/local_install/bin:$PATH"
export LD_LIBRARY_PATH="$REPO/postgres/local_install/lib"
export GOGC=100 GOMEMLIMIT=12GiB PGPASSWORD=tpch
export GOOPG_PGSHAPED_DP=1 GOOPG_PGSHAPED_COLLAPSE=1
PGDATA=$REPO/bench/tpch/runtime_goopg/data
PPROF=127.0.0.1:6171

Q6="select sum(l_extendedprice * l_discount) as revenue from lineitem where l_shipdate >= date '1994-01-01' and l_shipdate < date '1994-01-01' + interval '1 year' and l_discount between 0.05 - 0.01 and 0.05 + 0.01 and l_quantity < 24"
P() { psql -h 127.0.0.1 -p 65433 -U tpch -d tpch -Atc "set max_parallel_workers_per_gather=0; $1"; }

alloc_arm() {
  local depth="$1"
  local label="a-d${depth}"
  local unit="goopg-e11-alloc-d${depth}"
  echo "===== $(date -Is) ALLOC arm depth=$depth"
  "$BIN" stop -D "$PGDATA" >/dev/null 2>&1 || true
  systemctl --user stop "$unit.scope" >/dev/null 2>&1 || true
  systemctl --user reset-failed "$unit.scope" >/dev/null 2>&1 || true
  GOOPG_PPROF_ADDR=$PPROF GOOPG_SEQSCAN_LOOKAHEAD="$depth" GOOPG_ANALYZE_SEED=20260905 \
  GOOPG_MEM_HIGH=20G GOOPG_MEM_MAX=24G GOOPG_MEM_SWAP_MAX=0 GOOPG_CG_UNIT="$unit" \
    scripts/goopg-test-run.sh "$BIN" start -D "$PGDATA" --listen 127.0.0.1:65433 \
    --hba "$PGDATA/pg_hba.conf" >"$SP/$label.server.log" 2>&1 &
  local spid=$!
  for _ in $(seq 1 180); do pg_isready -h 127.0.0.1 -p 65433 -U postgres -q && break; sleep 1; done
  local pid; pid=$(pgrep -f "goopg-e11b start -D" | head -1)
  echo "--- DEFAULT-parallelism plan (prefetch window is SKIPPED on a parallel scan):"
  psql -h 127.0.0.1 -p 65433 -U tpch -d tpch -Atc "explain $Q6" | head -8
  echo "--- serial plan (bare Seq Scan => window is live):"
  P "explain $Q6" | head -6
  echo "--- warm-up (S-cold first touch, not counted):"
  /usr/bin/time -f "   cold serial Q6 wall=%e s" psql -h 127.0.0.1 -p 65433 -U tpch -d tpch \
    -Atc "set max_parallel_workers_per_gather=0; $Q6" 2>&1 | tail -1
  echo -n "   io before: "; grep -E "^read_bytes" /proc/$pid/io | tr '\n' ' '; echo
  curl -sf "http://$PPROF/debug/pprof/heap?gc=1" -o "$SP/$label.heap.before" || echo "CURL-BEFORE-FAILED"
  local t0; t0=$(date +%s.%N)
  for i in 1 2 3 4 5 6 7 8; do P "$Q6" >/dev/null; done
  local t1; t1=$(date +%s.%N)
  curl -sf "http://$PPROF/debug/pprof/heap?gc=1" -o "$SP/$label.heap.after" || echo "CURL-AFTER-FAILED"
  echo -n "   io after:  "; grep -E "^read_bytes" /proc/$pid/io | tr '\n' ' '; echo
  echo "   8x serial Q6 window wall=$(echo "$t1 - $t0" | bc) s"
  for idx in alloc_objects alloc_space; do
    echo "--- pprof $idx (depth $depth), top 10:"
    go tool pprof -sample_index=$idx -top -nodecount=10 -base "$SP/$label.heap.before" \
      "$BIN" "$SP/$label.heap.after" 2>&1 | grep -vE "^(File|Build|Type|Time|Duration)" | head -16
  done
  timeout 60 "$BIN" stop -D "$PGDATA" >/dev/null 2>&1 || true
  wait $spid 2>/dev/null || true
  systemctl --user stop "$unit.scope" >/dev/null 2>&1 || true
  systemctl --user reset-failed "$unit.scope" >/dev/null 2>&1 || true
  sleep 5
}

alloc_arm 4
alloc_arm 0
alloc_arm 64
alloc_arm 4
echo "ALLOC-DONE $(date -Is)"
