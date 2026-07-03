#!/bin/bash
# Drive headless haiku audits for batches 09..21; each writes agent_verdict_NN.json itself.
# Concurrency 2, one retry per batch.
set -u
DIR="$(cd "$(dirname "$0")" && pwd)"
REPO=/home/ryo/work/goopg/goopg
MODEL=claude-haiku-4-5-20251001

run_one() {
  local NN="$1"
  local out="$DIR/agent_verdict_${NN}.json"
  [ -s "$out" ] && { echo "batch $NN already done"; return 0; }
  local prompt="You are auditing claims that certain features are UNIMPLEMENTED in the goopg codebase (a Go reimplementation of PostgreSQL) at $REPO. Each claim came from an old git commit message; later commits may have implemented the feature since.

Read the claim list: $DIR/agent_batch_${NN}.json

For EACH item, investigate the CURRENT code under $REPO/internal and $REPO/cmd using the Grep tool (search identifiers/SQL keywords) and the Read tool (read the surrounding function to judge). Verdicts:
- \"implemented\": the described behavior now exists as real logic (not TODO/stub/parse-only/no-op/catalog-recording-only). Cite file:line.
- \"open\": the gap remains (missing, stub, no-op, parse-only, or the SPECIFIC sub-behavior claimed is absent).
- \"unclear\": cannot determine.
Rules: judge the SPECIFIC claim, not the general feature area. A mention in a comment, test, or error string is NOT implementation. When torn between open and implemented, verify by reading the actual function body. Work efficiently: at most 4 tool calls per item.

When finished, use the Write tool to write EXACTLY this JSON to $out :
{\"verdicts\": [{\"key\": \"<key from input>\", \"verdict\": \"implemented|open|unclear\", \"evidence\": \"<file:line + 8-word reason>\"}, ...]}
One verdict per input item, same keys, same order as input. Then reply with just: DONE ${NN}"
  for attempt in 1 2; do
    timeout 900 claude -p "$prompt" --model "$MODEL" \
      --allowedTools "Read,Grep,Glob,Write" \
      --permission-mode acceptEdits \
      --add-dir "$REPO" >"$DIR/audit_run_${NN}.log" 2>&1
    if [ -s "$out" ] && python3 -c "import json;json.load(open('$out'))" 2>/dev/null; then
      echo "batch $NN OK (attempt $attempt)"
      return 0
    fi
    echo "batch $NN attempt $attempt failed; retrying after 30s"
    sleep 30
  done
  echo "batch $NN FAILED"
  return 1
}

cd "$DIR"
pids=()
fail=0
for NN in 09 10 11 12 13 14 15 16 17 18 19 20 21; do
  run_one "$NN" &
  pids+=($!)
  # cap concurrency at 2
  while [ "$(jobs -rp | wc -l)" -ge 2 ]; do wait -n || fail=1; done
done
while [ "$(jobs -rp | wc -l)" -gt 0 ]; do wait -n || fail=1; done
echo "ALL BATCHES PROCESSED fail=$fail"
ls "$DIR"/agent_verdict_*.json | wc -l
exit $fail
