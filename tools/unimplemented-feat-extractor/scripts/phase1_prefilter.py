"""Phase 1: deterministic prefilter — drop chore commits and empty descriptions."""
from common import load_commits, save

commits = load_commits()
kept = [
    c for c in commits
    if "chore" not in c["title"].lower() and c["description"].strip()
]
print(f"total={len(commits)} kept={len(kept)} "
      f"(dropped {len(commits) - len(kept)}: chore or empty description)")
save("filtered.json", kept)
