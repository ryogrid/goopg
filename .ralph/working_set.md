(idle — nothing in flight)

M0127-P5.6-b is DONE and committed: `internal/planner/joinrelsize.go` (new) —
`calcJoinrelSize`, `superkeyJoinSelectivity`, `oneClausePerEquivClass`, and
`searchJoinRelBuilder`, the concrete `joinRelBuilder`. `selectivityClauses`
(joinrestrict.go) now delegates to `oneClausePerEquivClass`; `examineJoinVar`
(joinselectivity.go) now delegates its operand resolution to
`resolveJoinVarColumn`, which the superkey test reads too. Still inert.

**NEXT LOOP: re-read the `## Current Priority` banner (it wins over this note).
It parks M-NIGHTLY below M0127, so the banner selects `M0127-P5.6-c` — 04 §3.3's
clamp discipline: the FK-implied bound when a validated FK covers the join, with
M0126-0010's `max(l,r)` cap (cardinality.go:400-406) kept beside it for the
non-FK fallback. IMPLEMENTATION-TODO P5.6-c. Bar: UNITS.**

Carry-over facts a next loop should not re-derive:

- The sizer's signature is `s.calcJoinrelSize(cat, outer, inner, clauses)`; the
  catalog is NOT on `searchCtx` — the builder holds it (`newJoinRelBuilder`).
  `addParameterizedIndexPaths(cat)` still takes its own; both must be the
  planner's `SearchPathCatalog` (dbOid hazard).
- `superkeyJoinSelectivity` returns `(sel, residual)`; P5.6-c's clamp goes in
  `calcJoinrelSize` AFTER the residual product, and needs to know WHETHER a key
  fired (today that fact is only implicit in `sel`) — plan to return it.
- Divisor rule: UNIQUE index ⇒ its OWN relation's raw count; declared FK ⇒ the
  PARENT's (`1.0/ref_tuples`). Legacy `uniqueNoFanoutRawCount` gets that
  backwards — ledgered, dies with P6.3, do NOT "fix" it in the live planner.
- Raw counts come from `relInfos[i].baseRows` (PG's `rel->tuples`), never from
  `RelOptInfo.Rows` (post-filter).
- Column stats/keys resolve by NAME (`columnStatsByName`); `ColumnRef.Index` is
  a GLOBAL pre-search offset (03 §10).
- 4 new ledger rows: joinrel width = sum of inputs; `vardata->isunique` still
  unset in `examineJoinVar`; the legacy child-divisor defect; `nconst_ec`.
- Still open from earlier: P4.1 ledger row #3 (`mergeJoinStream.bufferGroup`
  twin); `pushOneConjunct` not taught the searched tag; `walkPlanExprs` misses
  `Aggregate.Passthrough`/`AggregateCall.Filter`/`WindowFunc`.
- Do NOT `git stash`; gofmt baseline go1.25 (never wholesale `-w`); `cd`
  persists across Bash calls — use absolute paths.
- Gate recipes — SPOT: `scripts/tpch-spotcheck.sh` (~30 s + build). DS05:
  `scripts/tpcds-sf05-regression.sh sweep` (~1 h). PLAN:
  `bench/tpch/setup_goopg.sh` → `PATH=$PWD/postgres/local_install/bin:$PATH
  make plan-gate` → `bench/tpch/stop_goopg.sh`.

Gates run this loop: build+vet clean; 12 new tests PASS; UNITS PASS (exit 0,
0 FAILs, `/tmp/units_p56b.log`); pgbench SMOKE via the commit hook. DS05 + SPOT
+ PLAN not applicable — the sizer has no production caller, so no plan can move.

Nightly triage: still the same 17 `AI-20260804-005028-*` subjects from run
20260804-005028, all already filed under M-NIGHTLY. Nothing new to file.

In-flight: none.
