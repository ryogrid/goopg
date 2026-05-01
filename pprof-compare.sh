#!/bin/bash
# Usage: ./pprof-compare.sh <base.prof> <latest.prof>
# Shows growth between two heap profiles.

set -euo pipefail

if [ $# -lt 2 ]; then
  echo "Usage: $0 <base.prof> <latest.prof>"
  exit 1
fi

echo "=== Top inuse_space summary ==="
go tool pprof -top -hide=runtime,testing,sync internal_packages=true "$2" 2>/dev/null

echo ""
echo "=== Diff: base → latest (inuse_space) ==="
go tool pprof -top -hide=runtime,testing,sync -base "$1" internal_packages=true "$2" 2>/dev/null

echo ""
echo "=== alloc_space summary (latest) ==="
go tool pprof -top -hide=runtime,testing,sync -alloc_space internal_packages=true "$2" 2>/dev/null

echo ""
echo "=== Top 20 objects by inuse_objects ==="
go tool pprof -top -hide=runtime,testing,sync -inuse_objects internal_packages=true "$2" 2>/dev/null
