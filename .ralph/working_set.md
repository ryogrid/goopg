(idle — nothing in flight)

M0127-P5.4c-i is CLOSED, committed and pushed.

**NEXT LOOP: re-read the `## Current Priority` banner (it wins over this note).
It parks M-NIGHTLY below M0127, so the banner selects the next unchecked M0127
item. Read the P5 list in fix_plan ORDER but honour the dependency rule the
banner itself states — see the two carry-over facts below before selecting.**

Carry-over facts a next loop should not re-derive:

- **`M0127-P5.4b-ii-b-2` is DEPENDENCY-DEFERRED, not skipped** (note added to
  its fix_plan body this loop). Both halves (Memoize eligibility, the §5.2
  constructor binding contract) need a built `*Join` NODE — `tryBuildNLI`
  analyses one — and `createPlan` builds none for a searched subtree until
  **P5.5**. Re-select it AFTER P5.5.
- **The next dependency-free P5 items are `P5.4c-ii` and `P5.5`.** P5.4c-ii
  (`generate_mergejoin_paths`, joinpath.c:1564 — the already-ordered-outer
  merge arm + mergeclause truncation + materialize-inner) needs a PRODUCER of
  pathkeys first: neither `generateScanPaths` (pathgen.go) nor
  `pathparamindex.go` records an index's own ordering. Its CONSUMER half
  already exists and is tested (`addMergeJoinPath`'s `pathkeysContainedIn`
  skip, `TestSortInnerAndOuter_SkipsSortWhenInputAlreadyOrdered`).
- **`joinpathsmerge.go` is the merge arm's only file.** `sortInnerAndOuter` is
  called from `addPathsToJoinrel` BEFORE the hash arm, matching PG's order
  inside `add_paths_to_joinrel` — that order IS the tie-break, since `addPath`
  keeps the incumbent on an exact cost tie.
- **`PathSort` now has a producer** (`sortPathFor`). The Sort is a child Path,
  deliberately NOT published to the input rel's pathlist. P5.5's `createPlan`
  must emit the executor Sort from `Children`, not from a merge-only field.
- **`equiClause` (joinpaths_test.go) now carries real operand expressions**
  (`col(int(relset))`), and `equiClauseOn` overrides them. The merge arm is the
  first consumer that reads `leftKey`/`rightKey`.
- **Every JOIN path in the search is still UNPARAMETERISED.** The merge arm
  refuses a parameterised result via PG's own test (empty `param_source_rels`,
  no `allow_star_schema_join` escape), so P5.4b-ii-b-1's invariant holds.
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

Gates run this loop: UNITS PASS (exit 0, 0 FAILs, `/tmp/units_p54ci.log`);
build + `go vet ./internal/planner` + gofmt clean on every touched file;
pgbench SMOKE PASS via the commit hook. SPOT/DS05 not applicable — the arm has
no non-test caller and `GOOPG_PGSHAPED_DP` is OFF, so no plan and no row can
move.

Nightly triage: the same 17 `AI-20260804-005028-*` subjects, all already filed
(fix_plan lines ~1093-1135). Nothing new to file.

In-flight: none.
