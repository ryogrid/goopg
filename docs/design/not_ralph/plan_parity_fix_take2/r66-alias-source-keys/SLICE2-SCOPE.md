# R66 SLICE-2 SCOPE — families G/T: the key chase (closes R66)

Follows `SLICE1-REPORT.md` (landed `1b9e91277`) and `STEP0.md`
(committed `4c082c4`). Rev 2: review APPROVE-WITH-NOTES on rev 1 is
fully remediated below (B1 covering citations, B2 bound entries +
census, B3 Gate-0 executed with a MATERIALIZED control). Closes the
round R63 ordered before the (a) Materialize re-triage.

## 0. Baseline

pp 6/14/0/2, rendering {Q7,Q9,Q10,Q13} (`pp66-post.txt`, identical vs
same-day live PG). Targets, all measured in STEP0 §2:

- Q7: Group+Sort `supp_nation, cust_nation, l_year` → PG `n1.n_name,
  n2.n_name, (EXTRACT(year FROM lineitem.l_shipdate))`.
- Q9: Group+Sort `nation, o_year` → PG `nation.n_name,
  (EXTRACT(year FROM orders.o_orderdate))` (Sort) / bare (Group).
- Q13: Sort key2 + Group Key `c_count` → PG `(count(orders.o_orderkey))`
  (Sort) / bare (Group). Key1 already `(count(*))` (Slice 1).

## 1. Premises closed (all three cite covering sites)

- **Table-0 = derived, covering-stated.** Aggregate outputs carry
  table 0 BY CONSTRUCTION: groups `planner.go:7829`, agg calls
  `:7996/:8022` build `SchemaColumn` without `SourceTableIdx`
  (zero-value 0 — a derived relation has no base identity).
  Pass-through Projects CARRY it, documented:
  `createplanroot.go:331-337` (`projectToBindingOrder`), 
  `upper_narrow_apply.go:557-562` (`narrowingProjectOver`, "CARRIED,
  not dropped"). Convention supports: `source_table_idx_test.go:85`
  (real tables `>= 1`). The opposite-direction stamper is known and
  excluded STRUCTURALLY: view-rename Project targets default to table 0
  (`planner.go:3691-3700`) but sit under `IsolatedScope: true`
  (`:3706`; the only other setter family is IN-unnest inners,
  `unnest.go:3142,3224`) — so the descend rule requires
  `!IsolatedScope` alongside table-0. Plain subquery Projects are
  `IsolatedScope=false` (unit-measured this round, probe reverted) —
  their handling is Gate-0's, decided in §3. Load-bearing guard stays
  runtime: child-output name match at every hop (Q7 `[41]=n_name`, Q9
  `[47]=n_name`, Q13 `count`→`count`, all in the dump).
- **Sort/Filter position-preservation: definitional.**
  `plan.go:1785` (`Sort.Output() == Child.Output()`),
  `:1573` (`Filter.Output() == Child.Output()`) — neither can rename
  nor narrow. The runtime `nout == ncout` check is defence-in-depth.
- **Filter hop: kept, disclosed.** Cited on `childAggregateThroughFilters`
  + Output-identity above. Zero Filter nodes in any dump — untested on
  corpus, fail-closed; the first corpus hit is adjudicated.

## 2. The cut (renderer-only, shared helper, bound entries)

`resolveKeySource(expr, node) -> (Expr, bool)`:

- Sort/Filter with `nout == ncout`: recurse `(expr, child)`.
- Project at `j = expr.Index` (`0 <= j < len(Targets)` else decline),
  `t = Targets[j]`, project `!IsolatedScope` (else stop, render expr
  as-is — view-rename/unnest boundary):
  - `t` bare ColumnRef, `SourceTableIdx == 0`, `t.Index` inside child
    output, child-output name match: descend `(t, child)` (pure
    rename — Q13 `count`→inner pos 1).
  - `t` bare ColumnRef with a real table + child-output name match:
    stop, render `t` (Q7/Q9 `n_name` → `n1.n_name`/`nation.n_name`).
  - `t` non-ColumnRef: stop, render `t` (ExtractExpr → existing wrap
    rules apply).
- Aggregate at `j` (with `GroupingSets == nil`, carried over — the
  layout question is real here): `j < len(GroupExprs)` → recurse
  `(GroupExprs[j], child)`; Aggs-section → synthesise via the R65/R66
  `expandAggOutputRef` admission (no second rule); else decline.
- Anything else (Join/NLI/scans/CTE scans/Memoize): decline. Depth cap
  4. The RETURNED expr passes `exprHasSubplanOrOuterRef`.

Entries (review B2 — bound explicitly):

- **Sort arm, entry (i) — Sort above agg** (Q9-top/Q13-top/Q16-top
  shape): today's Arm-S guard (child agg via
  `childAggregateThroughFilters`, `0 <= idx < len(GroupExprs)`,
  same-level name check) → `GroupExprs[idx]` → chase down. Slice-1
  behaviour preserved where the chase declines (probe-E shapes return
  `GroupExprs[i]` byte-identical).
