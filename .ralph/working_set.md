(idle — nothing in flight)

M0127-P5.4c-ii-a is CLOSED, committed and pushed.

**NEXT LOOP: re-read the `## Current Priority` banner (it wins over this note).
It parks M-NIGHTLY below M0127, so the banner selects the next unchecked M0127
item. Read the P5 list in fix_plan ORDER but honour the dependency rule the
banner itself states — see the carry-over facts below before selecting.**

Carry-over facts a next loop should not re-derive:

- **`M0127-P5.4b-ii-b-2` is DEPENDENCY-DEFERRED, not skipped.** Both halves
  (Memoize eligibility, the §5.2 constructor binding contract) need a built
  `*Join` NODE — `tryBuildNLI` analyses one — and `createPlan` builds none for
  a searched subtree until **P5.5**. Re-select it AFTER P5.5.
- **P5.4c-ii was SPLIT this loop into ii-a (DONE) / ii-b / ii-c.** The finding:
  recording index ordering does NOT unblock the merge arm. `addMergeJoinPath`
  refuses any parameterised path (joinpath.c:1073-1081) and every ordered path
  goopg has is parameterised, because index paths are built only from join
  clauses (`pathparamindex.go`). **The next dependency-free P5 items are
  `P5.4c-ii-b` and `P5.5`.**
- **P5.4c-ii-b needs a real `cost_index`** (costsize.c:520, correlation model).
  `paramIndexScanCost` prices ONE bound probe off the calibrated
  `indexProbeCost` and must NOT be stretched into a full-scan cost — 04 §1's
  one-currency rule. This is why ii-a stopped where it did.
- **`buildIndexPathkeys` (pathkeysindex.go) takes the column expressions from
  its CALLER.** goopg's pathkeys are syntactic, so a pathkey must be the very
  `*ColumnRef` the clauses carry; a re-synthesised same-named one has a
  different `Index`/`SourceTableIdx` and `exprEqual` reads it as another column.
  `paramIndexClause.innerKey` now keeps that operand. ii-b must find the same
  expressions from the rel's binding, not invent them.
- **`addPath`'s pathkey dimension is now live** (`comparePathkeysDim` is no
  longer a constant `dimEqual`), so a better-ordered parameterised index path
  survives a cheaper rival — PG's behaviour.
- **`sizeJoinRel` is STILL the open half of the `joinRelBuilder` seam** (P5.6).
  No concrete builder, no `planSelect` call site; `GOOPG_PGSHAPED_DP` stays
  OFF. Do not write a stand-in sizer.
- **P4.1 ledger row #3 still open**: `mergeJoinStream.bufferGroup` twin.
- **Repo gofmt baseline is go1.25; local gofmt is 1.26** — never wholesale `-w`.
- **Do NOT `git stash`** in this tree (9+ unrelated entries).
- **Gate recipes** — SPOT: `scripts/tpch-spotcheck.sh`. DS05:
  `scripts/tpcds-sf05-regression.sh sweep` (~1 h). PLAN:
  `bench/tpch/setup_goopg.sh` → `PATH=$PWD/postgres/local_install/bin:$PATH
  make plan-gate` → `bench/tpch/stop_goopg.sh`.

Gates run this loop: UNITS PASS (exit 0, 0 FAILs, `/tmp/units_p54ciia.log`);
build + `go vet ./internal/planner` + gofmt clean on every touched file;
pgbench SMOKE PASS via the commit hook. SPOT/DS05 not applicable — the new
pathkeys ride a path kind with no non-test caller and `GOOPG_PGSHAPED_DP` is
OFF, so no plan and no row can move.

Nightly triage: the same 17 `AI-20260804-005028-*` subjects, all already filed.
Nothing new to file.

In-flight: none.
