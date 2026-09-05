# C-03 (P3-03) — DP builds outer/semi/anti joinrels directly

Status: accepted. Implements TODO_ALL.md C-03. After B-01 and C-01.

## 1. Objective

`join_is_legal` consults real SJIs (DONE — `internal/optimizer/joinsearchlevel.go:182`,
M0128-P1.2/P1.4, C-01 populates Min/Strict) so the DP builds
outer/semi/anti joinrels directly, not just INNER ones. Gate: take3 09
§5 P3 (DPPATH OFFERED/ACCEPTED + `estimate-audit --enum-trace`).

## 2. Oracle

`make_join_rel` (joinrels.c:696): legality → relids (+`add_outer_joins_to_relids`)
→ restriction list → `populate_joinrel_with_paths` (joinrels.c:786),
whose arms are per-jointype (INNER both directions; LEFT/RIGHT/FULL only
the legal direction; SEMI/ANTI nestloop-only with the inner unique-ified).
PG has no PathSemi — NestLoop/Hash/Merge paths carry `jointype`.

## 3. What the tree already does (surveyed 2026-09-06, do not re-build)

- Legality side COMPLETE: `joinIsLegal` (LEFT/SEMI/ANTI arms),
  `joinOrderRestricted`, `hasJoinRestriction` all read real SJI
  Min/Syn/Strict. `makeJoinRel` calls `joinIsLegal`, applies the
  `reversed` swap, and passes sjinfo to `buildJoinRelRestrictList`
  (nullable-side filter clauses; SEMI/ANTI add zero — no arm).
- Generation side INNER-ONLY: `RelOptInfo` has NO jointype field
  (`internal/optimizer/path.go:244-313`); `addPathsToJoinrel` takes no jointype
  (`internal/optimizer/joinpaths.go:37-43` gauntlet deferred as unreachable);
  `searchJoinRelBuilder.addPaths` has no jointype branch; ALL
  `createPlanNode` arms hardcode `Type: JoinTypeInner` (hash
  `internal/optimizer/createplanjoin.go:463`, merge `:576`, NL `internal/optimizer/createplannl.go:137`,
  NLI `:279`, bitmap-NL `:393`); no PathSemi/PathAnti kinds.
- Reachability: NO outer/semi/anti link reaches `makeJoinRel` in
  production — LEFT/RIGHT peeled by `splitOuterSpine`, SEMI/ANTI
  declined at the seam gate (searched below via `runJoinSearchBelowPinned`,
  links pinned above), FULL declines the whole search, non-spine outer
  links fail `extractSearchLeaves`/leaf-count. ANTI from
  `reduceOuterJoins` demotion is pinned the same way. Executor DOES
  execute SEMI/ANTI `Join.Type` (batch/NL-stream/outer-fill/explain
  arms) — produced today only by unnesting, never by the search.
- Consequence: C-03 slices are INERT end-to-end until C-04 deletes the
  spine. Verifiable at enumeration level (DPPATH OFFERED for
  outer/semi/anti pairings on hand-built problems + enum-trace), not at
  suite level. Stated, not hidden.

## 4. Slices (one checkbox ≈ one commit)

Carrier decision (review-driven): jointype lives on PATHS only, never
on the rel. A relset-keyed singleton (`findRel`/`addRel` first-writer-wins)
cannot hold one jointype — different pairs spanning one relset can match
different SJIs, and rel jointype would become arrival-order-dependent.
PG agrees: `RELOPT_JOINREL` carries jointype but alternatives coexist as
*paths*; goopg's rel has no such field and gains none. Mixed-jointype
pathlists coexist on one rel exactly like PG.

- **C-03a — `Path.Jointype` field (inert).** `Jointype parser.JoinType`
  on the path struct (default `JoinInner` = zero value — VERIFIED
  `internal/parser/ast.go:727`, pinned by test in this slice);
  constructors stamp Inner; `comparePathCosts`/`comparePaths` IGNORE
  jointype (costs decide; type is a correctness attribute set by
  legality, not a cost dim — cross-jointype pruning questions, if any
  arise, belong to C-04); DPPATH format gains the type label;
  EXPLAIN/path goldens updated for the label (zero semantic moves).
  Sibling precedent for the must-avoid list:
  `internal/optimizer/path.go:116-143` NCols/Target. Gate: unit
  (default-Inner pin + label rendering) + suites green.
