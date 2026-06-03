#!/usr/bin/env python3
"""Stage 4 helpers - chunk the corpus (free) and reduce mined lessons (free).

This module never calls an LLM. The actual paid map step is mine_lessons.sh,
which calls ``claude -p --model haiku`` over the chunks this script produces.

Two modes:

  chunk   Split the curated corpus (milestones + completed + handover) into
          bounded text chunks under data/chunks/ and write chunk_manifest.json
          plus EXTRACTION_PROMPT.txt. Respects file boundaries; never splits a
          line. Cheap; run any time.

  reduce  Read data/lessons_raw.jsonl (produced by mine_lessons.sh) and
          dedupe/cluster the lessons by a normalised key, emitting
          lessons_clustered.md ranked by frequency x cost.

Rationale for chunking before the LLM: the corpus is ~2.7 MB. A single prompt
cannot hold it and would be expensive even if it could. Bounded chunks let the
cheap Haiku model map over the text in parallel, and the reduce step (free,
local) does the dedup that an LLM would otherwise be paid to do.
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent / "lib"))
import common as C  # noqa: E402

CHUNK_CHARS = 14000  # ~3.5k tokens of input per chunk; comfortable for Haiku

EXTRACTION_PROMPT = """\
You are mining an autonomous coding agent's milestone history for EFFICIENCY
lessons. From the text below, extract every DISTINCT lesson where the agent
struggled, wasted effort, regressed, got blocked, or discovered a non-obvious
gotcha. Ignore routine success narration.

Return ONLY a JSON array. Each element:
{
  "lesson": "<one sentence: what went wrong or was hard>",
  "root_cause": "<why, if stated; else empty>",
  "resolution": "<how it was fixed/unblocked, if stated; else empty>",
  "cost_signal": "<any mention of attempts/loops/days it cost; else empty>",
  "prevention": "<what rule/check/tool would have prevented or shortened it>",
  "category": "<one of: regression, blocker, scope, correctness, perf, tooling, process, other>",
  "source_hint": "<milestone id or short quote anchoring the lesson>"
}
If there are no real lessons, return []. Do not use any tools. Output JSON only.

