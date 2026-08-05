(idle — nothing in flight)

Last loop: **M0127-P5.9-b** — LANDED, gates green (UNITS + SPOT), committed + pushed.
Facts the next loop must NOT re-derive:

1. `internal/planner/joinsearchseam.go` is new and is the ONLY production
   reader of `GOOPG_PGSHAPED_DP`: `tryPGShapedJoinSearch` (entered from the
   FIRST line of `tryBushyDP`, so both call sites reach it), `searchConsumes`,
   `chainCarriesLateral`, `searchTupleFraction` / `limitParseConst`.
2. `resolveContext.tupleFraction` is new, set in `planSelect` right after the
   WHERE `*Filter` is built, from the UNRESOLVED `s.Limit`/`s.Offset` — the
   `*Limit` node is built ~350 lines later and resolving early would plan a
   `LIMIT (SELECT …)` subquery twice.
3. Residual rule: consumed ⟺ `buildRestrictInfos([]Expr{c},0,cum)` emits a
   clause whose `.clause == c` (pointer). Never re-derive it — an OR-of-ANDs
   reaches two relations but the producer emits the equalities COMMON to its
   branches, so the OR must stay in the `Filter`. `cum` is the FULL
   per-FROM-item space, not the per-joinlist-item one.
4. Locals go into the LEAF before the search (`Filter{LeafLocal:true}`), never
   onto the tree after it — `attachRelationLocalFilters` matches by POINTER
   identity and P5.5-c's index arm rebuilds leaves. Needed `leafBaseScan` in
   `initialRelRows` so a filter-wrapped base table is not re-estimated by
   `EstimateRows`.
5. 08 §3 is now FULLY enforced: 7 `isSearchedTree` skips — 3 renumbering
   (P5.5-f-ii-a) + 4 rewriting (`pushOneConjunct`, `walkRewriteNLI`,
   `rewriteMultiWayChain`, `rewriteScanInputsWithSingleTablePredicates`).
6. The seam DECLINES explicit-`JOIN` FROM items in BOTH collapse regimes (one
   node, several bindings; the `ON` quals are not in the WHERE `Filter`), so
   09 §3's collapse-ON arm currently measures the SAME population as
   collapse-OFF. Ledgered — read that row before running the acceptance bar.
7. Flag-on probe (informational, `GOOPG_PGSHAPED_DP=1 go test ./internal/planner/`):
   19 failures, all plan-shape assertions on STATISTICS-FREE fixtures — with
   1 row per side the cost model correctly prefers a nested loop where the
   legacy pipeline always built a hash join. Not a defect; it is why the seam
   applies `relSizeFallbackRows` tier 3 (`estimateBaseRelInfo` lacks it).

Gates run: UNITS green (`/tmp/units-p59b.log`, exit 0, zero FAIL lines);
SPOT green (`/tmp/spot-p59b.log`, Q12 2 rows / Q13 35 rows, RESULT=PASS);
planner package green; gofmt clean on the new files (the two pre-existing
gofmt-version diffs in `mhj_input_rewrite.go`/`planner.go` predate HEAD);
commit-hook pgbench smoke. No orphaned servers.

Nightly triage 20260805-014309: unchanged, both items already filed under
M-NIGHTLY (fix_plan lines ~1097/1203/1215), left unchecked per the banner.

Next step: **M0127-P5.9** — the S5 acceptance run. 09 §3 bar with collapse OFF
then ON + plan-shape ratchet baseline (§4) + estimate audit (§5), filed as
`analysis/leftdeep-joins/…-s5-acceptance.txt`; then flip `GOOPG_PGSHAPED_DP`
ON and retire `GOOPG_COST_DRIVEN_JOINORDER`, or record the documented no-go.
Read ledger row 6 above first — the collapse-ON arm may not be measurable yet.

In-flight: none.
