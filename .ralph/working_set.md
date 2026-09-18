Task: P0-H12 — TPC-DS SF1 Q9 shape divergence: new or pre-existing? DONE and
committed this loop (`641d561f7`). Finding: pre-existing, not a regression.
Banner item 1 ("Regressions found by P0-E7") had exactly two children —
P0-H11 (Parent: P0-E7, still `[!]`-equivalent: not selectable, owner decision
on M0142-0008 pending) and P0-H12 (now `[x]`) — so item 1 is now exhausted
with nothing selectable. **NEXT LOOP should re-read the banner and move to
item 2: M0141-S2a-fix2r** (re-apply the PG-faithful `hashAggEntrySize`
change that was discarded for parity reasons — owner Q4: no reverts;
degradations it causes are filed as their own tasks, not reverted). Confirm
by re-reading `.ralph/fix_plan.md`'s `## Current Priority` banner first
(S1/Working Set Carry precedence — the banner, not this file, is the
ordering authority) and re-grep `Parent: P0-E7` in case a concurrent loop
filed a new child.

Files: `docs/design/0100-0149/p0-h12-tpcds-sf1-q9-scan-type-divergence.md`
(new), `docs/design/0100-0149/m0137-0004-tpcds-match-reference-reconciliation.md`
(new "SF1 footnote" section), `docs/design/README.md` (new p0-h12 index
row), `.ralph/fix_plan.md` (P0-H12 ticked `[x]` with findings).
`analysis/m0142/p0h12-tpcds-sf1-{goopg-27d4ae001,pg}.plans.txt` +
`-diff.txt` (new tracked artifacts). No production code touched
(`internal/`, `cmd/`, `go.mod`, `go.sum` all clean before and after).

Key symbols/tools: `scripts/capture-tpcds.sh`, `scripts/pg-plan-parity-diff.py`,
`git worktree add --detach`, `scripts/goopg-test-run.sh` (direct binary
invocation, bypassing `bench/tpcds/server.sh`'s always-rebuild-from-HEAD
behavior).

Method used (reusable pattern for any future "is this pre-existing"
question): `git worktree add --detach /tmp/wt-<sha> <sha>`, build a binary
there, serve it against the *existing* data dir on the shared non-reference
port (`:65436` for TPC-DS SF1; never touch `:65432`/`:65433`/`:65438`),
capture with `GOOPG_EXPECT_BIN_SHA256` pinned to the old binary's hash,
diff against the already-saved HEAD capture. Worktree removed
(`git worktree remove --force`) and temp binary/logs deleted after use.

Finding: goopg's SF1 capture at `27d4ae001` is **numerically identical**
(same `match=1/99`, same `CATEGORIES`/`CATEGORIES-EXCL-MATCH` counts digit
for digit, Q9's own plan section byte-identical) to P0-E7's HEAD
(`a0e741a68`) capture. Q9's SF1 `scan-type` divergence pre-dates
`27d4ae001` — the `match >= 2 (Q9, Q41)` floor `m0137-0004` set is
**SF0.25-only** and never generalized to SF1. Not a regression from any of
the 72 commits in P0-E7's range. Root-causing *why* Q9 diverges at SF1 was
explicitly out of scope (question was "new or pre-existing", not "why") and
joins the pre-existing 73-query SF1 SHAPE-DIFF backlog rather than getting
its own new task — no deferral ledger row filed (D1 is for newly-discovered
gaps; nothing new was found).

Gates run: `go build ./...` clean (no production file touched, verified in
both the main tree and the `/tmp/wt-27d4ae001` worktree). `python3
scripts/ralph_protected_regions.py check-designdocs` exit 0. Pre-commit
hook's pgbench smoke: PASS (ran twice — once on the initial commit, once on
the amend that added the missing `Co-Authored-By` trailer I forgot the
first time; both PASS, no behavior difference). `make ralph-state-guard`:
same self-repairing status/progress mismatch pattern as the prior two loops
(prior loop's clean-exit "completed" marker read as stale by this loop's
start); self-repaired to "in_progress", clean on re-check.

In-flight: none. The historic-binary goopg TPC-DS SF1 server (`:65436`,
scope `goopg-p0h12-27d4ae001`) was started and stopped this loop via direct
binary invocation (never `bench/tpcds/server.sh`, which would have rebuilt
from current HEAD instead of `27d4ae001`); verified `down` again via
`bench/tpcds/server.sh status` before this loop ended. The `/tmp/wt-27d4ae001`
worktree and `/tmp/goopg-p0h12-27d4ae001-bin` binary were both removed. No
other servers/scopes/worktrees left running.
