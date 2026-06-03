#!/usr/bin/env bash
# Stage 4 (PAID, GATED) - LLM lesson-mining map step.
#
# For each chunk produced by `mine_lessons.py chunk`, call a CHEAP model
# (`claude -p --model haiku`) to extract structured efficiency lessons, append
# the JSON array elements to data/lessons_raw.jsonl, then the caller runs
# `mine_lessons.py reduce` to cluster them (free).
#
# COST SAFETY: dry-run is the DEFAULT. Nothing is sent to the API until you pass
# --confirm. Dry-run prints the chunk count and the cost estimate only.
#
#   ./mine_lessons.sh                 # dry run: show what it would do, no API calls
#   ./mine_lessons.sh --confirm       # actually call Haiku over all chunks
#   ./mine_lessons.sh --confirm --max-chunks 20   # bound the spend
#
# Flags also exposed via run.sh as: run.sh --with-llm [--llm-confirm].
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUT="${KAIZEN_OUT:-$SCRIPT_DIR/data}"
MODEL="${KAIZEN_LLM_MODEL:-haiku}"   # alias resolves to latest Haiku
CONFIRM=0
MAX_CHUNKS=0   # 0 = no cap

while [[ $# -gt 0 ]]; do
  case "$1" in
    --confirm) CONFIRM=1; shift ;;
    --max-chunks) MAX_CHUNKS="$2"; shift 2 ;;
    --model) MODEL="$2"; shift 2 ;;
    --out) OUT="$2"; shift 2 ;;
    -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

CHUNK_DIR="$OUT/chunks"
RAW="$OUT/lessons_raw.jsonl"
MANIFEST="$OUT/chunk_manifest.json"

if [[ ! -d "$CHUNK_DIR" ]]; then
  echo "[mine] no chunks at $CHUNK_DIR - run: python3 mine_lessons.py chunk" >&2
  exit 1
fi

mapfile -t CHUNKS < <(ls -1 "$CHUNK_DIR"/chunk_*.txt 2>/dev/null | sort)
N=${#CHUNKS[@]}
if [[ "$MAX_CHUNKS" -gt 0 && "$MAX_CHUNKS" -lt "$N" ]]; then
  CHUNKS=("${CHUNKS[@]:0:$MAX_CHUNKS}")
fi
TODO=${#CHUNKS[@]}

echo "[mine] model=$MODEL  chunks=$TODO/$N  out=$RAW"
if [[ -f "$MANIFEST" ]]; then
  python3 - "$MANIFEST" <<'PY' || true
import json,sys
m=json.load(open(sys.argv[1]))
ch=m.get("total_chars",0); n=m.get("chunks",0)
intok=ch/4; est=intok/1e6*1.0 + n*600/1e6*5.0 + n*0.05  # +~$0.05/call sys-prompt overhead
print(f"[mine] est input ~{intok/1000:.0f}k tok, est cost ~${est:.2f} (Haiku 4.5, incl per-call overhead)")
PY
fi

if [[ "$CONFIRM" -ne 1 ]]; then
  echo "[mine] DRY RUN - pass --confirm to call the API. Nothing sent."
  exit 0
fi

if ! command -v claude >/dev/null 2>&1; then
  echo "[mine] 'claude' CLI not found on PATH" >&2; exit 1
fi

: > "$RAW"
i=0
for chunk in "${CHUNKS[@]}"; do
  i=$((i+1))
  printf "[mine] %d/%d %s\r" "$i" "$TODO" "$(basename "$chunk")" >&2
  # --output-format json gives {result, total_cost_usd, ...}; we keep .result.
  # No tools are needed for extraction; disallow them to avoid headless hangs.
  resp="$(claude -p --model "$MODEL" --output-format json --max-turns 1 \
              --disallowed-tools "Bash,Edit,Write,Read,Grep,Glob" \
              < "$chunk" 2>/dev/null || true)"
  text="$(printf '%s' "$resp" | python3 -c 'import json,sys
try: print(json.load(sys.stdin).get("result",""))
except Exception: pass' 2>/dev/null || true)"
  # The model returns a JSON array; flatten to one object per line.
  printf '%s' "$text" | python3 -c '
import json,sys,re
t=sys.stdin.read().strip()
m=re.search(r"\[.*\]", t, re.S)
if not m: sys.exit(0)
try: arr=json.loads(m.group(0))
except Exception: sys.exit(0)
for el in arr:
    if isinstance(el,dict): print(json.dumps(el))
' >> "$RAW" 2>/dev/null || true
done
echo "" >&2
echo "[mine] wrote $(wc -l < "$RAW") lessons -> $RAW"
echo "[mine] now run: python3 mine_lessons.py reduce"
