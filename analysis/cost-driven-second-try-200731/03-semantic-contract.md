# 03 — The semantic contract of runtime fusion

**The governing rule of this chapter:** a fused pipeline must be *observationally
indistinguishable* from the unfused cascade of `joinOp`s it replaces, for every observable
the server exposes — result rows and their order, errors and which row raises them, notices,
cancellation latency, temp-file usage, `pg_stat_*` counters, and EXPLAIN output. "Same rows,
different order" is a **failure**, not a nuance, because ORDER BY-less TPC-H/TPC-DS anchors
and the regress suite compare textual output.

Each clause below is written so that a test can be derived from it directly; the derived
tests are listed in [09](09-staged-implementation-plan.md).

## C1 — Row ORDER must be preserved exactly

The unfused cascade emits, for probe row *p*:

```
for m0 in H0[key0(p)]:           # level 0 = the bottom-most (innermost) join
    if residual0: continue
    for m1 in H1[key1(p,m0)]:    # level 1
        if residual1: continue
        …
            emit (p, m0, m1, …)
```

because each level's `nextLazy` fully drains `o.lazyMatches` for the current probe row before
pulling the next one (`operators_join_agg.go`, the `for o.lazyActive && o.lazyMatchIdx <
len(o.lazyMatches)` loop), and the parent's probe order *is* the child's emit order. Within a
bucket, `lazyHash[k]` / `lazyIntHash[k]` is an append-ordered `[]Row`
(`lazyHashInsertDatum`, `operators_join_agg.go:975-995`), so match order is build-input
order.

**Contract:** the fused odometer must have level 0 = the *innermost* (deepest-left) join and
the last level varying fastest, and must iterate each level's bucket slice in append order.

**This is where today's MHJ operator would be wrong if lifted naively.** MHJ derives its step
order by BFS from the probe table over `plan.Keys` (`multi_hash_join.go:126-165`), which is
*not* the plan's cascade order, and it sorts its output columns by catalog OID
(`bushy.go:1760+`). A fused operator must **not** re-derive an order; it must take the plan's
level order verbatim.

## C2 — Output COLUMN order must be the top join's `Output()`, unchanged

MHJ re-orders columns (OID sort) and then needs `buildMHJPosMap` (`bushy.go:2348`) and
`remapKeyToLayout` (`bushy.go:1490`) to repair every consumer. That machinery is the origin
of the stale-permutation class of bug recorded at `internal/planner/plan.go:869-886`.

**Contract:** `fusedHashJoinOp.Schema()` returns the topmost fused `*planner.Join`'s
`Output()`, byte-identical, and column *i* of the emitted slot is the same value the unfused
cascade would have put at column *i*. No sorting, no remapping, no name resolution anywhere
in the fusion path. See [04 §4](04-fusion-site-and-data-structures.md) for the structural
assertions that enforce this — note that the **width** identity alone is insufficient (review
finding F1: `Join.Output()` is a cached schema that can go stale as a same-width permutation),
so the **element-wise** column-identity check is the load-bearing one.

## C3 — Only INNER equijoins are fusable; everything else is excluded

Excluded, unconditionally, at every level:

- `JoinTypeLeft`, `JoinTypeRight`, `JoinTypeFull` — null-padding is stateful per probe row
  (`nextLazy` tracks `lazyProbeMatched` precisely because a hash hit that fails the residual
  must still count as "no match", `operators_join_agg.go:60-66` and the
  `if o.lazyActive { … }` block). A fused odometer would have to track per-level "no match"
  padding, which multiplies the semantics and the bug surface.
- `JoinTypeSemi`, `JoinTypeAnti` — emit the outer row only, at most once
  (`plan.go:869-886`), and `buildLeft` is force-cleared for them
  (`operators_join_agg.go:~545`).
- `NullAware` anti (`plan.go:852-863`) — three-valued `NOT IN`, short-circuited on build-side
  flags in `nextLazy`.
- `Lateral` (`plan.go:846-851`) — the right side is driven per outer row.
- `Algo != JoinAlgoHash` — merge and nested-loop have entirely different materialisation.
- `UsingLeftCols != nil` — FULL JOIN USING coalescing (`plan.go:836-845`).

