(idle — nothing in flight)

Last loop: **M0127-P5.7-b** — LANDED, gates green, committed + pushed.
Facts the next loop must NOT re-derive:

1. `internal/planner/tuplefraction.go` is new: `preprocessLimit`
   (`preprocess_limit`, planner.c:2577), `getCheapestFractionalPath`
   (planner.c:6617), `compareFractionalPathCosts` (pathnode.c:127).
   `searchCtx.tupleFraction` carries the number; `searchCtx.finalPath()` is the
   ONLY value a caller may hand `createPlanAtSearchRoot` — reading
   `finalRel().CheapestTotal` discards the fraction.
2. **The finding.** `tuple_fraction` is TWO mechanisms and only the pair moves a
   plan: selection (above) and RETENTION — `RelOptInfo.ConsiderStartup`
   (`consider_startup`, relnode.c:211/707) enforced in
   `comparePathCostsFuzzily`'s two "different" arms (pathnode.c:178-183).
   goopg had neither, so it kept fast-start paths PG prunes AND then selected on
   total cost anyway.
3. Behaviour change to expect in dark-search tests: with no fraction, a path
   that loses on total and wins only on startup is now PRUNED. Three tests
   (joinpaths_test.go, joinpathsmergeouter_test.go ×2) now set
   `joinrel.ConsiderStartup = true` to observe the arm they test; that is the
   fix if a future dark-search test "loses" a path.
4. `buildInitialRels` gained a 5th parameter (`tupleFraction float64`); pass 0
   for "fetch all rows". `newSearchCtx` is unchanged (fraction defaults to 0).
5. **DS05/PLAN not run and that is not a skip** (same structural reason as
   P5.7-a/P5.6-d): every consumer is behind `GOOPG_PGSHAPED_DP` (OFF) and the
   search has no `planSelect` caller at all, so the default arm is
   byte-identical. `comparePathCostsFuzzily`'s only caller chain is
   `comparePaths`→`addToPathlist`→`addPath`, all inside the dark search
   (verified by grep this loop).
6. 3 ledger rows: no `estimate_expression_value` const-fold on the LIMIT expr
   (`LIMIT 5+5` / `LIMIT $1` take the 10% punt); `consider_param_startup`
   unreachable behind 03 §4.4's pin; no production PRODUCER of a fraction yet
   (that arrives with P5.9's `planSelect` wiring).

Gates run: UNITS green (`/tmp/units-p57b.log`, exit 0, zero FAIL lines);
planner package re-ran uncached at 0.60 s before the suite; commit-hook pgbench
smoke. No orphaned servers.

Nightly triage 20260805-014309: unchanged, both items already filed under
M-NIGHTLY (fix_plan lines 1097/1203/1215) and left unchecked per the banner.
No new nightly run since.

Next step: per the banner (M0124 closed → M0125 → M0127), the head of open
M0127 work is **M0127-P5.8** — collapse limits with PG's actual semantics
(03 §6: flat comma lists are ONE problem; limits govern sub-joinlists and
explicit JOINs only; =1 pin semantics), explicit INNER JOIN flattening behind
`GOOPG_PGSHAPED_COLLAPSE`, delete the 12-table bail-out. Bar: UNITS + DS05
(sub-flag OFF and ON).

In-flight: none.
