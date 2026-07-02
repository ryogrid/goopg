"""Phase 5: merge duplicate items, check resolution against three free
sources (deferral ledger, fix_plan, later commits), adjudicate ambiguous
cases with haiku, write unimplemented_feat.json."""
import json
import re
from collections import defaultdict

from common import REPO, haiku_json, load, save

STOP = set("""the and are for with from this that have been will can may not into
over under while when where which whose after before only also still than then
its it's via per each both all any now new old more most less least same other
goopg postgres postgresql upstream test tests loop slice task milestone commit
design docs doc file files path support supported adds added add fix fixed
""".split())

RESOLUTION_WORDS = re.compile(
    r"\b(CLOSED|RESOLVED|PROMOTED|COMPLETE[DS]?|fully landed|now (?:lands|persists|supports|implements|enforces|round-trips)|"
    r"closes (?:the )?(?:prior|previous|remaining)|no remaining scope|lands the deferred|"
    r"consumes? (?:the )?(?:ledger|deferral)|deferral (?:row )?(?:is )?(?:now )?resolved)\b",
    re.IGNORECASE,
)


def keywords(text, n=10):
    # prefer identifiers (CamelCase / snake_case / dotted / pg_*) then plain words
    idents = re.findall(r"\b(?:[A-Za-z]+_[A-Za-z0-9_]+|[a-z]+[A-Z][A-Za-z0-9]+|pg_[a-z_]+)\b", text)
    words = [w.lower() for w in re.findall(r"\b[A-Za-z][A-Za-z0-9-]{3,}\b", text)]
    words = [w for w in words if w not in STOP]
    out, seen = [], set()
    for w in idents + words:
        wl = w.lower()
        if wl not in seen:
            seen.add(wl)
            out.append(wl)
        if len(out) >= n:
            break
    return out


def jaccard(a, b):
    a, b = set(a), set(b)
    return len(a & b) / len(a | b) if a | b else 0.0


def merge_items(items):
    """Group by task_id, then merge similar features within a group."""
    groups = defaultdict(list)
    for it in items:
        groups[it["task_id"] or "_none"].append(it)
    merged = []
    for tid, its in groups.items():
        clusters = []
        for it in its:
            kws = keywords(it["feature"], 12)
            placed = False
            for cl in clusters:
                if jaccard(kws, cl["kws"]) >= 0.4:
                    cl["members"].append(it)
                    cl["kws"] = list(set(cl["kws"]) | set(kws))[:20]
                    placed = True
                    break
            if not placed:
                clusters.append({"kws": kws, "members": [it]})
        for cl in clusters:
            ms = sorted(cl["members"], key=lambda m: -m["i"])  # oldest first
            best = max(ms, key=lambda m: len(m["feature"]))
            merged.append({
                "feature": best["feature"],
                "task_id": None if tid == "_none" else tid,
                "resume_point": next((m["resume_point"] for m in ms if m["resume_point"]), None),
                "evidence": sorted({m["evidence"] for m in ms if m["evidence"]})[:3],
                "first_deferred_commit": ms[0]["commit_id"],
                "deferred_date": ms[0]["date"],
                "source_commits": [m["commit_id"] for m in ms],
                "newest_i": min(m["i"] for m in ms),
                "kws": keywords(best["feature"] + " " + (best["resume_point"] or ""), 12),
            })
    return merged


def parse_ledger():
    rows = []
    for line in (REPO / ".ralph" / "deferral_ledger.md").read_text().splitlines():
        if not line.startswith("|"):
            continue
        cols = [c.strip() for c in line.strip("|").split("|")]
        if len(cols) < 7 or cols[0] in ("status", "--------"):
            continue
        if set(cols[0]) <= {"-"} and len(cols[0]) > 1:
            continue
        rows.append({"status": cols[0], "task_id": cols[2],
                     "text": " ".join(cols[3:6]).lower()})
    return rows


def check_ledger(item, ledger_rows):
    """Return 'resolved' / 'open' / 'no-match' based on best-matching row."""
    best, best_score = None, 0.0
    for row in ledger_rows:
        score = sum(1 for k in item["kws"] if k in row["text"]) / max(len(item["kws"]), 1)
        if item["task_id"] and item["task_id"] == row["task_id"]:
            score += 0.25
        if score > best_score:
            best, best_score = row, score
    if best is None or best_score < 0.45:
        return "no-match"
    return "resolved" if best["status"] == "resolved" else "open"


def check_fix_plan(item, fix_plan_text):
    tid = item["task_id"]
    if not tid or not re.match(r"M\d{4}-\d{4}", tid or ""):
        return "no-match"
    checked = re.search(rf"- \[x\][^\n]*\b{re.escape(tid)}\b", fix_plan_text)
    unchecked = re.search(rf"- \[ \][^\n]*\b{re.escape(tid)}\b", fix_plan_text)
    if unchecked:
        return "unchecked"
    if checked:
        return "checked"
    return "no-match"


