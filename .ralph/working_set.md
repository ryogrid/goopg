Task: M0141-S2a-fix2r — re-apply the PG-faithful `hashAggEntrySize` currency
correction (owner Q4: no reverts). DONE and committed this loop
(`ef5f26a46` code, `65ffbd20f` docs/fix_plan). Banner item 2 is now
exhausted — **NEXT LOOP should re-read the banner and move to item 3**:
"Roll out fix1's success": select **M0141-S2a-fix1-sweep-a** first (the
ORDERED-upper-rel Sort narrowing site fix1-sweep filed, `Parent:
M0141-S2a-fix1-sweep`, named witness TPC-H Q18's 1.5M-row Sort), or
**M0141-S2a-fix1-sweep-b** (WINDOW's internal sort costing) — both are
`[ ]` and selectable; the banner text says fix1-sweep's own filed children,
then M0141-S2b-6-resume, then M0139-0007c. Confirm by re-reading
`.ralph/fix_plan.md`'s `## Current Priority` banner first (S1 precedence)
and re-check whether a concurrent loop already filed/took one of the two
sweep-children.

Files: `internal/optimizer/cost_funcs.go` (`costAgg`'s spill-arm guard
`inAvgVarBytes > 0` -> `inNcols > 0 || inAvgVarBytes > 0`, width argument
bare `inAvgVarBytes` -> `hashsize.EntryBytes(inNcols, inAvgVarBytes)` on
both `hashAggEntrySize` and the `pages` term, comments rewritten),
`internal/optimizer/cost_funcs_test.go` (2 blind sentinels `(ncols,0)` ->
`(0,0)`), `internal/optimizer/groupingpaths_test.go`
(`TestCostAggHashedNeverChargesSpill` renamed+inverted to
`TestCostAggHashedFixedWidthChargesSpill`, new
`TestCostAggHashedUnknownWidthNeverChargesSpill`),
`internal/optimizer/partialaggpaths_test.go` (row range 10M->3M).
Design doc: `docs/design/0100-0149/m0141-s2a-fix2r-hashaggentrysize-currency-reapply.md`
(new). `docs/design/README.md` (new index row + fix2's row annotated
"superseded"). `.ralph/fix_plan.md` (task ticked `[x]`).
`analysis/m0141/m0141-s2a-fix2r-{before,after}.{txt,plans.txt,pg.plans.txt}`
(6 new committed artefacts).

Key symbols: `costAgg` (cost_funcs.go:507), `hashAggEntrySize`,
`hashsize.EntryBytes`. Tools:
`scripts/tpch-acceptance-arm.sh`/`scripts/tpch-estimate-audit-arm.sh`
(private-worktree-baseline-binary-vs-working-tree-binary A/B, the same
method P0-H12 used last loop), `scripts/pg-plan-parity-diff.py`,
`docs/design/not_ralph/plan_parity_fix_take2/methodology/shape-delta.sh`,
`scripts/tpcds-sf025-regression.sh sweep`.

Finding: this is the SAME diff M0141-S2a-fix2 measured net-neutral-to-
regressing on 2026-09-15 and reverted — but re-measured on TODAY's baseline
(fix1 + fix1-sweep + other M0141 work has landed on top of the guard since)
it is a CLEAN WIN: TPC-H MATCH 7->8 (Q3 flips to MATCH), Q8 loses a tag
(its bushy spine now matches PG's own bushy choice for Q8), no category
rose anywhere. TPC-DS SF0.25: zero plan-shape change across all 99 queries
(Q31's old regression does not reproduce today). No query worsened in
either corpus, so per the task's own instruction ("file every query whose
categories worsen") **no `Parent: M0141-S2a-fix2r` follow-up was filed** —
there was nothing to file. Values confirmed byte-identical via
`tpch-acceptance-arm.sh`'s before/after digest diff (24/24 labels PASS).
Lesson for future re-applies of a previously-reverted change: always
re-measure on the CURRENT baseline before assuming the historical
measurement still holds — corpus drift can flip a net-neutral result into a
clean win (or the reverse) with zero code difference from the original
attempt.

Gates run: `go build ./...` clean. `go test ./internal/optimizer/...` PASS
(full package). `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=34 canonical,
gate-stamp PASS against staged code). `scripts/tpch-acceptance-arm.sh`
before/after digest diff: VERDICT PASS. `scripts/tpcds-sf025-regression.sh
sweep`: PASS (MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0, plan-shape
changed=0). `pg-plan-parity-diff.py` (TPC-H, same-PG-reference control):
MATCH 7->8, documented above. `make ea-ratchet`: N/A — reasoned in the
design doc (costing change, no row-estimate/selectivity path touched).
Pre-commit hook's pgbench smoke: PASS x2 (one per commit). `make
ralph-state-guard`: same self-repairing status/progress mismatch pattern as
the prior three loops (prior loop's clean-exit "completed" marker read as
stale by this loop's start); self-repaired to "in_progress", clean on
re-check.

In-flight: none. Detached worktree `/tmp/wt-fix2r-baseline` (built the
pre-change baseline binary at HEAD `683663605`) was created and removed
this loop (`git worktree remove --force`, confirmed via `git worktree
list`). All private-port servers (5582, 5583 — TPC-H estimate-audit and
acceptance arms) were stopped by their own scripts' EXIT traps; verified
only the legitimate shared `:65433` reference server remains running
(`systemctl --user list-units 'goopg-*'`). All temp binaries/output files
under `/tmp/` (baseline/after goopg binaries, estimate-audit binary, arm
digest files) removed after use. No shared cluster
(`:65432`/`:65433`/`:65437`/`:65438`) was started, stopped, reset, or
written beyond read-only `pg_basebackup`/`SELECT`/`EXPLAIN`.
