# 05 — The qualification predicate

`tryFuseHashCascade(p *planner.Join, root planner.Node) (Operator, bool)` returns `false` —
and the caller builds the ordinary cascade — unless **every** clause below holds. There is no
partial fusion (contract C14).

## Q0 — Global preconditions (checked once per `Build`, memoised on `buildEnv`)

All of these need the `buildEnv` plumbing of
[04 §1.1](04-fusion-site-and-data-structures.md) — `Build`/`buildRec` do not receive a root
or a session today (finding F3).

| clause | reason |
| --- | --- |
| the fusion kill switch is on | [10](10-rollback-and-kill-switches.md); process/env-scoped, **not** session-scoped (F3) |
| `env.root` contains no `*planner.LockRows` | C9: `preserveCTIDRel` is set between Build and Open |
| **`!env.inWorker`** — a positive flag set by `newGatherOp`'s per-worker closure | C10/F4: a *plan walk* for `Gather` cannot fire, because the worker closure calls `Build(p.Child)` (`executor.go:213-219`) and `p.Child` contains no `Gather` |
| `env.root` contains no `*planner.Gather` / `*planner.GatherMerge` | still checked, for the leader-side build; necessary but not sufficient — see the row above |
| `planner.HasShareableHashJoin(env.root)` is false, **or** `collectShareableJoins` has been taught a `fusedHashJoinOp` case | F4: `prebuildSharedHashJoins` walks the built **operator** tree for `*joinOp` (`parallel_hash_build.go:119-150`) and would silently find nothing |
| this build is not under instrumentation with timing on | C11/C12 — **see the caveat below** |
| `env.root` contains no `*planner.MultiHashJoin` | belt and braces during the transition: never mix the two strategies in one plan. **Note the Stage-2 consequence in [09](09-staged-implementation-plan.md): with packing still on by default, this clause makes fusion decline on exactly the queries that would benefit** (finding F12) |

Walking `env.root` is cheap (plans are small) and must be done **once per `Build`**, memoised
on `buildEnv`, not once per join (risk R11).

> **Instrumentation caveat (finding F8).** `instrumentScope` is a mutable package-level global
> (`internal/executor/instrument.go:215`), set and restored by `withInstrumentation`
> (`:225-233`) with no lock. Reading it from the fusion predicate means a concurrent session
> running `EXPLAIN ANALYZE` can silently flip fusion on or off in *other* sessions' plan
> builds — non-deterministic behaviour and a data race the repo's `make race-gate` can flag.
> The correct predicate is **"is *this* build under instrumentation"**, which must be a field
> on `buildEnv` set by `explainOp.Open`'s build call, not a read of the global.

## Q1 — Chain depth

Walk left-only from `p`, collecting levels while the node is a fusable `*planner.Join`.
Require `k >= 3` levels *joined at once* — i.e. at least 3 hash joins over 4 inputs, or
`k >= 2` if measurement in Stage 2 shows a 2-level win.

Rationale for starting at 3: at `k == 1` fusion is a no-op; at `k == 2` the saving is one
`VirtualSlot.Row()` materialisation per row, which Stage 0a removes anyway. Starting the
threshold high keeps the blast radius small during rollout. It is a tunable
(`goopg_runtime_join_fusion_min_levels`), not a constant.

## Q2 — Per-level node whitelist

For each level *i* (`j := levels[i].plan`):

```go
j.Type == planner.JoinTypeInner &&
j.Algo == planner.JoinAlgoHash  &&
!j.Lateral                      &&
!j.NullAware                    &&
j.UsingLeftCols  == nil         &&
j.UsingRightCols == nil         &&
j.LeftKey  != nil               &&
j.RightKey != nil               &&
!j.BuildLeft
```

`!j.BuildLeft` is essential: `BuildLeft == true` means the *left* subtree is drained into the
hash table (`operators_join_agg.go:~560-600`), which destroys the pipelined-probe premise for
that level. A `BuildLeft` level ends the chain. (It does not disqualify the plan — the chain
simply stops below it, and if the remaining chain is still `>= Q1` levels, the shorter chain
is fused.)