**Contract:** the fusion predicate is a whitelist, not a blacklist. A level joins the fused
set only if it is `Type == JoinTypeInner && Algo == JoinAlgoHash && !Lateral && !NullAware &&
UsingLeftCols == nil && UsingRightCols == nil` and its keys pass
[05](05-qualification-predicate.md). Any new field added to `planner.Join` in the future must
be added to this whitelist check or the fusion must decline — enforced by the
struct-field-count assertion in [05 §6](05-qualification-predicate.md).

## C4 — Residual predicate evaluation order and *count* must be preserved

Each `joinOp` evaluates `joinPredicateMatchSlot` on **every** hash-bucket candidate before
emitting (`nextLazy`, immediately after `o.lazyBuildSlot.row = m`). The comment there records
why: `pushOneConjunct` ANDs extra residual conjuncts onto `Predicate`, and dropping the
post-hash filter makes the join over-emit.

Two observables depend on this:

1. **Which rows survive** — obviously.
2. **Which errors are raised, and in what order.** A residual can raise (`1/0`, a cast
   failure, a range check). If the fused operator hoists a level-2 residual above a level-1
   match, an error can surface on a row the cascade would have filtered out first, or vice
   versa. This is a *visible semantic change* even when the row set is identical.

**Contract:** the fused operator evaluates level *L*'s residual at exactly level *L*, after
that level's hash match is bound and before descending to level *L+1*, on every candidate,
in bucket order. It does **not** adopt MHJ's `partitionFilters` re-partitioning
(`multi_hash_join.go:~300-400`) — that pass exists to recover pruning MHJ lost by flattening,
and the fused operator does not lose it, because the cascade's structure already puts each
residual at its own level.

A residual containing `OuterColumnRef`, `SubqueryExpr`, `ExistsExpr`, `InExpr`,
`MultiAssignSubqElem` or `MultiAssignSubqRow` (the disqualifying set in
`walkColumnRefs`, `multi_hash_join.go:~330-350`) **disqualifies fusion for that level and
every level above it**. MHJ routes them to `leafFilters`; the fused operator fails closed
instead, because subquery evaluation may depend on the executor context in ways the odometer
does not reproduce.

## C5 — NULL join keys must not match

`evalHashKeyDatum` returning `ok == false` skips insertion on the build side and skips
lookup on the probe side (`operators_join_agg.go:~600`, `~665`, and the probe path in
`nextLazy`). A NULL key participates in no INNER match. The int64 fast path preserves this:
`datumToInt64Key` failing on the probe side yields no match, which is sound only because the
table is all-int64 — the comment at the int64 probe branch states exactly this.

**Contract:** the fused operator reuses `datumToInt64Key` / `lazyHashInsertDatum` verbatim,
and reuses `evalHashKeyDatum`'s **body** through a slot-taking `evalHashKeyDatumSlot`
extracted in Stage 0b (review finding F11 — the current signature takes a `Row`, so it cannot
be called as-is from the slot-based odometer; extract, do not copy). It does not reimplement
key hashing anywhere. Reuse, not
reimplementation, is the only defence against a divergence in the 48-byte `Datum`'s
cross-kind comparison rules (cf. `analysis/0038-fix-compareDatum-cross-kind.md`).

## C6 — The int64 fast path is INNER-only and stays that way

`buildLazyHashTable` opts into `lazyHashInsertDatum` only for `JoinTypeInner`
(`operators_join_agg.go:~670-685`); semi/anti keep the string map. Since C3 restricts fusion
to INNER, the fused operator may always attempt the int64 path — but must fall back per
level, exactly as `lazyHashInsertDatum` does when the first non-int64 key arrives
(`operators_join_agg.go:975-995`), and must **not** assume a single global decision across
levels.

## C7 — Cancellation latency must not regress

Current guarantees:

- per `Next()` call: `ctx.Ctx.Err()` at the top of `nextLazy`.
- per 4096 build rows: `if buildCount&0xFFF == 0` in both build loops
  (`operators_join_agg.go:~570` and `~628`), with the recorded reason that a 6M-row build
  "runs minutes; without this check the cancel-after deadline can be exceeded by 100+ s".