- **Sort arm, entry (ii) — Sort over Project** (Q7's grouping-input
  Sort, whose child is a Project so Arm-S never resolved an agg):
  `(key, Sort.Child)` with the re-anchored name guard (`key.Name ==
  Sort.Child.Output[j].Name`) — Q7 `supp_nation`==`supp_nation`.
  No GroupingSets guard needed here (no output-layout question: direct
  child positions — stated, not assumed). This entry also reaches
  plain ORDER BY sorts over Projects corpus-wide; every move is a
  sourced-or-decline decision adjudicated under P3 (census below).
- **Group arm** (`GroupExprs[i]`, `Agg.Child`): first move of this arm
  (Slice 1 left it byte-identical); existing S18 wraps apply
  (GroupAggregate-over-Sort wraps non-Var → Q7's `(EXTRACT(...))`;
  HashAggregate-over-join stays bare → Q9/Q13). GroupingSets aggs
  skip the arm wholescale already (`operators_explain.go:1120`).

Reachability notes (dump-verified): Q7 `Targets[3]` (volume BinaryOp)
unreachable — entries carry group positions only. Q13's inner Sort is
never descended (Aggs-section arm synthesises and returns). Q8/Q22
chains undumped — §4.

## 3. Gate-0 RESOLVED (executed 2026-09-11, transcript saved —
no blocking probe remains)

Oracle `:65432`, session-local TEMP tables (zero persistent mutation;
per-session namespaces, dropped at disconnect). Evidence (reproduced
twice, byte-identical key lines): `/tmp/pp2/r66/gate0/{g-shape.sql,
cte-mat.sql, pg-gate0.txt}`. G-shape + `WITH s AS MATERIALIZED`
control:

- Flattened subquery: PG prints `r66a.n_name` (Sort AND Group Key).
  The chase rendering Project targets as base text is CORRECT there —
  Outcome A. The alias-no-op pin is SUPERSEDED (becomes a sourced-text
  pin; its guarded behaviour is exactly what this slice changes — it
  breaks loudly on schedule, as designed).
- MATERIALIZED CTE: PG prints `s.supp` (boundary stop). goopg
  materialises CTEs behind CTE-scan node kinds, which are outside the
  descend set → decline-by-construction; TPC-DS CTE group-bys watched
  under P3. (A plain-CTE control would have risked a false Outcome A —
  PG 12+ inlines single-ref CTEs (K31) — hence MATERIALIZED.)

## 4. Falsifiable predictions

Pre-cut alias-key census (`r66post.plans.txt`): Q7 (6 items), Q8
(`o_year` ×2), Q9 (4), Q13 (`c_count` ×2), Q22 (`cntrycode` ×2) —
Q1/Q4/Q20/Q21 bare-or-sourced lines cannot move (single-RTE or
already sourced; chase returns input).

