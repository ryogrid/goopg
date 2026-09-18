Task: M0140-0006c — executor claim-set for `setOp` under `Gather`
(`.ralph/fix_plan.md`, banner item 5). DONE and committed this loop
(`49d2dd087`).

Banner item 5 ("M0140-0006a → 0006b → 0006c") now has 0006a/0006b/0006c all
`[x]`. **Two tasks remain open under item 5, either order safe** (0006c's
executor half already landed, so 0006b-2's wiring arrives behind a complete
claim-set; the whitelist arm has no reachable input until then — verified
by caller audit this loop, see the 0006c design doc):
- **M0140-0006b-2** (Parent: M0140-0006b) — wire `generateUsefulGatherPaths`
  into the Phase-4 upper-rel pipeline generally (WINDOW/ORDERED/GROUP_AGG/
  SETOP all unwired). Needs its own recon first: no `*searchCtx` reaches
  any upper-rel producer, so the shape (trimmed struct? full `*searchCtx`?
  package knob?) is undecided.
- **M0140-0006c-2** (Parent: M0140-0006c, filed this loop) — widen the
  partial-SetOp admission past bare scans: teach `collectShareableJoins`/
  `collectBitmapScans` (+ both prebuild passes) to descend into `*setOp`
  children, then widen `setOpBranchDrivingKindIsSupported` branch by
  branch, each with its own serial-vs-parallel identity test.

**Read before starting either**: `docs/design/0100-0149/m0140-0006c-executor-claim-set.md`
(this loop — has the "why landing the whitelist before 0006b-2 is safe"
caller audit + the one-level-nesting bound), plus the parent
`m0140-0006-decomposition-into-a-b-c.md` and
`m0140-0006b-partial-append-cost-producer.md`.

Files this loop: `internal/executor/parallel_scan.go` (setOpLeft/
setOpRight, `newLeafParallelClaimSet`, `unwrapToSetOp`, `attachAll`
dispatch), `internal/executor/parallel_setop_claimset_test.go` (NEW:
`TestGatherOverSetOpIdentity`, 260+90-row fixture, 1/2/4 workers —
adopted from a cut-off loop's untracked file, fixed `intDatumForTest` →
`NewIntDatum`), `internal/executor/parallel_gather_merge_claimset_test.go`
(2 claim-kind arms), `internal/optimizer/parallel.go` (`*SetOp` twins in
stamp/driving/unstamp), `internal/optimizer/parallel_test.go` (4 tests),
`internal/optimizer/gatherpaths.go` (`case PathSetOp:` +
`setOpBranchDrivingKindIsSupported`), `internal/optimizer/
windowsetoppaths_test.go` (5 driving-kind tests). Design doc:
`docs/design/0100-0149/m0140-0006c-executor-claim-set.md` (new, indexed).
`.ralph/fix_plan.md` (0006c ticked `[x]`, 0006c-2 filed).
`.ralph/deferral_ledger.md` (one row: join/bitmap-driven branches).

Key symbols: `parallelClaimSet.attachAll` (`parallel_scan.go:586`, shared
by `gatherOp` + `gatherMergeOp` — no second call site). `drivingScan` /
`stampParallelScan` (`parallel.go`, general recursion, deliberately wider
than admission). `partialPathDrivingKind` (`gatherpaths.go`, fail-closed;
new `PathSetOp` arm). `generateUsefulGatherPaths` (`gatherpaths.go:156`,
still unreachable from any upper-rel producer — 0006b-2's job).

Finding: this loop did NOT re-derive 0006c — it adopted a cut-off loop's
uncommitted diff (~10 min old, "In-flight: none" in this baton was stale).
The diff was complete/coherent; the single gap was the untracked identity
test's bad helper ref. Mutation-verified: dispatch disabled → 700/700/
1750 rows vs 350 want (exact workers+1 N-copies defect); enabled → 350/350.

Gates run: `go build`/`go vet` clean. Optimizer+executor suites PASS.
`tpch-spotcheck` PASS (Q12=2/Q13=34). `tpcds-sf025 sweep` PASS=96
MISMATCH=0 PLAN-SHAPE 99/99 identical. `tpch-acceptance-arm` PGSHAPED=1
HEAD-baseline (stash round-trip, port 5583, seed pinned) vs staged:
VERDICT PASS 24/24 MATCH. Precommit units: no FAIL (`internal/parser`
green at this HEAD — its earlier AST-drift failure is gone). All stamps
share one `code_tree` matching the staged index. Lineage +
check-designdocs guards exit 0. Commit includes the pre-commit pgbench
smoke.

In-flight: none after commit. Private-arm binaries
(`tmp/goopg-acceptance-{baseline,mine}-bin`, `tmp/tpch-acceptance-runner`)
and `/tmp/arm-m0140-0006c-*.txt` digests deleted after the diff; port
5583 verified free. No shared cluster touched (all arms via private clone
of `:65433`, read-only source).
