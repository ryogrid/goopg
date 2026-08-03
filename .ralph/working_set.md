(idle — nothing in flight)

M0127-P5.3a is CLOSED, committed and pushed.

**NEXT LOOP: re-read the `## Current Priority` banner (it wins over this
note). It parks M-NIGHTLY below M0127, so the banner selects the next
unchecked M0127 item — `M0127-P5.4` (`addPathsToJoinrel`): hash both build
sides, NLI+Memoize parameterised paths, merge via pathkeys, NL fallback
(jointype-legal only; FULL-without-usable-clause error contract), qual
placement at the lowest covering level, deterministic tie-break, and 03 §9's
parameterisation discipline. IMPLEMENTATION-TODO P5.4; 03 §5.
Bar: UNITS + SPOT + DS05.**

**P5.4 has a stated PREREQUISITE**: fix_plan line ~4802 marks an M0125 item
"close this BEFORE M0127-P5.4" (P5.4's deterministic tie-break is specified
to build on M0125-0047's fix). Read that item first — it may be already
closed; if not, it is the selection.

Carry-over facts a next loop should not re-derive:

- **The enumerator is now COMPLETE**: `joinSearchOneLevel` runs phases 1, 2
  and 3; every unordered split of a level's relset is reachable, verified
  arithmetically ((3ⁿ−2ⁿ⁺¹+1)/2, n=2..7) by
  `TestJoinSearchPairCountMatchesClosedForm`.
- **P5.4's two seams are already cut**: `joinRelBuilder.addPaths` (P5.4) and
  `.sizeJoinRel` (P5.6) in `joinsearchlevel.go`. `makeJoinRel` calls
  `addPaths` TWICE per pair, once per outer/inner direction — P5.4 must treat
  its `outer` arg as the driving side and add to `joinrel` via `addPath`.
- **The pair gate ≠ the placement test**: `hasRelevantJoinClause` (overlap
  only) decides enumeration; `clausesFor` (coverage) decides which quals the
  join applies. P5.4 places quals with `clausesFor`.
- **Four ledger rows point AT P5.4** as their resume point: dummy-rel /
  `restriction_is_constant_false` short circuit (P5.3), per-index +
  parameterised base paths (P5.1), EC clause SYNTHESIS
  `generate_join_implied_equalities` (P5.2), `Materialize` as a plan node
  placed by `cost_rescan` (P4.3). P5.4 is where they converge.
- **`joinOrderRestricted` / `hasJoinRestriction` stay constant false in v1**
  (03 §4.4); the clauseless-rel skip in phase 2 is redundant BECAUSE of that
  and is mutation-confirmed unobservable — do not "simplify" it away.
- **`levelRels` returns the LIVE slice** — safe in phase 2 only because
  `makeJoinRel` appends at `lev` and phase 2 reads levels below it.
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
  stop the bench server first). DS05: `scripts/tpcds-sf05-regression.sh sweep`
  (~1 h, goopg-only).

Gates run this loop: UNITS PASS; build + `go vet` + gofmt clean; SMOKE via the
commit hook. PLAN/SPOT/DS05 not run — the change adds no `planSelect` call site
and `GOOPG_PGSHAPED_DP` is OFF, so no plan and no row can move; P5.3's PLAN
22/22 and SPOT anchors stand for the same files. Four mutations checked (three
bite, the fourth documents the clauseless skip's v1 redundancy).

In-flight: none.
