# C-02 (P3-02) — `distribute_qual_to_rels` + `check_outerjoin_delay`

Status: accepted. Implements TODO_ALL.md C-02. After B-01 (compute
payloads landed) and C-01 (SJI min/strict populated).

## 1. Objective

A qual is **placed**, not copied down. Supersedes `pushInnerJoinInputQuals`
copying (double evaluation) in `inner_join_qual_pushdown.go`, which
duplicates each single-side conjunct onto a descendant while keeping the
original in the residual `Filter` (property 2), so the executor evaluates
it twice and the plan carries Filters PG never builds.

Gate: take3 09 §5 P3 (PP both suites + enum-trace adjudication + timing on
moved plans + values-diff R8).

## 2. Oracle (PG 18.3, rebased — no `check_outerjoin_delay` there)

PG 18 removed `check_outerjoin_delay` in the nullingrels rework
(`inner_join_qual_pushdown.go:62-66` already notes this). The live
mechanism (`postgres/src/backend/optimizer/plan/initsplan.c:2400-2830`):

- `distribute_qual_to_rels` (unchanged in shape): each qual filed exactly
  once by the relids it reads — single-relation restriction → that
  relation's `baserestrictinfo` (applied at the scan); spanning → the
  join level's `joininfo`. A qual is never evaluated in two places.
- Delay = `outerjoin_nonnullable` / `ojscope` / `incompatible_relids`:
  a qual from above an outer join may not be evaluated below it when its
  relids are incompatible with (reach into the nullable side of) the
  joins it would cross. Strictness does NOT exempt a qual from delay —
  strictness feeds `reduce_outer_joins` demotion separately, and quals
  undistributed after demotion still obey the incompatibility test.

C-02a below ports the incompatibility test, not the removed function.
goopg has no nullingrels/`ojrelid`/`Commute*` (C-01 deliberately leaves
them empty), so the port is stated in SJI + plan-coordinate terms and
every gap defaults to delay (fail-closed).

## 3. What the tree already does (do not re-build)

- Search path: `tryPGShapedJoinSearch` (`joinsearchseam.go:182`)
  partitions WHERE + INNER-link ON quals (`partitionConjunctsForJoinPlanning`),
  attaches single-relation conjuncts as leaf-local Filters pre-search
  (`:322-334`), files spanning ones as join clauses via
  `buildJoinRelRestrictList` → `splitJoinClauses`. This IS
  distribute-placement for the searched prefix.
- Coarse delay: `prefixNullable(spine)` holds the WHOLE where in the
  residual above a nullifying spine (`:294-299`); `joinRestrictionSides`
  (`inner_join_qual_pushdown.go:222`) decides copy targets per join type
  (INNER both, LEFT left-only, RIGHT right-only, FULL neither).
- `restrictInfo` carries `relids` (= required_relids) but has no
  `outer_relids` / `nullable_relids` / `is_pushed_down` / `can_join` /
  `pseudoconstant` (surveyed 2026-09-05).

What remains is the legacy/last-pass half: `pushSingleSideQualsIntoInnerJoinInputs`
(`planner.go:1433`, runs LAST after `remapWithBindings`) duplicates
residual-Filter conjuncts down the join spine instead of placing them.
(The search path is admitted-INNER-prefix placement only: `prefixNullable`
holds the WHOLE where above a nullifying spine, spine-touching conjuncts
stay residual above the spine, every decline returns `(node,pred)`
untouched, and full-OR stays residual by design — so the residue C-02
owns is large, not a corner.)

## 4. The crux

The copy pass works in plan-tree coordinates (`ColumnRef.Index` against
`Output()` widths); C-01's SJIs live in deconstruct relid coordinates.
Bridging them needs plan-subtree → relset attribution. What exists:
`resolveContext.bindings` (+ offsets) at `planFromClause` time, and
`SourceTableIdx` on resolved refs. The design below never re-derives
attribution by name at plan level (RC-1b lesson: FROM-cumulative vs
OID-sorted confusion caused Q47/Q50 wrong answers); it threads the
already-resolved attribution the pass validates positionally today.

Safety contract (binding, all slices): a qual descends across a link
only on a delay proof for THAT link, and the proof is conjunctive over
the ENTIRE crossed path (upper + lower OJs — PG's `incompatible_joins` /
`joins_so_far` scan): one delay verdict anywhere stops the descent and
the residual keeps the conjunct (duplication is result-safe, a dropped
qual is not). There is NO strictness exemption: a strict qual on the
nullable side still delays — demotion (`reduceOuterJoins`, which runs
before deconstruction) already turned demoted links into INNER/ANTI, so
every link C-02a ever sees is a surviving outer link. Moves never cross
a FULL link, a LATERAL link, or a semi/anti whose `Output()` is
left-only. Nil SJI = delay (fail-closed).

## 5. Slices (one checkbox ≈ one commit; TODO_ALL C-02 split)

- **C-02a — `delayedAboveOJ` pure function + tests (inert).**
  Signature over relid sets, not plan nodes:
  `delayedAboveOJ(qualRelids RelSet, sj *SpecialJoinInfo) bool`
  = "qual must not be evaluated below this OJ link". Rule: delay iff
  `qualRelids` reaches the link's nullable side (LEFT: right side;
  RIGHT: left side; FULL: both — FULL always delays in practice).
  No strictness parameter and no strict exemption (see contract:
  demotion already ran). Nil sj = delay. No callers. Gate: unit tests
  incl. PG initsplan.c case table (`= const` on nullable side delays,
  `IS NULL` on nullable side delays, nullable-unmentioned places,
  preserved-side-only places, FULL always delays).