- **C-03b — jointype-aware `addPaths` (inert).** `makeJoinRel` passes
  the matched `sjinfo` (nil-safe) into `addPaths` (interface + test
  stub `twoShapeBuilder` updated): legality of a given
  (outer, inner) call depends on ORIENTATION (is outer == SJI LHS
  after the reversed swap?), which jointype alone cannot answer — the
  callee decides per direction (OUTER: legal iff outer covers LHS and
  inner covers RHS; SEMI/ANTI: nestloop-only arms, hash/merge decline
  — PG allows hashed SEMI only with unique inner; goopg has no
  uniqueness proof: decline = safe). Paths stamped with the sjinfo
  jointype; mixed-jointype pathlists coexist. No `createPlanNode`
  change yet (arms still emit INNER — paths carry the type, plans
  don't). Gate: DPPATH OFFERED/ACCEPTED unit adjudication on
  hand-built outer/semi/anti problems + suites green.
- **C-03c — `createPlanNode` jointype arms (inert).** Hash/merge/NL/NLI
  arms read the path jointype into `Join.Type` (SEMI/ANTI execute
  today; LEFT/RIGHT execute today via the legacy pinned path — same
  operators; EXPLAIN Semi/Anti/outer labels already exist —
  `internal/executor/operators_explain.go:2783-2786`). Schema side
  included (not just the Type assignment): SEMI/ANTI narrow to
  left-only layout in `joinInputsFor` (or per-arm) — a searched SEMI
  planned merged-width misaligns every parent translation. Sizing
  inputs narrowed likewise: `NCols`, `AvgVarBytes`/`ColVarBytes`
  union, `joinrelConsiderParallel` all take the SEMI/ANTI left-only
  bound in C-03c (fail-closed: over-wide beats under-wide; C-05
  installs the real switch). FULL: no arm (decline at path generation
  — executor has no FULL hash semantics; ledger it). Gate: searched-shape
  unit fixtures (semi/anti joinrels plan to left-only Semi/Anti
  Joins) + suites green + take3 R8 values-diff on both suites (no
  production shape reaches them — expect zero drift, any drift is a
  bug).
- **C-03d — enum-trace DPPATH for outer/semi/anti (evidence).**
  Adjudicate PG-only pairings OFFERED at their level on fixtures
  (spine shapes à la C-04's Q72 witness, run against the search
  directly since the seam still peels in production). Closes the gate's
  DPPATH half. Gate: enum-trace fixtures green.

Risks:
- R1: `parser.JoinInner` must be the zero value — VERIFIED
  (`internal/parser/ast.go:727`); pinned by test in C-03a.
- R2: `geqoSearch` shares the builder — jointype flows there for free;
  pin one geqo+outer unit or decline geqo+non-inner explicitly
  (threshold-gated path, `internal/optimizer/geqo.go:343,406`).
- R3: covered in C-03c (schema + sizing narrowing, fail-closed
  over-wide). C-05 installs the real jointype sizing switch.

Out of scope: C-04 spine deletion (next item); C-05 sizing switch;
FULL execution semantics (ledger); `reduce_unique_semijoins` (C-09).

## 5. Consumers

C-04 deletes the gate (`splitOuterSpine` + `pinnedOuter` decline) and
these paths start flowing end-to-end (Q72 witness). C-05 sizes them.
C-09 reduces unique semijoins. No SEMI/ANTI filter-clause arms are
filed anywhere: PG has no nullable-side filters for non-null-extending
SEMI/ANTI, so there is nothing to port (not deferred — non-existent).

Inertness evidence (load-bearing — suites are green in both worlds):
the decline tests (`joinsearchspine_test.go`, `joinsearchseam_test.go`
`used==false`, `collapse_corpus_test.go`, `predp_test.go`) PLUS one
direct unit pinning the mechanism: the full `ctx.joinInfoList`
(passed unfiltered to the prefix problem) yields nil from
`joinIsLegal` over all prefix-internal pairs (the RHS-disjointness
fast path as a regression test). Production `makeJoinRel` callers are
exactly `internal/optimizer/joinsearchlevel.go:481,496` + `internal/optimizer/geqo.go:406`, all behind the
declines above — no memoize/incremental caller exists.

`setCheapest` inherits the compare-ignore rule (cheapest selection is
jointype-blind like the comparison; any cross-jointype pruning question
defers to C-04 with the enumeration that produces it).

## 6. First-slice gate (C-03a)

Unit + suites green; no plan-shape, values, or timing arms (inert by
construction — §5 mechanism pin, not just suite greenness). Design
review before code (this file).