- `drainRowsCtx` checks every 1000 rows (`operators_join_agg.go:3355`).

**Contract:** the fused operator checks `ctx.Ctx.Err()` (a) once per `Next()`, (b) every 4096
rows in every level's build loop, and (c) additionally **every 4096 odometer steps**, because
a deep odometer can spin without emitting (a probe row whose deepest level has no match after
a large fan-out at shallower levels). Clause (c) is *new* — neither the cascade nor MHJ has
it, and it is a genuine improvement, not a compatibility break.

## C8 — Spill must be preserved per level

Per [02 §7](02-premise-audit.md): the cascade spills via `drainRowsBounded` (`spill.go:342`)
at `ctx.WorkMem`; MHJ does not spill at all (`drainRowsCtx`).

**Contract:** every fused level drains its build side with `drainRowsBounded(child,
ctx.WorkMem)` and honours the returned spill-backed `Operator` exactly as
`buildLazyHashTable` does. Each level gets its **own** budget, matching PG, where `work_mem`
is per hash node, not per query. Fusing k joins must therefore never reduce total permitted
build memory below the unfused cascade's `k × work_mem`, or a plan that ran before will
suddenly spill (a performance regression, and a temp-file-count observable).

## C9 — FOR UPDATE / eval-plan-qual must disable fusion

`lockRowsOp.Open` sets `joinOp.preserveCTIDRel` **before** the child's `Open`
(`operators_join_agg.go:105-118` doc comment), so the build side can capture heap ctids
(`buildHashRightWithCTID`, `:699`). This is decided at **Open** time, whereas fusion is
decided at **Build** time ([04](04-fusion-site-and-data-structures.md)).

**Contract:** at build time, if a `*planner.LockRows` appears anywhere in the plan tree,
fusion is disabled for the whole plan. This is a coarse, cheap, fail-closed rule; FOR UPDATE
result sets are small by design (the comment at `:693-698` says so), so nothing of value is
lost.

## C10 — Parallelism: decline, and decline in a way that actually fires

> **Strengthened after review (finding F4).** The first draft said "decline if the plan
> contains a `Gather`". That check **cannot fire in the case it targets**: `Gather` builds
> each worker's tree through a closure `func() (Operator, error) { return Build(p.Child) }`
> (`internal/executor/executor.go:213-219`), so inside a worker build the visible root is
> `p.Child`, which by construction contains no `Gather`.
>
> **Corrected rule:** the exclusion must be a *positive* signal set by the builder that is
> constructing a worker subtree (a field on the build environment of
> [04 §2](04-fusion-site-and-data-structures.md), set by `newGatherOp`'s closure), not a plan
> walk. Additionally, `prebuildSharedHashJoins` discovers shareable builds by walking the
> **built operator tree** for `*joinOp` (`internal/executor/parallel_hash_build.go:119-150`);
> a `fusedHashJoinOp` is invisible to it, so the leader would publish nothing while
> `planner.HasShareableHashJoin(plan)` still reports true. Either add a
> `fusedHashJoinOp` case there, or assert that fusion and shared builds never coexist.



`ctx.SharedHashBuilds` is a `map[*planner.Join]*sharedHashBuild`
(`internal/executor/parallel_hash_build.go:95-100`); the leader pre-builds a shareable hash
table and publishes it before worker fan-out (`prebuildSharedHashJoins`, `:119+`). A worker's
`joinOp.openLazyHashJoin` adopts it instead of rebuilding.

`parallel_hash_build.go:104-109` warns that the probe-side rule is duplicated in three places
and "a disagreement puts the parallel scan on the BUILD side, where each worker would build a
partition of the table and the join would silently lose rows."

**Contract (Stage 1-2):** fusion declines if the plan contains a `*planner.Gather` or
`*planner.GatherMerge` anywhere above or below the candidate cascade. Adopting shared builds
inside a fused operator is a separate, later design; doing it in the first cut would add a
fourth copy of the rule this comment already calls dangerous.

## C11 — EXPLAIN must be unchanged; EXPLAIN ANALYZE must not lie

See [06](06-explain-and-plan-shape.md). Summary of the contract:

- `EXPLAIN` (no ANALYZE): identical text with fusion on or off, because the plan tree is
  never rewritten.
- `EXPLAIN ANALYZE` with timing on: **fusion is disabled**, so per-node `actual time` is real.
- `EXPLAIN (ANALYZE, TIMING OFF)`: fusion stays on; per-node `rows` are reported exactly by
  level.

## C12 — Instrumentation identity

`maybeInstrument` keys stats on the plan node (`internal/executor/instrument.go:241-256`,
`instrumentScope.table[plan] = stats`). A fused operator spans k plan nodes. Under C11 the
timing case is excluded, so the fused operator only ever has to fill **row counts** for the k
nodes. It must register a `nodeStats` for each of the k `*planner.Join` nodes and increment
level *i*'s counter exactly when it emits a tuple that has passed level *i*'s residual — which
is the same number the unfused level-*i* `joinOp` would report.

## C13 — No observable resource-accounting change

Temp files (from C8), buffer reads, and `RowsAffected` must match.

> **Corrected after review (finding F5).** The first draft of this clause instructed the fused
> build **not** to deep-copy build rows, on the false premise that `drainRowsBounded` does not.
> It does: `internal/executor/spill.go:388-399` copies every retained row
> (`cloneRowOwned` when `rowHasArena(row)`, otherwise `make`+`copy`). That copy is a
> **correctness requirement**, not an inefficiency — without it a hash-table entry aliases a
> producer's reused buffer (`seqScanOp`'s `o.scanRow` is reused and released,
> `internal/executor/operators_storage.go:1361`), which is the M0097-0058 corruption class.
> Following the original clause would have introduced a silent wrong-answer bug.
>
> **The corrected contract:** the fused build uses `drainRowsBounded` and inherits its copy
> semantics **verbatim**. Do not "optimise away" the copy.

