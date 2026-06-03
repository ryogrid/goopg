#!/usr/bin/env python3
"""Stage 5 - assemble the one-context analysis pack (pure Python, free).

Merges the structured outputs of stages 1-4 into a single compact Markdown file
(target < ~40 KB) that one Claude context can read in full to (re)write the
kaizen report. Missing inputs become explicit "<not generated>" placeholders so
the pack is always producible, even in design-only mode.
"""

from __future__ import annotations

import argparse
import csv
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent / "lib"))
import common as C  # noqa: E402


def load_json(p: Path):
    if not p.exists():
        return None
    try:
        return json.loads(p.read_text(errors="replace"))
    except json.JSONDecodeError:
        return None


def fence(obj) -> str:
    return "```json\n" + json.dumps(obj, indent=2, default=str) + "\n```"


def section(title: str, body: str) -> str:
    return f"## {title}\n\n{body}\n"


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--out", default=None)
    args = ap.parse_args()
    out = Path(args.out) if args.out else Path(__file__).resolve().parent / "data"
    out.mkdir(parents=True, exist_ok=True)

    telem = load_json(out / "telemetry_summary.json")
    tools = load_json(out / "tool_usage_summary.json")
    corpus = load_json(out / "corpus_stats.json")
    lessons_md = (out / "lessons_clustered.md")
    lessons = lessons_md.read_text(errors="replace") if lessons_md.exists() else None

    parts = [
        "# Ralph Loop — Analysis Pack",
        "",
        "_Compact, machine-assembled extract for one-context analysis. "
        "Regenerate with `pipeline/run.sh`._",
        "",
    ]

    parts.append(
        section(
            "1. Loop telemetry (stage 1)",
            fence(telem) if telem else "_<not generated — run stage 1: extract_telemetry.py>_",
        )
    )
    parts.append(
        section(
            "2. Tool usage (stage 2)",
            fence(tools) if tools else "_<not generated — run stage 2: extract_tool_usage.py>_",
        )
    )
    parts.append(
        section(
            "3. Corpus anchors (stage 3)",
            fence(corpus) if corpus else "_<not generated — run stage 3: assemble_corpus.py>_",
        )
    )

    # Milestone index: include only a compact slice (those with blockers).
    mindex = out / "milestone_index.csv"
    if mindex.exists():
        with mindex.open() as fh:
            rows = list(csv.DictReader(fh))
        blocked = [r for r in rows if r.get("has_blocker") == "True"][:40]
        body = "Milestones flagged with a struggle anchor (first 40):\n\n"
        body += "| id | title | status | refs |\n|----|-------|--------|------|\n"
        for r in blocked:
            body += f"| {r['milestone']} | {r['title'][:50]} | {r['status']} | {r['n_design_refs']} |\n"
        parts.append(section("4. Milestones with blockers (stage 3)", body))
    else:
        parts.append(section("4. Milestones with blockers (stage 3)", "_<not generated>_"))

    parts.append(
        section(
            "5. Mined lessons (stage 4, optional/paid)",
            lessons if lessons else "_<not generated — run stage 4 (--with-llm) then reduce>_",
        )
    )

    pack = "\n".join(parts)
    pack_path = out / "ANALYSIS_PACK.md"
    pack_path.write_text(pack)
    print(f"[pack] wrote {pack_path} ({len(pack):,} bytes)")
    if len(pack) > 60000:
        print("[pack] WARNING: pack > 60 KB; consider trimming embedded JSON.", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
