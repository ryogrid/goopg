(idle — nothing in flight)

M0127-P5.3 is CLOSED, committed and pushed.

**NEXT LOOP: re-read the `## Current Priority` banner (it wins over this
note). It parks M-NIGHTLY below M0127, so the banner selects the next
unchecked M0127 item — `M0127-P5.3a` (phase 2, bushy joins, PG-verbatim:
k-loop to the halfway point, clauseless-rel skip `joinrels.c:170-172`,
mirror-half `first_rel` rule `:174-177`, `have_relevant_joinclause` pair
gate `:190-191`). IMPLEMENTATION-TODO P5.3a; 03 §4.3.
Bar: UNITS + pair-count verification test against 03 §7's arithmetic.**

Carry-over facts a next loop should not re-derive:

- **P5.3a's insertion point is already marked** in `joinsearchlevel.go`
  (`joinSearchOneLevel`, between the phase-1 loop and the phase-3 block).
  It MUST go there: phase 3's "level came up empty" test has to see the
  bushy pairs or it forces cartesians for an already-populated level.
- **The whole P5.3 substrate exists**: `joinSearch` (level driver +
  per-level `setCheapest`), `joinSearchOneLevel`, `makeRelsByClauseJoins`,
  `makeRelsByClauselessJoins`, `makeJoinRel`, and the `joinRelBuilder`
  seam (`sizeJoinRel` = P5.6, `addPaths` = P5.4).
- **`TestJoinSearchFourRelChainIsLeftDeepOnly` asserts the bushy pair
  ({a,b},{c,d}) is NOT offered** — P5.3a must flip that assertion, and it
  is the test that proves phase 2 actually arrived.
- **Test idiom for P5.3a**: `jslCtx(t, n)` + `jslClauses(relsets...)` +
  `recordingBuilder` in `joinsearchlevel_test.go`; assert on the recorded
  `(joinrel, outer, inner)` sequence, never on a cost.
- **PG's branch is per OLD REL, not per pair** (`joinrels.c:96`); the
  level-2 `first_rel` offset applies to the CLAUSE branch only. 03 §4.1's
  pseudocode moves it; do not "fix" the code to match the doc.
- **`joinOrderRestricted` / `hasJoinRestriction` are constant false in v1**
  (03 §4.4); phase 2's pair gate keeps the disjunct anyway.
- **`levelRels` returns the LIVE slice** — phase 2 indexes into it for the
  mirror-half rule; never reorder a level list.
- **P4.1's ledger row #3 is STILL OPEN**: `mergeJoinStream.bufferGroup`
  keeps its hand-rolled twin of `materialBuffer`.
- **NL inner work_mem bound stays OFF** (`GOOPG_NL_MATERIALIZE_WORK_MEM=1`);
  the flip needs `cost_rescan` in `costInnerNestLoop` (`joincost.go:115`) = P5.7.
- **Repo gofmt baseline is go1.25; local gofmt is 1.26** — never wholesale `-w`.
- **Do NOT `git stash`** in this tree (9+ unrelated entries).
- **Bundle discipline:** `docs/design/leftdeep-joins/**` is touched only at its
  IMPLEMENTATION-TODO checkboxes (or an oracle correction, as at P5.2).
  Tracker = `docs/design/0127-pg-shaped-join-search.md` §6.
- **Gate recipes** — PLAN: `bench/tpch/setup_goopg.sh` (no --reset), then
  `PATH=$PWD/postgres/local_install/bin:$PATH make plan-gate`, then
  `bench/tpch/stop_goopg.sh`. SPOT: `scripts/tpch-spotcheck.sh` (own server;
  stop the bench server first). Nightly lane was idle at 06:32 JST.

Gates run this loop: UNITS PASS; PLAN 22/22 MATCH vs `m0127-p21-hashkeys`
(structural, live 65433); SPOT PASS (Q12=2, Q13=35); SMOKE via the commit
hook. DS05/REGRESS/RACE not run — the change adds unreferenced files (no
`planSelect` call site, flag OFF), so no plan and no row can move; PLAN's
22/22 and SPOT's anchors are the empirical confirmation.

In-flight: none.
