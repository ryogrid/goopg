#!/usr/bin/env python3
"""Stage 1 - per-loop telemetry extraction (pure Python, free).

Reads the structured signals the ralph harness already records and emits two
artifacts:

  * loops.csv             - one row per loop log (cost / tokens / turns / model
                            mix / permission denials / terminal reason / a
                            best-effort RALPH_STATUS).
  * telemetry_summary.json - distributions and rates used by the analysis pack.

Sources (all read-only):
  .ralph/logs/claude_output_*.log   per-loop result summary (both formats)
  .ralph/logs/metrics.jsonl         loop spine: timestamp / duration / success
  .ralph/logs/ralph.log             driver narrative: timeouts / errors / CB
  .ralph/.circuit_breaker_history   circuit-breaker state transitions + reasons

Nothing here calls an LLM; it is safe and cheap to run over the full history.
"""

from __future__ import annotations

import argparse
import csv
import json
import re
import sys
from datetime import timedelta
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent / "lib"))
import common as C  # noqa: E402


def model_family(model: str | None) -> str:
    if not model:
        return "unknown"
    m = model.lower()
    for fam in ("opus", "sonnet", "haiku"):
        if fam in m:
            return fam
    return model


def collect_loop_rows(logs: Path, limit: int | None) -> tuple[list[dict], int, int]:
    rows: list[dict] = []
    files = C.loop_log_files(logs, stream=False)
    if limit:
        files = files[:limit]
    parse_failures = 0
    for f in files:
        ts = C.filename_timestamp(f)
        res = C.extract_result_object(f)
        if res is None:
            # Fallback for an empty/truncated main log: try its _stream.log
            # sibling. In practice the empty main logs (~35) correspond to
            # aborted loops whose siblings also lack a result, so this rarely
            # recovers anything — they are then counted as parse_failures.
            sib = f.with_name(f.name.replace(".log", "_stream.log"))
            if sib.exists():
                res = C.extract_result_object(sib)
        if res is None:
            parse_failures += 1
            continue
        usage = res.get("usage", {}) or {}
        model_usage = res.get("modelUsage", {}) or {}
        # Per-model cost/token rollup (only present in the rich result format).
        per_model = {}
        for model_id, mu in model_usage.items():
            fam = model_family(model_id)
            entry = per_model.setdefault(fam, {"cost_usd": 0.0, "output_tokens": 0})
            entry["cost_usd"] += C.to_float(mu.get("costUSD"))
            entry["output_tokens"] += C.to_int(mu.get("outputTokens"))
        text = C.result_text(res)
        status = C.parse_ralph_status(text) or {}
        denials = res.get("permission_denials")
        n_denials = len(denials) if isinstance(denials, list) else C.to_int(denials)
        rows.append(
            {
                "file": f.name,
                "timestamp": ts.isoformat() if ts else "",
                "format": "synthesised" if res.get("synthesised") else "result",
                "model": model_family(res.get("model")),
                "models_used": ";".join(sorted(per_model)) or model_family(res.get("model")),
                "is_error": bool(res.get("is_error")),
                "stop_reason": res.get("stop_reason") or "",
                "terminal_reason": res.get("terminal_reason") or "",
                "duration_ms": C.to_int(res.get("duration_ms")),
                "duration_api_ms": C.to_int(res.get("duration_api_ms")),
                "num_turns": C.to_int(res.get("num_turns")),
                "total_cost_usd": C.to_float(res.get("total_cost_usd")),
                "input_tokens": C.to_int(usage.get("input_tokens")),
                "output_tokens": C.to_int(usage.get("output_tokens")),
                "cache_creation_input_tokens": C.to_int(usage.get("cache_creation_input_tokens")),
                "cache_read_input_tokens": C.to_int(usage.get("cache_read_input_tokens")),
                "permission_denials": n_denials,
                "rs_status": status.get("STATUS", ""),
                "rs_tasks_completed": status.get("TASKS_COMPLETED_THIS_LOOP", ""),
                "rs_files_modified": status.get("FILES_MODIFIED", ""),
                "rs_tests_status": status.get("TESTS_STATUS", ""),
                "rs_work_type": status.get("WORK_TYPE", ""),
                "rs_exit_signal": status.get("EXIT_SIGNAL", ""),
                "has_ralph_status": bool(status),
                "cost_per_model_json": json.dumps(per_model),
            }
        )
    return rows, parse_failures, len(files)