- **C-02b — plan-level attribution infra (inert).**
  `outputRelSet` / `qualSrcRelSet` / `planJoinDelaySJI`: the delay proof
  made computable at plan-tree joins from SourceTableIdx attribution
  (no joinlist alignment needed). Deliberately NOT consumed by the copy
  pass: wiring it there is vacuous (review-proven — the legacy side
  gates already decline every nullable-side qual, so the check can only
  fire on Index-vs-SourceTableIdx disagreement, where attribution is
  least trustworthy). Verdict parity with legacy is the gate, not
  fewer copies: no moves expected, any move is a bug. The proof becomes
  load-bearing at the C-02c/d moves, where dropping the residual needs
  a positive placeability proof the legacy declines cannot supply.
  Gate: units (attribution tables + parity pins with complete
  attribution + unknown-attribution skip) + walker-inventory pin.
- **C-02c — move, don't copy, on all-delay-proven paths.**
  When the copy lands AND every crossed link passed the C-02a proof AND
  the conjunct is single-side AND non-volatile (existing FuncCall
  decline stays), drop the original from the residual Filter.
  (INNER-ness of the landing join alone proves nothing — the descent
  may have crossed an outer link above it; the proof is the full path,
  inherited from C-02b.) `PushedBelow` charging (`filterSelectivity`)
  retires for moved conjuncts. `deriveConstAcrossJoinEquality` may only
  seed while the original is KEPT (copy phase): its soundness comment
  (`inner_join_qual_pushdown.go:492-498`) relies on the residual
  masking match→null-extension flips — moves must re-prove the derived
  qual over its own path or drop the derivation. Gate: units + R8
  values-diff both suites + timing arm (double-eval win measured, not
  claimed) + PP (fewer Filters is an intended move, reported).
- **C-02d — move across preserved-side outer links.**
  Same as C-02c with descents that cross LEFT-preserved /
  RIGHT-preserved links (still gated per-link by C-02a; nullable-side
  originals never move). Gate as C-02c.

  **Realisation (2026-09-05).** C-02d needs no new machinery and no new
  side test: it is the REMOVAL of C-02c's `crossedOuter` veto, because
  two existing gates already restrict an outer-link descent to exactly
  the safe case.

  1. `joinRestrictionSides` answers left-only for LEFT and right-only
     for RIGHT, and refuses FULL, SEMI, ANTI and LATERAL outright. So a
     descent that crosses an outer link can only ENTER the preserved
     side.
  2. `delayedAboveOJ` (via `proven`) additionally requires the conjunct
     not to reach that link's nullable side, conjunctively over the whole
     crossed path.

  Together: a crossed outer link is always a preserved-side descent by a
  qual reading only preserved-side columns. For that case, placing below
  and filtering above select the same rows — a preserved row rejected
  below produces no join row at all, and every join row it would have
  produced (matched or NULL-extended) is one the residual above would
  have rejected on the same, un-extended values, since the join never
  null-extends the preserved side. This is precisely PG filing a
  single-relation restriction into that relation's `baserestrictinfo`
  (`distribute_restrictinfo_to_rels`, initsplan.c).

  The nullable-side case is refused twice over and is pinned by its own
  test rather than left implicit, because either gate alone would look
  correct in isolation while the other was relaxed.

  **The two gates speak different attributions, and that matters.**
  Gate 1 constrains `ColumnRef.Index` (positional, and what the executor
  actually evaluates); gate 2 constrains `SourceTableIdx` (identity, and
  what survives every remap). On disagreement, Index wins for side
  selection and for execution, while the identity proof fails closed and
  demotes the move back to a copy — so a stale or permuted schema costs a
  missed optimisation, never a wrong answer. Because the two notions can
  disagree, the proof also carries a POSITIVE containment requirement
  (every identity the conjunct reads lies inside the side descended into),
  not only the negative "does not reach the nullable side": at an
  INNER/CROSS link the negative test returns false without inspecting the
  qual at all, so containment is what makes the claim hold at every link
  rather than only at outer ones. A conjunct reading no relation at all is
  unprovable rather than vacuously proven, so the ledgered
  `pseudoconstant` work cannot turn a vacuous proof into a silent move.

Out of scope (ledgered, not forgotten): FULL-link placement (needs
USING-coalescing positions — and LEFT/RIGHT `USING` merged vars have
the same position problem — same blocker as C-01 follow-up);
`pseudoconstant` quals; volatile FuncCall pushing (no volatility model).

## 6. Consumers

C-03 (`join_is_legal` on real SJIs) reads placed quals via the search
path (unchanged here); C-04 (single search problem) inherits fewer
residual Filters. `PushedBelow`/`pricedBelow`/`notePushedBelow` retire
when the last duplicating call site is gone (C-02d), not before.

## 7. First-slice gate (C-02a)

`go test ./internal/optimizer/` green (new `outerjoin_delay_test.go`);
no plan-shape, values, or timing arms (inert by construction — no
callers). Design review before code (this file).
