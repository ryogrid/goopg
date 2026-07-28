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
  Q66, Q28, Q46). Now the top of the value-divergence queue.
- **M0125-0011** — FULL OUTER JOIN drops all but the first ON conjunct (§6).
- The `char(n)` blank-padding rendering gap (Q50/Q62/Q99 above) remains as
  recorded by M0124-0006.
