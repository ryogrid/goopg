# R47 slice 2 — ordered loop over grouping survivors (implementation plan)

*Companion to `DESIGN.md` rev 3 (APPROVED-WITH-NOTES — the approval
covers this slice; what follows is the code-level mapping of handover
§3, written 2026-09-10 from live-tree study, agent-reviewed before
the implementation commit). No predicted flip, no invented pick rule:
safe measurement. If Q4 (or anything) flips toward PG → stretch,
recorded. If nothing flips → the §3.3 census must eliminate ≥1 of
the three named hypotheses. Either outcome is a pass; inventing a
rule is the only fail.*

## 0. Grounded facts (all verified on the live tree this session)

- `buildAggregateStage` returns `(aggNode, outputCtx, surface, having,
  nil)` with `surface.node == aggNode`, same pointer
  (`planner.go:8123-8145`). At the ORDER BY arm, `agg` is that
  surface, so `agg != nil && node == agg.node` holds exactly when no
  HAVING filter / window stage / min-max wrap / ProjectSet has
  re-wrapped the node — otherwise the loop declines pre-mutation.
  Q4 (no HAVING) satisfies it.
- Grouping runs on the same function-local `upper` registry
  (`planner.go:1749`), so the loop re-fetches the identical rel via
  `fetchUpperRel(upper, UpperGroupAgg, 0, orderTupleFraction)`
  (find-or-create by `(kind, relids)`) and reads `rel.Pathlist` —
  the `add_path` survivors. That IS the slice-1 exposure; no new
  registry field.
