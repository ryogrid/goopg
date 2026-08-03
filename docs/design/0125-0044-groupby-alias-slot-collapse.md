# 0125-0044 — Two aliases of one table collapse onto one GROUP BY slot

**Status:** FIXED and landed 2026-07-31 (M0125-0044).
**Branch:** `tpcds-fix2`. **Acceptance:** TPC-DS Q64 `64|OK|2|31f0342ff9d55c4a`.

## The symptom, and why it was invisible for so long

TPC-DS Q64's `cross_sales` CTE joins `date_dim` three times — `d1` on
`ss_sold_date_sk`, `d2` on `c_first_sales_date_sk`, `d3` on
`c_first_shipto_date_sk` — and projects `d1.d_year as syear`, `d2.d_year as
fsyear`, `d3.d_year as s2year`. goopg and PostgreSQL agreed on the **same 26
rows** out of the 18-way join, so the join was right. What was wrong was
`syear`: goopg spread those 26 rows over 9 distinct years (1994–2002) where PG
gives 5 (1998–2002), reporting first-sales years as sold years. The outer query
filters `cs1.syear = 1999 and cs2.syear = 2000`, so the wrong years emptied a
2-row answer to 0.

Two things kept this hidden.

The first is that **the grouping was correct while the projection was not**.
`Aggregate.GroupExprs` held two properly resolved, distinct `ColumnRef`s, so the
executor built the right groups and counted the right rows — goopg emitted five
separate `1993|1993|1993` groups under `GROUP BY 1,2,3`, distinct group keys
carrying identical projected columns. Every row-count gate in this project
therefore reported green. That is the same signature `M0125-0013` found in Q47's
CTE body and the same one `M0125-0009` found for aggregate slots: *right
cardinality, wrong values* is the failure mode this codebase's gates are worst
at seeing, which is why the checksum column of the SF0.5 gate exists.

The second is that **Q64 did not complete**. It TIMEOUTed at HEAD even at a
1848 s budget, so there was no wrong answer to look at. It only became visible
after `M0125-0034`'s connectivity arm made Q64 a ~33 s query. The defect is
older than that change and provably independent of it: `alias_a.sql` and
`alias_b.sql` differ only in where `customer` sits in the FROM list — arm A
fires the reorder, arm B is a fixed point so the pass declines entirely — and
goopg's output is byte-identical and wrong in both.

## Root cause

`parserExprKey` (`internal/planner/planner.go`) deliberately drops a
`ColumnRef`'s table and schema qualifier:

```go
case *parser.ColumnRef:
	// Use only the column name (not the table/schema qualifier) so that
	// `lower(c)` and `lower(t.c)` resolve to the same GROUP BY key. M0097-0003.
	return "c:" + strings.ToLower(x.Column)
```

That is not a bug. GROUP BY `c` must satisfy SELECT `t.c`, and GROUP BY
`lower(c)` must satisfy SELECT `lower(t.c)`, and at parser level — before column
resolution — the qualifier is the only thing distinguishing them. Dropping it is
how those two requirements are met.

The price is that **every alias of a self-joined table hashes to one key**.
`buildAggregateStage` records "which output slot does this expression occupy" in
a `map[string]int` keyed on that string:

```go
groupByExpr[parserExprKey(g)] = idx
```

With `GROUP BY d1.d_year, d2.d_year` both items key as `"c:d_year"`, so the
second write **overwrites** the first and the map says slot 1 for both.
`resolveExprAfterAggregate` consults that map first, before its own correct,
index-keyed `groupByInputCol` path, so both target columns short-circuit onto
slot 1 and project `d2`'s value twice.

The bisection that isolated it is worth keeping, because three plausible
suspects were eliminated by measurement rather than by argument:

| variant | result |
|---|---|
| same query without the CTE | still wrong → the CTE is irrelevant |
| same query **without GROUP BY** | **correct** → the join and the scan layout are right |
| ordinal `GROUP BY 1,2` vs spelled-out `GROUP BY d1.y, d2.y` | identical → not the positional substitution |
| `d1.y + 0` instead of `d1.y` | also wrong → not specific to bare columns |
| plain 3-alias self-join with no aggregate | correct → not alias binding in general |

Two relations suffice. It is the aggregate surface, and nothing below it.

## The fix

`parserExprKey` is left exactly as it is — its blindness is load-bearing for
M0097-0003 and changing it would trade one silent wrong answer for another. The
fix adds a **second, qualifier-preserving key**, consulted only where the first
one is contested:

1. `buildAggregateStage` marks a key as contested (`groupByAmbiguous`) when a
   later GROUP BY item claims a key already bound to a **different** slot. The
   test is "already bound elsewhere", not "duplicate key", because `GROUP BY a,
   a` names one slot twice and is not ambiguous. `groupByInputCol` is read
   before this iteration writes to it, so the second alias reads as *unbound*
   rather than as slot 0 — the difference between detecting the collision and
   silently missing it.
