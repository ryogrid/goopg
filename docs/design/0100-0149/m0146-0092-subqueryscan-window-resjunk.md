# M0146-0092 — Subquery Scan: resjunk columns and the window input target

Status: done 2026-10-07 (56ad4f97a). Parent: M0146-0066 (class B of the
Subquery Scan recon). Evidence: `analysis/m0146/m0146-0092/`, 20 probe
shapes on PG 18.3 and goopg.

## Problem

`stripTrivialSubqueryScans` stands in for setrefs.c's
`trivial_subqueryscan`. In the pathtarget regime it treated the
consumer's first-reference order as the scan's tlist. PG keeps
`Subquery Scan` in two situations that this proxy cannot see:

- TPC-DS Q44 (`v1`/`v2`, `v11`/`v21`)
- TPC-DS Q49 (`in_web`/`in_cat`/`in_store`)
- TPC-DS Q67 (`dw1`)

## PG behaviour (probed)

### Resjunk entries in the subquery's tlist

A GROUP BY, ORDER BY or DISTINCT ON item that matches no select-list
entry becomes a resjunk TargetEntry. Matching uses output name, position
or equal expression (`findTargetlistEntrySQL92`). A window
PARTITION/ORDER BY item that matches no entry by expression
(`findTargetlistEntrySQL99`) does too.

- The subplan's tlist keeps these entries.
- A pathtarget-regime scan tlist names only the columns its parent needs,
  so the two lengths differ and `trivial_subqueryscan` keeps the node:
  - `select * from (select sum(b) s from t group by a) x` → kept.
  - `select * from (select b from t order by a) x` → kept.
  - `select * from (select a, rank() over (order by b) r from t) w` → kept.
- `build_physical_tlist` (plancat.c, RTE_SUBQUERY arm) includes resjunk
  entries. So under an Aggregate the scan is trivial again:
  `select count(*) from (…group by a) x` → stripped.
- No resjunk entry, so stripped: `order by 1`, an output alias,
  `group by 1`, and DISTINCT ON over selected columns.

### A leaf below a WindowAgg

`make_window_input_target` (planner.c) builds the window's input target
from the final target in three steps:

1. Entries carrying a window sortgroupref (partition/order keys), in
   final-target order, with resjunk key entries at the tlist's end.
2. Then `pull_var_clause` over every other entry, recursing into window
   function arguments, de-duplicated.

The leaf gets that target as its tlist under the WindowAgg
(CP_SMALL_TLIST) or under the window's Sort.

| select list over `v(a, c)` | PG's leaf tlist | verdict |
|---|---|---|
| `a, c, rank() over (order by c)` | `(c, a)` | kept |
| `a, c, rank() over (partition by a order by c)` | `(a, c)` | stripped |
| `c, rank() over (order by a)` | `(a, c)` — resjunk key `a`, then Var `c` | stripped |
| `rank() over (order by c), a, c` | `(c, a)` | kept |

## Change

- **`selectHasResjunk`** (`subqueryscan_window.go`) applies the matching
  rules to the subquery's AST. It reuses `resolveOrderBySubstitution` for
  names and positions, and `parserExprKey` for expressions; a star in the
  select list covers a bare column.
  - `SubqueryScan.resjunk` carries the result for derived tables
    (`planSubqueryRangeVar`) and for inlined CTEs (`wrapInlinedCTEScans`,
    from the CTE's query).
  - In the pathtarget regime, a resjunk wrapper is kept.
  - Set operations and GROUPING SETS report false, which is the previous
    behaviour.
- **`windowInputOrder`** computes PG's order for a leaf whose parent chain
  is `WindowAgg [→ Sort]`.
  - It takes the window stack (bottom first; a Sort between windows is
    allowed) and its partition/order keys.
  - The final target is the Project above the stack, past
    Sort/Limit/Filter/Distinct, or else the top window's output.
  - Each WindowAgg emits its input followed by its functions, so a
    reference past the base columns maps to a window function, whose
    arguments are flattened.
  - Kept entries come first, then the keys missing from the select list,
    then the flattened Vars.
  - A computed key, or an unmappable reference, keeps the wrapper.
- **Leaf paths.** The strip walk records each leaf's path from its region
  root, so the window chain is visible.

## Verification

- **Probes.** 20 probe shapes against PG 18.3 (`probe-window-resjunk.sql`):
  goopg agreed with PG on 14 before and 19 after. The remaining one is a
  hash join's inner side (below).
- **Unit test.** `TestSubqueryScanWindowAndResjunkRules` covers 11 shapes
  with PG's verdicts. Disabling both rules fails 6 of them.
- **TPC-DS.** Only Q44, Q49 and Q67 change, at both scales, and each now
  has PG's Subquery Scan count: Q44 0→4, Q49 3→6, Q67 0→1. Because the
  plans align with PG further, the first-divergence categories drop:
  - SF1: scan-type 36→34, parameterisation 32→31, parallelism 41→40.
  - SF0.25: join-order 49→48, join-method 25→24, scan-type 28→27,
    parallelism 28→27.
- **Regress A/B.** window, subselect, with and union are identical.
- **Gates.** units, TPC-H spotcheck, acceptance arm, fire set, SF0.25 sweep
  and ea-ratchet all PASS.

## Not covered (filed / ledgered)

- **A hash join's inner side** (filed as M0146-0097). PG's Hash node
  requests CP_SMALL_TLIST, so a leaf on the hashed side is in the
  pathtarget regime. goopg's strip treats every `Join` as a physical-regime
  breaker for both children. Probe:
  `select * from t, (select sum(b) s from t group by a) x where t.b = x.s`.
- **Approximations.**
  - Resjunk ordering among several missing window keys follows the stack
    (bottom first). PG follows WINDOW-clause parse order.
  - A query-level ORDER BY resjunk Var is not in the final Project, so
    `windowInputOrder` can miss it (that only keeps a wrapper).
  - `parserExprKey`'s qualifier-blind match can call a key matched where
    PG would not (that only strips, the old behaviour).
