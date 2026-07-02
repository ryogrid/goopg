"""Phase 3: regex candidate extraction — scan filtered commits with the
vocabulary, cut a window around each match, attach work-unit ids."""
import re

from common import WORK_UNIT_RE, load, save

# phrases that are pure noise as standalone triggers (too generic even for a
# candidate pass) — they only appear in windows if a stronger term also hit
DROP_PHRASES = {"partial", "ledger", "todo"}  # 'ledger' handled explicitly below


def build_pattern(vocab):
    terms = set(t.lower() for t in vocab["seed_terms"])
    terms.update(vocab["discovered_phrases"])
    terms = {t for t in terms if len(t) >= 4 and t not in DROP_PHRASES}
    # longest-first so multi-word phrases win
    alts = sorted((re.escape(t) for t in terms), key=len, reverse=True)
    return re.compile("|".join(alts), re.IGNORECASE), terms


def windows(text: str, pat: re.Pattern, ctx_lines: int = 2, max_chars: int = 1800):
    lines = text.splitlines()
    hit_lines = set()
    for i, ln in enumerate(lines):
        if pat.search(ln):
            hit_lines.update(range(max(0, i - ctx_lines), min(len(lines), i + ctx_lines + 1)))
    if not hit_lines:
        return ""
    out, prev = [], None
    for i in sorted(hit_lines):
        if prev is not None and i > prev + 1:
            out.append("[...]")
        out.append(lines[i])
        prev = i
    return "\n".join(out)[:max_chars]


def main():
    commits = load("filtered.json")
    vocab = load("vocab.json")
    pat, terms = build_pattern(vocab)
    print(f"pattern terms: {len(terms)}")

    candidates = []
    for idx, c in enumerate(commits):
        blob = c["title"] + "\n" + c["description"]
        if not pat.search(blob):
            continue
        win = windows(c["description"], pat)
        if not win and not pat.search(c["title"]):
            continue
        ids = sorted(set(WORK_UNIT_RE.findall(blob)))
        candidates.append({
            "i": idx,  # index in filtered.json (smaller = newer)
            "commit_id": c["commit_id"],
            "date": c["commited_at"][:10],
            "title": c["title"],
            "window": win,
            "ids": ids,
        })

    total_chars = sum(len(c["window"]) + len(c["title"]) for c in candidates)
    print(f"candidates: {len(candidates)}  window chars: {total_chars}")
    save("candidates.json", candidates)


if __name__ == "__main__":
    main()