2. It also records `groupByExprQual`, the same map keyed with the qualifiers
   (`qualifiedGroupKey`, `internal/planner/groupby_alias_key.go`).
3. `resolveExprAfterAggregate` consults the name key as before; when that key is
   contested it hands off to `groupBySlotContested`, which tries the qualified
   key, then — for a bare column — the input-column map, which catches the mixed
   spelling `GROUP BY y, d2.y` where SELECT `d1.y` is the unqualified item's
   column under another spelling.
4. If neither places the expression, the contested key is **abandoned rather
   than fallen back on**: `SELECT d3.y` under `GROUP BY d1.y, d2.y` falls through
   to ordinary resolution, where a functionally-determined column becomes a
   passthrough and anything else raises the 42803 PostgreSQL raises. Keeping the
   name-keyed slot would have projected a different alias's value — trading a
   diagnosable error for a silent wrong answer, which is the wrong direction for
   this milestone.

Non-`ColumnRef` keys come along for free. `qualifiedGroupKey` appends the
qualifier of every `ColumnRef` in the expression, in walk order, using a
reflective walk rather than a second hand-written switch. Two expressions with
equal `parserExprKey` have, by construction, the same tree shape, so their
`ColumnRef`s align positionally and comparing qualifier *sequences* compares
like with like. One walk therefore covers every expression type — including the
seventeen that reach `structuralExprKey` — instead of a parallel switch that
would have to be kept in step with `parserExprKey` forever. That is the
`M0125-0009` lesson applied preemptively: the class, not the instance.

### One approach measured and rejected

The obvious way to place a computed key is to resolve it and compare it
structurally against `Aggregate.GroupExprs` with `exprEqual`. It was
implemented, and it is **wrong**. `GroupExprs` are indexed against the
aggregate's *child* schema, which join reordering permutes; a freshly resolved
copy of the identical expression can therefore carry a different
`ColumnRef.Index` and read unequal. Observed directly: a `d2.y` whose group-key
twin had been remapped from index 5 to index 1, same `SourceTableIdx`, same
name, `exprEqual` false. It happened to *look* correct in a unit test, because
the first alias matched and the second silently fell back onto the name-keyed
slot the test expected. The parser-level qualified key has no such coupling — it
is decided before resolution and cannot be invalidated by a later remap. This is
recorded because the failure was invisible in the test that "passed".

## Measurement

- **Reduced repro** (`analysis/m0125-0044/`, plus `analysis/m0125-0034b/alias_a.sql`):
  every arm now matches the PG oracle on `tpcds05` column for column, including
  the computed-key variant `d1.d_year + 0`.
- **Q64**: TIMEOUT-then-MISMATCH → **PASS, 33 s, 2 rows, ck=31f0342ff9d55c4a**,
  the oracle checksum.
- **Full 99-query TPC-DS SF0.5 gate**, one binary, three chunks:
  **PASS=93 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=2 SKIP=4**. Diffed cell by
  cell against HEAD (`d50c0b4a`): **exactly one of 99 cells moved**, Q64
  MISMATCH → PASS. The remaining two timeouts (Q65, Q78) are the pre-existing
  performance class. Reports in `analysis/m0125-0044/gate/`.
- **TPC-H**: 22/22 plans MATCH the same-era baseline `m0125-0043-after`; new
  snapshot `plan_snapshots/m0125-0044-after.txt`. Inert by construction as well
  as by measurement — the new path runs only when two GROUP BY items share a
  `parserExprKey`, which no TPC-H query does.
- `scripts/tpch-spotcheck.sh` RESULT=PASS (Q12=2, Q13=35);
  `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`; planner,
  executor and parser suites.

Regression coverage is `internal/planner/groupby_alias_collapse_test.go`: the
collapse in three spellings (ordinal, explicit, computed), plus the three
properties the fix must not cost — `GROUP BY a, a` is not ambiguous, GROUP BY
`y` still satisfies SELECT `d1.y`, and an ungrouped third alias is still a
42803 rather than a wrong slot.

## What this does NOT fix

`aggregateCallKey` builds its dedup key from the same qualifier-blind
`parserExprKey`, so `count(d1.y)` and `count(d2.y)` in one select list collapse
onto **one aggregate slot**. Measured, not inferred: both targets resolve to agg
slot 0. Same class, same cause, different consumer — see the deferral ledger row
of 2026-07-31 and the follow-up fix_plan item. No TPC-DS query in the SF0.5 gate
currently exercises it, which is exactly why it needs to be written down.
