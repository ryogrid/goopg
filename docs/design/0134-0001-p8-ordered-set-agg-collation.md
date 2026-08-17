# M0134-0001 P8 — an ordered-set aggregate's collation comes from its WITHIN GROUP key

- Status: accepted
- Date: 2026-08-17
- Milestone: M0134-0001 (`aggregates.sql` regress-case digestion)
- Slice: S20
- Supersedes nothing; sibling of `0134-0001-p2-explain-format.md` (S11/S15/S17/S18/S19)

## The divergence

`aggregates.sql` (diff hunk `@@ -2239,… @@`, 3 lines — the smallest fully
isolated hunk left in the case):

```sql
select pg_collation_for(percentile_disc(1) within group (order by x collate "POSIX"))
  from (values ('fred'),('jim')) v(x);
```

| | result |
|---|---|
| PG 18.3 | `"POSIX"` |
| goopg HEAD (`0f1670e4`) | `default` |

The explicit `COLLATE "POSIX"` on the WITHIN GROUP sort key is silently
discarded. This is a **semantics** gap, not an EXPLAIN-rendering one — the first
non-formatter slice of this milestone since S15.

## Why the obvious fix shape is wrong (the fifth time this milestone)

The natural reading — "teach `foldPgCollationFor` a case for the ordered-set
aggregate node and recurse into its `WithinGroupOrderBy`" — **cannot work**, and
a probe proved it rather than an argument. At the moment
`foldPgCollationFor` (`internal/optimizer/planner.go:11491-11550`) runs, its
argument is:

```
*optimizer.ColumnRef{Index:0, Name:"percentile_disc", Type:{Name:"text"}, SourceTableIdx:0}
```

— neither a `*parser.FuncCall` nor an `*optimizer.Aggregate`. The aggregate
stage (`buildAggregateStage`/`aggregateSurface`) has already registered the
`percentile_disc(…) WITHIN GROUP (…)` call as a computed aggregate **output
column**, so `resolveExpr`'s generic `*parser.FuncCall` case
(`planner.go:12740-12767`) rewrote the reference into a plain `ColumnRef` into
that output schema. `SchemaColumn` (`internal/optimizer/plan.go:40-44`) is
`{Name, Type, SourceTableIdx}` — **it has no collation field at all**, and the
`WITHIN GROUP` clause is not reachable from the resolved argument by any path.

The existing `*ColumnRef` case then calls `explicitColumnCollationName`, which
matches only real base-table columns (`planner.go:11572-11575`); the synthetic
name `"percentile_disc"` matches none, returns `""`, and control falls to the
base-type-name switch → `text` → `"default"`. That is exactly the observed bug.

**Therefore the interception must happen earlier**, on the *raw, unresolved*
parser expression — before the argument is resolved and the structure destroyed.
This is the load-bearing finding of the slice.

### Correction (S20 round 1): *which* resolver, not just *when*

The first draft of this design named `resolveExpr`'s `pg_collation_for`
interception (`planner.go:12787-12797`) as the site. **That site is dead code for
this query shape**, and an implementation attempt proved it (change written,
never fired, reverted):

`Plan()` routes the *entire* SELECT target list through
**`resolveExprAfterAggregate`**, not `resolveExpr`, whenever any target contains
an aggregate (`planner.go:4241-4247`). `percentile_disc(…) WITHIN GROUP (…)` is
itself an aggregate, so the enclosing `pg_collation_for(…)` wrapper goes down the
post-aggregate path too. `foldPgCollationFor` has exactly one call site in the
package — the unreachable one.

`resolveExprAfterAggregate`'s `*parser.FuncCall` case (`:7356-7409`) handles
`pg_typeof` and nothing else; its generic fallback recurses into the args, hits
the `isAggregateFunc` branch (`:7360-7383`), and returns the bare
`ColumnRef{Name:"percentile_disc", Type:text}` quoted above.

**`pg_typeof` is the exact structural precedent** — the same "a compile-time
function wrapping an aggregate loses its argument to the aggregate surface"
problem, solved in the same function, with its own post-aggregate branch at
`:7376-7381` (aggregate arg) and `:7385-7392` (non-aggregate arg), the latter
carrying the M0097-0035 note. S20 adds the `pg_collation_for` sibling beside it.
This is emphatically **not** aggregate-surface or `SchemaColumn` surgery.

