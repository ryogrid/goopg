#!/usr/bin/env python3
"""Task-conditional practice-card router (cheap, deterministic, no LLM).

Given the current top open task and the working-tree diff, print only the
practice cards relevant to this loop's task type. Designed to run from a
`UserPromptSubmit` hook so each ralph loop gets task-scoped guidance instead of
all guidance always-on. See ../04-rules-and-practices.md.

Zero API cost: classification is pure path/keyword matching. Total per-loop
overhead is a couple of file reads and a `git diff --name-only`.

Usage:
  route.py                       # auto-detect from fix_plan.md + git diff
  route.py --task "wal recovery standby"   # force task text
  route.py --explain             # show which cards matched and why (no card bodies)
  route.py --max-cards 2         # cap how many cards are emitted
"""

from __future__ import annotations

import argparse
import re
import subprocess
import sys
from pathlib import Path

CARD_DIR = Path(__file__).resolve().parent
# repo root = .../analysis/ralph-loop-kaizen/practice-cards -> up 3
REPO_ROOT = CARD_DIR.parents[2]

# card -> (path substrings that imply it, keyword regexes that imply it)
RULES: dict[str, tuple[list[str], list[str]]] = {
    "executor-planner-change.md": (
        ["internal/planner", "internal/executor"],
        ["planner", "executor", "join", "predicate", "row count", "row-count",
         "tpch", "tpc-h", r"\bq\d{1,2}\b", "residual", "semijoin", "anti"],
    ),
    "codec-storage-change.md": (
        ["internal/access", "internal/storage"],
        ["codec", "encode", "decode", "on-disk", "page format", "datum",
         "tuple format", "fixed-width", "round-trip", "round trip"],
    ),
    "wal-replication-change.md": (
        ["internal/wal", "internal/mvcc"],
        ["wal", "replication", "standby", "recovery", "checkpoint", "lsn",
         "replay", "logical replication", "failover", "subscription"],
    ),
    "tpch-perf.md": (
        ["analysis/perf", "analysis/tpch"],
        ["pgbench", "hammerdb", "pprof", "benchmark", "perf", "throughput",
         "tps", "heap", "allocation", "gc", "bottleneck"],
    ),
    "server-test.md": (
        [],
        ["start a server", "goopg start", "psql", "pgbench", "manual test",
         "integration test", "oracle test", "data dir", "datadir", "--listen"],
    ),
    "regress-port.md": (
        ["internal/testport"],
        ["regress", "tap test", "oracle port", "port status", "pg_regress",
         "isolation test"],
    ),
    "catalog-ddl.md": (
        ["internal/catalog", "internal/parser"],
        ["catalog", "create view", "create function", "ddl", "pg_class",
         "pg_attribute", "constraint", "functional dep", "dependency"],
    ),
}

# Cards that exist as shipped samples (others are proposed-but-unwritten).
EXISTING = {p.name for p in CARD_DIR.glob("*.md")} - {"README.md"}


def git_changed_paths(repo: Path) -> list[str]:
    paths: set[str] = set()
    for args in (["diff", "--name-only"], ["diff", "--name-only", "--cached"]):
        try:
            out = subprocess.run(
                ["git", "-C", str(repo), *args],
                capture_output=True, text=True, timeout=10,
            )
            paths.update(p for p in out.stdout.splitlines() if p.strip())
        except (subprocess.SubprocessError, OSError):
            pass
    return sorted(paths)


def top_open_task(repo: Path) -> str:
    """First unchecked '[ ]' task line in fix_plan.md, with a little context."""
    fp = repo / ".ralph" / "fix_plan.md"
    if not fp.exists():
        return ""
    try:
        lines = fp.read_text(errors="replace").splitlines()
    except OSError:
        return ""
    for i, line in enumerate(lines):
        if re.search(r"^\s*-?\s*\[ \]", line):
            return " ".join(lines[i : i + 4])
    return ""


def classify(task_text: str, paths: list[str]) -> dict[str, list[str]]:
    blob = (task_text + " " + " ".join(paths)).lower()
    matched: dict[str, list[str]] = {}
    for card, (path_hints, keywords) in RULES.items():
        reasons = []
        for ph in path_hints:
            if any(ph in p.lower() for p in paths):
                reasons.append(f"path:{ph}")
        for kw in keywords:
            if re.search(kw, blob):
                reasons.append(f"kw:{kw}")
        if reasons:
            matched[card] = reasons
    return matched


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--task", default=None, help="override task text")
    ap.add_argument("--repo-root", default=None)
    ap.add_argument("--explain", action="store_true")
    ap.add_argument("--max-cards", type=int, default=3)
    args = ap.parse_args()

    repo = Path(args.repo_root).resolve() if args.repo_root else REPO_ROOT
    task = args.task if args.task is not None else top_open_task(repo)
    paths = [] if args.task is not None else git_changed_paths(repo)

    matched = classify(task, paths)
    # Rank by number of reasons (more signal first); cap to max-cards.
    ranked = sorted(matched.items(), key=lambda kv: -len(kv[1]))[: args.max_cards]

    if args.explain:
        print(f"task: {task[:120]!r}")
        print(f"changed paths: {paths[:8]}")
        if not ranked:
            print("matched: (none) -> would load no card / a general fallback")
        for card, reasons in ranked:
            present = "" if card in EXISTING else "  [card not yet authored]"
            print(f"matched: {card}{present}  <- {', '.join(reasons[:6])}")
        return 0

    if not ranked:
        # No confident match -> emit nothing (the loop keeps its base prompt).
        return 0

    out = []
    for card, _ in ranked:
        path = CARD_DIR / card
        if path.exists():
            out.append(path.read_text(errors="replace").rstrip())
        else:
            out.append(f"<!-- practice card '{card}' proposed but not yet authored -->")
    print("\n\n---\n\n".join(out))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
