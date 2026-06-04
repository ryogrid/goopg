#!/usr/bin/env bash
# Ralph-loop-kaizen preprocessing pipeline - single entrypoint.
#
# Distils ~2.5 GB of loop logs + ~2.5 MB of milestone prose into a compact
# ANALYSIS_PACK.md that one Claude context can read. CHEAP-FIRST: stages 1-3 and
# 5 are pure Python (free); stage 4 (LLM lesson-mining) is GATED behind
# --with-llm and DRY-RUN by default.
#
# Usage:
#   ./run.sh                       # free stages only (1,2,3,5)
#   ./run.sh --sample 300          # analyse more stream logs in stage 2
#   ./run.sh --with-llm            # also chunk + DRY-RUN the LLM map (no API calls)
#   ./run.sh --with-llm --llm-confirm   # actually call Haiku (costs money)
#   ./run.sh --stages 1,5          # run a subset
#   ./run.sh --help
#
# Read-only against .ralph/ and docs/. All output lands in ./data (gitignored).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUT="$SCRIPT_DIR/data"
SAMPLE=150
STAGES="1,2,3,5"
WITH_LLM=0
LLM_CONFIRM=0
LLM_MAX_CHUNKS=0
REPO_ROOT=""
LOGS_DIR=""

usage() { sed -n '2,28p' "$0"; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --out) OUT="$2"; shift 2 ;;
    --sample) SAMPLE="$2"; shift 2 ;;
    --stages) STAGES="$2"; shift 2 ;;
    --with-llm) WITH_LLM=1; shift ;;
    --llm-confirm) LLM_CONFIRM=1; shift ;;
    --llm-max-chunks) LLM_MAX_CHUNKS="$2"; shift 2 ;;
    --repo-root) REPO_ROOT="$2"; shift 2 ;;
    --logs-dir) LOGS_DIR="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown arg: $1" >&2; usage; exit 2 ;;
  esac
done

mkdir -p "$OUT"
PY=(python3)
COMMON=()
[[ -n "$REPO_ROOT" ]] && COMMON+=(--repo-root "$REPO_ROOT")
EXTRA_LOGS=()
[[ -n "$LOGS_DIR" ]] && EXTRA_LOGS+=(--logs-dir "$LOGS_DIR")

has_stage() { [[ ",$STAGES," == *",$1,"* ]]; }

echo "=== ralph-loop-kaizen pipeline (stages: $STAGES, out: $OUT) ==="

if has_stage 1; then
  echo "--- stage 1: telemetry"
  "${PY[@]}" "$SCRIPT_DIR/extract_telemetry.py" --out "$OUT" "${COMMON[@]}" "${EXTRA_LOGS[@]}"
fi

if has_stage 2; then
  echo "--- stage 2: tool usage (sample=$SAMPLE)"
  "${PY[@]}" "$SCRIPT_DIR/extract_tool_usage.py" --out "$OUT" --sample "$SAMPLE" \
      "${COMMON[@]}" "${EXTRA_LOGS[@]}"
fi

if has_stage 3; then
  echo "--- stage 3: corpus anchors"
  "${PY[@]}" "$SCRIPT_DIR/assemble_corpus.py" --out "$OUT" "${COMMON[@]}"
fi

if has_stage 4 || [[ "$WITH_LLM" -eq 1 ]]; then
  echo "--- stage 4: lesson mining (LLM)"
  "${PY[@]}" "$SCRIPT_DIR/mine_lessons.py" chunk --out "$OUT" "${COMMON[@]}"
  MINE_ARGS=(--out "$OUT")
  [[ "$LLM_CONFIRM" -eq 1 ]] && MINE_ARGS+=(--confirm)
  [[ "$LLM_MAX_CHUNKS" -gt 0 ]] && MINE_ARGS+=(--max-chunks "$LLM_MAX_CHUNKS")
  KAIZEN_OUT="$OUT" bash "$SCRIPT_DIR/mine_lessons.sh" "${MINE_ARGS[@]}"
  if [[ "$LLM_CONFIRM" -eq 1 ]]; then
    "${PY[@]}" "$SCRIPT_DIR/mine_lessons.py" reduce --out "$OUT"
  fi
fi

if has_stage 5; then
  echo "--- stage 5: analysis pack"
  "${PY[@]}" "$SCRIPT_DIR/build_analysis_pack.py" --out "$OUT"
fi

echo "=== done. Pack: $OUT/ANALYSIS_PACK.md ==="