- Candidate anatomy (`groupingpaths.go:308-411`): `PathAgg`
  with `AggStrategy`, per-candidate `Agg` spec clone (slice 1),
  `Children[0]` = input path, `Pathkeys` = input-coord sort order
  (sorted) or empty (hashed). The sorted variant's child is a
  **PathSort `*Path*** (`sortPathForBounded`, `joinpathsmerge.go:
  475-504`), never a `*Sort` node — losers stay unbuilt. Handover
  "child must be Sort" maps to `Children[0].Kind == PathSort`.
  Direction/nulls ride on `PathKey.SortAsc/NullsFirst`
  (`pathkeysForSortKeys`, `pathkeys.go:82-91`). The index variant's
  child is a `PathPrebuilt` seed → declines via the child-kind
  check (plus an explicit `GroupKeyOrder != nil` decline).
- Output layout contract (`plan.go:1310-1359` + fill sites):
  output = [group cols (`len(GroupExprs)`) | agg cols | grouping
  cols | passthrough after]. `GroupExprs` is NEVER reordered
  (index variant carries a narrowed clone + EXPLAIN-only
  `GroupKeyOrder`). Group output columns carry a ZERO-VALUED
  `SourceTableIdx` ("unknown/derived", `planner.go:~7822` —
  `ColumnRef` HAS the field, `plan.go:429-436`); group-hit ORDER BY
  keys from `resolveExprAfterAggregate` likewise carry zero (only
  `Index` + output `Name`). Hence
  the handover's "(Name, SourceTableIdx) map" is implemented as a
  **name-verified positional prefix**: require
  `outCols[j].Name == groupExprName(groups[j])` for all `j`
  instead of assuming the prefix. Uniqueness falls out
  positionally; duplicate group names (`GROUP BY a, a`) stay
  harmless duplicate pathkeys.
- `addOrderedPaths(ordered, input, sortPathkeys, cp, limitTuples)`
  (`upperordered.go:122-127`): no-sort iff
  `pathkeysContainedIn(input.Pathkeys, sortPathkeys)` — the input
  pathkeys MUST be output-coord, so each candidate is shallow-copied
  with translated pathkeys (nil when untranslatable → Sort-only,
  PG's cheapest-input arm). `sortPathForBounded` inherits the
  input's `DisabledNodes` via `disabledNodesFor` (`path.go:
  1193-1204`) — hashagg-off / sort-off markers survive stacking
  (verified; flip #2 reproducibility intact).
- `sizeUpperRelFromNode(ordered, aggNode)` charges exactly what the
  normal `createOrderedPaths` call would (`upperordered.go:75-78`
  sizes from the same `input` node) — loop-elected and loop-declined
  paths size identically. All mutation happens AFTER the pure gates;
  decline leaves the ORDERED rel pristine for the normal call.
- `createPlanNode(best)` on the winner yields `*Sort` (PathSort
  over a candidate) or `*Aggregate` (no-sort candidate offered
  as-is, priced as the agg itself). The existing
  `if srt, ok := node.(*Sort)` stamp code is reused untouched;
  loop-elects ⇒ the normal `createOrderedPaths` call is skipped.
- Slice-1 TDD pins (`upperordered_test.go:299-365`) already encode
  the gap: translated pathkeys elect no-sort; input-coord pathkeys
  never satisfy. The translation helper turns the second test's
  hand-built input-coord keys into the first test's output-coord
  keys for the group-keys Sort variant. `TestOrderedDropsHashedSort
  ForPresortedGrouping` uses Q4's live numbers (70122 vs 69911) and
  elects the no-sort sorted path — NOT hashed+Sort (the 1.01 fuzz
  band makes it a near-tie and the election falls through to
  pathkeys/tie-break, which the translated candidate wins). Stated
  prediction, sharpened on review: the GROUPING election still
  picks hashed (unchanged — no invented pick rule), but the
  ORDERED election is EXPECTED to flip Q4 to no-sort sorted, i.e.
  toward PG's default-arm choice, IF production numbers match the
  pin's. A flip is stretch-achieved, recorded; no flip means
  production diverged from the pin and the census must say where
  (which candidates survive where is the deliverable either way).

## 1. Translation helper

`func groupingEmissionPathkeys(aggNode *Aggregate, cand *Path) []PathKey`
(new file `internal/optimizer/upperorderedgrouping.go`):

1. `cand == nil || cand.Kind != PathAgg` → nil. Guard mirrors the
   executor (`operators_join_agg.go:2222`): `cand.AggStrategy ==
   Sorted && cand.Agg.GroupingSets == nil &&
   len(cand.Agg.GroupExprs) > 0 && cand.Agg.Mode == Simple`,
   plus `cand.Agg.GroupKeyOrder == nil` (index variant). Else nil.
2. `len(cand.Children) == 0 || cand.Children[0].Kind != PathSort`
   → nil (index/plain seeds are `PathPrebuilt`).
3. Leading run: for `j` over `GroupExprs`: require `*ColumnRef`
   ("bare group keys") and `j < len(childPK)` and
   `exprEqual(childPK[j].Expr, groups[j])` (both input-coord, so
   positional `Index` equality is the match). First failure stops
   the run; require run length `== len(groups)` (extras after a
   full group prefix cannot change group-emergence order, so they
   are allowed but the group-keys variant has none). Else nil.
   Presorted variants fall out here unless group-prefixed — in
   which case translation is still semantically correct (emergence
   order = group-prefix order), so accepting them is sound.
4. Output prefix verification: `outCols := aggNode.Output()`;
   require `len(outCols) >= len(groups)` and
   `outCols[j].Name == groupExprName(groups[j])` for all `j`.
   Else nil (never assume the layout).
5. Emit: `PathKey{Expr: &ColumnRef{Index: j, Name: outCols[j].Name,
   Type: outCols[j].Type}, SortAsc: childPK[j].SortAsc,
   NullsFirst: childPK[j].NullsFirst}` per `j`.

Pure function of (tree node, unbuilt path); no stamps, no registry.

## 2. Ordered loop (planSelect normal ORDER BY arm only)

Insert immediately before the `createOrderedPaths` call
(`planner.go:1940`, arm starts `:1882`), inside the existing
`len(s.OrderBy) > 0 && (selectSrfPending == nil || selectSrfPreSort)`
arm, gated pre-mutation on ALL of:

- `selectSrfPending == nil` (normal arm only; SRF pre/post-sort
  excluded even though the outer condition admits pre-sort),
- `agg != nil && node == agg.node` (pointer equality; HAVING,
  window, min-max wrap `:1712`, ProjectSet decline automatically),
- grouping rel `fetchUpperRel(upper, UpperGroupAgg, 0,
  orderTupleFraction)` holds ≥2 `PathAgg` and 0 `PathFinalizeAgg`.
  The Finalize half is load-bearing for CORRECTNESS, not just
  churn-avoidance: with a parallel split present the loop's
  serial-only ORDERED rel could elect a serial plan the grouping
  rel would have lost to `Finalize` under parallel knobs — so any
  `PathFinalizeAgg` declines. Single candidate → today's shape
  already optimal,
- at least one candidate translates (else the loop can only
  reproduce Sort-over-winner — still correct but pure churn;
  decline to keep the tree movement-minimal... *decision recorded:
  decline, because a Sort-only loop re-prices but cannot change the
  election versus the normal call*).

Then (mutation): `ordered := fetchUpperRel(upper, UpperOrdered, 0,
orderTupleFraction)`; `sizeUpperRelFromNode(ordered, agg.node)`
(unconditional overwrite — idempotent on the same input, no
snapshot needed for sizing); snapshot `ordered.Pathlist` +
cheapest fields (`CheapestTotal`, `CheapestStartup`, plus
`CheapestParam` if the struct carries one — read the struct at
implementation) BEFORE the per-candidate adds; per `PathAgg`
candidate: shallow copy, `.Pathkeys =
groupingEmissionPathkeys(agg.node, cand)` (nil-tolerant —
untranslatable takes the stack-Sort arm via
`pathkeysContainedIn(nil, req) == false`),
`addOrderedPaths(ordered, &copy, pathkeysForSortKeys(keys),
plannerSet.costParams(), orderLimitTuples)` — with the invariant
stated at the call site that `keys` is non-empty here (the arm
guarantees `len(s.OrderBy) > 0`; `createSortPlan` panics on empty
`Pathkeys`, `createplansimple.go:411+`, so a future empty-`keys`
caller must not reuse this call shape blindly);
`setCheapest(ordered)`;
`best := getCheapestFractionalPath(ordered, orderTupleFraction)`
(nil → restore + decline, fall through); `built, _ :=
createPlanNode(best)`; copy back descending through `*Sort` only:

- `*Sort` with `*Aggregate` child → `*agg.node = *builtAgg`;
  re-run `stampAggregateInputTarget(agg.node, nil)` (keys-only,
  strategy changed under it; the B-01c above-aware re-stamp before
  return covers the rest);
- bare `*Aggregate` → same copy-back + re-stamp;
- anything else → restore + decline, fall through to the normal
  call on the byte-identical pristine rel (decline-after-mutation
  without restore would leave priced, valid loop candidates for
  the normal call to re-adjudicate — a loop candidate could win
  the post-decline election, making "decline" an EXTRA-flip
  vector; restore closes it — review recommendation adopted).

On elect: `node = built` (no early return — control MUST flow
through the shared `if srt, ok := node.(*Sort)` block at
`:1941-1947` with the outer `*Sort` landing in `orderSort`, or the
B-01c above-aware re-stamp misses it), `srt.pos` fix-up mirrors
the normal call (`upperordered.go:107-112` positions at the
statement); the normal `createOrderedPaths` call is skipped for
this arm.

## 3. Tests (TDD pins first, green before the loop lands)

In `upperordered_test.go` (same hand-built style as `:299-365`):

- translated group-keys Sort child elects no-sort vs hashed+Sort
  (Q4 numbers) — the post-slice-2 counterpart of
  `TestOrderedStacksSortWithoutTranslatedPathkeys`;
- hashed candidate → nil; `PathPrebuilt` child → nil;
  `GroupingSets != nil` → nil; empty groups → nil;
  non-`Simple` mode → nil; `GroupKeyOrder != nil` → nil;
  non-ColumnRef group expr → nil; run shorter than groups → nil;
  name-mismatched output prefix → nil; direction/nulls carried
  from child `PathKey`s (descending group-keys Sort → `SortAsc:
  false` emitted);
- comparator-parity pins still missing per DESIGN §3.2 (fuzz
  bands 1.01, `considerPathStartupCost` gating stated,
  prefix-pathkeys dim set, DisabledNodes trump, M0129-S1
  direction): audit which exist, add the missing ones;
- post-decline byte-identity: ORDERED `Pathlist` length +
  cheapest fields identical after a declining loop (pins the
  snapshot/restore — without it the §2 recommendation is
  unverified); loop-level decline with a `GroupKeyOrder`-carrying
  index candidate on the rel (helper-level nil pins exist; the
  rel-level "≥1 translates" gate is what must decline);
- census (diagnostic, both magnitudes rows 57066/13628, widths
  448/64): grouping survivors, ordered survivors, final pick —
  RECORDED via the existing test-harness style, not asserted
  (either election outcome passes; a flip toward PG is stretch).

## 4. Gates (DESIGN §2 slice-2 gate, unchanged)

Byte-guard A/B both corpora (sections MAY move — that is the
measurement), per-query census with ZERO EXTRA flips required,
shape-delta reported, units + suites + TPC-H spotcheck + SF0.5
sweep `MISMATCH=0`-class, Memoize-node census Q4 before/after,
inode-verified serving binary (K91), estimate re-baseline IF Q4's
shape moves (EA-ratchet/c13a). `match` count is NOT a criterion.
