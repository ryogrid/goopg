#!/usr/bin/env bash
# resource_monitor.sh — sample CPU% and RSS of a goopg process every
# INTERVAL seconds, appending CSV lines to OUTPUT_FILE.
#
# Usage:
#   resource_monitor.sh <pid> <output_file> [interval_seconds]
#
# Output format (CSV):
#   epoch_seconds,cpu_pct,rss_mb
#
# The script runs until SIGTERM (sent by the caller when the query ends).
set -euo pipefail

PID="${1:?Usage: resource_monitor.sh <pid> <output_file> [interval]}"
OUTPUT="${2:?Usage: resource_monitor.sh <pid> <output_file> [interval]}"
INTERVAL="${3:-30}"

# Write CSV header.
echo "epoch_seconds,cpu_pct,rss_mb" > "$OUTPUT"

while kill -0 "$PID" 2>/dev/null; do
    # ps -p <pid> -o %cpu,rss --no-headers
    # %cpu is the average over the process lifetime (good enough for benchmarks)
    # rss is in kilobytes on Linux
    LINE=$(ps -p "$PID" -o %cpu,rss --no-headers 2>/dev/null | head -1 || true)
    if [ -n "$LINE" ]; then
        CPU=$(echo "$LINE" | awk '{print $1}')
        RSS_KB=$(echo "$LINE" | awk '{print $2}')
        RSS_MB=$(echo "scale=1; $RSS_KB / 1024" | bc)
        echo "$(date +%s),$CPU,$RSS_MB" >> "$OUTPUT"
    fi
    sleep "$INTERVAL"
done
