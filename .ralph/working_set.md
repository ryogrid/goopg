(idle — nothing in flight)

M0127-P4.1 is CLOSED and committed. **S4 is OPEN; P4.2 is next.**

**NEXT LOOP: re-read the `## Current Priority` banner (it wins over this
note). It parks M-NIGHTLY below M0127, so the banner selects the next
unchecked M0127 item — `M0127-P4.2` (hash outer-fill: matched bitmap per
batch, RIGHT sweep, FULL = LEFT fill + sweep, planner legality matrix for
RIGHT/FULL hash paths). IMPLEMENTATION-TODO P4.2; 07 §3. Bar: REGRESS
outer-join files + DS05.**

Carry-over facts a next loop should not re-derive:

- **P4.1 built the matched-bitmap pattern P4.2 needs.** `mergeJoinStream.
  groupMatched` is a `[]bool` sized by row COUNT (not bytes) with the
  RIGHT/FULL sweep running after the outer group — the same shape 07 §3
  asks for per hash batch. Read `join_merge_stream.go` `bufferGroup` /
  `stepGroupFill` before writing P4.2's.
- **RIGHT/FULL are still pinned onto merge join by planning rule**
  (0003-0001). P4.2 is what unpins them, so `join.sql`'s outer-join
  coverage currently exercises the NEW merge code, not hash.
- **`spillReader` gained `rewind()` and `closeKeepFile()`** — needed
  because `Close()` UNLINKS, which a replayed file must not do.
- **REGRESS technique that worked**: `scripts/pg-regress-runner.sh <tests>`
  in the tree and in a HEAD worktree, then compare the `.diff` files with
  `tail -n +3` (drops the tmpfile header) + `sed` on the repo path. Each
  test is seconds; `aggregates`/`with` are the slow ones and got their
  server killed at 2400 s — leave them out or budget separately.
  A worktree needs `ln -s <main>/postgres postgres` (it is an untracked
  real directory in the main tree, not a tracked path).
- **goopg's 512 MB `work_mem` default is NOT a no-spill setting** (P3.5
  measurement): Q3's lineitem build reports `Batches: 8 (originally 4)` at
  the default and needs 6 GB for nbatch 1. Input for P5.7's `hashJoinCost`.
- **P3.4's SPOT timing regression stands and is deliberate** (28.0 s this
  loop vs 28.3 s at P3.4 — flat). Do not "fix" it.
- **Repo gofmt baseline is go1.25; local gofmt is 1.26** — never `gofmt -w`.
- **Do NOT `git stash`** in this tree (9+ unrelated entries).
- **Bundle discipline:** `docs/design/leftdeep-joins/**` is NEVER modified
  except its IMPLEMENTATION-TODO checkboxes.

Gates run this loop: UNITS PASS; DS05 PASS=94 MISMATCH=0 CKMISMATCH=0
ERROR=0 over all 99 (Q72 TIMEOUT pre-existing) —
`analysis/leftdeep-joins/2026-08-04-p41-ds05-sweep.txt`; REGRESS zero delta
vs HEAD baseline on join/join_hash/select/subselect/union; SPOT PASS
(Q12=2 / Q13=35); SMOKE via the commit hook; `make ralph-state-guard` OK
(it repaired the previous loop's stale completed marker). RACE not run —
no new shared state (the streams are per-operator); PLAN not run — no plan
surface touched.

In-flight: none.
