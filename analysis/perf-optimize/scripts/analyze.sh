#!/usr/bin/env bash
# analyze.sh — post-process pprof artefacts produced by run_perf_suite.sh
#
# Usage: analyze.sh <RUN_ID>
#   produces  analysis/perf-optimize/runs/<RUN_ID>/pprof_top/*.txt
#   produces  analysis/perf-optimize/runs/<RUN_ID>/results_summary.tsv

set -uo pipefail

REPO_ROOT="${REPO_ROOT:-/home/ryo/work/goopg/goopg}"
RUN_ID="${1:?usage: analyze.sh <RUN_ID>}"
RUN_DIR="${REPO_ROOT}/analysis/perf-optimize/runs/${RUN_ID}"
PROF_DIR="${RUN_DIR}/profiles"
TOP_DIR="${RUN_DIR}/pprof_top"
BIN="${RUN_DIR}/goopg.bin"

mkdir -p "$TOP_DIR"

[[ -f "$BIN" ]] || { echo "missing $BIN — was the suite run?" >&2; exit 2; }

dump_top() {
  local prof="$1"
  local kind; kind="$(basename "$prof" .pb.gz)"
  kind="${kind##*.}"
  local stem; stem="$(basename "$prof" .pb.gz)"
  local out="$TOP_DIR/${stem}.txt"

  echo "[pprof] $stem" >&2

  {
    echo "# go tool pprof -top -nodecount=40 -cum  $prof"
    go tool pprof -top -nodecount=40 -cum "$BIN" "$prof" 2>&1 | head -120
    echo
    echo "# top symbols (-list) — top 8 hot functions, flat order"
    local hot; hot=$(go tool pprof -top -nodecount=8 "$BIN" "$prof" 2>/dev/null \
                   | awk 'NR>5 && $NF !~ /^$/ && $NF !~ /^[0-9]/ {print $NF}' \
                   | head -8)
    for sym in $hot; do
      echo "## -list $sym"
      go tool pprof -list "$sym" "$BIN" "$prof" 2>&1 | head -40
      echo
    done
  } > "$out"
}

dump_diff() {
  local base="$1" final="$2"
  local stem; stem="$(basename "$final" .pb.gz)"
  local out="$TOP_DIR/${stem}.delta.txt"
  echo "[pprof-diff] $stem" >&2
  {
    echo "# go tool pprof -top -nodecount=40 -cum -base $base $final"
    go tool pprof -top -nodecount=40 -cum -base "$base" "$BIN" "$final" 2>&1 | head -120
  } > "$out"
}

# CPU, heap, allocs, threadcreate: simple -top
for p in "$PROF_DIR"/goopg_*.cpu.pb.gz "$PROF_DIR"/goopg_*.heap.pb.gz \
         "$PROF_DIR"/goopg_*.allocs.pb.gz; do
  [[ -f "$p" ]] || continue
  dump_top "$p"
done

# Mutex/Block: delta vs baseline using `-base`
for label_path in "$PROF_DIR"/goopg_*.mutex.pb.gz; do
  [[ -f "$label_path" ]] || continue
  base_path="${label_path%.mutex.pb.gz}.mutex_base.pb.gz"
  [[ -f "$base_path" ]] && dump_diff "$base_path" "$label_path"
  dump_top "$label_path"
done
for label_path in "$PROF_DIR"/goopg_*.block.pb.gz; do
  [[ -f "$label_path" ]] || continue
  base_path="${label_path%.block.pb.gz}.block_base.pb.gz"
  [[ -f "$base_path" ]] && dump_diff "$base_path" "$label_path"
  dump_top "$label_path"
done

# Trace: high-level summary using `go tool trace -d` is interactive only,
# so we just record file sizes for the report. `go tool trace` requires
# launching a browser; we'll mention the file path in the report.
echo "# trace files" > "$TOP_DIR/trace_files.txt"
ls -la "$PROF_DIR"/goopg_*.trace.out 2>/dev/null >> "$TOP_DIR/trace_files.txt" || true

# Build results summary TSV: target, clients, workload, tps, lat_avg_ms, lat_stddev_ms, status
SUM="$RUN_DIR/results_summary.tsv"
printf 'target\tclients\tworkload\ttps\tlat_avg_ms\tlat_stddev_ms\tstatus\n' > "$SUM"
for f in "$RUN_DIR"/pgbench_*.txt; do
  [[ -f "$f" ]] || continue
  fn="$(basename "$f" .txt)"
  rest="${fn#pgbench_}"
  target="${rest%%_*}"
  rest="${rest#*_c}"
  clients="${rest%%_*}"
  wl="${rest#*_}"
  tps="$(awk '/^tps =/ {print $3; exit}' "$f")"
  lat_avg="$(awk '/^latency average/ {print $4; exit}' "$f")"
  lat_std="$(awk '/^latency stddev/ {print $4; exit}' "$f")"
  status="ok"
  [[ -z "$tps" ]] && status="failed"
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$target" "$clients" "$wl" "${tps:-NA}" "${lat_avg:-NA}" "${lat_std:-NA}" "$status" >> "$SUM"
done

# Mark SKIPPED rows
for f in "$RUN_DIR"/SKIPPED_*.txt; do
  [[ -f "$f" ]] || continue
  bn="$(basename "$f" .txt)"
  rest="${bn#SKIPPED_}"   # e.g. goopg_c100_standard
  target="${rest%%_*}"
  rest="${rest#*_c}"
  clients="${rest%%_*}"
  wl="${rest#*_}"
  printf '%s\t%s\t%s\tNA\tNA\tNA\tSKIPPED\n' "$target" "$clients" "$wl" >> "$SUM"
done

echo "wrote $SUM"
echo "wrote $TOP_DIR/"
