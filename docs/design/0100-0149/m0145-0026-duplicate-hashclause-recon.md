# M0145-0026 — duplicate hashclause in the path key list (recon)

Status: complete 2026-09-23; the fix landed as M0145-0026a (§Fix landed).
Task: `.ralph/fix_plan.md` M0145-0026 (Kind: recon, Parent: M0145-0007).

## Question

M0145-0007 slice 5 saw 5 ANTI hash joins in TPC-DS Q78 reach lowering with
the key list `[t = r, t = r, item = item]`. Only the late
`fillJoinHashKeys` de-duplicated it, so EXPLAIN hid it. PG builds
hashclauses from a restrictlist that cannot hold the same RestrictInfo
twice (`hash_inner_and_outer`, joinpath.c). Where does the duplicate enter,
and does costing or join selectivity read the duplicated list?

## Apparatus

A temporary probe (saved as `m0145-0026-dupkey-probe.patch`, not committed
as code, gated on `GOOPG_TMP_DUPKEY=1`) prints a structural key for every
hash key pair at several points: path generation (`addHashJoinPath`),
lowering (`createHashJoinPlan`, both the path's restrictinfos and the
lowered `JoinKeyPair`s), just before `fillOneJoinHashKeys`, the input of
`buildRestrictInfos`, and each conjunct segment the seam assembles. It ran
over TPC-DS SF0.25 through the private-clone capture lane
(`scripts/jointree-parity-capture.sh tpcds-sf025`, default pipeline,
EXPLAIN-only).

A plain two-table and three-table unit reproduction of Q78's
`LEFT JOIN … WHERE x IS NULL` arm does **not** duplicate. The duplicate
needs the seam's semi/anti link route that Q78's CTE bodies take. A first,
cruder probe (column name plus node type) also flagged Q47-style
`rn = rn + 1` / `rn = rn - 1` pairs. That was a false positive; the
structural key clears it.

## Findings

1. **Population:** Q78's three arms (`ws`/`wr`, `cs`/`cr`, `ss`/`sr`), each
   an ANTI hash join with `n=3` keys whose first two are the same
   equality: 3 joins, where M0145-0007 counted 5 (its count may include
   the knob arm's lowering). No other query duplicates on the default
   pipeline. The knob
   arm was not probed this time, though M0145-0007 reported the duplicate on
   both arms.
2. **The path holds two different restrictinfos for one clause:**
   `ws_order_number = wr_order_number` and `wr_order_number =
   ws_order_number`. Both are explicit (`inferred=false`) and share one
   equivalence class. They reach `buildRestrictInfos` as two conjuncts of
   `prob.conjuncts`.
3. **Entry point:** the conjunct segment for semi/anti chain links. The
   seam's `extractSearchLeaves` (`joinsearchseam.go:1888`) and
   `extractScopeLeaves` (`:2252`) fold `LeftKey = RightKey` into the link
   predicate. They follow the unnest rewrite's convention
   (`unnestExistsExpr` keeps its primary equijoin out of `Predicate`, design
   doc §28.4). The ANTI joins here are not unnest-built, though. They come
   from the LEFT→ANTI outer-join reduction (`FromOuterReduction`,
   transplanted by `demotedForPlan`) in the rule-based `planFromItem`
   (`planner.go:4467`), which sets `LeftKey`/`RightKey` from the ON
   predicate *and keeps the equality in `Predicate`*. The fold then adds
   the commuted twin.
4. **Selectivity does not double-count.** Both copies share an EC id, and
   `selectivityClauses` → `oneClausePerEquivClass` (joinrelsize.go) keeps
   one member per class.
5. **Cost does double-count.** `addHashJoinPath` passes
   `numHashClauses: len(keys)` (3 instead of 2) to `hashJoinCost`
   (cost_funcs.go:823-853), which charges `cpu_operator_cost ×
   numHashClauses` per inner row at build and per outer row at probe. That
   is PG's `initial_cost_hashjoin` term, fed with one clause too many. For
   Q78's store arm, one extra clause over ~700k probe rows plus the build
   side adds on the order of 0.0025 × 770k ≈ 1900 cost units to every
   hash-anti candidate for this join. Whether the merge-join producer
   sees the duplicate too was not checked. Plan movement from fixing it is
   unmeasured.

## Fix direction (filed as M0145-0026a)

Fold `LeftKey = RightKey` at the two seam sites only when the link's
`Predicate` does not already contain that equality (either orientation).
That makes the restrictlist match PG's single RestrictInfo per clause.
Fixing it at the fold is preferred to changing the legacy producer's
convention: the fold is the one place observed adding a second copy, while
changing what `planFromItem` stores in `Predicate` would touch every
downstream reader of that field. It serves both pipelines, which matches
M0145-0007's both-arms report. Gates: the full
default-arm set plus the fire-set gate, with the Q78 hash-anti costs before
and after as the movement witness.

## Fix landed (M0145-0026a, 2026-09-23)

`semiAntiPredHasKeyEq` (joinsearchseam.go) reports whether an AND-conjunct
of the link predicate is already `LeftKey = RightKey` in either orientation,
using the fail-closed `exprEqual`. Both folds (`extractSearchLeaves`,
`extractScopeLeaves`) now add the key equality only when it is absent.
Unnest-built semi/anti joins, whose `Predicate` excludes the key, fold
exactly as before. Pinned by
`TestExtractSearchLeaves_OuterReductionAntiKeyFoldedOnce` (fails without the
fix: 2 equalities) and `TestSemiAntiPredHasKeyEq`.

Movement, TPC-DS SF0.25, values unchanged, plan shapes unchanged apart from
costs (the plan diff counts Q78 as changed only because costs moved):

| Q78 hash anti join | default arm (startup..total) | knob arm (startup..total) |
|---|---|---|
| web | 1256.80..10452.70 → 1212.00..9958.01 | 824.80..14338.82 → 780.00..12269.52 |
| catalog | 2581.82..20950.74 → 2491.65..19959.59 | 1712.82..28489.16 → 1622.65..24344.53 |
| store | 4885.09..35815.99 → 4705.90..33837.11 | 3167.09..37045.76 → 2987.90..33267.19 |

On the default arm each delta is exactly one clause's
`cpu_operator_cost × (build rows + probe rows)`. For the web join that is
0.0025 × 17 920 = 44.80 at startup and 0.0025 × 197 876 = 494.69 in total.
The knob arm's startup deltas match, and its total deltas are larger. The
knob arm's hash costing charges the clause count through more than the
initial term; that was not decomposed further.
