#!/usr/bin/env bash
# E-11 / EX5-04 depth sweep (re-run on a quiet machine, 2026-09-06).
# One binary, fresh capped server per arm, interleaved depth order.
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
  local label="d${depth}-r${rep}"
  echo "== $(date -Is) START $label"
  # reap any orphan from a previous arm before starting the next one
  "$BIN" stop -D "$PGDATA" >/dev/null 2>&1 || true
  systemctl --user stop "goopg-tpch-acceptance-${label}.scope" >/dev/null 2>&1 || true
  systemctl --user reset-failed "goopg-tpch-acceptance-${label}.scope" >/dev/null 2>&1 || true
  echo "   loadavg=$(cut -d' ' -f1-3 /proc/loadavg)"
  GOOPG_SEQSCAN_LOOKAHEAD="$depth" \
  GOOPG_BIN="$BIN" NO_BUILD=1 PGSHAPED=1 COLLAPSE=1 PER_Q=600 \
  GOGC=100 GOMEMLIMIT=12GiB GOOPG_ANALYZE_SEED=20260905 \
    scripts/tpch-acceptance-arm.sh "$label" "$SP/$label.txt" >"$SP/$label.log" 2>&1
  echo "== $(date -Is) DONE  $label rc=$? oklines=$(grep -cE '^Q[0-9]+: OK' "$SP/$label.txt")"
  sleep 10
}

# Interleaved so arm position is balanced across depths (drift cannot
# masquerade as a depth effect).
arm 4   1; arm 0   1; arm 16  1; arm 64  1; arm 128 1
arm 128 2; arm 64  2; arm 16  2; arm 0   2; arm 4   2
arm 16  3; arm 128 3; arm 4   3; arm 0   3; arm 64  3
echo "SWEEP-DONE $(date -Is)"
