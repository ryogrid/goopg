# C-09 (P3-08) — `reduce_unique_semijoins` — DECLINED with verification

Status: declined 2026-09-06 (see §6). Implements TODO_ALL.md C-09
(marked `[-]` with ledger row). This document is kept as the decline
record: the hazard survey (§2) stands and protects future workers near
SEMI code; the reduction is what is declined.

## 1. Objective

Port PG's `reduce_unique_semijoins` (analyzejoins.c:844): a SEMI join
whose inner is a single baserel provably unique for the join clauses
becomes a plain INNER join. In PG the change is deleting the SEMI's SJI
from `join_info_list` (jointree type left alone — never consulted
again). goopg has no jointree SJI for SEMI (parser never produces it;
SEMI plan nodes are born in `unnest.go`), so the operation is on PLAN
nodes: flip `Join.Type` SEMI→INNER with merged schema. Gate: take3 09
§5 P3 (PP + values-diff — this moves plans AND execution by design).

## 2. The hazard (ledger 794, surveyed 2026-09-06)

SEMI `Output()` is left-only, live-derived (`plan.go:1189-1196`); the
cached `schema` is left-only at birth (`unnest.go:3120,3254,4307`).
Right-side keys/predicates live in MERGED coordinates (producer shifts
at unnest: `unnest.go:3971-3974,4234-4236,3077-3083`; consumer rebinds
`joinlayout.go:1232-1314`). Flipping to INNER widens `Output()` to
merged, and every by-name rebinder above (`predRebind` miss-fallback
to the other side, `reresolveExprByName` against widened child
`Output()`) can collapse a right-side ref onto a same-named left
column — documented silent wrong answers (Q21 NOT-EXISTS 0 rows;
Q18 SUM moved past outer width). SIX coordinated sites maintain the
left-only invariant (`joinlayout.go:225,867,1072,1120,1214`;
`planner.go:2514`) — a flip must satisfy all six, not just `Output()`.

## 6. Decline verification (binding — read before re-proposing)

C-09 is declined under TODO_ALL SKIP policy #1 (no measured performance
gain), verified structurally — an A/B is impossible without the
implementation, and the implementation's risk (below) exceeds any
bounded gain. All three value channels PG's reduction serves are closed
in goopg's architecture:

1. **Join-order freedom: UNAVAILABLE.** PG deletes the SJI so the DP
   reorders the semi as inner. goopg's SEMI links are pinned OUTSIDE
   the search by design (`runJoinSearchBelowPinned`, predp spine;
   C-04 explicitly keeps SEMI there). No reordering can result, before
   or after C-04.
2. **Estimates: already unique-aware (mechanism, not assertion).**
   `semiJoinMatchFraction`/`semiPairMatchFraction`
   (`cardinality.go:728+`, M0127-P5.6-f/g) consume ndistinct/MCV
   uniqueness evidence incl. the MCV arm and nullfrac — the same
   evidence a reduction would unlock. For unique-inner the INNER
   product and the SEMI fraction agree by construction (each outer
   matches ≤1); ledger line 794 records it measured-inert on Q18.
   Re-deriving the same number through a plan rewrite buys nothing.
3. **Execution: bounded by early-break.** The SEMI probe loop breaks
   on first match (`operators_join_agg.go:1780+`); unique inner
   bounds it to ~1 candidate. The remaining overhead (a flag + break
   per probe) is immeasurable against the ±17% sweep band — and
   measuring it would require building the reduction first (circular).
   The NLI-on-SEMI prize (index probe on unique inner — C-20c's
   resume condition) belongs to P6-04 NLI work, not to this item.

Risk side (review-proven, 10 findings): unnest-time reduction does NOT
avoid re-indexing (later by-name passes re-observe width; isolation
guards are type-dispatched so born-INNER loses Right-scope
protection); ANTI must be gated separately (negation/NullAware);
spine/NLI/post-unnest interactions unanalyzed in the original draft.
A correct implementation needs all of §3–§5's machinery for the three
closed channels above. Do not re-propose without (a) SEMI entering
the search (architecture change), or (b) a measured exec win with the
reduction already built.

