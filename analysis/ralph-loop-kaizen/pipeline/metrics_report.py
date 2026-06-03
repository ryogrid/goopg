#!/usr/bin/env python3
"""Print a compact loop-health headline from the pipeline's JSON outputs.

Used by `make ralph-metrics` (kaizen T1) as a quick, free loop-health check —
the thing nobody was watching when the success rate sat at 29%. Reads
data/telemetry_summary.json + data/failure_classification.json (+ corpus) and
prints a few headline numbers. Run the extractors first (the Makefile target
does). Read-only; never calls an LLM.
"""

from __future__ import annotations

import json
import sys
from pathlib import Path

DATA = Path(__file__).resolve().parent / "data"


def load(name):
    p = DATA / name
    if not p.exists():
        return None
    try:
        return json.loads(p.read_text())
    except json.JSONDecodeError:
        return None


def main() -> int:
    t = load("telemetry_summary.json")
    f = load("failure_classification.json")
    if not t:
        print("no telemetry yet — run: ./run.sh --stages 1", file=sys.stderr)
        return 1
    ms = t.get("metrics_summary", {})
    ls = t.get("logs_summary", {})
    ev = t.get("ralph_log_events", {})

    print("=== ralph loop-health ===")
    print(f"loops started        : {ev.get('loops_started', '?')}")
    sr = ms.get("success_rate")
    print(f"success rate         : {round(sr*100,1) if sr is not None else '?'}%  "
          f"(ok {ev.get('exec_completed','?')} / failed {ev.get('exec_failed','?')})")
    dur = ms.get("duration_seconds", {})
    print(f"loop duration (s)    : p50 {dur.get('p50','?')}  p90 {dur.get('p90','?')}  max {dur.get('max','?')}")
    cost = ls.get("cost_usd", {})
    print(f"cost/loop ($, logged): p50 {round(cost.get('p50',0),2)}  p99 {round(cost.get('p99',0),2)}  "
          f"total {ls.get('total_cost_usd','?')}")
    print(f"cache-read fraction  : {ls.get('cache_read_fraction','?')}")
    print(f"status-block coverage: {round((ls.get('ralph_status_coverage') or 0)*100,1)}%")
    print(f"permission-denial loops: {ls.get('permission_denial_loops','?')}")
    cb = t.get("circuit_breaker", {})
    print(f"circuit-breaker trips: {cb.get('trips','?')}  {cb.get('by_reason',{})}")

    if f:
        print("--- failure breakdown ---")
        for cause, pct in list(f.get("by_cause_pct", {}).items())[:6]:
            n = f.get("by_cause", {}).get(cause, "?")
            print(f"  {cause:28s} {pct:5}%  (n={n})")
        rs = f.get("retry_storms", {})
        print(f"  retry storms: {rs.get('failure_runs','?')} runs, "
              f"max {rs.get('max_consecutive_failures','?')} consecutive, "
              f"{rs.get('failures_in_runs_ge_5','?')} failures in runs>=5")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