# --- driver-log (ralph.log) event tallies -----------------------------------
#
# Patterns are matched against the EXACT driver event strings (verified against
# the live ralph.log) so each fires at most once per event. Do NOT loosen these
# to bare words like "timeout" or "[ERROR]" — the driver echoes "(timeout: 15m)"
# on every single loop, which would over-count timeouts ~140x.
_RALPH_LOG_PATTERNS = {
    "loops_started": re.compile(r"=== Starting Loop"),
    "exec_completed": re.compile(r"Claude Code execution completed successfully"),
    "exec_failed": re.compile(r"Claude Code execution failed"),
    "exec_timed_out": re.compile(r"Claude Code execution timed out after"),
    "no_result_parsed": re.compile(r"Could not find result message in stream output"),
    "cb_opened": re.compile(r"Circuit breaker opened"),
    "permission_denied_halt": re.compile(r"Permission denied - halting loop"),
    "graceful_exit": re.compile(r"Graceful exit triggered"),
    "session_reset": re.compile(r"Starting new Claude session"),
    "rate_limit": re.compile(r"Rate limit reached"),
}


def tally_ralph_log(logs: Path) -> dict:
    path = logs / "ralph.log"
    counts = {k: 0 for k in _RALPH_LOG_PATTERNS}
    if not path.exists():
        return counts
    for line in path.read_text(errors="replace").splitlines():
        for key, pat in _RALPH_LOG_PATTERNS.items():
            if pat.search(line):
                counts[key] += 1
    return counts


def circuit_breaker_reasons(repo: Path) -> dict:
    path = repo / ".ralph" / ".circuit_breaker_history"
    out = {"trips": 0, "by_reason": {}}
    if not path.exists():
        return out
    try:
        events = json.loads(path.read_text(errors="replace"))
    except json.JSONDecodeError:
        return out
    for e in events:
        if e.get("to_state") == "OPEN":
            out["trips"] += 1
            reason = (e.get("reason") or "unknown").split(" - ")[0].strip()
            out["by_reason"][reason] = out["by_reason"].get(reason, 0) + 1
    return out


_FAILED_LOG_RE = re.compile(r"execution failed, check:\s*(\S+\.log)")
_START_RE = re.compile(r"=== Starting Loop")
_OK_RE = re.compile(r"Claude Code execution completed successfully")
_FAIL_RE = re.compile(r"Claude Code execution failed")
_TIMEDOUT_RE = re.compile(r"Claude Code execution timed out after")


def _classify_one(res: dict | None) -> str:
    """Bucket a single failed loop by the cause recorded in its .log."""
    if res is None:
        return "empty_or_unparseable_log"
    api = res.get("api_error_status")
    text = (res.get("result") or "").lower()
    is_err = bool(res.get("is_error"))
    if api == 429 or ("hit your limit" in text) or ("usage limit" in text):
        return "usage_limit_429"
    if api and api != 429:
        return f"api_error_{api}"
    if res.get("terminal_reason") == "timed_out" or "timed out" in text:
        return "timeout"
    if "stream" in text and "could not find result" in text:
        return "stream_parse_failure"
    if is_err:
        return "agent_error_nonapi"
    return "other_marked_failed"


