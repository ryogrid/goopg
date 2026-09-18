Task: M0140-0006b — the partial-Append cost producer for SETOP
(`.ralph/fix_plan.md:2657`, banner item 5). DONE and committed this loop
(`36ffb5f07`).

Banner item 5 reads "M0140-0006a → 0006b → 0006c". 0006a and 0006b are both
`[x]`. **Two tasks are now open under item 5, either order is safe** (see
this loop's design doc's ordering note — read it before picking either):
- **M0140-0006c** (`.ralph/fix_plan.md:2671`) — executor claim-set for
  `setOp` under `Gather` (correctness prerequisite; also the task that adds
  `case PathSetOp:` to `partialPathDrivingKind` in `gatherpaths.go`).
- **M0140-0006b-2** (`.ralph/fix_plan.md`, Parent: M0140-0006b) — wire
  `generateUsefulGatherPaths` into the Phase-4 upper-rel pipeline generally
  (WINDOW/ORDERED/GROUP_AGG/SETOP all currently unwired, not just SETOP).
  Needs its own recon first: no `*searchCtx` reaches any upper-rel producer
  today, so the shape of the fix (trimmed struct? full `*searchCtx`? a
  package knob?) is undecided.

**Read before starting either**:
`docs/design/0100-0149/m0140-0006-decomposition-into-a-b-c.md` (parent) and
`docs/design/0100-0149/m0140-0006b-partial-append-cost-producer.md` (this
loop's — has the "why this cannot move a plan today (two independent
reasons)" section explaining why 0006c and 0006b-2 can land in any order
EXCEPT: 0006c's own whitelist-opening step (`case PathSetOp:` in
`partialPathDrivingKind`) must be the LAST of the three to land.

Files this loop: `internal/optimizer/windowsetoppaths.go` (new
`addPartialSetOpPath`, called from `createSetOpPaths` right after 0006a's
branch-rel threading; new `setOpPartialAppendProducer` const,
`appendCPUCostMultiplier` const), `internal/optimizer/windowsetoppaths_test.go`
(14 new tests). Design doc:
`docs/design/0100-0149/m0140-0006b-partial-append-cost-producer.md` (new,
indexed in `docs/design/README.md`). `.ralph/fix_plan.md` (M0140-0006b
ticked `[x]`, M0140-0006b-2 filed). `.ralph/deferral_ledger.md` (one row:
the mixed partial/non-partial Append arm not built + the upper-rel-wide
Gather-wiring gap).

Key symbols: `addPartialSetOpPath` (`windowsetoppaths.go`, new).
`generateUsefulGatherPaths` (`gatherpaths.go:156`, method on `*searchCtx` —
NOT reachable from any upper-rel producer, confirmed this loop by reading
all three of its call sites). `partialPathDrivingKind`
(`gatherpaths.go:364`, the fail-closed whitelist with no `PathSetOp` arm —
0006c's job to add).

Finding: the parent decomposition doc's own open question — "does
`generateUsefulGatherPaths` read `PartialPathlist` for free?" — resolves to
NO, and the gap is upper-rel-wide (WINDOW/ORDERED/GROUP_AGG too, not just
SETOP): every call site of `generateUsefulGatherPaths` reads a `*searchCtx`'s
own `joinrels`/`joinrel`, and every upper-rel producer runs from
`planner.go`'s SetOp fold / window chain / grouping paths entirely outside
any `*searchCtx`. This means the producer landed this loop is inert by TWO
independent, already-existing mechanisms (no reader at all, AND the
whitelist gap even if it had one) rather than needing a brand-new env flag —
confirmed live by `tpch-spotcheck`/`tpcds-sf025-regression sweep`/
`tpch-acceptance-arm` all showing zero plan movement.

Gates run: `go build ./...` clean. `go vet ./internal/optimizer/...` clean.
`go test ./internal/optimizer/...` PASS (full package, no `-count=1`,
includes the 14 new tests). `go test ./internal/executor/...` PASS
(sibling-path audit — no executor change needed, by design: 0006c owns
that). `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=34) against the staged
tree. `scripts/tpcds-sf025-regression.sh sweep` PASS=96 MISMATCH=0
CKMISMATCH=0 ERROR=0 TIMEOUT=0, PLAN-SHAPE queries=99 same=99 changed=0,
against the staged tree. `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` all packages PASS. `scripts/tpch-acceptance-arm.sh`
PGSHAPED=1 HEAD baseline (`3f80f802a`, via a `git stash push -- <2 files>`/
build/`git stash pop`/re-`git add` round-trip, private port 5583) vs this
staged tree: VERDICT PASS, 24/24 labels MATCH — ran cleanly on the FIRST
attempt this time (PGSHAPED=1 from the start, no Q9-timeout gotcha). All
gate stamps' `code_tree` verified to match the committed index (the
commit-msg hook itself caught and rejected the first commit attempt for a
stale `tpch-acceptance-arm.json` from a prior loop — re-ran the arm against
this loop's own staged tree and it passed on retry). `python3
scripts/ralph_protected_regions.py check-designdocs` exit 0. `python3
scripts/ralph-lineage-guard.py` — caught a real formatting bug in this
loop's own new task (M0140-0006b-2's `Parent:` line was mid-sentence on a
body line instead of starting its own line); fixed and re-ran clean. `make
ralph-state-guard`: same status/progress clean-exit-marker inconsistency the
last several loops also hit, auto-repaired, then clean. Commit succeeded
(`36ffb5f07`) including the pre-commit pgbench smoke (PASS, all three
transaction types).

In-flight: none. All three private-arm binaries
(`tmp/goopg-acceptance-{baseline,mine}-bin`, `tmp/tpch-acceptance-runner`)
and the `/tmp/arm-m0140-0006b-*.txt` digest files deleted after the diff;
port 5583 verified free (`ss -ltnp`) before finishing. No shared cluster
(`:65432`/`:65433`/`:65437`/`:65438`) was started, stopped, reset, or
written beyond the online `pg_basebackup -X fetch` clone source reads the
private-clone scripts already do.
