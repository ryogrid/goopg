# M0125-0009 — `parserExprKey`'s type-name fallback, and the structural key that replaces it

**Status:** landed 2026-07-29 (branch `tpcds-fix2`)
**Scope:** `internal/planner/exprkey.go` (new), `internal/planner/planner.go`
(`parserExprKey`, `aggregateCallKey`), `internal/planner/exprkey_test.go` (new)
**Measured against:** TPC-DS SF=1, goopg `:65436` vs PostgreSQL 18.3 `:65438`

---

## 1. The defect

goopg dedups aggregates and matches GROUP BY entries by a *string key* built
from the parse tree. `aggregateCallKey` builds the key of an aggregate call from
its name and the `parserExprKey` of each argument; `planSelect` then drops any
aggregate whose key it has already seen and points every reference at the
surviving slot (`planner.go`'s `aggByKey` map). GROUP BY matching uses the same
`parserExprKey` (`groupByExpr`).

`parserExprKey` enumerated fifteen expression node types explicitly and ended:

```go
return fmt.Sprintf("expr:%T", e)
```

That is the **Go type name** — no expression content at all. Seventeen
`parser.Expr` types shared it: `CaseExpr`, `ExtractExpr`, `InExpr`, `RowExpr`,
`SubqueryExpr`, `ExistsExpr`, `IntervalLit`, `ArrayConstructorExpr`,
`ArraySubqueryExpr`, `ArraySubscriptExpr`, `CollateExpr`, `IsBoolExpr`,
`GroupingCall`, `TypedStringLit`, `DefaultMarker`, `IndirectionStar`,
`PartitionRangeBoundKeyword`. Every instance of a given type therefore compared
**equal to every other instance of that type**.

The consequence for the commonest shape in analytical SQL — the CASE pivot:

```sql
select sum(case when d_day_name='Sunday' then 1 else 0 end),
       sum(case when d_day_name='Monday' then 1 else 0 end) from date_dim;
```

Both aggregates key to `sum|expr:*parser.CaseExpr|`. The second is discarded as
a duplicate and its output column reads the first one's slot. goopg returned
`10435|10435`; PG returns `10435|10436`.

**Row counts stay intact**, which is why it survived every row-count gate this
project has: the SF0.5 regression oracle, `ci/batch/tpcds-row-anchors.csv`, and
ten chunks of the M0124-0001 sweep all passed these queries. It was found only
once M0124-0006 compared *values* for cells that agreed on row count (protocol
rule D6a), where it accounted for **ten of the twenty-three value-divergent
queries**: Q2 Q21 Q40 Q43 Q50 Q59 Q62 Q66 Q97 Q99.

Q97 is the sharpest statement of the bug: its three columns
(`store_only`, `catalog_only`, `store_and_catalog`) are disjoint by
construction, yet goopg reported `392155|392155|392155`. Not merely wrong —
impossible.

### 1.1 Third recurrence of one failure mode

This is not a new bug, it is the third *instance* of one bug that had twice been
patched instance-wise:

| when | instance | patch |
|---|---|---|
| M0097-0003 | `ColumnRef` keyed with its qualifier, so `lower(c)` and `lower(t.c)` did not match one GROUP BY entry | added a `ColumnRef` case |
| M0097-0032 | `count(*)` and `count(*) FILTER (WHERE p)` keyed equal, so the filtered count reported the unfiltered total | added `Filter` to `aggregateCallKey` |
| M0125-0009 | all seventeen unenumerated types keyed equal | **this doc** |

Each earlier fix closed the one shape that had been observed and left the
fallback intact. That is why the deliberate design choice here is to close the
**class**.

## 2. What PostgreSQL does

Upstream has the same requirement — `transformGroupClause` and
`transformAggregateCall` must decide when two parse-tree nodes are the same
expression — and solves it with `equal()` (`src/backend/nodes/equalfuncs.c`),
which compares **every field of every node tag**. Crucially, a node tag with no
`_equal<Tag>` function is not "equal by default": `equal()` ends in

```c
default:
    elog(ERROR, "unrecognized node type: %d", (int) nodeTag(a));
```

so an unhandled type is a hard error, never a silent match. Location fields are
excluded from the comparison (`COMPARE_LOCATION_FIELD` is a no-op), which is
what lets a SELECT-list expression match its GROUP BY twin written at a
different source offset.

goopg's fallback had inverted both properties: unhandled types matched silently,
and the only thing compared was the type.

## 3. The fix

`internal/planner/exprkey.go` replaces the fallback with `structuralExprKey`, a
reflective walk over the node:

- **Type first, then every exported field, recursively.** Field names are
  written into the key, and strings are length-prefixed, so no two distinct
  trees can render to the same string by concatenation accident.
- **Unexported fields are skipped.** In this AST that is exactly the `pos int`
  source offset every node carries — the direct analogue of PG's
  `COMPARE_LOCATION_FIELD` no-op. This is load-bearing, not incidental: without
  it, `SELECT <case> … GROUP BY <case>` would stop matching and start raising a
  spurious 42803.
- **Nested `parser.Expr` values are routed back through `parserExprKey`**, so
  the hand-written normalisations (notably `ColumnRef` dropping its qualifier)
  apply at every depth rather than only at the root.
- **Determinism** is explicit: map fields are rendered in sorted order. A key
  that varied with Go's map iteration order would make dedup decisions
  irreproducible across runs of the same query.
- **Cycles** are detected by marking pointers on the recursion path and clearing
  them on exit, so a genuine cycle renders `<cycle>` while a DAG (the same node
  pointer reachable twice, which planner rewrites do produce) still renders in
  full. A depth cap of 200 is a stack backstop only.

Two explicit cases were leaking content the same way and are folded in:

- **`FuncCall`** keyed only name/star/distinct/args, dropping `FILTER`, `OVER`,
  the in-argument `ORDER BY`, `WITHIN GROUP`, and `VARIADIC`. So
  `string_agg(x, ',' ORDER BY a)` and `string_agg(x, ',' ORDER BY b)` in one
  SELECT collapsed onto one slot. `funcCallTailKey` renders that tail and is
  used by both `parserExprKey` and `aggregateCallKey` (subsuming M0097-0032's
  one-off `Filter` handling, whose rationale is preserved in the comment). It
  returns `""` when the tail is empty, so a plain call's key is byte-for-byte
  what it was.
- **`CastExpr`** dropped `Typmods`, so `x::numeric(10,2)` and `x::numeric(20,4)`
  keyed equal — `ObjectName.String()` does not render the parenthesised
  arguments.

### 3.1 Why reflection rather than a hand-written switch

A hand-written case per type is what produced the bug: seventeen types were
missing and nothing said so. Reflection makes the *default* behaviour correct,
so a type added tomorrow is keyed by its content on the day it is added. The
cost is paid only on the fallback path — the hot node types (`ColumnRef`,
`BinaryOp`, `FuncCall`, the constants) still take their hand-written cases.

## 4. The exhaustiveness gate

`exprkey_test.go` carries two tests that make the class un-reopenable:

1. **`TestExprTypeRegistryIsExhaustive`** scans `internal/parser/*.go` for
   `exprNode()` receivers and fails if `allExprTypes` (31 entries today) has
   fallen behind in either direction. Adding a `parser.Expr` type without
   registering it is a test failure, which is goopg's equivalent of upstream's
   `elog(ERROR, "unrecognized node type")`.
2. **`TestParserExprKeyUsesEveryField`** asserts, for every registered type and
   every exported field of it, that mutating the field changes the key.
   Deliberate exceptions live in `keyInsensitiveFields` with a stated reason;
   there are exactly two today (`ColumnRef.Schema` / `.Table`, per M0097-0003),
   and the test also fails if an exemption goes *stale*.

Run against the pre-fix key, test 2 reports **40+ field-level collapses** across
`ArrayConstructorExpr`, `CaseExpr`, `CollateExpr`, `ExistsExpr`, `ExtractExpr`,
`InExpr`, `IntervalLit`, … — the full blast radius, enumerated mechanically
rather than guessed.

The remaining behavioural tests pin both directions of the contract: three
different `sum(CASE …)` get three slots; two *identical* ones still get one (a
fix that stopped deduping would trade a wrong answer for redundant work and a
spurious 42803); `GROUP BY <case>` still matches its SELECT-list twin.

## 5. Measurement (SF=1, `:65436` vs `:65438`)

Flat reproducer, three buckets: goopg `10435|10436|10436` = PG. (Pre-fix:
`10435|10435`.)

All ten queries in the M0124-0006 evidence set were re-run against PG:

| query | before | after |
|---|---|---|
| Q2 | value-divergent | **byte-identical to PG** |
| Q40 | value-divergent | **byte-identical to PG** |
| Q43 | value-divergent | **byte-identical to PG** |
| Q59 | value-divergent | **byte-identical to PG** |
| Q50 | 5 identical buckets | **values identical to PG** (`67\|48\|61\|66\|98`, …) |
| Q62 | value-divergent | **values identical to PG** |
| Q99 | `1231\|1231\|1231\|1231\|1231` | **`1231\|1228\|1289\|0\|0` = PG** |
| Q97 | `392155\|392155\|392155` | collapse gone (`392155\|177135\|1553910`); **still diverges — new defect, §6** |
| Q21 | `1516\|1516` | still `1516\|1516` — **M0125-0010**, see §5.1 |
| Q66 | 34 replicated columns | still replicated — **M0125-0010**, see §5.1 |

Seven of ten are now value-correct. Q50, Q62 and Q99 differ from PG only by
`char(n)` blank-padding, the answer-neutral rendering gap already recorded by
M0124-0006 (fix_plan line 925).

### 5.1 Q21 and Q66 are M0125-0010, not a partial fix

Both wrap their aggregates in a **FROM-subquery**:
`select * from (select … sum(case …) inv_before, sum(case …) inv_after … ) x`.
M0125-0009 makes the inner aggregates distinct — and they now occupy distinct
slots — but `remapSubqueryColumnRefs` (`planner.go:2450`) rebinds every
`Project` target of a FROM-subquery by matching the **column name** against the
child schema, and an `Aggregate` names its outputs after the function, so two
`sum` outputs both bind to the first slot. That is M0125-0010, filed
independently on 2026-07-29 with a `CASE`-free reproducer. The two defects
compose: Q21 and Q66 need both fixes, which is exactly what the fix_plan's "do
not confuse this with M0125-0010 / neither subsumes the other" note predicted.

## 6. Discovery: Q97's residual gap is a FULL OUTER JOIN defect (new)

With the collapse gone, Q97 still disagrees (`392155|177135|1553910` vs PG's
`541140|286927|161`). Isolating it on the SF=1 clusters:

| probe | goopg | PG |
|---|---|---|
| `count(*)` of each CTE (`ssci`, `csci`) | `548694 / 287769` | `548694 / 287769` |
| `ssci JOIN csci ON (customer_sk AND item_sk)` | `161` | `161` |
| `ssci FULL OUTER JOIN csci ON (customer_sk)` | `2131274` | `2131274` |
| `ssci FULL OUTER JOIN csci ON (customer_sk AND item_sk)` | **`2131274`** | **`836302`** |

The inputs agree, the inner join on *both* keys agrees, and the single-key full
outer join agrees exactly. Only the two-conjunct FULL OUTER JOIN diverges — and
it returns precisely the single-key number, so **goopg drops all but the first
conjunct of a FULL OUTER JOIN's ON condition**. PG's `836302` is
`548694 + 287769 − 161`, the arithmetic identity for a full outer join with 161
matches, which confirms the reference side.

(The `sum(case …)` total is `8074` below the row count on *both* engines: rows
where both sides' `customer_sk` are NULL match none of the three CASE arms. Not
a defect.)

Filed as **M0125-0011** with a deferral-ledger row. It is a wrong answer with
the row count *not* intact, so unlike M0125-0009 the SF0.5 gate can see it.

## 7. Risk and gates

The key participates in aggregate dedup and GROUP BY matching, so the change is
plan-shaping: strictly *more* aggregate slots than before, never fewer. The two
failure directions are covered by tests in opposite directions (§4).

Gates run for this commit: planner / analyzer / parser / executor unit suites,
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`,
`scripts/tpch-spotcheck.sh` (planner change — canonical Q12/Q13 row counts), the
TPC-DS SF0.5 regression gate, and the mandatory pgbench smoke in the pre-commit
hook. See the commit message for the recorded outcomes.

## 8. Follow-ups

- **M0125-0010** — FROM-subquery `Project` remap binds by function name (Q21,
  Q66, Q28, Q46). Now the top of the value-divergence queue. **Landed
  2026-07-29 — see §9 below.**
- **M0125-0011** — FULL OUTER JOIN drops all but the first ON conjunct (§6).
- The `char(n)` blank-padding rendering gap (Q50/Q62/Q99 above) remains as
  recorded by M0124-0006.

---

## 9. M0125-0010 — the same failure mode, one layer down: the FROM-subquery remap

**Status:** landed 2026-07-29 (branch `tpcds-fix2`)
**Scope:** `internal/planner/planner.go` (`remapSubqueryColumnRefs`),
`internal/planner/subquery_remap_test.go` (new)

This section lives here rather than in a doc of its own because it is the *same
defect class* as §1 — **an ambiguous key resolved by taking the first match** —
and because the two defects **compose**: Q21 and Q66 need both fixes, so neither
can be graded by "does the query match PG" alone.

### 9.1 The defect

`remapSubqueryColumnRefs` is a repair pass, called from exactly one place —
`planSubqueryRangeVar`, after a FROM-clause subquery is planned. Its stated job
(M0097-0058) is to undo *outer resolve-context leakage*: a sub-SELECT's
`Project` may reference a column by its **global FROM-clause index** (e.g. 57)
instead of by the subquery's own output index, which is an index-out-of-bounds
crash at execution time.

It did that job by rebuilding **every** bare-`ColumnRef` target from scratch:
match `cr.Name` against the child output schema, take the first hit, `break`.

The key it matched on — a schema column's *name* — is not unique. An
`Aggregate` names its output columns after the aggregate **function**, so

```sql
select * from (select sum(d_dom) a, sum(d_year) b from date_dim) d;
```

plans an `Aggregate` whose output schema is literally `[sum, sum]`. Both
`Project` targets found "the first column named `sum`" and bound to slot 0:

| engine | result |
|---|---|
| goopg (before) | `1149021|1149021` |
| PostgreSQL 18.3 | `1149021|146061700` |

The pre-remap indices were **already correct** (0 and 1) — verified by dumping
the plan with the pass disabled. The pass was not repairing damage; it was
*causing* it. The identical flat query (`select sum(d_dom), sum(d_year) from
date_dim`) is correct, because the pass only runs for FROM-subqueries.

`count(x)` vs `count(distinct x)` collapse for the same reason: `DISTINCT` does
not change the output column name.

### 9.2 Why row-count gates are blind to it

The collapse replaces one column's *values* with another's. Cardinality,
grouping, and join structure are untouched, so `scripts/tpch-spotcheck.sh` and
the TPC-DS SF0.5 row-count oracle both pass with the defect present. This is the
same blind spot M0124-0005 quantified: 18 of 99 SF=1 queries pass a
row-count-only gate while answering wrongly.

### 9.3 The fix — repair conditionally, don't rebind unconditionally

The pass now **verifies before it repairs**. For each bare-`ColumnRef` target:

1. If `cr.Index` is in range of the child schema **and** the column it addresses
   has the name the ref asks for, the index is already sound — leave the target
   alone. This is the only branch that can tell two same-named child columns
   apart, so it must run *before* any name-based search.
2. Otherwise the index is broken — out of range, or naming a different column,
   which is precisely the leakage signature the pass exists for. Re-derive it by
   name as before. A duplicate name on this path is genuinely ambiguous; first
   match is a best effort on a plan that is already wrong.

Note the alternative that looks obvious and is not: a *positional* remap
(target `i` → index `i`), which the pass's own doc comment claimed to implement.
That is wrong for any `Project` that reorders or subsets its child's columns
(`select b, a from t`), and would have traded a value bug for a much broader one.

### 9.4 Gate

`internal/planner/subquery_remap_test.go`, three tests:

- **`TestSubqueryRemapKeepsSiblingAggregateSlots`** — the reproducer above:
  sibling `sum()` targets must bind to slots `[0 1]`, not `[0 0]`.
- **`TestSubqueryRemapControlMatrix`** — six shapes probed against PG when the
  defect was isolated, including the ones that were *already* correct (distinct
  function names, three distinct functions) so a future change cannot regress
  them while keeping the reproducer green. Against the old code, four of the six
  fail — and `group by with sibling sums` fails as `[0 1 1]`, a *partial*
  collapse, which is the shape's own signature.
- **`TestSubqueryRemapStillRepairsLeakedIndices`** — the M0097-0058 guard. The
  narrowed pass must still rebuild an out-of-range index (57) and an in-range
  index that names a different column. Without this test the fix could be
  "simplified" into deleting the pass, reintroducing the original crash.

### 9.5 Measurement (SF=1, `:65436` vs `:65438`)

All six queries that carried the defect, re-run after the fix. Values compared
byte-for-byte, and again with `char(n)` padding normalised (that padding gap is
a separate, already-recorded rendering defect — M0124-0006):

| query | before | after |
|---|---|---|
| reproducer | `1149021|1149021` | **byte-identical to PG** |
| Q21 | `1516|1516` (PG `1516|2833`) | **byte-identical to PG** |
| Q28 | `count`/`count distinct` pair wrong in all six blocks | **identical mod `char(n)` padding** |
| Q46 | `profit` = `amt` | **identical mod `char(n)` padding** |
| Q66 | 34 replicated columns in 5 rows | **identical mod `char(n)` padding** |
| Q68 | `extended_tax` and `list_price` both = `extended_price` | **identical mod `char(n)` padding** |
| Q79 | `profit` = `amt` | **identical mod `char(n)` padding** |

Q21 and Q66 are the two that needed **both** fixes — §3's structural expression
key to give the sibling `sum(CASE …)`s distinct aggregate slots, and §9.3 to
stop the remap re-collapsing those now-distinct slots. Q66 is the widest blast
radius on record for either defect.

Artifacts: `analysis/m0125-0010-acceptance/`.

### 9.6 The pattern, stated once

Three defects now share one shape — M0097-0003, M0097-0032, §1, and §9:

> A lookup key that is not unique, resolved by silently taking the first match.

In every case the correct fix was to make the *matching* sound (structural key;
verify-then-repair), never to make the names unique. When adding a pass that
looks something up by name in a schema, the question to ask first is: **can two
columns here have the same name?** For any `Aggregate` output the answer is yes.
