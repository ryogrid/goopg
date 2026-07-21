# 06 — Partial and Finalize Aggregation

| field | value |
| --- | --- |
| status | draft (DESIGN ONLY) |
| date | 2026-07-21 |
| depends on | [03](03-concurrency-substrate.md), [05](05-gather-and-gather-merge.md) |

Aggregation is where parallelism pays on TPC-H: the reference capture contains
11 `Partial …` and 11 `Finalize …` aggregate nodes across its 23 plans. Q1 — a grouped aggregate over the
whole of `lineitem` — is the canonical demonstrator.

## 1. The split

An aggregate becomes two nodes with a Gather between them:

```
Finalize Aggregate            ← leader: combine partial states, apply final fn
  -> Gather / Gather Merge
       -> Partial Aggregate   ← each worker: transition over its share
            -> Parallel Seq Scan
```

The transition function runs per worker over that worker's rows. Each worker
emits, per group, its **partial state**. The leader combines partial states for
the same group and applies the final function once.

### 1.1 Plan representation

`planner.Aggregate` (`internal/planner/plan.go:885`) gains a mode:

```go
type AggMode int8
const (
    AggModeSimple   AggMode = iota // transition + final (today's behaviour)
    AggModePartial                 // transition only; emits partial state
    AggModeFinalize                // combine + final; consumes partial state
)
```

`AggModeSimple` must remain the zero value so every existing construction site
keeps today's semantics untouched.

The partial node's output schema is *not* the aggregate's result schema — it is
the group keys plus one column per aggregate carrying opaque partial state. See
§3 for what that column contains.

## 2. Decomposability: which aggregates can split

Derived from the actual transition cases in `applyAgg`
(`internal/executor/operators_join_agg.go:1553-2156`) and final cases in
`finishAgg` (`:2425-2912`). This table is the specification; anything not
listed as decomposable is refused (§5).

| Aggregate | Decomposable | Combine rule |
| --- | --- | --- |
| `count` | yes | add `count` |
| `sum` (int lane) | yes | add `sum` |
| `sum` (numeric lane) | yes | add `numericSum` |
| `sum` / `avg` (float lane) | yes, with care | add sums **and** combine `floatSpecial` by precedence (§2.1) |
| **`avg`** | **yes, already** | add `sum`/`numericSum` **and** `count`; divide only in the final step |
| `min`, `max` | yes | `min`/`max` of `value`, honouring `hasValue` |
| `bool_and`, `every`, `bool_or` | yes | AND / OR of `boolResult` |
| `bit_and`, `bit_or`, `bit_xor` | yes | apply the same bitwise op |
| `any_value` | yes | take either side |
| `var_pop`, `var_samp`, `variance`, `stddev_pop`, `stddev_samp`, `stddev` | yes, with care | §2.2 |
| `regr_count`, `regr_avgx`, `regr_avgy`, `regr_sxx`, `regr_syy`, `regr_sxy`, `covar_pop`, `covar_samp`, `regr_r2`, `regr_slope`, `regr_intercept`, `corr` | yes | plain addition of `regrN`, `regrSumX`, `regrSumY`, `regrSumXX`, `regrSumXY`, `regrSumYY` |
| `array_agg`, `string_agg` | **no** | order-dependent; see §5 |
| any aggregate with `DISTINCT` | **no** | needs global dedup; `distinct` map is per-worker |
| `WITHIN GROUP` ordered-set aggregates | **no** | needs the whole group's rows |
| user aggregate with `COMBINEFUNC` | yes | call the combine function |
| user aggregate without `COMBINEFUNC` | **no** | no combine rule exists |

**`avg` is the pleasant case.** `sum` and `avg` share one transition arm
(`case "sum", "avg":`) accumulating `sum`/`numericSum` *and* `count`, and
diverge only in `finishAgg`'s separate `case "avg"`. The (sum, count) pair PG
has to synthesise as a composite transition type is already goopg's
representation. Partial `avg` therefore needs no new state at all.

### 2.1 Float special values

`floatSpecialKind` (`operators_join_agg.go:1196-1203`) tracks NaN / +Inf / -Inf
with the documented rule that **NaN dominates**. Combining two partial states
must reproduce that precedence exactly:

- if either side is `floatSpecialNaN` → NaN
- else if one is `+Inf` and the other `-Inf` → NaN
- else if either is `±Inf` → that infinity
- else → none

This is not optional detail: getting it wrong produces a result that differs
from serial execution only on data containing infinities, which no ordinary
test would catch. [09](09-verification-and-measurement.md) requires an explicit
case.

### 2.2 Variance and the exact-arithmetic lanes

The variance family keeps **three** representations, and each combines
differently:

