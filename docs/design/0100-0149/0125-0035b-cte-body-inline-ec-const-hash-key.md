# 0125-0035b — Q78: CTE-body qual descent, join-equality constant derivation, and degenerate hash-key re-selection

**Task**: M0125-0035 arm (a), CTE-body half — the last piece of the item's
acceptance (`78|OK|45|8f67acff…`). **Status**: acceptance MET 2026-08-01.
Evidence: `analysis/m0125-0035b-cte-body/` (probe + full 99-query gate).
Predecessors: `0125-0035-c2-single-table-qual-placement.md` (binary-join
arm), `0125-0035a-preserved-side-restriction-descent.md` (preserved-side
extension + descent).

## The residue arm (a) left behind

After 0035a, Q78's `ss_sold_year = 1998` reached the `CTE Scan on ss`
*reference* — but that filters the CTE's OUTPUT after the body's
aggregate. All three channel CTEs (ss/ws/cs) still aggregated every year.
PG's plan filters `d_year = 1998` at the `date_dim` scan inside all three
channels. Three separate mechanisms turned out to be needed, and the third
was not visible until the first two were in place.

## 1. CTE-body qual descent (`internal/planner/cte_inline_pushdown.go`)

goopg's analogue of the PG 12+ composition `inline_cte`
(`subselect.c`) + subquery qual pushdown (`allpaths.c:
subquery_push_qual` guarded by `qual_is_pushdown_safe`): a restriction on
a single-reference CTE's output crosses the reference into the body,
crosses the body's Project/Aggregate layers when every referenced output
column is a plain GROUP BY key (restricting a group key removes whole
groups and leaves surviving groups' membership — hence every aggregate
value — untouched), then hands off to `pushConjunctIntoSubtree` for the
join-tree descent.

Load-bearing decisions:

- **refcount == 1 is the gate, read only from `Plan()`'s tail.** A CTE's
  body Node is SHARED between references (`planScanRangeVar` wraps the
  same `ce.body` per consumer) and the executor materializes the first
  reference's rows into the name-keyed `ctx.CTERowCache` which later
  references REPLAY. Pushing one reference's restriction into a shared
  body would filter every other reference twice over. `plannedCTE.refs`
  is incremented at the single consumption site, so it is
  exact-or-overcounted (overcounting declines inlining — the safe
  direction), and it is final only after the whole statement is planned.
  Q31's 6×-referenced `ws3` is the control: PASS 19 rows
  `ck=2a74acfb556c21a7` unchanged throughout.
- **`inlineEligible`**: only a plain non-recursive SELECT body qualifies.
  DML bodies (side effects run once, rows replayed) and recursive bodies
  (WorkTableScan protocol) never do.
- **Property 2 everywhere** (duplicate-never-move): the residual Filter
  keeps its copy, so any decline degrades to today's post-aggregate
  filtering, never to a dropped qual.
- Aggregate crossing declines on partial/split aggregates
  (`Mode != AggModeSimple`, `PartialSource`, `Passthrough`) and Limit
  (a filter below LIMIT changes which rows are kept).

## 2. Join-equality constant derivation (`deriveConstAcrossJoinEquality`)

Body descent alone fixed only the ss channel: `ws`/`cs` hang off the
NULLABLE sides of `LEFT JOIN … ON ws_sold_year = ss_sold_year AND …`,
which `joinRestrictionSides` rightly refuses for the original conjunct.
This is goopg's bounded analogue of `equivclass.c`
(`generate_implied_equalities_for_column`): while a `col = const`
conjunct descends through a join, each equality in the join's OWN
predicate linking `col` to a column of the other input seeds a derived
`col' = const` into that input — **including a nullable side**, the one
shape where that needs no nullingrels model:

- matching requires `col' = col`, and `col = const` holds on the
  preserved side, so a nullable row failing `col' = const` can never
  match; filtering it converts nothing — the preserved rows it would
  not have matched are null-extended exactly as before;
- at depth, property 2 keeps the original conjunct in the residual
  Filter above, so any pair whose preserved half violates it is
  discarded there regardless.

