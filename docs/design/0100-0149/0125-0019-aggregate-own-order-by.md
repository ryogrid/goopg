# 0125-0019 — An aggregate's own ORDER BY decides its VALUE, and `string_agg` ignored it

Status: accepted
Milestone: M0125-0019 (discovered by M0125-0006, 2026-07-29)
Branch: `tpcds-fix2`

## The defect

```sql
select string_agg(x::text, ',' order by x) from (values (3),(1),(2)) v(x);
--  PG 18.3: 1,2,3
--  goopg  : 3,1,2
```

One row, one column, right type, right length. Nothing a row-count gate can
see — the same quiet wrong-answer class as M0125-0009 and M0125-0010, and the
reason M0124-0005 had to teach the SF0.5 gate to compare values at all.

## Why PostgreSQL's answer is the one that matters

An aggregate's `ORDER BY` is not a display clause. `nodeAgg.c` puts the
transition inputs into a tuplesort, sorts them, and only then runs the
transition function over the sorted stream
(`postgres/src/backend/executor/nodeAgg.c`,
`process_ordered_aggregate_single` / `process_ordered_aggregate_multi`). The
clause therefore participates in computing the aggregate's value, and for an
order-sensitive aggregate it fully determines it.

Corollary confirmed on the oracle: PG rejects the clause where it *would* be
presentational —

```
select string_agg(x::text,',' order by x) over () …
ERROR:  aggregate ORDER BY is not implemented for window functions
```

so there is no window path to keep in sync.

## Root cause: a sibling asymmetry inside one `switch`

`applyAgg` (`internal/executor/operators_join_agg.go`) has exactly two
order-sensitive built-in branches, adjacent in the same `switch name`:

| branch | ORDER BY keys captured? | sorted in `finishAgg`? |
|---|---|---|
| `array_agg` | yes (`arrayElemKeys`) | yes |
| `string_agg` | **no** | **no** |

`string_agg` concatenated straight into `st.strResult` in arrival order. The
clause was intact at every earlier stage — the parser keeps it
(`FuncCall.OrderBy`), M0125-0009's `funcCallTailKey` even keys the aggregate
dedup on it, and the planner resolves it into `AggregateCall.OrderBy` with
`NullsFirst` already defaulted by `sortByNullsFirst` (ASC→LAST, DESC→FIRST).
The executor branch was the only place it went unread. This is Hard-won Rule
\#2 verbatim: a green `array_agg` test proved nothing about its twin.

### The second, latent defect this closes

`planner.AggregateIsOrderSensitive` refuses a parallel plan for
`array_agg`/`string_agg`/… precisely because a `Gather` does not preserve
arrival order — but it makes an exception:

```go
if len(call.OrderBy) > 0 {
    return false   // "the aggregate sorts its own input, so that form is not refused"
}
```

That comment was false for `string_agg` until this change. A parallel plan
under `string_agg(x, ',' ORDER BY x)` was permitted on a premise the executor
did not honour, so the result could shuffle *differently on every run*. The
premise is now true.

## The fix

Deferred concatenation, mirroring what `array_agg` already did.

1. `evalAggOrderByKeys(orderBy, slot, ctx)` — factored out of the `array_agg`
   branch. A key that fails to evaluate becomes NULL rather than aborting the
   aggregate (pre-existing behaviour, preserved deliberately).
2. `aggRuntime` gains `strElems` / `strDelims` / `strElemKeys`, used **only**
   when `len(call.OrderBy) > 0`. The unordered case still accumulates into
   `strResult`, so the common path allocates nothing new.
3. `finishAgg` sorts and joins.

`aggOrderBySortedIdx(keys, orderBy)` is the single comparator now shared by
both branches — the `array_agg` block was replaced by a call to it, so the two
cannot silently diverge a second time. It is a **stable** sort: rows whose keys
tie keep arrival order, which is what PG's tuplesort yields when the sort keys
do not fully determine the order.

### The delimiter is the subtle part