- **Float lane** — `floatSx` and `floatM2`. Read the field comments before
  writing the combine: `floatSx` is the **running sum of values (Σx)**, not the
  mean (`operators_join_agg.go:1238`; transition `st.floatSx += f` at `:2107`),
  and `floatM2` is the Youngs-Cramer Sxx. Therefore **`Sx` adds plainly** and
  only `Sxx` needs a correction term.

  Combine exactly as PG's `float8_combine`
  (`postgres/src/backend/utils/adt/float.c:2979-3012`):

  ```
  N   = N1 + N2
  Sx  = Sx1 + Sx2                                      // plain addition
  Sxx = Sxx1 + Sxx2 + N1*N2*(Sx1/N1 - Sx2/N2)^2 / N
  ```

  **The N==0 cases must be handled before the general case**, exactly as PG does
  (`float.c:2987-3003`): a worker producing an empty partial for a group is
  routine, not exotic, and the general formula divides by `N1` and `N2`. PG also
  raises on overflow when `isinf(Sxx)` while neither input was infinite
  (`float.c:3010-3011`); reproduce that check.

  An earlier draft of this chapter described `floatSx` as the mean and
  prescribed a Chan-Golub-LeVeque combine over means. That is wrong for this
  state representation and would have produced silently incorrect `var_*` /
  `stddev_*` results — the exact failure mode §2.1 warns about. Recorded rather
  than quietly corrected, because the mistake is an easy one to make again from
  the algorithm's name alone.

- **Exact integer lane** — `intSx` / `intSxx` (`*big.Int`), gated by the
  `intExact` flag. These are plain sums and add with `big.Int.Add`.
- **Exact rational lane** — `numericSx` / `numericSxx` (`*big.Rat`), gated by
  `numericExact`. Plain sums, add with `big.Rat.Add`.

Lanes cannot diverge between workers: the lane is selected from
`call.InputType.Name` (`operators_join_agg.go:2047-2072`), which is identical in
every worker. The real edge case is a partial state where `intSx` / `intSxx` /
`numericSx` / `numericSxx` are still `nil` because that worker saw no rows for
the group — the combine must treat nil as zero and handle `count == 0`, which is
the same N==0 concern as the float lane above.

**Variance NaN convention.** The variance lane signals NaN/Inf by setting
`floatM2 = NaN` (`operators_join_agg.go:2108-2110`) — a goopg-specific
convention distinct from the `floatSpecial` field used by `sum`/`avg`. The
combine rule is therefore its own: **NaN in either input's `floatM2` yields
NaN**. §2.1's precedence table does not cover this case.

### 2.3 `aggRuntime` has pointer fields

`aggRuntime` (`operators_join_agg.go:1205-1281`) is a fat struct containing
`numericSum Datum`, `distinct map[string]struct{}`, `arrayElems []string`,
`arrayElemKeys [][]Datum`, `intSx`/`intSxx *big.Int`,
`numericSx`/`numericSxx *big.Rat`, `userState Datum`, `withinGroupElems [][]Datum`
and `distinctUserAggRows [][]Datum`.

Two consequences:

