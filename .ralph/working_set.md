(idle — nothing in flight)

M0127-P5.2 is CLOSED, committed and pushed.

**NEXT LOOP: re-read the `## Current Priority` banner (it wins over this
note). It parks M-NIGHTLY below M0127, so the banner selects the next
unchecked M0127 item — `M0127-P5.3` (`joinSearchOneLevel` phases 1+3:
clause joins against initial rels, disconnected cartesian, last-ditch;
`makeJoinRel` with PG's outer/inner printing convention).
IMPLEMENTATION-TODO P5.3; 03 §4.1-§4.2 (`joinrels.c:118`, `:200-256`).
Bar: UNITS + SPOT + PLAN.**

Carry-over facts a next loop should not re-derive:

- **P5.3 has both its substrates already.** `joinsearch.go` (P5.1:
  `searchCtx`, `addRel`, `levelRels`, `findRel`, `finalRel`,
  `buildInitialRels`) and `joinrestrict.go` (P5.2). `levelRels` returns the
  LIVE slice — phase 2 indexes into it, never reorder.
- **P5.3's phase-1 gates are already written** in joinrestrict.go:
  `hasRelevantJoinClause(a,b)` (OVERLAP, no coverage — PG joininfo.c:39) and
  `hasNoJoinClauseAtAll(rel)` (joinrels.c:120 else-branch). `clausesFor` is
  the COVERAGE rule (build_joinrel_restrictlist) for qual placement;
  `selectivityClauses` is the EC-deduped subset P5.6 sizes from.
- **03 §3 was corrected this loop** — it had defined hasRelevantJoinClause
  with a coverage requirement PG does not have. Do not "restore" it.
- **`joinOrderRestricted` is reserved, constant false in v1** (03 §4.4);
  outer joins stay pinned until `join_is_legal` inference lands.
- **Every initial rel has exactly ONE `PathPrebuilt`**; per-index +
  `PATH_PARAM_BY_REL` paths are DEFERRED to P5.4 (ledger `2026-08-04
  M0127-P5.1`), as is EC clause SYNTHESIS (ledger `2026-08-04 M0127-P5.2`).
- **`GOOPG_PGSHAPED_DP` read once into `pgShapedDP`**; use
  `pgShapedDPEnabled()`. `TestPgShapedDPDefaultsOff` pins it OFF until P5.9.
- **P4.1's ledger row #3 is STILL OPEN**: `mergeJoinStream.bufferGroup` keeps
  its hand-rolled twin of `materialBuffer`.
- **NL inner work_mem bound stays OFF** (`GOOPG_NL_MATERIALIZE_WORK_MEM=1`);
  the flip needs `cost_rescan` in `costInnerNestLoop` (`joincost.go:115`) = P5.7.
- **Repo gofmt baseline is go1.25; local gofmt is 1.26** — never wholesale
  `gofmt -w` (per-new-file is fine; only comment alignment moved this loop).
- **Do NOT `git stash`** in this tree (9+ unrelated entries).
- **Bundle discipline:** `docs/design/leftdeep-joins/**` is only ever touched
  at its IMPLEMENTATION-TODO checkboxes (03 §3 was an oracle correction).
  Tracker = `docs/design/0127-pg-shaped-join-search.md` §6.
- **PLAN gate recipe:** `bench/tpch/setup_goopg.sh` (no --reset), then
  `PATH=$PWD/postgres/local_install/bin:$PATH make plan-gate`, then
  `bench/tpch/stop_goopg.sh`. Nightly lane was idle at 06:17 JST.

Gates run this loop: UNITS PASS; PLAN 22/22 MATCH vs `m0127-p21-hashkeys`
(structural, live 65433); SMOKE via the commit hook. SPOT/DS05/REGRESS/RACE
not run — the change adds an unreferenced file (no `planSelect` call site,
flag OFF), so no plan, no row and no shared state can move; PLAN's 22/22 is
the empirical confirmation.

In-flight: none.