| # | Claim | Verdict on miss |
|---|---|---|
| P0 | rendering {Q7,Q9,Q10,Q13} → {Q10}; Q7/Q9/Q13 key lines PG-identical (modulo non-rendering categories); Q8/Q22 moves, if any, PG-faithful (their chains are undumped — EITHER a sourced move OR no-move is acceptable; an away-move is not) | surviving text gap on Q7/Q9/Q13, or any away-move → STOP, re-audit |
| P1 | pp 6/14/0/2 unmoved; ZERO new categories; ZERO EXTRA flips | any previously-matching query newly diverges, or any category rises → STOP |
| P2 | values 24/24 MATCH pre/post | ANY move → STOP |
| P3 | explain A/B moves confined to Sort Key + Group Key lines, each adjudicated toward the live PG pair | any node/child/Filter move, or an unadjudicated key move → STOP |
| P4 | DS SF0.25 sweep PASS=96 SKIP=3 | any mismatch → STOP |
| P5 | Q6 and Q11 stay MATCH | either moves → STOP |

No Slice-2 query can close (conjunction rule) — judge by CATEGORY.

## 5. Sibling audit (delta on Slice 1's)

- `BareVarKeysUnchanged`: re-anchored — passes because a scan child
  declines ("non-Project/Sort/Filter/Aggregate child"), not because
  entry needs an agg (entry (ii) has none in path).
- Memoize/Gather: decline-by-construction (outside the descend set);
  captures are serial (Gather absent) — stated.
- Correlated-SubPlan `.col1` remainder, Q10 FD-trim: untouched.
- `planExprContentKey` (no Distinct key): synthesised calls still never
  stored/keyed — re-state the survey in the REPORT.
- Comment maintenance (scheduled in the implementation gates — the
  stale-comment failure class): `sortGroupKeySource` ("Slice 2"
  supersession note), `expandAggOutputRef` header, the S18 SIBLING PAIR
  block (gains the chase rule), `childAggregateThroughFilters`
  ("Project between Sort and Aggregate fails closed" — verify before
  touching: it governs above-agg Projects at entry (i), which still
  fail closed; the chase only descends below-agg. Do not "fix" an
  accurate comment).

## 6. Gates (implementation, all FOREGROUND)

1. `go test ./internal/executor/ ./internal/optimizer/` green (new
   Q7/Q9/Q13-shape pins incl. Group Key text, G-shape sourced-text pin
   per Gate-0-A, sublink guard intact; no `-count=1`); `go vet` clean.
2. `scripts/tpch-spotcheck.sh` PASS (re-run if `:65433` is free;
   Slice-1's deferral rationale carries otherwise — renderer-only).
3. Values A/B 24/24 + pp vs fixtures AND live PG (P0–P2, P5).
4. Explain A/B per-query adjudication incl. Q8/Q22 (P3) + DS
   plans-channel classification with a REAL pre-flight (enumerate
   alias Sort/Group key lines pre-cut — review N4 carried). Every
   censused line gets a stated reason (moved-PG-faithful with the
   live-PG line quoted, or declined with the failing guard named) —
   a no-move where live PG shows sourced text is a P0 miss, not a
   pass-by-silence.
5. DS SF0.25 sweep foreground, private lane (P4).
6. `make plan-gate` triage (expect Q7/Q9/Q13 verdict moves; re-pin or
   R65-precedent opt-out with pre/post-identical rationale).
7. `SLICE2-REPORT.md`, review, commit (explicit pathspec, `-n`) + push.

## 7. Handoff

Landing Slice 2 closes R66 — the last R65-ordered round. Next due per
R65 REPORT §4: the (a) Materialize re-triage (R63's
insufficient-alone verdict re-measured on post-R66 numbers), then #6,
R61 #4, (b) Q4, watches. That re-triage is a new round with its own
scope, not this slice's tail.