1. **Combine is a deep merge**, not a struct add. Each pointer field needs an
   explicit rule (or a refusal, per §2's table).
2. **An `aggRuntime` handed to the leader must not be mutated by its producer
   afterwards.** Since the worker emits it at end-of-partial and then stops
   touching it, a channel send provides the necessary happens-before edge. The
   discipline is nonetheless worth stating: **no worker retains a reference to
   a partial state it has emitted.**

`Datum` fields inside a partial state are subject to the same arena rule as
rows ([03](03-concurrency-substrate.md) §3) — a `numericSum` or `value` with
`ArenaID != 0` must be promoted before crossing. This is easy to miss because
the partial state is not a `Row` and does not pass through `Materialize()`.

## 3. Transporting partial state

**goopg needs no serialisation.** PG's `aggserialfn` / `aggdeserialfn` exist
solely because an `internal`-typed transition state cannot cross a process
boundary; the worker must flatten it to `bytea` and the leader must rebuild it.

goopg hands the `aggRuntime` across a channel directly. This removes an entire
category of work — and an entire category of bugs, since PG's serialise/
deserialise pair is a classic place for round-trip mismatches.

The partial node's output column therefore carries an in-memory reference
rather than an encoded value. Two ways to express that in the existing type
system:

- a new `Datum` kind holding an opaque pointer, or
- a side-channel on the batch, keyed by group.

**Recommendation: the side-channel.** Introducing a pointer-bearing `Datum`
kind cuts directly against `perf-optimize`'s pointer-free-Datum work
([`02-datum-pointer-free.md`](../perf-optimize/02-datum-pointer-free.md)),
whose whole premise is that pointer density in the live heap drives GC mark
cost. A partial-aggregate batch is already a distinct object crossing a
distinct boundary; giving it a typed field is cheaper and does not perturb the
`Datum` layout. This is a design decision with a real trade-off and should be
re-examined during implementation if the side-channel proves awkward for
`Gather Merge`, which interleaves rows rather than forwarding batches whole.

## 4. Grouped aggregation

`aggregateOp` is **fully blocking**: `Open`
(`operators_join_agg.go:1286`) drains the child to EOF and materialises every
output row; `Next` (`:2913`) is a cursor. That is convenient here — a partial
aggregate naturally produces its whole output at the end of its share.

There is **only one implementation and it is hash-based**: `Open` builds
`groups := map[string]*groupRuntime{}` with an insertion-order slice
(`:1306-1307`), and the no-GROUP-BY case uses a synthetic `"__all__"` key
(`:1320-1321`). There is no sorted/streaming group aggregate and no hash-agg
spill.

### 4.1 An EXPLAIN label bug worth fixing here

`describePlan` (`internal/executor/operators_explain.go:1093-1097`) emits
`Aggregate` for zero group keys and `GroupAggregate (%d keys)` otherwise — but
the runtime is a hash aggregate in **both** cases. `GroupAggregate` in PG means
specifically the sorted, streaming variant.

Since this bundle adds `Partial `/`Finalize ` prefixes to these labels, it
would otherwise cement `Partial GroupAggregate` onto a node that is really a
hash aggregate. The label should be corrected to `HashAggregate` as part of
this work. That is a plan-gate-visible change affecting every grouped query,
so it is sequenced as its own step in [10](10-roadmap.md) with a snapshot
recapture, rather than being smuggled in alongside the parallel work.

### 4.2 Grouping across workers

Each worker builds its own hash table over its share, so the same group key can
appear in several workers' output. The leader's `Finalize` node re-groups by
key and combines. This is PG's model and needs no coordination — notably, no
shared hash table and no repartitioning.

Memory: N workers each hold a full hash table sized to *their share's* distinct
keys. For low-cardinality grouping (Q1 has 4 groups) this is trivial; for
high-cardinality grouping it is N× the group count in the worst case where
every group appears in every worker. Since there is no hash-agg spill, a
high-cardinality parallel aggregate is a memory-growth risk that serial
execution does not have. [08](08-planner-integration.md) uses estimated group
count as one of the gate inputs, and [09](09-verification-and-measurement.md)
requires measuring it.

## 5. Refusals

The planner refuses to split an aggregate when any aggregate call in the node
is:

- `DISTINCT` (`AggregateCall.Distinct`, `plan.go:844`) — correctness requires
  global dedup; each worker's `distinct` map sees only its share.
- `WITHIN GROUP` — ordered-set aggregates need the entire group's rows
  (`withinGroupElems`, `finishWithinGroupAgg` at `:3307`).
- `array_agg` / `string_agg` — the result depends on input order. PG treats
  these as parallel-restricted for the same reason. Note goopg keeps
  `arrayElemKeys` for the `ORDER BY` case, which makes the order-dependence
  explicit in the state itself.
- a user aggregate whose `CombineFunc` is empty
  (`internal/catalog/catalog.go:3129`).

**The refusal must be expressed as a whitelist, not a blacklist.** `applyAgg`
ends in a `default:` catch-all (`operators_join_agg.go:2141-2145`) that does
`st.count++; st.sum += arg.Int` for *any* unrecognised aggregate name. A
blacklist would let an aggregate added later split through that arm and return
garbage. Only the names listed as decomposable in §2's table may be split;
everything else refuses, including names the table does not mention at all.

Refusal means the whole aggregate stays serial — the Gather, if any, moves
below it or the plan stays serial entirely. It must never mean "split it
anyway and hope", which is precisely the failure mode that produces
plausible-but-wrong results.

`AggregateCall.Filter` (`plan.go:872`) needs no special handling: it applies
per row during transition and pushes into each worker unchanged.

## 6. Reusing `COMBINEFUNC`

`catalog.UserAggregate.CombineFunc` (`internal/catalog/catalog.go:3129`) is
commented, verbatim, "combine function name for **parallel agg**". It is parsed
by `CREATE AGGREGATE … COMBINEFUNC` (`internal/parser/ddl.go:1388`) and already
invoked (`operators_join_agg.go:2534-2543`) in the degenerate single-partial
case PG also exercises — with the existing comment explaining that PG calls the
combine function once to merge the NULL initial state with the single partial.

So the user-aggregate path needs no new catalog surface, no new DDL, and no new
executor entry point: the Finalize node calls `CombineFunc` pairwise across
partial states, which is exactly what the existing call site already does once.

## 7. Divergence from PostgreSQL

| PG | goopg | Cost |
| --- | --- | --- |
| `aggserialfn` / `aggdeserialfn` required for `internal` transition states | **Not needed at all** — `aggRuntime` crosses a channel directly | Removes a whole feature surface and its round-trip bug class; costs an in-memory partial-state representation (§3) |
| Partial state is a `bytea` in the tuple | Side-channel object on the batch | Keeps `Datum` pointer-free, consistent with `perf-optimize` |
| `aggcombinefn` in `pg_aggregate` | `CombineFunc` on `catalog.UserAggregate`, already present and already called | None |
| Built-in aggregates have hand-written combine functions in C | Combine rules specified per lane in §2 | goopg's three-lane variance state is *more* work to combine than PG's, because goopg keeps exact integer and rational lanes PG does not |
| `Partial HashAggregate` / `Partial GroupAggregate` are genuinely different nodes | One hash implementation; the `GroupAggregate` label is a misnomer to be fixed (§4.1) | Label fidelity improves; no runtime difference |

The net is strongly favourable — the single largest piece of PG's parallel
aggregation machinery (serialisation) evaporates — with one genuine
counter-example: goopg's exact-arithmetic variance lanes have no PG counterpart
and must have combine rules written for them.