## 3. Route analysis (SUPERSEDED by §6 — kept for the hazard record)

The unnest-time route argued here was refuted in review (later
by-name passes re-observe width; isolation guards are
type-dispatched). Retained because §2's hazard survey and the
re-indexing mechanism analysis are correct and load-bearing for any
future work touching SEMI plan nodes — read §2 + §6, treat the rest
of §3–§5 as the investigated-and-rejected alternative.

Two routes exist. Post-pass reduction (walk a planned tree flipping
SEMIs) inherits the full hazard: every ancestor ref/width above each
flipped join must be re-derived, which is exactly the bug class §2
records. Unnest-time reduction avoids it structurally:
`unnestExistsExpr` (`unnest.go:4025`) builds the SEMI node directly —
decide THERE, before any ancestor in the walked tree observes it, and
build the node INNER-shaped (merged schema at birth) when the proof
holds. No ancestor re-indexing can exist for a shape no ancestor has
seen. `planner.go:1333,1404` runs unnesting on the join subtree before
upper nodes are built, so parents see final widths.

Conditions (ALL must hold, else keep SEMI — decline-biased like every
primitive here):
1. Inner side is a single base-relation scan (leaf `SeqScan`/
   `IndexScan`/`IndexOnlyScan` — not a CTE/subquery/aggregate; matches
   PG's `bms_get_singleton_member(min_righthand)` + `find_base_rel`).
2. Uniqueness: the join-key columns are covered by
   `uniqueKeyColumnSets(cat, tbl)` (`joinkeyproof.go:56` — the
   base-relation evidence already in tree, planner's-own-catalog).
   Composite keys accepted (superset of PG's single-column
   `has_unique_index`, matching goopg's FK-substitution precedent).
   EC-derived clauses (`generate_join_implied_equalities` analogue:
   `inferEquivClassConstants`, constants-only) count as key columns.
3. The SEMI shape is the plain one: equi-keys + conjunctive residual
   only (no OR/volatile/sublink in the residual — `innerJoinPushTarget`
   already proves this vocabulary decidable; reuse its verdict, do not
   re-derive).
4. `joinFilterRemoved`-style counters: none attached yet (reduction
   runs before the executor observes the node — vacuously true at
   unnest time; stated so a future reordering re-checks it).

Consequence to record (not a problem): the flipped INNER join keeps
the SEMI's *keys* (LeftKey/RightKey already merged-space) and
*predicate*; only `Type` + `schema` change. `NullAware`/emit-once
state never exists yet at unnest time.

## 4. Out of scope (ledgered, not forgotten)

- `reduce_outer_joins`-style LEFT→INNER via strict quals (exists as
  `reduce_outer_joins.go`; untouched).
- Non-equi/OR residuals (condition 3 declines; PG handles them via
  full EC machinery goopg lacks).
- `CommuteAbove/Below` or SJI bookkeeping: no SJI exists for these
  joins (C-01), so there is nothing to delete — the plan node IS the
  record. C-03's `joinIsLegal` never sees them (SEMI never reaches
  deconstruction); no interaction.
- Unique-ified RHS + `uniqueKeyColumnSets` staleness across DDL:
  evidence read at plan time from the planner's catalog (same trust
  root as costing).

## 5. Gate

- Unit: flip matrix (unique-covered → INNER merged schema; every
  decline condition → SEMI untouched) + schema-width pins (flipped
  node `Output()` == left++right; ancestors unaffected by
  construction) + key/predicate identity (same pointers, only Type
  changes).
- Suites + units scope green.
- PP both suites (Q4/Q22-class EXISTS shapes are the movers —
  expect SHAPE moves to INNER; report with cost roll-up) +
  values-diff both suites (R8 — SEMI→INNER re-executes the join;
  TPC-DS Q18/Q4/Q22 checksums are the tripwires) + spotcheck.
- Timing arm (unique-inner SEMI→INNER removes the emit-once +
  inner-rewind overhead; measure, do not claim).
