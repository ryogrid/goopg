#!/usr/bin/env bash
# E-11 serial-path arm: the same acceptance harness, but with
# max_parallel_workers_per_gather = 0 per query, so refillPrefetchWindow is
# actually LIVE (it returns early for a parallel scan). Scan-heavy subset.
set -u
REPO=/home/ryo/work/goopg/goopg
cd "$REPO" || exit 1
SP=/tmp/claude-1000/-home-ryo-work-goopg-goopg/b8e0d095-04da-41e7-b272-805106e9cf98/scratchpad/e11b
BIN=$SP/goopg-e11b
export PATH="$REPO/postgres/local_install/bin:$PATH"
export LD_LIBRARY_PATH="$REPO/postgres/local_install/lib"
PGDATA=$REPO/bench/tpch/runtime_goopg/data

arm() {
  local depth="$1"
  local rep="$2"
  local label="s${depth}-r${rep}"
  echo "== $(date -Is) START $label"
  "$BIN" stop -D "$PGDATA" >/dev/null 2>&1 || true
  systemctl --user stop "goopg-tpch-acceptance-${label}.scope" >/dev/null 2>&1 || true
  systemctl --user reset-failed "goopg-tpch-acceptance-${label}.scope" >/dev/null 2>&1 || true
  GOOPG_SEQSCAN_LOOKAHEAD="$depth" \
  GOOPG_BIN="$BIN" NO_BUILD=1 PGSHAPED=1 COLLAPSE=1 PER_Q=900 \
  GOGC=100 GOMEMLIMIT=12GiB GOOPG_ANALYZE_SEED=20260905 \
  QUERIES="1,3,6,12,14,19,22" \
    scripts/tpch-acceptance-arm.sh "$label" "$SP/$label.txt" -parallel-workers 0 \
    >"$SP/$label.log" 2>&1
  echo "== $(date -Is) DONE  $label rc=$? oklines=$(grep -cE '^Q[0-9]+: OK' "$SP/$label.txt")"
  sleep 10
}

arm 4 1; arm 0 1; arm 16 1
arm 0 2; arm 4 2; arm 16 2
arm 4 3; arm 0 3
echo "SERIAL-DONE $(date -Is)"
