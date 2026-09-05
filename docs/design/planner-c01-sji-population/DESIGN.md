# C-01 (P3-01) — `SpecialJoinInfo` population

Status: accepted. Implements TODO_ALL.md C-01.

## 1. Objective

Populate `SpecialJoinInfo.MinLefthand / MinRighthand / LhsStrict` from the
ON/USING qual instead of degenerating every entry to `min = syn`. Moves no
plan alone — unobservable until C-03/C-04 consume it. Gate: unit tests only
(TODO_ALL.md C-01).

## 2. Oracle

`postgres/src/backend/optimizer/path/../initsplan.c:make_outerjoininfo`:

- `clause_relids` = `pull_varnos` over the qual (which base rels the qual
  mentions); `strict_relids` = `find_nonnullable_rels` (which rels the qual
  needs non-NULL from).
- `min_lefthand = clause_relids ∩ syn_lefthand` (+ `inner_join_rels`,
  provably empty here — §4); same on the right.
- Lower-outer-join ordering scan (`:1823-1958`, grow steps only): FULL
  barrier expansion + LHS/RHS preserve-ordering adds.
- Punt (`:2007-2013`): an empty min side falls back to the full syntactic
  side. FULL returns early with `min = syn` (`:1772-1778`).

## 3. Why this was blocked, and the route taken

Take3 08 §6.1: deconstruction runs on raw `parser.FromExpr`s where
`ColumnRef` carries names, not relation indexes, and the legacy
`makeSpecialJoinInfo` receives no catalog, no bindings, no resolver.

Route taken (08 §6.1's smaller route): thread a scope — the current comma
item's leaves in deconstruct leaf order plus the catalog — into
deconstruction (`deconstructJointreeScoped` / `deconstructFromItemScoped` /
`makeSpecialJoinInfoScoped`; `newSjiScope` built once per statement in
`planFromClause`). Qualified refs resolve structurally against leaf
alias/name (+ schema); unqualified refs additionally require every in-scope
leaf to be catalog-backed and exactly one leaf to hold the column (mirroring
`resolveColumnRefAt` / `bindingMatchesRelation` precedence, restricted to
the ON's own comma-item scope).

## 4. Safety contract (binding)

`min` sets only ever SHRINK from `syn` toward PG's values on
fully-resolved evidence; ANY uncertainty falls back to `syn`:

- unknown qualifier, ambiguous/unresolvable column, table-less leaf
  (subquery/tablefunc/CTE/shadowed) under an unqualified ref,
- unhandled expression node (sublinks, arrays, EXTRACT, aggregates/windows
  … — whitelist is transparent scalar containers + constants only),
- nil scope (legacy callers keep exact legacy behaviour).

An underestimate would permit a reordering PG forbids (wrong answers); an
overestimate only withholds one (missed optimisation). `LhsStrict`
defaults to `false` (withholds LHS-strict association). `inner_join_rels`
is provably empty: goopg's chain is strictly left-deep with a single fresh
base leaf on the right, and subquery-internal joins live in that
subquery's own deconstruction.

FULL keeps PG's exact early-return (`min = syn`). RIGHT keeps `min = syn`:
PG rewrites RIGHT→LEFT before `make_outerjoininfo` and goopg's flat chain
can only flip the first join (`reduce_outer_joins.go` S9.4).

## 5. Deliberately NOT populated

- `ojrelid` stays 0: goopg has no RT indexes for join RTEs.
- `CommuteAbove/Below` stay empty: keyed by `ojrelid`; no goopg consumer
  reads them. PG's identity-3 shrink step is skipped for the same reason
  (skipping a shrink is the safe direction).
- `SemiOperators/SemiRhsExprs` stay empty: SEMI never reaches
  deconstruction (parser produces only INNER/LEFT/RIGHT/FULL/CROSS;
  SEMI/ANTI are planner-internal and only ANTI arrives here via the
  LEFT→ANTI demotion, which runs before deconstruction). ANTI is aligned
  with upstream (`false/false`); the legacy optimistic-ANTI assignment is
  removed. Both flags remain unread by path generation.
- PlaceHolderVars: vacuous (no placeholder machinery).
- FOR UPDATE-on-nullable-side error: not replicated (pre-existing
  orthogonal gap in row-mark planning).

Strictness reuses `collectNonNullableTableNames` translated through the
scope; the translation can only UNDER-approximate strict (safe direction).

## 6. Consumers (unchanged this item)

`joinIsLegal`, `joinOrderRestricted`, `hasJoinRestriction`,
`buildJoinRelRestrictList` consult only Min/Syn/Jointype/LhsStrict — all
preserved. No search, costing, or plan-shape change in this item.

## 7. Follow-up (not this item)

USING/NATURAL joins (`j.On == nil`) always punt to `syn` with
`LhsStrict=false` — safe (overestimate) but leaves the most common
`LEFT JOIN … USING` shape permanently unshrunk. Threading
`buildUsingPredicate` output into the analysis is filed follow-up work.

## 8. Gate

- `go test ./internal/optimizer/` green (incl. new
  `specialjoin_scoped_test.go`: qualified shrink, unqualified resolve +
  ambiguity/table-less fallback, FULL/RIGHT syn, nil-scope legacy,
  LhsStrict strict/non-strict, lower-OJ FULL barrier, SEMI/ANTI flags).
- TPC-H 24/24 MATCH + plan-parity zero shape moves expected (no consumer
  change); run before commit per ground rule 3.