def classify_failures(logs: Path) -> dict:
    """Root-cause the driver's 'execution failed' loops by reading each
    referenced .log (exact loop->log join via ralph.log) and bucketing the
    cause. Also measures consecutive-failure run lengths (retry storms)."""
    ralph = logs / "ralph.log"
    if not ralph.exists():
        return {}
    failed_paths: list[str] = []
    outcomes: list[str] = []  # ordered 'S'/'F' per resolved loop outcome
    for line in ralph.read_text(errors="replace").splitlines():
        if _OK_RE.search(line):
            outcomes.append("S")
        elif _TIMEDOUT_RE.search(line):
            outcomes.append("F")
        m = _FAILED_LOG_RE.search(line)
        if m:
            failed_paths.append(m.group(1))
            outcomes.append("F")

    buckets: dict[str, int] = {}
    bucket_durations: dict[str, list[float]] = {}
    reset_msgs: dict[str, int] = {}
    missing = 0
    for p in failed_paths:
        f = logs / Path(p).name
        if not f.exists():
            missing += 1
            res = None
        else:
            res = extract_result_object_safe(f)
        cat = _classify_one(res)
        buckets[cat] = buckets.get(cat, 0) + 1
        if res:
            bucket_durations.setdefault(cat, []).append(C.to_float(res.get("duration_ms")) / 1000.0)
            if cat == "usage_limit_429":
                msg = (res.get("result") or "").strip()[:80]
                reset_msgs[msg] = reset_msgs.get(msg, 0) + 1

    # consecutive-failure run lengths (retry storms)
    runs: list[int] = []
    cur = 0
    for o in outcomes:
        if o == "F":
            cur += 1
        else:
            if cur:
                runs.append(cur)
            cur = 0
    if cur:
        runs.append(cur)

    total_failed = len(failed_paths)
    return {
        "failed_loops_seen": total_failed,
        "missing_logs": missing,
        "by_cause": dict(sorted(buckets.items(), key=lambda kv: -kv[1])),
        "by_cause_pct": {
            k: round(100 * v / total_failed, 1) for k, v in sorted(buckets.items(), key=lambda kv: -kv[1])
        }
        if total_failed
        else {},
        "median_duration_s_by_cause": {
            k: round(sorted(v)[len(v) // 2], 1) for k, v in bucket_durations.items() if v
        },
        "retry_storms": {
            "failure_runs": len(runs),
            "max_consecutive_failures": max(runs) if runs else 0,
            "runs_ge_5": sum(1 for r in runs if r >= 5),
            "runs_ge_20": sum(1 for r in runs if r >= 20),
            "failures_in_runs_ge_5": sum(r for r in runs if r >= 5),
        },
        "top_usage_limit_messages": dict(sorted(reset_msgs.items(), key=lambda kv: -kv[1])[:5]),
    }


def extract_result_object_safe(f: Path) -> dict | None:
    try:
        return C.extract_result_object(f)
    except Exception:
        return None


def metrics_summary(rows: list[dict]) -> dict:
    durations = [C.to_float(r.get("duration")) for r in rows if r.get("duration") is not None]
    successes = [bool(r.get("success")) for r in rows]
    n = len(rows)
    return {
        "loops_recorded": n,
        "success_rate": round(sum(successes) / n, 4) if n else None,
        "failure_count": sum(1 for s in successes if not s),
        "duration_seconds": C.percentiles(durations),
        "first_timestamp": rows[0].get("timestamp") if rows else None,
        "last_timestamp": rows[-1].get("timestamp") if rows else None,
    }


def summarise(rows: list[dict]) -> dict:
    def col(name):
        return [r[name] for r in rows]

    cost = [r["total_cost_usd"] for r in rows if r["total_cost_usd"] > 0]
    turns = [r["num_turns"] for r in rows if r["num_turns"] > 0]
    out_tok = [r["output_tokens"] for r in rows if r["output_tokens"] > 0]
    cache_read = [r["cache_read_input_tokens"] for r in rows if r["cache_read_input_tokens"] > 0]
    dur = [r["duration_ms"] / 1000.0 for r in rows if r["duration_ms"] > 0]

    by_work_type: dict[str, int] = {}
    by_status: dict[str, int] = {}
    by_model: dict[str, int] = {}
    denial_loops = 0
    for r in rows:
        if r["rs_work_type"]:
            by_work_type[r["rs_work_type"]] = by_work_type.get(r["rs_work_type"], 0) + 1
        if r["rs_status"]:
            by_status[r["rs_status"]] = by_status.get(r["rs_status"], 0) + 1
        by_model[r["model"]] = by_model.get(r["model"], 0) + 1
        if r["permission_denials"]:
            denial_loops += 1

    total_cost = sum(r["total_cost_usd"] for r in rows)
    cache_read_total = sum(r["cache_read_input_tokens"] for r in rows)
    fresh_input_total = sum(r["input_tokens"] + r["cache_creation_input_tokens"] for r in rows)
    cache_ratio = (
        cache_read_total / (cache_read_total + fresh_input_total)
        if (cache_read_total + fresh_input_total)
        else None
    )

    top_cost = sorted(rows, key=lambda r: r["total_cost_usd"], reverse=True)[:20]
    top_turns = sorted(rows, key=lambda r: r["num_turns"], reverse=True)[:20]

    return {
        "loops_with_result": len(rows),
        "total_cost_usd": round(total_cost, 2),
        "ralph_status_coverage": round(
            sum(1 for r in rows if r["has_ralph_status"]) / len(rows), 4
        )
        if rows
        else None,
        "cost_usd": C.percentiles(cost),
        "num_turns": C.percentiles([float(x) for x in turns]),
        "output_tokens": C.percentiles([float(x) for x in out_tok]),
        "cache_read_input_tokens": C.percentiles([float(x) for x in cache_read]),
        "duration_seconds": C.percentiles(dur),
        "cache_read_fraction": round(cache_ratio, 4) if cache_ratio is not None else None,
        "by_work_type": by_work_type,
        "by_status": by_status,
        "by_model": by_model,
        "permission_denial_loops": denial_loops,
        "top_cost_loops": [
            {"file": r["file"], "cost_usd": r["total_cost_usd"], "turns": r["num_turns"]}
            for r in top_cost
        ],
        "top_turn_loops": [
            {"file": r["file"], "turns": r["num_turns"], "cost_usd": r["total_cost_usd"]}
            for r in top_turns
        ],
    }


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--repo-root", default=None)
    ap.add_argument("--logs-dir", default=None)
    ap.add_argument("--out", default=None, help="output directory (default pipeline/data)")
    ap.add_argument("--limit", type=int, default=None, help="cap number of logs (debug)")
    ap.add_argument(
        "--classify-failures",
        action="store_true",
        help="root-cause the driver's 'execution failed' loops (reads each referenced .log)",
    )
    args = ap.parse_args()

    repo = C.repo_root(args.repo_root)
    logs = C.logs_dir(repo, args.logs_dir)
    out = Path(args.out) if args.out else Path(__file__).resolve().parent / "data"
    out.mkdir(parents=True, exist_ok=True)

    if not logs.exists():
        print(f"[telemetry] logs dir not found: {logs}", file=sys.stderr)
        return 1

    if args.classify_failures:
        cls = classify_failures(logs)
        C.dump_json(cls, out / "failure_classification.json")
        print(f"[telemetry] wrote {out / 'failure_classification.json'}")
        print(json.dumps(cls, indent=2))
        return 0

    rows, parse_failures, n_files = collect_loop_rows(logs, args.limit)
    print(f"[telemetry] parsed {len(rows)}/{n_files} loop logs ({parse_failures} unparseable)")

    # loops.csv
    csv_path = out / "loops.csv"
    if rows:
        with csv_path.open("w", newline="") as fh:
            w = csv.DictWriter(fh, fieldnames=list(rows[0].keys()))
            w.writeheader()
            w.writerows(rows)
        print(f"[telemetry] wrote {csv_path}")

    summary = {
        "logs_summary": summarise(rows),
        "metrics_summary": metrics_summary(C.read_metrics(logs)),
        "ralph_log_events": tally_ralph_log(logs),
        "circuit_breaker": circuit_breaker_reasons(repo),
        "parse_failures": parse_failures,
        "log_files_seen": n_files,
    }
    C.dump_json(summary, out / "telemetry_summary.json")
    print(f"[telemetry] wrote {out / 'telemetry_summary.json'}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
