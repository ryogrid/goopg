"""Phase 6: precision re-verification of kept items.

The phase-5 timeline check required resolution vocabulary in later commit
bodies, which misses implementing commits whose TITLE alone announces the
feature ("m0005: walsender for START_REPLICATION"). This pass:
  1. matches each kept item's keywords against LATER commit titles,
  2. probes the current codebase with `git grep` for distinctive identifiers,
  3. asks haiku to re-adjudicate with that evidence.
Items judged implemented are dropped; unclear ones downgrade to medium.
"""
import json
import re
import subprocess
from collections import Counter

from common import REPO, haiku_json, load, save

STOP = set("""the and are for with from this that not into over under only also
still support supported implement implemented implementation goopg postgres
postgresql test tests missing behavior feature currently query queries table
tables column columns value values function functions internal executor parser
planner catalog server storage index indexes create alter drop select insert
update delete when where which
""".split())

GENERIC_IDENTS = {"fix_plan", "commit_id", "task_id", "go_test", "pg_dump",
                  "pg_catalog", "test_port", "testport"}


def kws_of(item, n=10):
    text = item["feature"] + " " + " ".join(item["evidence"])
    words = [w.lower() for w in re.findall(r"\b[A-Za-z][A-Za-z0-9_-]{4,}\b", text)]
    out, seen = [], set()
    for w in words:
        if w in STOP or w in seen:
            continue
        seen.add(w)
        out.append(w)
        if len(out) >= n:
            break
    return out


def idents_of(item, n=3):
    text = item["feature"] + " " + " ".join(item["evidence"]) + " " + (item["resume_point"] or "")
    cands = re.findall(r"\b(?:[A-Za-z]+_[A-Za-z0-9_]+|[a-z]+[A-Z][A-Za-z0-9]{3,}|pg_[a-z_]{4,})\b", text)
    out, seen = [], set()
    for c in cands:
        cl = c.lower()
        if len(c) < 8 or cl in GENERIC_IDENTS or cl in seen:
            continue
        seen.add(cl)
        out.append(c)
        if len(out) >= n:
            break
    return out


def git_grep(ident):
    try:
        proc = subprocess.run(
            ["git", "grep", "-il", "-F", ident, "--", "internal/", "cmd/"],
            cwd=REPO, capture_output=True, text=True, timeout=30)
        files = [f for f in proc.stdout.splitlines() if f]
        return files[:4]
    except Exception:  # noqa: BLE001
        return []


PROMPT = """Items below were flagged as "deferred/unimplemented" in old commits of goopg (a Go PostgreSQL reimplementation). For each, LATER commit titles that might have implemented the gap are listed, plus which current source files mention related identifiers.

For each case decide: was THIS SPECIFIC gap most likely implemented later?
- "implemented": a later title clearly delivers the specific missing behavior (a feat/fix title naming the feature counts as delivery).
- "unimplemented": no later title covers it (code-file mentions alone do NOT prove implementation).
- "unclear": titles are related but partial/ambiguous.

Return ONLY JSON: {{"verdicts": [{{"case": <n>, "verdict": "implemented|unimplemented|unclear", "by": "<commit title fragment or empty>"}}, ...]}}

{body}
"""


def main():
    doc = json.loads((REPO / "unimplemented_feat.json").read_text())
    items = doc["unimplemented_features"]
    commits = load("filtered.json")
    # newest_i was stripped from the final file; rebuild from source_commits
    idx_of = {c["commit_id"]: i for i, c in enumerate(commits)}

    cases = []
    for item in items:
        newest_i = min(idx_of.get(cid, len(commits)) for cid in item["source_commits"])
        kws = kws_of(item)
        scored = []
        for c in commits[:newest_i]:
            tl = c["title"].lower()
            hits = sum(1 for k in kws if k in tl)
            if hits >= 1:
                scored.append((hits, c["title"]))
        scored.sort(key=lambda x: -x[0])
        titles = [t for h, t in scored[:6] if h >= max(2, scored[0][0] - 1)] if scored else []
        if scored and not titles:
            titles = [scored[0][1]]
        greps = {}
        for ident in idents_of(item):
            files = git_grep(ident)
            if files:
                greps[ident] = files
        cases.append({"item": item, "titles": titles, "greps": greps})

    with_evidence = [c for c in cases if c["titles"]]
    print(f"{len(items)} items, {len(with_evidence)} have later-title candidates")

    verdicts = {}
    batch_size = 8
    for i in range(0, len(with_evidence), batch_size):
        batch = with_evidence[i:i + batch_size]
        parts = []
        for n, c in enumerate(batch, 1):
            grep_s = "; ".join(f"{k} in {','.join(v)}" for k, v in c["greps"].items()) or "none"
            titles_s = "\n".join(f"  - {t[:130]}" for t in c["titles"])
            parts.append(
                f"=== case {n} ===\nGAP ({c['item']['deferred_date']}): {c['item']['feature'][:300]}\n"
                f"LATER TITLES:\n{titles_s}\nCODE MENTIONS: {grep_s}")
        res = haiku_json(PROMPT.format(body="\n\n".join(parts)))
        if res:
            for v in res.get("verdicts", []):
                k = v.get("case")
                if isinstance(k, int) and 1 <= k <= len(batch):
                    key = id(batch[k - 1]["item"])
                    verdicts[key] = (v.get("verdict", "unclear"), v.get("by", ""))
        print(f"re-adjudicated {min(i + batch_size, len(with_evidence))}/{len(with_evidence)}")

    kept, dropped = [], []
    tally = Counter()
    for c in cases:
        item = c["item"]
        verdict, by = verdicts.get(id(item), ("no-candidates", ""))
        tally[verdict] += 1
        if verdict == "implemented":
            item["resolution_check"]["later_commits"] = f"implemented-later (title: {by[:80]})"
            dropped.append(item)
        else:
            if verdict == "unclear":
                item["confidence"] = "medium"
                item["resolution_check"]["later_commits"] += "; title-recheck-unclear"
            kept.append(item)
    print("verdict tally:", dict(tally))

    prior_dropped = load("resolved_dropped.json")
    save("resolved_dropped.json", prior_dropped + dropped)

    kept.sort(key=lambda x: (x["confidence"] != "high", x["deferred_date"]))
    doc["unimplemented_features"] = kept
    doc["counts"]["dropped_as_resolved"] = doc["counts"]["dropped_as_resolved"] + len(dropped)
    doc["counts"]["unimplemented"] = len(kept)
    doc["method"] += " + later-title/code-grep re-verification"
    (REPO / "unimplemented_feat.json").write_text(json.dumps(doc, ensure_ascii=False, indent=1))
    print(f"FINAL after phase6: {len(kept)} items (dropped {len(dropped)} as implemented-later)")


if __name__ == "__main__":
    main()