`string_agg`'s delimiter is a per-row argument, so "which delimiter separates
these two pieces?" is only answerable *after* the sort. PG's
`string_agg_transfn` appends the **right-hand** row's own second argument
before that row's value, and skips it for the first row — so under an ORDER BY
the delimiters travel with their values through the permutation. Verified on
the oracle rather than assumed:

```sql
select string_agg(n, d order by n) from (values ('c','|'),('a','+'),('b','*')) v(n,d);
--  a*b|c        -- sorted a,b,c carrying +,*,| ; the first ('+') is dropped
```

Hence `strDelims` is permuted by the same index vector as `strElems`, and
`i > 0` (position after sorting), not `orig > 0`, decides whether a delimiter
is emitted.

## Acceptance — BY VALUE against PG 18.3

`internal/executor/agg_order_by_test.go`. Every `want` was captured by running
the identical statement on the read-only oracle (port 65438, role `ryo`), not
derived from goopg. 17 subtests:

- ASC / DESC / no-ORDER-BY control (arrival order must NOT become sorted);
- sort key that is a different column, a two-key mixed-direction list, and an
  arbitrary expression (`ORDER BY -x`);
- NULL sort keys under all three of ASC-default, explicit `NULLS FIRST`, and
  DESC-default;
- a NULL *value* (still skipped, and skipping must not leave a stray
  delimiter);
- the per-row-delimiter case above;
- `DISTINCT` composed with `ORDER BY`;
- empty input → NULL, not `''`;
- `array_agg` as the already-working control, guarding the shared comparator;
- per-GROUP ordering (`TestAggregateOrderByPerGroup`) — each group sorts its
  own inputs.

**Proved to fail before the fix**: 13 subtests failed at `6088e41b` while all
three controls (`no_order_by_keeps_arrival_order`, `empty_input_is_null`,
`array_agg_control`) passed there — so the matrix discriminates the fix rather
than the fixture.

## Two PG gaps discovered while testing this, deferred

The bytea branch of `string_agg` is covered by `TestAggregateOrderByByteaStringAgg`,
which asserts **order only** rather than the exact rendering, because goopg's
bytea representation diverges independently of this fix:

| statement | goopg | PG 18.3 |
|---|---|---|
| `length('\xaabb'::bytea)` | `6` | `2` |
| `encode('\xaabb'::bytea,'hex')` | `''` | `aabb` |
| `encode('abc'::bytea,'base64')` | `''` | `YWJj` |
| `string_agg(b,'\x00'::bytea order by o)::text` | `\xbb\x00\xaa` | `\xbb00aa` |

A `'\xaa'::bytea` literal is carried as the six-character *text* `\xaa`, so
`arg.Kind == KindBytes` is never true on this path and the text branch runs.
Filed as **M0125-0021** with a ledger row; freezing `\xbb\x00\xaa` into a
`want` would have cemented the wrong answer, so the test asserts that `bb`
(o=1) precedes `aa` (o=2) — which is exactly what M0125-0019 owns, and is now
PG's order.

Also recorded (ledger): `json_agg` / `jsonb_agg` / `xmlagg` /
`json_object_agg` / `jsonb_object_agg` are named by
`AggregateIsOrderSensitive` and by the parser's aggregate list, but have **no
branch in `applyAgg` at all** — so their ORDER BY is moot until they exist.

## Gates

`go test ./internal/executor ./internal/planner ./internal/parser`,
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`,
`scripts/tpch-spotcheck.sh` (Q12/Q13 canonical counts), pgbench smoke via the
pre-commit hook.

**TPC-DS cannot reach this defect**: a scan of all 100 query files under
`bench/tpcds/runtime_goopg/tpcds-data/queries/` finds zero occurrences of
`string_agg`, `array_agg`, `json_agg` or `xmlagg` — the benchmark uses only
`sum`/`count`/`avg`/`min`/`max`. Like M0125-0018 this is correctness for
hand-written SQL and the regress corpus, not a round-2 blocker, so no SF0.5
sweep is owed by this change specifically (the standing full-gate debt from the
nightly wedge is tracked in the working set).