## The PG rule — general, and conditional

Oracle: `postgres/src/backend/parser/parse_collate.c`.

`assign_collations_walker`'s `T_Aggref` case (`:589-617`) dispatches on
`aggref->aggkind`:

| aggkind | function | rule |
|---|---|---|
| `AGGKIND_NORMAL` | `assign_aggregate_collations` (`:880-899`) | walks `aggref->args` unconditionally through the ordinary bottom-up combine |
| `AGGKIND_ORDERED_SET` | `assign_ordered_set_collations` (`:918-943`) | **conditional** merge — see below |
| `AGGKIND_HYPOTHETICAL` | `assign_hypothetical_collations` (`:954-1054`) | pairs direct args with aggregated args; source of the 42P21 message goopg already matches |

The ordered-set rule (`:926-927`):

```c
merge_sort_collations = (list_length(aggref->args) == 1 &&
                         get_func_variadictype(aggref->aggfnoid) == InvalidOid);
```

- **merge = true** (exactly one aggregated argument, non-variadic): the sort
  column's `TargetEntry` is walked by the *generic* `assign_collations_walker`
  (`:939`), so its explicit `COLLATE` propagates up into the aggregate's own
  result collation by the ordinary combination algorithm. This is our case:
  `percentile_disc(1) WITHIN GROUP (ORDER BY x COLLATE "POSIX")` → `"POSIX"`.
- **merge = false** (2+ aggregated/ORDER BY args): each sort column instead gets
  `assign_expr_collations` called on it *in isolation* (`:941`); nothing feeds
  back to the parent. PG's own comment (`:901-916`) says this split exists
  precisely so that
  `agg(…) WITHIN GROUP (ORDER BY x COLLATE foo, y COLLATE bar)` does **not**
  throw. So multi-key conflicting collations are legal and simply do not merge.

Note there is nothing percentile_disc-specific here, and nothing
aggregate-specific in the merging itself: an ordered-set aggregate is just a
*conditional application* of the same rule that makes
`f(x COLLATE "POSIX")` — and `max(x COLLATE "POSIX")` — report `"POSIX"`.

## Design

Intercept in **`resolveExprAfterAggregate`**'s `*parser.FuncCall` case
(`planner.go:7356-7409`), as a `pg_collation_for` branch beside the existing
`pg_typeof` ones, acting on the RAW `x.Args[0]` before it is resolved. Keep the
`resolveExpr:12787` interception as-is for the non-aggregate query shapes it
already serves — the two are siblings and must agree on the rules below.

1. If the raw `x.Args[0]` is a `*parser.FuncCall` with `len(.WithinGroup) == 1`
   (`internal/parser/expr.go:496`), derive the collation by **recursing the
   existing fold** on that single sort key's expression — not by pattern-matching
   `CollateExpr` alone, so a plain column key, a `COLLATE`-decorated key and a
   type-default key all resolve through one already-tested code path.
2. `len(.WithinGroup) >= 2` → **do not merge**; fall through to today's behaviour
   (the type-name default). This is PG's `merge_sort_collations == false` branch,
   and it is the reason we must not "just always take key 0".
3. Anything else — a non-`FuncCall` arg, an unresolvable key, a shape we do not
   recognise — **declines and falls through to the pre-S20 path**. A declined
   fold returns today's answer, which is at worst the existing bug; a wrong fold
   invents a collation PG never assigned. This is the same decline-and-fall-through
   discipline that made S19 safe and that Slice 3f lacked.

**Deliberate, recorded divergence:** PG's merge condition also requires
`get_func_variadictype(…) == InvalidOid`. goopg surfaces no variadic-aggregate
predicate at this call site, so the gate is `len(.WithinGroup) == 1` alone. Every
ordered-set builtin in goopg's corpus (`percentile_disc`, `percentile_cont`,
`mode`, `rank`) is non-variadic, so the two conditions coincide today. Ledgered.

### Sibling-pair analysis

