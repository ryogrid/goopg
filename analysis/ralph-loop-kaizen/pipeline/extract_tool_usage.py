#!/usr/bin/env python3
"""Stage 2 - tool-usage extraction from stream logs (pure Python, free).

Tool calls are not in the compact result object; they live in the streaming
event logs (``*_stream.log`` for most of history, and the main ``.log`` for the
last ~1 day). Parsing every stream log is heavy (thousands of multi-MB JSONL
files), so by default we SAMPLE a deterministic, evenly-spaced subset. Pass
``--all`` to sweep everything.

Emits tool_usage_summary.json:
  * per-tool call counts and share
  * mean tool calls per loop
  * turns-to-first-edit (how long before the agent makes its first Write/Edit)
  * read/grep/bash volume per loop
  * failed-tool-call rate (tool_result with is_error)

Determinism note: sampling is by evenly-spaced index over the sorted file list
(no RNG), so re-runs are reproducible and diffable.
"""

from __future__ import annotations

import argparse
import sys
from collections import Counter
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent / "lib"))
import common as C  # noqa: E402

EDIT_TOOLS = {"Edit", "Write", "NotebookEdit", "MultiEdit"}


def sample_files(files: list[Path], n: int | None) -> list[Path]:
    if not n or n >= len(files):
        return files
    step = len(files) / n
    return [files[int(i * step)] for i in range(n)]


def analyse_stream(path: Path) -> dict | None:
    """Per-loop tool stats from one stream log. None if no agent activity."""
    tool_calls = Counter()
    n_tool_results = 0
    n_tool_errors = 0
    turns_to_first_edit = None
    assistant_turn = 0
    saw_activity = False
    # The stream format splits one logical assistant message across several lines
    # sharing a message.id. Count a TURN once per unique id, but count tool_use
    # blocks on every line (they are NOT duplicated across the split lines, so
    # summing them is correct; only the per-line turn counter would inflate).
    seen_msg_ids: set = set()
    for obj in C.iter_json_objects(path):
        t = obj.get("type")
        if t == "assistant":
            saw_activity = True
            msg = obj.get("message", {}) or {}
            mid = msg.get("id")
            if mid is None or mid not in seen_msg_ids:
                if mid is not None:
                    seen_msg_ids.add(mid)
                assistant_turn += 1
            for block in msg.get("content", []) or []:
                if isinstance(block, dict) and block.get("type") == "tool_use":
                    name = block.get("name", "unknown")
                    tool_calls[name] += 1
                    if turns_to_first_edit is None and name in EDIT_TOOLS:
                        turns_to_first_edit = assistant_turn
        elif t == "user":
            for block in (obj.get("message", {}) or {}).get("content", []) or []:
                if isinstance(block, dict) and block.get("type") == "tool_result":
                    n_tool_results += 1
                    if block.get("is_error"):
                        n_tool_errors += 1
    if not saw_activity:
        return None
    return {
        "tool_calls": dict(tool_calls),
        "total_calls": sum(tool_calls.values()),
        "tool_results": n_tool_results,
        "tool_errors": n_tool_errors,
        "turns_to_first_edit": turns_to_first_edit,
        "assistant_turns": assistant_turn,
    }


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--repo-root", default=None)
    ap.add_argument("--logs-dir", default=None)
    ap.add_argument("--out", default=None)
    ap.add_argument("--sample", type=int, default=150, help="loops to sample (default 150)")
    ap.add_argument("--all", action="store_true", help="process every stream log (slow)")
    args = ap.parse_args()

    repo = C.repo_root(args.repo_root)
    logs = C.logs_dir(repo, args.logs_dir)
    out = Path(args.out) if args.out else Path(__file__).resolve().parent / "data"
    out.mkdir(parents=True, exist_ok=True)

    streams = C.loop_log_files(logs, stream=True)
    if not streams:
        print("[tools] no *_stream.log files found", file=sys.stderr)
        return 1
    chosen = streams if args.all else sample_files(streams, args.sample)
    print(f"[tools] analysing {len(chosen)}/{len(streams)} stream logs")

    agg_tools = Counter()
    per_loop_calls = []
    first_edit = []
    errors = 0
    results = 0
    analysed = 0
    for f in chosen:
        stats = analyse_stream(f)
        if not stats:
            continue
        analysed += 1
        agg_tools.update(stats["tool_calls"])
        per_loop_calls.append(stats["total_calls"])
        if stats["turns_to_first_edit"] is not None:
            first_edit.append(stats["turns_to_first_edit"])
        errors += stats["tool_errors"]
        results += stats["tool_results"]

    total_calls = sum(agg_tools.values())
    summary = {
        "loops_analysed": analysed,
        "sampled_of_total": [len(chosen), len(streams)],
        "mean_tool_calls_per_loop": round(sum(per_loop_calls) / analysed, 2) if analysed else None,
        "tool_calls_per_loop": C.percentiles([float(x) for x in per_loop_calls]),
        "tool_call_share": {
            name: round(cnt / total_calls, 4) for name, cnt in agg_tools.most_common()
        }
        if total_calls
        else {},
        "tool_call_counts": dict(agg_tools.most_common()),
        "turns_to_first_edit": C.percentiles([float(x) for x in first_edit]),
        "loops_with_no_edit": analysed - len(first_edit),
        "tool_error_rate": round(errors / results, 4) if results else None,
    }
    C.dump_json(summary, out / "tool_usage_summary.json")
    print(f"[tools] wrote {out / 'tool_usage_summary.json'}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
