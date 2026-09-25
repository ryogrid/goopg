# R25 — decompose the NLI node into PG-native operators

*Round of `docs/design/not_ralph/plan_parity_fix_take2/TODO.md`.
Status: DESIGN + self-review (subagent delegation unavailable —
recorded honestly in §7, not elided). Filed per owner direction
2026-09-09: `NestedLoopIndexJoin` exists nowhere in PG 18.3.*

## 0. What is being removed, and what it becomes

Today a parameterized index join plans into ONE fused node,
`NestedLoopIndexJoin{Outer, Inner(IndexScan|IndexOnlyScan|
BitmapHeapScan, +optional InnerMemo), Predicate}` (`plan.go:876`),
built at three sites (`createplannl.go:282,403`,
`nl_index_join.go:786`) and driven by `nestedLoopIndexJoinOp`
(`operators_nljoin.go:41`) — 74 files reference the type. PG builds
TWO nodes for the same shape: `NestLoop` (optionally `Semi`/`Anti`)
over an `IndexScan` whose indexqual references the outer tuple, e.g.
TPC-H Q19/Q20/Q4/Q22 in `bench/tpch/plans-pg/`:

```
->  Nested Loop  (cost=0.43..71973.76 ...)
    ->  Seq Scan on part ...
    ->  Index Scan using lineitem_part_supp_fkidx on lineitem
          Index Cond: (l_partkey = part.p_partkey)
```

(EXPLAIN already renders goopg's NLI as "Nested Loop" + the probe's
own label — `operators_explain.go:2654,2989` — so the rendered SHAPE
is close; the divergence is node identity, costing, whitelists, and
the driver. The round is therefore structural, and its gates measure
structure, not strings.)

After this round the same query plans into `Join{Algo:NestedLoop,
Type, Left: outer, Right: parameterized IndexScan, Predicate}` and
runs on a generic per-row driver. The fused type is deleted.

## 1. Why now (unblocks, not just parity)

- **E-20 Cut 4's deferral names this node**: `*NestedLoopIndexJoin`
  sits in `terminatesPartial`, and a partial path through it has no
  per-worker inner-probe story. Decomposition does not write that
  story, but it deletes the alien whitelist entry and leaves a
  `NestLoop + IndexScan` shape every existing walk already models.
- **K26 §8's NLI shape** (semi joins changing FORM to NLI) becomes a
  `JoinTypeSemi` nestloop with an index inner — a PG-native spelling
  the NLI test branch must learn instead of a second node kind.