Checked explicitly, because S11/S17/S18 were each bitten by a twin emitter — and
round 1's correction revealed that **this slice does have a twin after all**:
`resolveExpr`'s `pg_collation_for` interception (`:12787`) and the new
`resolveExprAfterAggregate` one (`:7356-7409`) are the aggregate-free and
post-aggregate halves of one rule, exactly as `pg_typeof` already is in both.
They must implement the same rules 1-3 and carry reciprocal comments. Note the
milestone's pattern held once more: the twin was found by an implementation
attempt failing, not by reading.

By contrast the executor runtime path is **not** a twin: the executor path
(`internal/executor/expr.go:8294-8320`) is unreachable for this query —
`planner.resolveExpr` intercepts `pg_collation_for` unconditionally at
`planner.go:12787`, and the runtime path's own comment (`expr.go:8304-8306`)
documents it as reachable only from a non-`resolveExpr` compiler (plpgsql).
It also *could not* implement this rule if reached: it sees only the evaluated
`Datum` of the argument (`expr.go:8314`), never the parser-level clause. So it
is a structurally narrower approximation, not a diverging sibling. **Left
unchanged, deliberately.**

## Scope boundary

The plain-aggregate form `pg_collation_for(max(x COLLATE "POSIX"))` is governed
by the same general PG rule (`assign_aggregate_collations`, unconditional merge)
but has **no witness in this case's corpus**. It is therefore characterised by a
test that records goopg's actual behaviour and is *not* fixed here — shipping an
unmeasured second rule is exactly how Slice 3f grew the diff. If it diverges, it
becomes a ledger row and its own slice.

## Acceptance

- The corpus query returns `"POSIX"`; hunk `@@ -2239,… @@` closes.
- `aggregates` 956 → ~953 lines, 27 → 26 hunks.

### Measured (2026-08-17, landed)

`aggregates` **956 → 943 lines, 27 → 26 hunks** — the target hunk is gone
(`grep pg_collation_for tmp/regress-diffs/aggregates.diff` → 0 hits), and the
win beat the prediction by 10 lines because closing the hunk also collapsed its
surrounding context block. `functional_deps` **56, unchanged**.

Two corrections this slice produced, both worth carrying:

1. **The call site, not the rule, was wrong** (round 1). The rule as designed
   needed no change; only its address did. The failure mode is now named in the
   doc: a design that cites a `file:line` interception point must state *which
   resolver actually runs for the query shape in question* — goopg has two, and
   `Plan()` picks between them on "does any target contain an aggregate".
2. **The SQLSTATE was wrong** — scoping research reported the conflicting-collation
   error as 42P22; it is **42P21** (`errcodes.txt:352`). goopg already emits
   42P21 and was never wrong; only the doc was.

The `max(x COLLATE "POSIX")` characterisation subtest records goopg's actual
answer as `"default"` where PG gives `"POSIX"` — declined by design, ledgered,
and now covered by a test so the next slice starts from a measurement rather
than a guess.
- The already-passing conflicting-collation case
  (`rank('adam'::text collate "C") within group (order by x collate "POSIX")`
  → **42P21** `collation mismatch between explicit collations "C" and "POSIX"`,
  zero diff today via the hypothetical-set path) stays byte-identical.
  (Scoping research first reported this as 42P22; `errcodes.txt:352-353` gives
  `ERRCODE_COLLATION_MISMATCH` = **42P21**, 42P22 being
  `ERRCODE_INDETERMINATE_COLLATION`. goopg already raises 42P21 — correct, and
  untouched by S20.)
- Sentinel `functional_deps` stays at 56 (the only trustworthy sentinel — see the
  S18 ledger rows; `groupingsets`/`subselect` vary run-to-run on an unchanged tree).

## PG oracle citations

- `postgres/src/backend/parser/parse_collate.c:589-617` — `assign_collations_walker`, `T_Aggref` dispatch.
- `…:880-899` — `assign_aggregate_collations` (plain aggregates).
- `…:918-943` — `assign_ordered_set_collations`; merge condition at `:926-927`, generic walk at `:939`, isolated walk at `:941`, rationale comment at `:901-916`.
- `…:954-1054` — `assign_hypothetical_collations`; 42P21 pairing at `:1002-1010`.
- `…:227,474,854` — `ERRCODE_COLLATION_MISMATCH` raised by the generic combine.
