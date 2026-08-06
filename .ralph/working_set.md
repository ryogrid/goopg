(idle — nothing in flight)

Last loop: **M0127 S7 gate — the confirming nightly RAN, the wedge is gone, and
the last two items were ONE wrong-answer bug, now fixed.**

Nightly `20260806-232940` at `dffb05be` (68 min): **every stage PASS** —
preflight/units/race/**testport**/pgbench/tpch/tpcds — first `testport` pass in
ten runs, **no `suite-wedge` item**. That confirms root-0040 (stranded page
latch) and its 8 casualties. Action items **10 → 2**.

The 2 survivors (`regress/portals_p2`, `regress/select`) were labelled "output
mismatch; normalization rules need extension" and **pass standalone** — which
three earlier loops read as a dead end. It was the clue: `onek2`'s partial
indexes are created by `create_index`, so only a full-suite pass has them.
Root cause: **goopg chose partial indexes without proving the predicate from
the quals.** `onek2 WHERE unique1 = 50` took `Index Only Scan using
onek2_u1_prtl` (predicate `unique1 < 20 OR unique1 > 980`) → **0 rows where 1
exists**; reproduced live, with controls proving scan + index maintenance are
sound (selection was the fault). PG proves it in `check_index_predicates()`
(`predicate_implied_by()` → `predOK`). goopg already declined partial indexes
in `addOneOrderedIndexPath` but never mirrored it — `HasPredicate` had ZERO
readers on the main scan path (sibling-path failure).

Fix: decline `idx.HasPredicate` at `findBTreeIndexForColumn` (covers plain path
+ all 3 `mhj_input_rewrite.go` sites) and `pickIndexCoveringAllLeadingColumns`
(parameterized path). `continue`, not `return nil`.

Files: `internal/planner/{planner.go,nl_index_join.go,partial_index_predicate_test.go}`;
`docs/design/root-0041-partial-index-predicate-guard.md` + README index;
fix_plan (wedge item CLOSED, S7 amendment, 3 items); 3 ledger rows;
`analysis/m0127-s7-regress/order-dep-20260807/`.

Gates run: new guards PASS + non-vacuous (neutering both guards fails them);
full `TestPort_RegressSuite` in suite order PASS 197 s with both cases fixed
(diverging 89 → 87, `hash_index` also recovered); units PASS;
`tpch-spotcheck` PASS (Q12=2, Q13=35); pgbench smoke via hook.

NEXT LOOP (banner: M0124/M0125 closed → **M0127** → M-NIGHTLY → M0123).
**Run `make nightly-batch`. If `status: pass`, M0127-P6.1 is selectable** — no
known blocker remains. One chance of a false red, filed as its own item:
`regress/truncate` is nondeterministic (FK `DETAIL:` ordering, 1 of 3 runs); a
nightly returning ONLY that item is the flake, not a regression. Also filed:
the nightly never sets `GOOPG_REGRESS_DIFF_DIR`, so regress divergences arrive
without evidence (`ci/batch/stages/stage-testport.sh`) — cheap, and it is what
cost three loops here.

In-flight: none.