## C14 — Fail closed, always

Every predicate in [05](05-qualification-predicate.md) and every assertion in this contract
resolves to one of two outcomes: **fuse**, or **build the ordinary cascade**. There is no
third outcome. There is no partial fusion of a level, no "fuse and null-pad the rest", no
best-effort. A structural surprise (an unexpected width, an unresolvable key, an unknown
node type) must produce the cascade — the path that is already covered by 100 % of the
existing gates.

This clause exists because the current MHJ packer does the opposite: `collectMultiHashTables`
silently drops a key whose scan cannot be resolved (`bushy.go:1596-1604`, `if li >= 0 && ri
>= 0`), never asserts `len(keys) == len(scans)-1`, and the executor's `keySteps` BFS simply
`break`s when it can make no further progress (`multi_hash_join.go:158-163`), leaving the
unreached tables' slots pinned to `o.nulls[i]` forever. The result is a silently NULL-padded
column set and a silently dropped join predicate. See [08 R1](08-risk-register.md).

## C15 — `Open()` must be re-entrant (rescan)

`internal/executor/subplan.go:223-230` classifies a `*planner.Join` inside a SubPlan as
`rescanCloseOpen` — the operator is `Close`d and `Open`ed again **per outer row**. Other
re-Open sources: cursors/portals, `RecursiveUnion`/`WorkTableScan`, `Memoize`, and
prepared-statement plan reuse across executions.

**Contract:** `fusedHashJoinOp.Open` must fully reset `active`, `probeEOF`, every level's
`cursor`, `matches`, `slot.row`, and the hash tables, and must rebuild each level's hash
table from scratch — matching the unfused cascade, where each level's `joinOp.Open` rebuilds.
An `Open` that reuses stale odometer state from a previous execution emits rows from the
previous outer row: a silent wrong-answer bug of the worst kind.

`Close` must close every level's `buildOp` and the probe op, and must be safe to call after a
failed `Open` (partial construction), because `Build`'s error paths call `Close` on already
built children (`executor.go`, the `SetOp` arm shows the pattern).

Stage 1 test: `TestFusedCascadeRescan` — a fused cascade under a correlated SubPlan, asserting
identical output fused and unfused.
