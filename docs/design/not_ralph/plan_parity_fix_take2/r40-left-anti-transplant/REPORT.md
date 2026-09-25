# R40 — the LEFT→ANTI transplant landed, plus a latent wrong-rows bug it uncovered

*2026-09-09. Completes K30's deferral from R27 §4a. All gates green.
Read `DESIGN.md` first; §8 there records the pre-implementation review.*

## 1. Result

| | before | after | PG 18.3 |
|---|---|---|---|
| `outer-link-no-sjinfo` declines | 3 | **0** | — |
| `leaf-count` declines | 0 | **3** | — |
| TPC-DS declines, total | 8 | **8** | — |
| Q78 join TYPE (×3 CTEs) | `Hash Left Join` + `Filter: … IS NULL` | **`Hash Anti Join`, qual dropped** | `Merge Anti Join`, qual dropped |
| Q78 runtime | 16 s | **14 s** | — |
| TPC-DS match | 0/99 | 0/99 | — |
| TPC-H match | 1/22 | 1/22 | — |

**Read this honestly: the round did NOT increase the match count, and the
decline total did not move.** What it did is (a) fix a latent bug that
would have shipped wrong rows, and (b) make Q78's join TYPE PG-faithful,
converting one decline class into a different, later one. §4 states the
new blocker precisely.

## 2. The latent bug — the round's most important outcome (K71)

goopg's S9.3 LEFT→ANTI rule compared **table names**. PG uses **two
different granularities on purpose** (`prepjointree.c:3340-3403`):

- LEFT→INNER: `bms_overlap(nonnullable_rels, …)` — `find_nonnullable_RELS`,
  **relation** granularity;
- LEFT→ANTI: `mbms_overlap_sets(nonnullable_vars, forced_null_vars)` —
  `find_nonnullable_VARS`, **column** granularity.

goopg used relation granularity for both, so the ANTI rule **over-fired**:

```sql
a LEFT JOIN b ON a.id = b.id WHERE b.y IS NULL
```

The ON is strict for `b.id`; the WHERE forces `b.y`. Different columns, so
PG keeps the LEFT join — oracle-verified on 18.3: `Merge Left Join` +
`Filter: (b.y IS NULL)`. goopg demoted it to ANTI.

This was invisible while no ANTI verdict reached a plan (exactly K30's
reason for declining the transplant), but it was **already wrong in
`ctx.joinInfoList`**, which `reduceOuterJoins` populates today. The
transplant made it reachable, and it surfaced immediately as
`TestNonSpineOuterAdmissionValues` failing to resolve a nullable-side
column that an ANTI join no longer publishes.

Measured on the SF0.5 data, the two shapes are genuinely different
queries, and both now match PG exactly:

| WHERE | goopg | PG 18.3 | converts? |
|---|---|---|---|
| `wr_order_number IS NULL` (same col as ON) | 323 532 | 323 532 | yes → Anti |
| `wr_return_amt IS NULL` (different col) | 325 179 | 325 179 | no → stays outer |

The old rule would have answered the second with the first's plan.

## 3. What landed

1. **Column-granular S9.3** (`reduce_outer_joins.go`): `find_nonnullable_vars`
   / `find_forced_null_vars` twins at `"table\x00column"` granularity, and
   `antiForcingColumns` = PG's `mbms_overlap_sets` restricted to the join's
   right relids. The relation-level sets stay for the INNER rule, which is
   what PG uses there.
2. **`demotedForPlan` transplants ANTI** and returns the forcing column
   keys (not table names — one column of a table may force the conversion
   while others keep filtering).
3. **`mapJoinType` learns `parser.JoinAnti`** — it silently fell to
   `JoinTypeInner` before, which would have been wrong rows, not a no-op.
   `JoinSemi` deliberately not added: `applyDemotion` never emits one.
