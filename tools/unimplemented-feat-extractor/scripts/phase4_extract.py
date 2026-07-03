"""Phase 4: structured extraction — haiku reads candidate windows in batches
and emits deferral items; false positives dropped. 4-way parallel, resumable."""
import json
import sys
from concurrent.futures import ThreadPoolExecutor, as_completed
from pathlib import Path

from common import HERE, haiku_json, load

BATCH = 10
PARALLEL = 2
OUT = HERE / "items_raw.json"
DONE = HERE / "phase4_done.json"  # batch indices already processed (resume)

PROMPT_TMPL = """You are analyzing excerpts of git commit messages from goopg (a Go reimplementation of PostgreSQL, built by an autonomous agent). Each commit landed some work; many also DEFERRED some PostgreSQL behavior — left it unimplemented, out of scope, delegated to a later task/loop/slice, or faked with a shortcut.

For each commit excerpt below, extract EVERY distinct deferred/unimplemented item. Rules:
- Only extract things the message says were NOT implemented / left for later / done partially with a known gap.
- Do NOT extract: bugs that the commit FIXED, work the commit COMPLETED, generic future-tense niceties ("could be optimized someday") with no concrete gap, or descriptions of pre-existing tests.
- "feature" must state the missing INTERNAL BEHAVIOR concretely (1-2 sentences, English), understandable without the commit.
- "task_id" = the milestone/task id the gap belongs to (e.g. M0110-0001, root-0022, DU-002, 0118-0099), or null.
- "evidence" = short verbatim phrase from the excerpt proving the deferral.
- If a commit has no genuine deferral, return an empty list for it.

Return ONLY JSON:
{{"items": [{{"commit_id": "<8-char id>", "feature": "...", "task_id": "... or null", "resume_point": "... or null", "evidence": "..."}}, ...]}}

=== COMMIT EXCERPTS ===
{body}
"""


def format_batch(batch):
    parts = []
    for c in batch:
        parts.append(
            f"--- commit {c['commit_id'][:8]} ({c['date']}) ---\n"
            f"TITLE: {c['title']}\n{c['window']}"
        )
    return "\n\n".join(parts)


def run_batch(bidx, batch):
    res = haiku_json(PROMPT_TMPL.format(body=format_batch(batch)))
    if res is None:
        return bidx, None
    items = res.get("items", [])
    # attach full metadata back
    by_short = {c["commit_id"][:8]: c for c in batch}
    out = []
    for it in items:
        c = by_short.get(str(it.get("commit_id", ""))[:8])
        if not c or not it.get("feature"):
            continue
        out.append({
            "feature": it["feature"],
            "task_id": it.get("task_id") or None,
            "resume_point": it.get("resume_point") or None,
            "evidence": it.get("evidence") or "",
            "commit_id": c["commit_id"],
            "date": c["date"],
            "title": c["title"],
            "i": c["i"],
        })
    return bidx, out


def main():
    cands = load("candidates.json")
    batches = [cands[i:i + BATCH] for i in range(0, len(cands), BATCH)]
    done = set(json.loads(DONE.read_text())) if DONE.exists() else set()
    items = json.loads(OUT.read_text()) if OUT.exists() else []
    todo = [(i, b) for i, b in enumerate(batches) if i not in done]
    print(f"{len(batches)} batches total, {len(todo)} to run")

    failed = []
    with ThreadPoolExecutor(max_workers=PARALLEL) as ex:
        futs = {ex.submit(run_batch, i, b): i for i, b in todo}
        for n, fut in enumerate(as_completed(futs), 1):
            bidx, out = fut.result()
            if out is None:
                failed.append(bidx)
                print(f"[{n}/{len(todo)}] batch {bidx} FAILED")
            else:
                items.extend(out)
                done.add(bidx)
                print(f"[{n}/{len(todo)}] batch {bidx}: +{len(out)} items (total {len(items)})")
            if n % 5 == 0 or n == len(todo):
                OUT.write_text(json.dumps(items, ensure_ascii=False, indent=1))
                DONE.write_text(json.dumps(sorted(done)))

    OUT.write_text(json.dumps(items, ensure_ascii=False, indent=1))
    DONE.write_text(json.dumps(sorted(done)))
    print(f"done: {len(items)} items, {len(failed)} failed batches: {failed}")
    if failed:
        sys.exit(1)


if __name__ == "__main__":
    main()