=== TEXT ===
"""


def curated_files(repo: Path) -> list[Path]:
    files: list[Path] = []
    files += sorted((repo / "docs" / "milestones").glob("*.md"))
    files += sorted((repo / "completed_milestones").glob("*.md"))
    files += sorted((repo / "docs" / "handover").glob("*.md"))
    return [f for f in files if f.exists()]


def chunk_text(text: str, limit: int) -> list[str]:
    """Split on blank lines, packing paragraphs up to `limit` chars."""
    chunks, cur, cur_len = [], [], 0
    for para in text.split("\n\n"):
        plen = len(para) + 2
        if cur and cur_len + plen > limit:
            chunks.append("\n\n".join(cur))
            cur, cur_len = [], 0
        # A single oversized paragraph is hard-split.
        if plen > limit:
            for i in range(0, len(para), limit):
                chunks.append(para[i : i + limit])
            continue
        cur.append(para)
        cur_len += plen
    if cur:
        chunks.append("\n\n".join(cur))
    return chunks


def do_chunk(repo: Path, out: Path, limit: int) -> int:
    chunk_dir = out / "chunks"
    chunk_dir.mkdir(parents=True, exist_ok=True)
    # Clear stale chunks so re-runs are clean and idempotent.
    for old in chunk_dir.glob("chunk_*.txt"):
        old.unlink()
    manifest = []
    idx = 0
    total_chars = 0
    for f in curated_files(repo):
        rel = str(f.relative_to(repo))
        for piece in chunk_text(f.read_text(errors="replace"), limit):
            idx += 1
            name = f"chunk_{idx:04d}.txt"
            (chunk_dir / name).write_text(EXTRACTION_PROMPT + piece)
            manifest.append({"chunk": name, "source": rel, "chars": len(piece)})
            total_chars += len(piece)
    C.dump_json(
        {"chunks": len(manifest), "total_chars": total_chars, "items": manifest},
        out / "chunk_manifest.json",
    )
    (out / "EXTRACTION_PROMPT.txt").write_text(EXTRACTION_PROMPT)
    # Cost estimate (Haiku 4.5 ~ $1/Mtok in, $5/Mtok out). IMPORTANT: each
    # `claude -p` is a FRESH process with no cross-call cache reuse, so every
    # call also pays cache-creation for its system prompt — empirically ~$0.05
    # per call. That per-call overhead dominates for small chunks, so include it.
    OVERHEAD_PER_CALL = 0.05
    in_tok = total_chars / 4
    est_cost = (
        in_tok / 1e6 * 1.0
        + (len(manifest) * 600) / 1e6 * 5.0
        + len(manifest) * OVERHEAD_PER_CALL
    )
    print(
        f"[chunk] {len(manifest)} chunks, {total_chars:,} chars "
        f"(~{in_tok/1000:.0f}k input tokens, est ${est_cost:.2f} on Haiku "
        f"incl. ~${OVERHEAD_PER_CALL}/call system-prompt overhead)"
    )
    return 0


_WORD = re.compile(r"[a-z0-9]+")


def norm_key(lesson: str) -> str:
    words = [w for w in _WORD.findall(lesson.lower()) if len(w) > 3]
    return " ".join(sorted(set(words))[:8])


def do_reduce(out: Path) -> int:
    raw = out / "lessons_raw.jsonl"
    if not raw.exists() or raw.stat().st_size == 0:
        # Tolerate an empty/missing raw file (e.g. a confirmed run that found no
        # lessons): write an empty clustered file and succeed, so run.sh's
        # `set -e` does not abort the whole pipeline before stage 5.
        (out / "lessons_clustered.md").write_text(
            "# Mined Lessons (clustered)\n\n_No lessons mined "
            "(lessons_raw.jsonl missing or empty)._\n"
        )
        print(f"[reduce] no raw lessons at {raw}; wrote empty clustered file")
        return 0
    clusters: dict[str, dict] = {}
    n = 0
    for line in raw.read_text(errors="replace").splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            lesson = json.loads(line)
        except json.JSONDecodeError:
            continue
        if not isinstance(lesson, dict) or not lesson.get("lesson"):
            continue
        n += 1
        key = norm_key(lesson["lesson"])
        c = clusters.setdefault(
            key,
            {"count": 0, "lesson": lesson["lesson"], "category": lesson.get("category", "other"),
             "root_causes": set(), "preventions": set(), "sources": set()},
        )
        c["count"] += 1
        if lesson.get("root_cause"):
            c["root_causes"].add(lesson["root_cause"][:200])
        if lesson.get("prevention"):
            c["preventions"].add(lesson["prevention"][:200])
        if lesson.get("source_hint"):
            c["sources"].add(lesson["source_hint"][:80])

    ranked = sorted(clusters.values(), key=lambda c: -c["count"])
    lines = ["# Mined Lessons (clustered)", "", f"_{n} raw lessons -> {len(ranked)} clusters_", ""]
    by_cat: dict[str, list] = {}
    for c in ranked:
        by_cat.setdefault(c["category"], []).append(c)
    for cat in sorted(by_cat):
        lines.append(f"## {cat}")
        lines.append("")
        for c in by_cat[cat]:
            lines.append(f"- **(x{c['count']})** {c['lesson']}")
            if c["preventions"]:
                lines.append(f"  - prevention: {' | '.join(sorted(c['preventions'])[:3])}")
            if c["sources"]:
                lines.append(f"  - sources: {', '.join(sorted(c['sources'])[:5])}")
        lines.append("")
    (out / "lessons_clustered.md").write_text("\n".join(lines))
    print(f"[reduce] {n} lessons -> {len(ranked)} clusters -> {out / 'lessons_clustered.md'}")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("mode", choices=["chunk", "reduce"])
    ap.add_argument("--repo-root", default=None)
    ap.add_argument("--out", default=None)
    ap.add_argument("--chunk-chars", type=int, default=CHUNK_CHARS)
    args = ap.parse_args()

    repo = C.repo_root(args.repo_root)
    out = Path(args.out) if args.out else Path(__file__).resolve().parent / "data"
    out.mkdir(parents=True, exist_ok=True)

    if args.mode == "chunk":
        return do_chunk(repo, out, args.chunk_chars)
    return do_reduce(out)


if __name__ == "__main__":
    raise SystemExit(main())
