# 0125-0039 — EXPLAIN column qualification: correlations stop reading as self-comparisons

Status: **landed** (2026-07-31), `internal/executor` only.
Task: `M0125-0039` (filed by `M0125-0026`, evidence
`analysis/m0125-0026-timeout-plans/README.md` §"C4", closing paragraph).

## 1. The defect, stated as a reading problem

`M0125-0026` classified the TPC-DS timeout set from captured plans. Five of the
eighteen captures contained a line that could be read two ways and gave the
reader nothing to choose between them:

| query | goopg printed | PostgreSQL 18.3 printed |
|---|---|---|
| Q30, Q81 | `Filter: (ctr_state = ctr_state)` | `Filter: (ctr1.ctr_state = ctr_state)` |
| Q64 | `Filter: (cd_marital_status <> cd_marital_status)` | `Join Filter: (cd1.cd_marital_status <> cd2.cd_marital_status)` |
| Q72 | `Filter: (… AND (d_week_seq = d_week_seq))` | `Hash Cond: (d2.d_week_seq = d1.d_week_seq)` |
| Q31 | `Filter: ((d_qoy = 1) AND … AND (d_qoy = 2) …)` | per-scan `Filter: ((d_qoy = 1) AND (d_year = 1999))` |

Every one of these is a *correlation between two distinct bindings of the same
relation*. Rendered without a qualifier they read as `x = x` (always true) or
`x <> x` (never true) — i.e. as a planner bug in the predicate itself. A triage
loop cannot tell "self-join across two aliases" from "unsatisfiable predicate"
from this output, which is the whole reason the class was filed.

Root cause is structural, not cosmetic: goopg's plan expressions are *resolved*
references. `planner.ColumnRef` carries a slot index and a name for
diagnostics — the FROM-clause alias it came from is not part of the expression.

## 2. What upstream actually does

Two mechanisms, both taken from the oracle rather than inferred.

**(a) Prefix-or-not is decided per node kind.**
`postgres/src/backend/commands/explain.c`:

- `show_scan_qual` → `useprefix = (IsA(planstate->plan, SubqueryScan) || es->verbose)`
  — a scan's `Filter:` / `Index Cond:` prints **bare** column names.
- `show_upper_qual`, `show_sort_group_keys`, `show_grouping_sets`,
  `show_plan_tlist` → `useprefix = (es->rtable_size > 1 || es->verbose)` — a
  join/aggregate/result qual, a `Sort Key:` and a `Group Key:` print
  **qualified** names as soon as the query has more than one range-table entry.

That split is why a single-table query's EXPLAIN is unchanged by this work, and
why Q30's *local* `ctr_state` stays bare while its outer side does not.

**(b) A correlated reference is always prefixed.**
`ruleutils.c` `get_parameter` calls `push_ancestor_plan` and then forces
`context->varprefix = true` while deparsing a Param's expansion, with the
comment *"Force prefixing of Vars, since they won't belong to the relation
being scanned in the original plan node."* goopg's `planner.OuterColumnRef` is
exactly that Param.

Naming itself is `ruleutils.c` `set_rtable_names`: alias if written, else the
relation name, with a repeated name disambiguated as `name`, `name_1`, ….

## 3. Implementation

New file `internal/executor/explain_names.go`, plus call-site changes in
`internal/executor/operators_explain.go`. Nothing outside `internal/executor`
changed — no planner, no executor semantics, no JSON/XML/YAML renderer (those
never emitted qual expressions).

### 3.1 `explainNames` — the range-table name table

Maps `SourceTableIdx` (the per-FROM-clause id `M0071-0009` introduced) to the
printed name. Built by walking the plan tree via `planChildren` and registering
every scan-like node (`SeqScan`, `IndexScan`, `IndexOnlyScan`, `CTEScan`,
`MaterializedCTEScan`), ordered by `SourceTableIdx` so the `_N` suffixes do not
depend on which join shape the planner picked. It lives on `subPlanReg`, the
one piece of per-EXPLAIN state already threaded through every walker.

`qualify()` is upstream's `es->rtable_size > 1`, and the per-site decision in
`emitNodeDetailLines` is `reg.names().qualify() && !explainIsScanNode(n)` —
mechanism (a) above.

### 3.2 The column-membership guard (a correctness requirement, not polish)

`planner.go`'s `nextSourceIdx` **restarts at 1 for every query level**
(`planFromItem`, and the FROM loop at `planner.go:1928`). A subquery binding in
an outer scope and a base relation inside that subquery therefore carry the
**same** `SourceTableIdx`. Qualifying blindly produced, in the implementation
probe, `Filter: (a.s1 <> a.s2)` for

```sql
SELECT t.s1 FROM (SELECT a.st AS s1, b.st AS s2 FROM zq a, zq b WHERE a.id=b.id) t
WHERE t.s1 <> t.s2
```

