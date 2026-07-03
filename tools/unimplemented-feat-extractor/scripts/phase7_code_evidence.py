"""Phase 7: final precision pass — judge each kept item against actual code
lines from the current tree. `git grep` line content (not just file names) is
decisive for "feature entirely missing" items; haiku weighs it per item and
keeps anything doubtful."""
import json
import re
import subprocess
from collections import Counter

from common import REPO, haiku_json

GENERIC = {"fix_plan", "pg_dump", "pg_catalog", "postgresql", "goopg_test"}


def idents_of(item, n=4):
    text = item["feature"] + " " + " ".join(item["evidence"]) + " " + (item["resume_point"] or "")
    cands = re.findall(
        r"\b(?:[A-Za-z]+_[A-Za-z0-9_]+|[a-z]+[A-Z][A-Za-z0-9]{3,}|pg_[a-z_]{4,}"
        r"|[A-Z]{2,}[A-Z_]{4,})\b", text)
    # also plain distinctive words >=9 chars (e.g. 'walsender', 'RETURNING')
    cands += [w for w in re.findall(r"\b[A-Za-z]{9,}\b", text)]
    out, seen = [], set()
    for c in cands:
        cl = c.lower()
        if len(c) < 8 or cl in GENERIC or cl in seen:
            continue
        seen.add(cl)
        out.append(c)
        if len(out) >= n:
            break
    return out


def grep_lines(ident, max_lines=5):
    try:
        proc = subprocess.run(
            ["git", "grep", "-in", "-F", ident, "--", "internal/", "cmd/"],
            cwd=REPO, capture_output=True, text=True, timeout=30)
        lines = [ln for ln in proc.stdout.splitlines()
                 if "_test.go" not in ln.split(":", 1)[0]]
        return [ln[:160] for ln in lines[:max_lines]]
    except Exception:  # noqa: BLE001
        return []


PROMPT = """Each case below is a feature gap recorded in an old commit of goopg (a Go PostgreSQL reimplementation). CODE LINES show what the CURRENT source tree contains for related identifiers (test files excluded).

Decide per case whether the gap is now implemented in the current tree:
- "implemented": code lines clearly show the missing behavior now exists as real logic (registration + handling, actual execution paths) — not a TODO, stub, error-return, or mere mention in a comment/string.
- "open": code lines are absent, or only show stubs/TODOs/unrelated usage, or the gap is about a SPECIFIC sub-case the lines don't prove.
When in doubt choose "open".

Return ONLY JSON: {{"verdicts": [{{"case": <n>, "verdict": "implemented|open", "why": "<8 words"}}, ...]}}

{body}
"""


def main():
    doc = json.loads((REPO / "unimplemented_feat.json").read_text())
    items = doc["unimplemented_features"]

    cases = []
    for item in items:
        ev = {}
        for ident in idents_of(item):
            lines = grep_lines(ident)
            if lines:
                ev[ident] = lines
        cases.append({"item": item, "ev": ev})
    with_ev = [c for c in cases if c["ev"]]
    print(f"{len(items)} items, {len(with_ev)} have code-line evidence")

    verdicts = {}
    batch_size = 6
    for i in range(0, len(with_ev), batch_size):
        batch = with_ev[i:i + batch_size]
        parts = []
        for n, c in enumerate(batch, 1):
            ev_s = "\n".join(f"  [{k}]\n" + "\n".join(f"    {ln}" for ln in v)
                             for k, v in c["ev"].items())
            parts.append(f"=== case {n} ===\nGAP ({c['item']['deferred_date']}): "
                         f"{c['item']['feature'][:300]}\nCODE LINES:\n{ev_s}")
        res = haiku_json(PROMPT.format(body="\n\n".join(parts)))
        if res:
            for v in res.get("verdicts", []):
                k = v.get("case")
                if isinstance(k, int) and 1 <= k <= len(batch):
                    verdicts[id(batch[k - 1]["item"])] = (
                        v.get("verdict", "open"), v.get("why", ""))
        print(f"code-judged {min(i + batch_size, len(with_ev))}/{len(with_ev)}")

    kept, dropped = [], []
    tally = Counter()
    for c in cases:
        item = c["item"]
        verdict, why = verdicts.get(id(item), ("no-evidence", ""))
        tally[verdict] += 1
        if verdict == "implemented":
            item["resolution_check"]["later_commits"] = f"implemented-in-current-code ({why[:60]})"
            dropped.append(item)
        else:
            kept.append(item)
    print("tally:", dict(tally))

    from common import HERE
    dropped_path = HERE / "resolved_dropped.json"
    prior = json.loads(dropped_path.read_text())
    dropped_path.write_text(json.dumps(prior + dropped, ensure_ascii=False, indent=1))

    kept.sort(key=lambda x: (x["confidence"] != "high", x["deferred_date"]))
    doc["unimplemented_features"] = kept
    doc["counts"]["dropped_as_resolved"] += len(dropped)
    doc["counts"]["unimplemented"] = len(kept)
    doc["method"] += " + current-code line-evidence final pass"
    (REPO / "unimplemented_feat.json").write_text(json.dumps(doc, ensure_ascii=False, indent=1))
    print(f"FINAL after phase7: {len(kept)} items (dropped {len(dropped)})")


if __name__ == "__main__":
    main()
