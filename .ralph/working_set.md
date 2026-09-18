Task: M0140-0006b-2 — wire `generateUsefulGatherPaths` into the Phase-4
upper-rel pipeline (banner item 5). DONE and staged this loop (commit next).

Banner item 5 now has 0006a/0006b/0006c/0006b-2 all `[x]`. **One task
remains open under item 5** (banner order: items 0–4 exhausted — P0-E7
closed, fix2r/sweep/6-resume/0007c closed, S7 diagnosis done with impl
children explicitly non-selectable):
- **M0140-0006c-2** (Parent: M0140-0006c) — widen the partial-SetOp
  admission past bare scans: teach `collectShareableJoins`/
  `collectBitmapScans` (+ both prebuild passes) to descend into `*setOp`
  children, then widen `setOpBranchDrivingKindIsSupported` branch by
  branch, each with its own serial-vs-parallel identity test.

**Read before starting it**: `docs/design/0100-0149/
m0140-0006b-2-upper-rel-gather-wiring.md` (this loop — the reader 0006c-2's
widened branches will flow through, + the `top`-mode refusal policy),
plus `m0140-0006c-executor-claim-set.md` and
`m0140-0006-decomposition-into-a-b-c.md`.

Files this loop: `internal/optimizer/gatherpaths.go`
(`generateUpperRelGatherPaths`, delegates via minimal `&searchCtx{}` —
no twin body), `upperordered.go` / `upperorderedgrouping.go` /
`windowsetoppaths.go` (one call each, before `setCheapest`),
`windowsetoppaths_test.go` (e2e re-pinned: Gather generated but
dominated), `gatherpaths_upperrel_test.go` (NEW, 6 tests).
Design doc: `docs/design/0100-0149/m0140-0006b-2-upper-rel-gather-wiring.md`
(new, indexed). `.ralph/fix_plan.md` (0006b-2 ticked `[x]`).

Key symbols: `generateUpperRelGatherPaths` (`gatherpaths.go`, the only
upper-rel `PartialPathlist` reader). `parallelModeOK(cp)`
(`considerparallel.go:61`, pure — why no `*searchCtx` is needed).
`createSetOpPaths` (live site — must run after `addPartialSetOpPath`'s
`ConsiderParallel` stamp).

Finding: TPC-H provably unmoved (0/22 queries contain a setop — grepped
`tmp/c7-tpch-queries/`; other three sites inert by construction +
unit-pinned), so the match=8 floor holds transitively; TPC-DS 99/99
shapes identical. Chain now live: bare-scan UNION ALL can elect
Parallel Append when it wins on cost.

Gates run: `go build`/`go vet` clean. Optimizer+executor suites PASS.
Precommit units PASS (44 ok, no FAIL). `tpch-spotcheck` PASS
(Q12=2/Q13=34). sf025 sweep PASS=96 MISMATCH=0 PLAN-SHAPE 99/99
identical. `ea-ratchet` N/A (no estimate path). Acceptance-arm skipped
(not a stats/costing/executor change; values covered by sweep +
no-setop proof). Commit carries `PARITY: N/A` + reason (G2).

In-flight: none. No shared cluster touched (sweep via its own lane;
`:65433` only read by spotcheck as designed).
