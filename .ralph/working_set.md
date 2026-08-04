(idle — nothing in flight)

M0127-P5.4c-ii-c is CLOSED and committed. **P5.4c as a whole is CLOSED.**

**NEXT LOOP: re-read the `## Current Priority` banner (it wins over this note).
It parks M-NIGHTLY below M0127, so the banner selects the next unchecked M0127
item, which is `P5.5`.**

Carry-over facts a next loop should not re-derive:

- **P5.5 has a STATED PREREQUISITE, ledgered at P5.4c-ii-b:** `Path` names
  neither its index nor its scan direction, so a chosen `PathIndexScan` cannot
  be re-emitted as an `*IndexScan`. Add a `*catalog.Index` + direction carrier
  to `path.go` and fill it at BOTH index-path constructors
  (`addOneParameterizedIndexPath`, `addOneOrderedIndexPath`).
- **`P5.4b-ii-b-2` stays DEPENDENCY-DEFERRED until after P5.5** (Memoize + the
  §5.2 binding contract both need a built `*Join` NODE; `createPlan` builds none
  for a searched subtree until P5.5).
- **Merge path anatomy, now final for P5.5's createPlan arm:** `Children[0]` =
  outer, `Children[1]` = inner (a `PathSort` child when that side needed
  sorting); `HashKeys` = the ORDERED merge clauses; `Residual` = everything
  else, INCLUDING clauses a truncated merge demoted
  (`demoteDroppedMergeClauses`). `Pathkeys` is the OUTER PATH's full ordering,
  which may be longer than the merge key list.
- **`mergeInnerSortKeys` is the single `make_inner_pathkeys_for_merge`** used by
  both merge arms (`sortInnerAndOuter` and `generateMergeJoinPaths`). One outer
  key can owe several inner keys — do not re-introduce a per-group single inner
  key (Rule #2 sibling).
- **goopg's merge join never mark/restores.** `mergeJoinStream.bufferGroup`
  buffers each inner equal-key group (spilling past work_mem), so PG's
  `materialize_inner` decision is structurally absent and NO `PathMaterial` is
  wanted. Its COST is unpriced and ledgered against P5.6/P5.7.
- **An ordered index path is never `CheapestTotal`** (`indexCorrelationFor` is
  0). Anything that wants one must walk `rel.Pathlist`.
- **`sizeJoinRel` is STILL the open half of the `joinRelBuilder` seam** (P5.6).
  `GOOPG_PGSHAPED_DP` stays OFF. Do not write a stand-in sizer.
- **P4.1 ledger row #3 still open**: `mergeJoinStream.bufferGroup` twin.
- **Repo gofmt baseline is go1.25; local gofmt is 1.26** — never wholesale `-w`.
- **Do NOT `git stash`** in this tree (9+ unrelated entries).
- **Gate recipes** — SPOT: `scripts/tpch-spotcheck.sh`. DS05:
  `scripts/tpcds-sf05-regression.sh sweep` (~1 h). PLAN:
  `bench/tpch/setup_goopg.sh` → `PATH=$PWD/postgres/local_install/bin:$PATH
  make plan-gate` → `bench/tpch/stop_goopg.sh`.

Gates run this loop: UNITS PASS (exit 0, 0 FAILs, `/tmp/units_p54ciic.log`);
SPOT PASS (`/tmp/spot_p54ciic.log`, Q12=2 Q13=35, canonical); pgbench SMOKE PASS
via the commit hook; build + `go vet ./internal/planner` + gofmt clean on every
touched file. DS05 not applicable — the new arm adds paths to a search with no
`planSelect` caller and `GOOPG_PGSHAPED_DP` is OFF, so no plan and no row can
move.

Nightly triage: the same 17 `AI-20260804-005028-*` subjects, all already filed.
Nothing new to file.

In-flight: none.