4. **`stripForcingNullQuals`** drops exactly the certified conjuncts from a
   LOCAL copy of the WHERE, mirroring `collectForcedNullColumnKeys`'
   top-level-AND-only descent. `s.Where` is never mutated. PG drops the same
   clause for the same stated reason ("must be removed to prevent bogus
   selectivity calculations").
5. **Per-item schema narrowing** (`planFromItem`, DESIGN §4d — added by
   review): `jn.schema` and the loop-carried `leftCtx` stay left-only for
   Semi/Anti, so a join chained after the ANTI one in the same `FromExpr`
   (Q78's `… JOIN date_dim`) computes offsets against the real width.
   Mirrors `unnest.go`'s existing construction.
6. **`Join.FromOuterReduction`** + an NLI decline for it — see §5.

## 4. The new blocker (K72), stated precisely

The 3 declines became `leaf-count` (`joinsearchseam.go`), all
`nrels=2 nleaves=2`. The plan tree now yields 2 search leaves where the
joinlist counts 3 FROM items: an ANTI join's right side is not a search
leaf (it is a pinned sub-problem), but it is not a top-of-tree spine
either — Q78's ANTI sits at the BOTTOM of its chain with an INNER
`date_dim` join above it, so `runJoinSearchBelowPinned` (which expects the
pinned spine at the top) does not apply. That is the shape the next round
must handle, and it is adjacent to K70's `outer-spine` work.

So Q78 remains ineligible, and per K27 an ineligible query cannot match at
any cost setting. The round bought a PG-faithful join type and a
correctness fix, not a match.

## 5. A measurement that reversed a decision (K73)

First attempt at the NLI interaction was WRONG and was caught by
measurement, not review:

`nliCostGateAccepts`' SEMI/ANTI arm charges a B-tree descent as **1 unit**,
the same as a hash probe, so with a unique probe index NLI looks free. It
accepted Q78's 1 439 608-row `store_sales` outer by a hair
(1 439 608 < 1 583 094) and ran the query at **54 s vs 16 s (3.4×)**.

The file's own comment explains why it was ever safe: semi/anti joins came
only from the unnest rewrite, whose *"outer is the small side by
construction"*. The reduction is a second population that breaks it.

**Fix v1 — capping the gate by outer size — was rejected by measurement.**
It also caught unnest-sourced SEMI joins, where the premise fails the OTHER
way: TPC-H Q4's `EXISTS` has a 385 k outer but a **6 M-row `lineitem`
inner**, so NLI is genuinely right there. The cap took Q4 from **1.5 s to
13.1 s (8.6×)**. One threshold cannot serve both populations.

**Fix v2 — `Join.FromOuterReduction`** marks only the joins this round
creates and declines NLI for them. Every existing unnest-sourced decision
is byte-identical (TPC-H plan structure verified identical across all 22),
Q4 is back to `Nested Loop Semi Join` at 1.54 s, and Q78 runs 14 s — 2 s
FASTER than the 16 s pre-R40 baseline. The flag's doc comment records both
measurements and names its own retirement condition: when the gate learns a
real index-descent probe cost (join-METHOD parity, its own round).

## 6. Gates

| gate | result |
|---|---|
| `RALPH_PRECOMMIT_SCOPE=units` | green, 0 failures |
| optimizer + executor suites | green |
| new unit tests | 6 (column granularity both directions, `demotedForPlan` ANTI + INNER, `stripForcingNullQuals` ×4) |
| new VALUES regression | 3-relation chained-after-ANTI case (review-required; a 2-relation case passes even with §4d broken) |
| **TPC-DS SF0.5 sweep** | **PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0** |
| Q78 checksum | `ck=8f67acff3895183f` — unchanged from baseline |
| TPC-DS plan-shape delta vs baseline | 1 changed (Q78, intended); total runtime 800 s → 799 s |
| TPC-H values digest, 22 queries | byte-identical |
| TPC-H plan STRUCTURE, 22 queries | identical (cost/rowest jitter only) |
| decline census | `outer-link-no-sjinfo` 3→0, `leaf-count` 0→3, total 8 |

## 7. Method notes

- The diagnosis came from **instrumenting `outerLinksHaveSJInfos` directly**
  (temporary trace, reverted before commit), not from reading code — R37's
  standing rule after two inferred-and-wrong conclusions. The trace gave the
  exact mismatch (`want jt=1(LEFT) … have jt=6(ANTI)`, same relids) in one
  step.
- **Two decisions this round were reversed by measurement after passing
  review**: the NLI cap (§5) and, in review, the missing schema narrowing
  (DESIGN §4d). Both would have shipped a regression. The adversarial
  review caught one; only the A/B caught the other.
- The 3-relation VALUES case is the review's contribution and earns its
  keep: the 2-relation case passes even with §4d removed, because
  `Join.Output()` masks the stale `.schema` until a later join reads it.
