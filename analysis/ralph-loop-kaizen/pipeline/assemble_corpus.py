#!/usr/bin/env python3
"""Stage 3 - text-corpus anchor assembly (pure Python, free).

Greps the human-readable milestone history for "struggle" anchors and builds a
compact index, so the (optional, paid) LLM lesson-mining stage and the final
analyst both work from a small, signal-dense extract instead of ~2.7 MB of prose.

Corpus (all read-only):
  docs/milestones/*.md          116 milestone specs (Context / DoD / Out of scope)
  completed_milestones/*.md     6 retrospective implementation journals (1.7 MB)
  docs/handover/*.md            10 phase-level retrospectives

Emits:
  anchors.jsonl        one record per anchor hit (file, line, anchor, text, context)
  milestone_index.csv  id / title / status / date / has_blocker / design-doc refs
  corpus_stats.json    anchor tallies by type and by file; corpus sizes
"""

from __future__ import annotations

import argparse
import csv
import json
import re
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent / "lib"))
import common as C  # noqa: E402

# Case-insensitive struggle/effort anchors. Word-ish boundaries keep noise down.
ANCHORS = [
    "root cause",
    "blocked",
    "blocker",
    "deferred",
    "regression",
    "regress",
    "hang",
    "hung",
    "partial scope",
    "partial",
    "revert",
    "reverted",
    "had to",
    "surprise",
    "surprising",
    "pitfall",
    "gotcha",
    "timed out",
    "timeout",
    "oom",
    "flaky",
    "silently",
    "footgun",
    "risk",
]
_ANCHOR_RE = re.compile("|".join(rf"\b{re.escape(a)}\b" for a in ANCHORS), re.IGNORECASE)

_MILESTONE_TITLE = re.compile(r"^#\s*Milestone\s+(\d{3,4})\s*[—–-]\s*(.+)$", re.IGNORECASE)
_STATUS_LINE = re.compile(r"\*\*Status:\*\*\s*(.+)", re.IGNORECASE)
_DATE = re.compile(r"(\d{4}-\d{2}-\d{2})")
_DESIGN_REF = re.compile(r"docs/design/([\w./-]+\.md)")


def corpus_files(repo: Path) -> dict[str, list[Path]]:
    return {
        "milestones": sorted((repo / "docs" / "milestones").glob("*.md")),
        "completed": sorted((repo / "completed_milestones").glob("*.md")),
        "handover": sorted((repo / "docs" / "handover").glob("*.md")),
    }


def scan_anchors(files: dict[str, list[Path]], repo: Path):
    anchors = []
    by_anchor: dict[str, int] = {}
    by_file: dict[str, int] = {}
    for group, paths in files.items():
        for p in paths:
            lines = p.read_text(errors="replace").splitlines()
            rel = str(p.relative_to(repo))
            for i, line in enumerate(lines):
                for m in _ANCHOR_RE.finditer(line):
                    a = m.group(0).lower()
                    by_anchor[a] = by_anchor.get(a, 0) + 1
                    by_file[rel] = by_file.get(rel, 0) + 1
                    ctx = lines[max(0, i - 1) : i + 2]
                    anchors.append(
                        {
                            "group": group,
                            "file": rel,
                            "line": i + 1,
                            "anchor": a,
                            "text": line.strip()[:300],
                            "context": " / ".join(s.strip()[:160] for s in ctx),
                        }
                    )
    return anchors, by_anchor, by_file


def build_milestone_index(files: dict[str, list[Path]], repo: Path) -> list[dict]:
    rows = []
    for p in files["milestones"]:
        text = p.read_text(errors="replace")
        title_m = _MILESTONE_TITLE.search(text)
        status_m = _STATUS_LINE.search(text)
        date_m = _DATE.search(text)
        refs = sorted(set(_DESIGN_REF.findall(text)))
        rows.append(
            {
                "file": p.name,
                "milestone": title_m.group(1) if title_m else "",
                "title": (title_m.group(2).strip()[:120] if title_m else ""),
                "status": (status_m.group(1).strip()[:40] if status_m else ""),
                "date": date_m.group(1) if date_m else "",
                "has_blocker": bool(_ANCHOR_RE.search(text)),
                "n_design_refs": len(refs),
                "design_refs": ";".join(refs[:8]),
            }
        )
    return rows


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--repo-root", default=None)
    ap.add_argument("--out", default=None)
    args = ap.parse_args()

    repo = C.repo_root(args.repo_root)
    out = Path(args.out) if args.out else Path(__file__).resolve().parent / "data"
    out.mkdir(parents=True, exist_ok=True)

    files = corpus_files(repo)
    sizes = {
        g: {"files": len(ps), "bytes": sum(p.stat().st_size for p in ps)}
        for g, ps in files.items()
    }
    if not any(files.values()):
        print("[corpus] no corpus files found", file=sys.stderr)
        return 1

    anchors, by_anchor, by_file = scan_anchors(files, repo)
    with (out / "anchors.jsonl").open("w") as fh:
        for a in anchors:
            fh.write(json.dumps(a) + "\n")
    print(f"[corpus] wrote {len(anchors)} anchors -> {out / 'anchors.jsonl'}")

    index = build_milestone_index(files, repo)
    if index:
        with (out / "milestone_index.csv").open("w", newline="") as fh:
            w = csv.DictWriter(fh, fieldnames=list(index[0].keys()))
            w.writeheader()
            w.writerows(index)
        print(f"[corpus] wrote {out / 'milestone_index.csv'}")

    stats = {
        "corpus_sizes": sizes,
        "total_anchors": len(anchors),
        "anchors_by_type": dict(sorted(by_anchor.items(), key=lambda kv: -kv[1])),
        "top_files_by_anchor_density": dict(
            sorted(by_file.items(), key=lambda kv: -kv[1])[:25]
        ),
        "milestones_with_blocker": sum(1 for r in index if r["has_blocker"]),
        "milestones_total": len(index),
    }
    C.dump_json(stats, out / "corpus_stats.json")
    print(f"[corpus] wrote {out / 'corpus_stats.json'}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