- Every generic plan walker (K25's lesson) stops needing an NLI arm.

## 2. The tuple-passing protocol (the owner's second requirement)

The efficient mechanism ALREADY EXISTS in two dialects that must
become one — this is the round's core design decision, not plumbing:

- **NLI dialect** (`nliInner`: `openPrep` once, then per outer row
  `BindOuter(slot, width)` + `Rescan`, `Next` drains):
  the probe evaluates `Key/Keys` exprs against the OUTER slot
  (`evalExprSlot(ke, o.outerSlot)`, `operators_index.go:616,942,968`)
  — zero-copy (no row concat; VirtualSlot composes outerMS/innerMS
  at emit). Keys are POSITIONAL outer-layout exprs
  (`translateToLayout`, `createplannl.go`), a coordinate translation
  that exists only because the fused node fused the spaces.
- **Lateral dialect** (`lateralBindable` + `ctx.OuterRows` push,
  `join_lateral_stream.go:47-90`): the outer slot's `row` is
  REPOINTED per tuple; `OuterColumnRef` (level≥1) resolves against
  it; SRF-style children bind the slot at Open.

**Decision: unify on OuterColumnRef + repointed slot (the lateral
dialect, which is PG's `nestloop params` mechanism).** Rationale:
PG-faithful (params by Var identity, not positions); it DELETES the
`translateToLayout` coordinate translation and the whole
outer-layout/binding-index map rather than moving it; and the
lateral stream's phase machine (`latPhaseOuter/Inner/Done`) already
implements per-row re-execution with LIMIT-safe mid-flight close.
`BindOuter(slot,width)` retires with the fused op; `Rescan` stays as
the probe's rewind entry but takes the outer slot in the lateral
form (repointed, never copied).

Per-row cost budget (stated so a reviewer can falsify it): one slot
repoint + one index descent setup per outer row — the same work the
NLI driver does today; the VirtualSlot emit composition is unchanged.
No per-row allocation is introduced (pair buffer reuse mirrors
`lateralJoinStream.pair`).

## 3. Slices, in landing order (each gated byte-identical EXPLAIN
unless named)

- **Slice 1 — planner construction.** The three sites build
  `Join{Algo: JoinAlgoNestedLoop, Type, Left: outer, Right:
  parameterized IndexScan (Keys as OuterColumnRef over the outer
  schema), Predicate, Lateral: true}` instead of the fused node.
  `InnerMemo` construction moves unchanged (Memoize over the probe).
  The NLI TYPE stays until slice 4 (74 files migrate in its train).
  Gate: byte-identical EXPLAIN on both corpora (rendering already
  matches) + suites + values.
- **Slice 2 — executor driver.** Generic nestloop parameterized-inner
  path in `joinOp` reusing `lateralJoinStream`'s phase machine with
  the `nliInner` Rescan protocol folded into `lateralBindable`
  (ONE seam, not two). Preserved verbatim: INNER/SEMI/ANTI/LEFT
  emit-once semantics keyed on QUALIFYING matches
  (`outerMatched`'s R3-1 definition), residual on the merged row,
  EX1-02b deform bounds, TID-provider walks, InnerMemo caching.
  Gate: same as slice 1 + targeted semi/anti/left identity tests.
- **Slice 3 — EXPLAIN Index Cond.** The probe renders
  `Index Cond: (inner.col = outer.col)` with the outer reference
  (verify PG-exact against Q19/Q20/Q4/Q22 fixtures, not from memory).
  Gate: PG-fixture string comparison on those nodes.
- **Slice 4 — deletion + cost/whitelist migration.** Delete the type;
  migrate the 74 referencing files (mostly tests); move the NLI cost
  gate onto the nestloop path; drop the `terminatesPartial` NLI entry
  with a note that partial-NL execution is still unwritten (E-20 Cut
  4 stays deferred — decomposition removes the alien entry, not the
  missing worker story; claiming otherwise would be K22). Gate:
  suites + values + full parity re-read.

## 4. Non-goals (filed, not bundled)

- Partial nested-loop execution (stays deferred with its reason).
- Restriction-clause→indexqual matching (R22 follow-up).
- Changing join ORDER or METHOD choices: decomposition must not move
  a plan by itself — any movement is a costing bug, not parity.

## 5. Gates (every slice)

Suites green (pre-commit bar); values TPC-H digest 24/24 MATCH +
TPC-DS sweep PASS=95 all-zero; parity shape verdicts both corpora
(judged by CATEGORY per the roadmap); timing reported, never
adjudicated; a timing move on an unmoved plan fails the slice.

## 6. Open questions for slice 1 (recorded, not hand-waved)

- Key representation edge: multi-key probes (`Keys` list) and SAOP
  keys through OuterColumnRef — the eval sites
  (`operators_index.go:616,942,968,1009,1025`) read `o.outerSlot`
  positionally today; the translation moves into key construction.
- `BitmapHeapScan` inners (the third `nliInnerProbe` kind): same
  treatment or decline-and-keep-fused-temporarily — decide in slice
  1 with a probe-kind census, not a priori.
- `deformSplitPredicate`/`deformNLIMappedInner` bounds: re-derive
  against the split nodes or prove the merged-row derivation still
  applies — EX1-02b's comment is the spec.

## 7. Review record

Subagent delegation unavailable (Task cancelled at R0; TODO.md log).
Review as adversarial second pass by the author:

- **Source pass** (at HEAD): three construction sites confirmed by
  grep (not assumed — root-causes §6's error class); `nliInner`
  protocol re-read (`openPrep`/`BindOuter`/`Rescan`/`Next`);
  `lateralJoinStream` phase machine + dual correlation mechanisms
  re-read (`join_lateral_stream.go:47-140`); generic `joinOp`
  confirmed to Materialize-and-replay NL inners (no per-row
  re-execution — the gap slice 2 fills); EXPLAIN NLI arms re-read
  (`:715,821,2654,2989`); PG fixtures Q19/Q20/Q4/Q22/Q8 read for the
  target shape (not recalled).
- **Correction applied by review:** first draft unified on the NLI
  dialect (positional keys + BindOuter, "smaller diff"). Falsified:
  it preserves the coordinate translation this round exists to
  delete, and it contradicts the lateral machinery the codebase
  already uses for the same per-row problem (two seams for one
  mechanism is what K25 warns about). The OuterColumnRef decision
  above is the correction.
