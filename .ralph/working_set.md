(idle — nothing in flight)

M0127-P5.1 is CLOSED, committed and pushed. M0125-0037 was ticked with it
(its own body pre-authorised "close on P5.1's landing"; no new work).

**NEXT LOOP: re-read the `## Current Priority` banner (it wins over this
note). It parks M-NIGHTLY below M0127, so the banner selects the next
unchecked M0127 item — `M0127-P5.2` (restrictInfo list +
`hasRelevantJoinClause`; equivalence-class selectivity rule — inferred edges
admissible, no double-count). IMPLEMENTATION-TODO P5.2; 03 §3; 04 §5. Bar:
UNITS + PLAN (ZERO diffs).**

Carry-over facts a next loop should not re-derive:

- **`internal/planner/joinsearch.go` is the new file P5.2–P5.5 all extend.**
  `searchCtx{joinrels, relMap, nrels, cp}`; `addRel` derives the level from
  `bits.OnesCount16` and errors on a duplicate relset; `levelRels` returns the
  LIVE slice (phase 2 indexes into it — never reorder); `finalRel` returns an
  error rather than asserting.
- **`buildInitialRels(bindings, scans, relInfos, cp)`** takes the three
  positionally-aligned slices `tryBushyDP` assembles at `bushy.go:184-196`.
  P5.3's entry point should collect them the same way.
- **Every initial rel has exactly ONE `PathPrebuilt`** over the leaf node,
  cost re-derived via `costSeqscan(cp, estScanPages(rows,width), rows, 0)`.
  Per-index + `PATH_PARAM_BY_REL` parameterised paths are DEFERRED to P5.4
  (ledger row `2026-08-04 M0127-P5.1`) — `cost_funcs.go` has no `cost_index`.
- **`GOOPG_PGSHAPED_DP` is read once into `pgShapedDP`**; read it via
  `pgShapedDPEnabled()`. `TestPgShapedDPDefaultsOff` pins it OFF until P5.9.
- **Non-table leaves need `EstimateRows(leaf)`, not `filteredRows`** — a
  subquery binding's `catalog.Table` is synthetic (row count 0 → floors to 1).
- **P4.1's ledger row #3 is STILL OPEN**: `mergeJoinStream.bufferGroup` keeps
  its hand-rolled twin of `materialBuffer`.
- **NL inner work_mem bound stays OFF** (`GOOPG_NL_MATERIALIZE_WORK_MEM=1`);
  the flip needs `cost_rescan` in `costInnerNestLoop` (`joincost.go:115`) = P5.7.
- **Repo gofmt baseline is go1.25; local gofmt is 1.26** — never `gofmt -w`.
- **Do NOT `git stash`** in this tree (9+ unrelated entries).
- **Bundle discipline:** `docs/design/leftdeep-joins/**` is only ever touched
  at its IMPLEMENTATION-TODO checkboxes. Tracker = `docs/design/0127-pg-shaped-join-search.md` §6.
- **PLAN gate recipe:** `bench/tpch/setup_goopg.sh` (no --reset), then
  `PATH=$PWD/postgres/local_install/bin:$PATH make plan-gate`, then
  `bench/tpch/stop_goopg.sh`. Verify no nightly batch is live first
  (`tmp/goopg-bench-bin` is shared with that lane).

Gates run this loop: UNITS PASS; PLAN 22/22 MATCH vs `m0127-p21-hashkeys`
(structural); SMOKE via the commit hook. SPOT/DS05/REGRESS/RACE not run — the
change adds an unreferenced file (no `planSelect` call site, flag OFF), so no
plan, no row, and no shared state can move; PLAN's 22/22 is the empirical
confirmation of that.

In-flight: none.