def check_later_commits(item, commits):
    """Scan commits NEWER than the item's newest source commit for resolution
    evidence. Returns (verdict, snippets)."""
    kws = item["kws"]
    tid = item["task_id"]
    hits = []
    for c in commits[:item["newest_i"]]:  # newer = smaller index
        blob = c["title"] + "\n" + c["description"]
        kw_hits = sum(1 for k in kws if k in blob.lower())
        tid_hit = bool(tid) and tid in blob
        if kw_hits < max(3, len(kws) // 3) and not (tid_hit and kw_hits >= 2):
            continue
        has_res = bool(RESOLUTION_WORDS.search(blob))
        hits.append({"commit_id": c["commit_id"][:8], "date": c["commited_at"][:10],
                     "title": c["title"], "kw_hits": kw_hits, "tid_hit": tid_hit,
                     "resolution_lang": has_res,
                     "snippet": blob[:600]})
        if len(hits) >= 4:
            break
    if not hits:
        return "no-resolution-found", []
    strong = [h for h in hits if h["resolution_lang"] and (h["tid_hit"] or h["kw_hits"] >= 5)]
    if strong:
        return "likely-resolved", hits
    return "ambiguous", hits


ADJUDICATE_PROMPT = """A feature gap was recorded in a git commit of goopg (a Go PostgreSQL reimplementation). Later commits MAY have implemented it. For each case below decide whether the gap was later implemented.

Return ONLY JSON: {{"verdicts": [{{"case": <n>, "resolved": true/false, "reason": "<10 words"}}, ...]}}
Judge strictly from the snippets: "resolved": true only if a later commit clearly implements THAT SPECIFIC gap (not just the same task area).

{body}
"""


def adjudicate(ambiguous):
    verdicts = {}
    batch_size = 6
    for i in range(0, len(ambiguous), batch_size):
        batch = ambiguous[i:i + batch_size]
        parts = []
        for n, (idx, item, hits) in enumerate(batch, 1):
            later = "\n".join(
                f"  [{h['date']} {h['commit_id']}] {h['title']}\n  {h['snippet'][:400]}"
                for h in hits[:3])
            parts.append(f"=== case {n} ===\nGAP ({item['deferred_date']}): {item['feature']}\n"
                         f"LATER COMMITS:\n{later}")
        res = haiku_json(ADJUDICATE_PROMPT.format(body="\n\n".join(parts)))
        if res:
            for v in res.get("verdicts", []):
                k = v.get("case")
                if isinstance(k, int) and 1 <= k <= len(batch):
                    verdicts[batch[k - 1][0]] = bool(v.get("resolved"))
        print(f"adjudicated {min(i + batch_size, len(ambiguous))}/{len(ambiguous)}")
    return verdicts


def main():
    items = load("items_raw.json")
    commits = load("filtered.json")
    print(f"raw items: {len(items)}")
    merged = merge_items(items)
    print(f"after merge: {len(merged)}")

    ledger_rows = parse_ledger()
    fix_plan_text = (REPO / ".ralph" / "fix_plan.md").read_text()
    print(f"ledger rows: {len(ledger_rows)}")

    kept, dropped, ambiguous = [], [], []
    for idx, item in enumerate(merged):
        led = check_ledger(item, ledger_rows)
        fp = check_fix_plan(item, fix_plan_text)
        later, hits = check_later_commits(item, commits)
        item["resolution_check"] = {"ledger": led, "fix_plan": fp, "later_commits": later}
        if led == "resolved" or later == "likely-resolved":
            dropped.append(item)
        elif later == "ambiguous":
            ambiguous.append((idx, item, hits))
        else:
            kept.append(item)

    print(f"kept={len(kept)} dropped={len(dropped)} ambiguous={len(ambiguous)}")
    verdicts = adjudicate(ambiguous) if ambiguous else {}
    for idx, item, _hits in ambiguous:
        if verdicts.get(idx, False):
            item["resolution_check"]["later_commits"] = "resolved-by-adjudication"
            dropped.append(item)
        else:
            item["resolution_check"]["later_commits"] = "unresolved-by-adjudication"
            kept.append(item)

    for item in kept:
        supports = [
            item["resolution_check"]["ledger"] == "open",
            item["resolution_check"]["fix_plan"] == "unchecked",
            item["resolution_check"]["later_commits"] in
            ("no-resolution-found", "unresolved-by-adjudication"),
        ]
        item["confidence"] = "high" if (supports[0] or supports[1]) and supports[2] else "medium"
        item.pop("kws", None)
        item.pop("newest_i", None)

    kept.sort(key=lambda x: (x["confidence"] != "high", x["deferred_date"]))
    save("resolved_dropped.json", [
        {k: v for k, v in d.items() if k not in ("kws", "newest_i")} for d in dropped])

    out = {
        "generated_at": "2026-07-02",
        "source": "commit-log.json (2597 commits, 2026-04-28..2026-07-01)",
        "method": ("python prefilter + haiku vocabulary discovery + regex candidate windows "
                   "+ haiku batch extraction + ledger/fix_plan/timeline resolution check "
                   "+ haiku adjudication of ambiguous cases"),
        "counts": {"raw_extracted": len(items), "merged": len(merged),
                   "dropped_as_resolved": len(dropped), "unimplemented": len(kept)},
        "unimplemented_features": kept,
    }
    with open(REPO / "unimplemented_feat.json", "w") as f:
        json.dump(out, f, ensure_ascii=False, indent=1)
    print(f"FINAL: {len(kept)} unimplemented features -> unimplemented_feat.json")


if __name__ == "__main__":
    main()