Field-by-field justification is in [03 C3](03-semantic-contract.md).

## Q3 — Key shape

Both `j.LeftKey` and `j.RightKey` must be `*planner.ColumnRef`.

Expression keys are excluded in the first cut for one reason only: `evalHashKeyDatum` on an
expression may invoke arbitrary function machinery whose interaction with the shared
`VirtualSlot` (whose sources mutate under it) has not been audited. A `ColumnRef` is a pure
read. This restriction matches today's MHJ predicate (cost-model doc 15 §2 clause 3) and can
be relaxed later behind its own measurement.

Additionally: `j.LeftKey.Index` must fall in `[0, offset(level i))` — i.e. inside the
already-bound prefix — and `j.RightKey.Index` must fall inside level *i*'s own right-side
width range. This is the fused analogue of "the probe key must come from the accumulated
row", and it is a pure arithmetic check enabled by [04 §4](04-fusion-site-and-data-structures.md).

> **Coordinate-space caveat — revised after review (finding F9).**
> `joinOp` evaluates both keys against a merged `leftWidth+rightWidth` scratch row
> (`nextLazy`'s `lazyKeyRow`, `operators_join_agg.go:1219-1232`; the build loops at `:653-659`),
> and the DP explicitly shifts the right key into that space
> (`internal/planner/bushy.go:1391-1396`, `RightKey.Index += len(leftSchema)`). So the merged
> space holds **for the DP-built cascade**.
>
> But it is **not a global invariant of `planner.Join`**: `internal/planner/unnest.go:2107`
> constructs a `JoinTypeInner`/`JoinAlgoHash` node with `RightKey: &ColumnRef{Index: 0}` while
> a sibling expression in the same function uses `Index: outerWidth + i` (`:2071-2077`), and
> the space is repaired late by `reresolveJoinByName` (`bushy.go:2902-2925`) rather than
> guaranteed by construction.
>
> **Therefore the range check is the only authority.** It must be evaluated per level and must
> fail closed. A unit test on one 3-level shape (as the first draft proposed) cannot establish
> a global invariant and **must not be read as licence to skip the check**. Keep the test as a
> canary; keep the check as the gate.

## Q4 — Residual shape

`residual_i := splitAnd(j.Predicate)` minus the canonical `LeftKey = RightKey` equality
(`isCanonicalKeyEquality`, `bushy.go:1647-1657` — reuse it).

Every remaining conjunct must satisfy:

- no `OuterColumnRef`, `SubqueryExpr`, `ExistsExpr`, `InExpr`, `MultiAssignSubqElem`,
  `MultiAssignSubqRow` — reuse `walkColumnRefs`'s `onOuter` callback
  (`multi_hash_join.go:~330-350`);
- every `ColumnRef.Index` in it falls within `[0, offset(level i) + width(level i))` — the
  columns bound at or before this level.

Any violation ends the chain at that level (the level and everything above it are unfused).

## Q5 — Prefix-boundedness (the correctness core)

Stated once, formally, because it is what makes the odometer safe:

> For every level *i*, every column index referenced by `probeKey_i`, `buildKey_i` and
> `residual_i` must be `< offset(level i) + width(level i)`.

Q3 and Q4 together imply it. It is restated here because a Stage-1 unit test asserts it
directly on every fused plan (`TestFusionPrefixBoundedness`), independent of how Q3/Q4 are
implemented.

## Q6 — Structural assertions (fail closed)

Checked per level, at build time:

1. `len(j.Output()) == len(j.Left.Output()) + len(j.Right.Output())`
2. `len(j.Left.Output()) == runningWidth` (the telescoping width identity)
3. **element-wise:** `j.Output()[c]` equals `j.Left.Output()[c]` for `c < lw` and
   `j.Right.Output()[c-lw]` otherwise, compared on column identity (name + type + source),
   for every `c`
4. `j.Left` is the level below (pointer identity), and `j.Right` is not `nil`
5. no level appears twice (defensive against a plan DAG)

Clause 3 is **mandatory and load-bearing** (finding F1): `Join.Output()` returns the *cached*
`n.schema` (`internal/planner/plan.go:889-897`), which the comment at `plan.go:869-886`
records can become "a stale *permutation* of the real layout" — and a permutation passes
clauses 1 and 2. Clauses 1-2 alone are **not** a mitigation for
[08 R2](08-risk-register.md). See [04 §4](04-fusion-site-and-data-structures.md).

Any failure ⇒ no fusion for the whole plan.

## Q7 — The struct-drift guard

`planner.Join` currently has the fields listed at `internal/planner/plan.go:826-865`
(`Type, Algo, Left, Right, Predicate, LeftKey, RightKey, BuildLeft, UsingLeftCols,
UsingRightCols, Lateral, NullAware`, plus unexported `pos`/`schema`). Q2 whitelists the
semantics of every one of them.

**A future field added to `planner.Join` will silently be ignored by Q2 and can silently
change join semantics under fusion.** Guard with a compile-time-ish test:

```go
func TestJoinStructFieldCountGuard(t *testing.T) {
    // 14 fields as of 2026-07-31. If this fails, someone added a field to
    // planner.Join: decide whether the runtime-fusion whitelist (design 05 Q2)
    // must reject it, then bump this number.
    if n := reflect.TypeOf(planner.Join{}).NumField(); n != 14 {
        t.Fatalf("planner.Join field count changed: %d", n)
    }
}
```

This pattern is the only cheap defence against the "sibling paths must agree" failure class
this repository keeps rediscovering.

## Q8 — What this predicate deliberately does NOT check

Compared to today's MHJ qualification (cost-model doc 15 §2), the following are **absent, and
their absence is correct**:

| MHJ needed | fusion does not, because |
| --- | --- |
| composite-free (no table-pair with >1 edge) | there are no "edges" — the plan fixed which predicate sits at which level, and extra conjuncts are just `residual_i` |
| tree / `#edges == #tables-1` | the left-deep chain *is* the tree; there is nothing to rediscover |
| connectivity | ditto |
| single-column **base-table** key columns | Q3's `*ColumnRef` in the merged space is weaker and sufficient |
| no self-join | key attribution is by index, not by name, so aliasing is irrelevant |
| probe = largest post-filter rows | the planner already chose the probe side; second-guessing it is what doc 15 called order surgery |

Every one of these was needed only because `collectMultiHashTables` had to *infer* a join
graph from a finished tree. This is the concrete sense in which the "third option" is a real
simplification, and it is the strongest argument in its favour.

## Q9 — The bug this predicate must not inherit

Today's `collectMultiHashTables` (`bushy.go:1519-1645`):

- resolves keys **by column name** via `findScanByColName` (`bushy.go:1731-1751`);
- when resolution fails, `if li >= 0 && ri >= 0` (`bushy.go:1596-1604`) simply **skips**
  appending the key;
- validates only that every table's degree is `<= 2` (`bushy.go:1620-1633`) — it **never**
  asserts `len(keys) == len(scans)-1`;
- so a packed MHJ can be emitted with fewer keys than tables−1.

Downstream, `multiHashJoinOp.Open`'s `keySteps` loop `break`s when it can find no further
progress (`multi_hash_join.go:158-163`). Unreached tables never get a `keyStep`, their
`tableSlots[i]` stays at `o.nulls[i]` (allocated at `multi_hash_join.go:~275`), and the join
predicate that should have connected them is simply gone. The output is a silently
NULL-padded, under-constrained row set.

The fusion design is immune by construction (there is nothing to resolve — level *i*'s key
is `levels[i].plan.LeftKey/RightKey` and there are exactly *k* of them for *k* levels), but
Q6 asserts it anyway, and [08 R1](08-risk-register.md) tracks the pre-existing packer bug as
a separate finding worth its own fix regardless of whether this proposal proceeds.