where `s2` comes from `b`, not `a`. **A confidently wrong relation name is
strictly worse than the bare name it replaced** — it converts a merely
uninformative line into a misleading one, in a diagnostics feature whose only
purpose is to be trusted.

The guard: a qualifier is emitted only when the claimed relation actually
exposes a column of that name. The case above degrades back to `(s1 <> s2)`,
i.e. to the pre-change rendering, which is honest. Pinned by
`TestExplainDoesNotQualifyDerivedColumns`.

### 3.3 Ancestor resolution for correlated references

The name table alone does not solve Q30, and the reason is worth recording. Its
CTE body ends in an aggregate, and an aggregate's output columns carry
`SourceTableIdx 0` — "no identity assigned". Both sides of
`ctr1.ctr_state = ctr2.ctr_state` therefore arrive with nothing to look up.

`explainNames.resolveInAncestor` mirrors upstream's answer to the identical
problem: the reference is resolved against the **ancestor plan node** — the one
the `SubPlan N` subtree hangs off — by finding the single scan-like node in
that subtree exposing the column name. `subPlanReg.ancestor` is set around each
`emitSubPlanSubtrees` call, which is `push_ancestor_plan`.

The match must be **unique** or nothing is printed; the same
wrong-name-is-worse rule as §3.2.

## 4. Result, measured

Captured on the TPC-DS SF=0.5 cluster (goopg `:65437`), oracle PG 18.3
(`:65438`); plain `EXPLAIN` only, nothing executed. Arm:
`analysis/m0125-0026-timeout-plans/goopg-warm-m0125-0039/`.

| query | after | oracle | verdict |
|---|---|---|---|
| Q30 | `Filter: (ctr1.ctr_state = ctr_state)` | `Filter: (ctr1.ctr_state = ctr_state)` | **byte-identical** |
| Q81 | `Filter: (ctr1.ctr_state = ctr_state)` | `Filter: (ctr1.ctr_state = ctr_state)` | **byte-identical** |
| Q64 | `Filter: (cd1.cd_marital_status <> cd2.cd_marital_status)` | `Join Filter: (cd1.cd_marital_status <> cd2.cd_marital_status)` | expression identical; goopg has no `Join Filter:` label |
| Q72 | `Filter: ((inventory.inv_date_sk = d2.d_date_sk) AND (d1.d_week_seq = d2.d_week_seq))` | `Hash Cond: (d2.d_week_seq = d1.d_week_seq)` | correlation now legible |
| Q31 | 2 of 12 conjuncts qualified (`date_dim.d_qoy = 3`) | per-scan filters | **partial** — see §5 |

Regression tests: `internal/executor/explain_qualify_test.go` (8 cases —
upper-qual, upper-filter, correlated ref, correlated-through-aggregate,
single-relation bare, scan-qual bare, derived-column guard, `_N`
disambiguation).

## 5. What is still missing (deferral ledger, 2026-07-31)

1. **`SourceTableIdx` is not a range-table id.** It restarts per query level,
   so cross-scope collisions are possible and are only *contained* (§3.2), not
   resolved. PostgreSQL's `varno` is unique across the whole query's range
   table. Fixing this is planner work with plan-shape blast radius
   (`findColumnIndexByNameAndSource`, equivalence classes, cardinality, the
   bushy remap all key off the current values), which is why it is not in an
   EXPLAIN-only change. This is what leaves Q31 partial.
2. **`_N` suffixes are numbered over plan nodes, not over the query's range
   table.** PG's `set_rtable_names` counts RTEs the plan tree never
   materialises (pulled-up subqueries, eliminated joins), so goopg's suffix for
   the N-th duplicate can differ from PG's for the same query.
3. **VERBOSE does not force prefixing.** Upstream's `|| es->verbose` arm is not
   implemented; `EXPLAIN (VERBOSE)` on a single-relation query still prints
   bare names where PG would qualify.
4. **`Output:` lines are not qualified.** `show_plan_tlist` uses the same
   `rtable_size > 1` rule; goopg's `schemaColumnNames` prints schema names
   directly and never went through the expression printer.
5. **Nested sublinks may see the wrong ancestor.** `emitSubPlanSubtrees` drains
   its pending queue in one loop at the owning node's level, so a sublink
   discovered *inside* another sublink is rendered with the outer node as
   ancestor. The uniqueness gate makes the failure mode "prints nothing"
   rather than "prints wrongly" in the common case.

## 6. Related

- `docs/design/0125-0037-explain-set-operations.md` — the previous EXPLAIN
  legibility fix; without it Q30/Q81's subtrees were not even walked.
- `analysis/m0125-0026-timeout-plans/README.md` §"C4" — where this was filed.
- `.ralph/deferral_ledger.md`, rows dated 2026-07-31 / `M0125-0039`.
