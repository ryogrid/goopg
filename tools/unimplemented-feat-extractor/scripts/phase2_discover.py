"""Phase 2: vocabulary discovery — stratified random sample read by haiku
to find deferral phrasings and work-unit id formats; merged with a seed list."""
import json
import random

from common import haiku_json, load, save

SEED_TERMS = [
    "defer", "deferred", "deferral", "out of scope", "out-of-scope",
    "follow-up", "followup", "follow up loop", "future loop", "future slice",
    "future milestone", "future work", "later loop", "later milestone",
    "not yet", "unimplemented", "not implemented", "unsupported", "TODO",
    "stub", "no-op", "punt", "remains un", "remains missing", "left out",
    "left as", "left for", "in-memory only", "dump-fidelity only",
    "stays under", "stay under", "blocked on", "blocks on", "ledger",
    "resume point", "unblocks", "next loop", "shortcut", "placeholder",
    "partial", "narrow fix", "does not implement", "goopg does not",
    "not wired", "not persisted", "catalog-only", "accepted-and-dropped",
    "parse-only", "silently ignored",
]

PROMPT_TMPL = """You are analyzing git commit messages from goopg, a Go reimplementation of PostgreSQL, developed by an autonomous agent loop. Commits routinely note work that was intentionally deferred/left unimplemented.

Read the {n} commit messages below. Return ONLY a JSON object (no prose) with:
{{
 "deferral_phrases": [list of exact words/phrases found in THESE messages that indicate work was intentionally left unimplemented, postponed, delegated to a later task, done only partially, or faked with a shortcut — multi-word phrases welcome],
 "work_unit_formats": [list of identifier patterns seen that name tasks/milestones/slices/specs, as concrete examples e.g. "M0110-0001", "DU-002 slice 142"]
}}
Only include phrases actually present in the messages. Do not include phrases that describe completed work.

=== COMMITS ===
{body}
"""


def main():
    commits = load("filtered.json")
    random.seed(20260702)
    # stratify: 6 time buckets over the (desc-sorted) list
    n_buckets, per_bucket = 6, 9
    size = len(commits) // n_buckets
    sample = []
    for b in range(n_buckets):
        chunk = commits[b * size:(b + 1) * size] or commits[-size:]
        sample.extend(random.sample(chunk, min(per_bucket, len(chunk))))
    print(f"sampled {len(sample)} commits")

    phrases, formats = set(), set()
    batch_size = 6
    for i in range(0, len(sample), batch_size):
        batch = sample[i:i + batch_size]
        body = "\n\n".join(
            f"--- commit {c['commit_id'][:8]} ---\nTITLE: {c['title']}\n{c['description'][:2500]}"
            for c in batch
        )
        res = haiku_json(PROMPT_TMPL.format(n=len(batch), body=body))
        if res:
            phrases.update(p.strip().lower() for p in res.get("deferral_phrases", []) if p.strip())
            formats.update(f.strip() for f in res.get("work_unit_formats", []) if f.strip())
            print(f"batch {i // batch_size + 1}: +{len(res.get('deferral_phrases', []))} phrases")
        else:
            print(f"batch {i // batch_size + 1}: FAILED (skipping)")

    vocab = {
        "seed_terms": SEED_TERMS,
        "discovered_phrases": sorted(phrases),
        "work_unit_examples": sorted(formats),
    }
    save("vocab.json", vocab)
    print(json.dumps(vocab, indent=1)[:3000])


if __name__ == "__main__":
    main()