Fail-closed bounds: bare `ColumnRef = const` only (`isConstantPlanExpr`
excludes ColumnRefs/sublinks/FuncCalls — no volatility or scope
question); join equality between two bare ColumnRefs validated
positionally by name; type-NAME equality required (no opfamily model, so
cross-type transitivity stays unproven — PG makes the opfamily prove it).

The derived conjuncts land on the `ws`/`cs` references, and mechanism 1
carries them into the bodies (refs==1 each) down to `date_dim`. After
1+2 the goopg plan shows `Filter: (d_year = 1998)` on all three
channels' `date_dim` scans — PG's placement.

## 3. Degenerate hash-key re-selection (`reselectDegenerateHashKeys`)

Q78 STILL timed out (>900 s) with the correct plan text, and a
derived-table variant with hand-inlined filters timed out identically —
so the sink was the top spine's execution, not the CTE machinery. Each
channel standalone: ~3.5 s. The cause: `splitEqualityForHash` keys a
hash join on the FIRST disjoint-side equi-pair in ON-clause order, which
for Q78's top joins is `ws_sold_year = ss_sold_year` — and after
mechanisms 1+2 pin both sides to 1998, every surviving row carries the
same key. The entire build side lands in ONE bucket and each of 245,587
probes walks ~30k entries: a quadratic join wearing a hash join's
clothes. (This degeneracy predates this work — it is why Q78 timed out
even before the channels were filtered — but qual placement makes it
*guaranteed* rather than incidental.)

PG never faces the choice: its Hash Cond keeps EVERY equi-pair
(`get_switched_clauses`; `ExecHashGetHashValue` hashes them all), so a
constant-pinned column merely stops contributing bucket spread. goopg's
join carries one LeftKey/RightKey and evaluates the remaining conjuncts
as a per-match residual (`operators_join_agg.go` post-hash filter) —
which is also what makes re-picking the pair RESULT-NEUTRAL: the full
Predicate is enforced per emitted row either way, and a NULL in either
pair fails its equality conjunct on both routes. The pass runs at
`Plan()`'s tail after the qual-placement passes, judges degeneracy
against the join input's IMMEDIATE Filter (exactly where those passes
deposit `col = const`), and swaps to the first non-degenerate pair,
cloned into the key slots (M0097-0060).

The honest full fix is PG-shaped multi-column hash keys — a
planner+executor sibling-pair change (ledger row). This pass removes the
pathological case qual placement itself creates.

## Measured

- Probe (S-cold, quiet host): Q78 TIMEOUT 318 s → **PASS 24 s, 45 rows,
  `ck=8f67acff3895183f` = the oracle** (the item's acceptance). Q31
  control unchanged.
- Full 99-query SF0.5 gate, one binary
  (`tmp/goopg-sf05-m0125-0035b-bin`), 3 chunks
  (`analysis/m0125-0035b-cte-body/gate/`): **PASS=95 (57 ck-verified)
  MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=4.** Diffed cell by
  cell vs loop #17 (`analysis/m0125-0034c/gate/`): exactly two cells
  changed — Q78 TIMEOUT→PASS (claimed) and Q72 TIMEOUT→PASS at 306 s
  (NOT claimed: all-`JOIN…ON`, unreachable by these passes, and inside
  its documented 300–314 s straddle band). All 93 common PASSes
  identical in rows AND checksum.
- TPC-H: `tpch-spotcheck.sh` RESULT=PASS (Q12=2 Q13=35);
  `make plan-diff LABEL=m0125-0044-after` **22/22 MATCH** — zero TPC-H
  plan change.
- Units: planner/executor/parser suites + `RALPH_PRECOMMIT_SCOPE=units`
  green. New pins: `internal/planner/cte_inline_pushdown_test.go`
  (single-ref push + idempotence, multi-ref/ineligible/nil-backpointer
  declines, EC derivation onto the nullable side + type-mismatch
  decline, degenerate-key re-selection + non-degenerate no-op, and three
  end-to-end shapes through `Plan()`).

## Deferred (ledger rows 2026-08-01)

1. Multi-column hash join keys (the PG-faithful fix for §3).
2. EC propagation is one-hop per join, not `equivclass.c` transitive
   closure; same-type-name bound stands in for an opfamily model.
3. `inline_cte`'s volatile-body refusal has no goopg counterpart (no
   volatility model; the pushed conjunct itself is FuncCall-free).
