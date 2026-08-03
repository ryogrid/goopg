# 04 — Where the fusion decision is made, and on what

## 1. There are TWO operator builders, and they must not diverge

| builder | entry | join arm | MHJ arm |
| --- | --- | --- | --- |
| legacy `Build` | `internal/executor/executor.go:21` | `case *planner.Join` (returns `maybeInstrument(p, newJoinOp(p,l,r))`) | `case *planner.MultiHashJoin`, `executor.go:275-284` |
| live `BuildFast` | `internal/executor/executor.go:563` → `buildRec`, `:424` | `case *planner.Join`, `executor.go:535-547` | falls to `default:` → calls legacy `Build` (`executor.go:549-557`) |

`BuildFastIterator` (`internal/executor/opnode.go:395`) is "the drop-in replacement for
executor.Build in dispatch" — i.e. the **live server path**. `Build` remains reachable
through the `default:` adapter arm, through `Gather`'s per-worker closure
(`executor.go:213-219`), and from tests.

This repository's recorded failure pattern is *sibling code paths silently disagreeing*
(encode/decode, fast-path/interpreted evaluator, the three copies of `probeSideIsLeft` that
`parallel_hash_build.go:104-109` warns about). A fusion implemented twice will diverge.

**Decision:** implement exactly one function

```go
// tryFuseHashCascade inspects the immutable plan subtree rooted at p and returns a
// fused operator when the whole cascade qualifies. It NEVER mutates p.
// Second return false ⇒ caller builds the ordinary cascade, unchanged.
func tryFuseHashCascade(env *buildEnv, p *planner.Join) (Operator, bool)
```

called from **both** join arms as the first statement, and from nowhere else. When it returns
false, both arms execute byte-identically to today.

### 1.1 `buildEnv` — required plumbing, and it is not free (finding F3)

The first draft's signature was `tryFuseHashCascade(p *planner.Join, root planner.Node)`.
**That is unimplementable at this site.** `func Build(plan planner.Node) (Operator, error)`
(`internal/executor/executor.go:21`) takes a single node and recurses as `Build(child)`:
there is no root parameter, no session, and no `*Context` (the context arrives at `Open`).
`buildRec` (`executor.go:424`) has the same shape.

Three consequences the plan must own:

1. Q0's "walk the root" has nothing to walk unless a root reference is threaded in.
2. **The kill switch cannot be a session-level GUC at build time.** Only a process-global or
   environment switch is reachable. [10 §KS1](10-rollback-and-kill-switches.md) is corrected
   accordingly — the "A/B in one psql session without a restart" benefit is **withdrawn**
   unless the plumbing below lands.
3. The worker-build signal for C10/F4 also needs somewhere to live.

So the plumbing is **explicit scope**, not an implementation detail:

```go
type buildEnv struct {
    root        planner.Node  // the plan root for this Build
    inWorker    bool          // set by newGatherOp's per-worker closure (C10 / F4)
    fusion      fusionConfig  // resolved once, from env/GUC snapshot
    q0          q0Result      // memoised root-walk result (R11)
}
```

threaded through `Build`/`buildRec` as an extra parameter, with `Build(plan)` retained as a
thin wrapper that constructs a default `buildEnv{root: plan}` so no external caller changes.

This touches every arm of two large switches. It is mechanical, but it is not small, and
Stage 1's estimate must include it. **The alternative — deciding at `Open`, where `*Context`
and the session are available — was rejected in §2 for reasons that still hold, but if the
plumbing proves unacceptable in review, moving to `Open` and re-arguing C9 is the fallback,
not abandoning the root walk.**

## 2. The decision is made at operator-BUILD time, on the immutable `planner.Node` tree

Three candidate sites were considered:

| site | verdict |
| --- | --- |
| a planner post-pass that rewrites the tree (what `rewriteMultiWayChain` does) | **rejected** — rewriting the plan is exactly what breaks EXPLAIN parity and what created the stale-permutation bug at `plan.go:869-886` |
| operator-build time, reading the tree without mutating it | **chosen** |
| `Open()` time | rejected — `Open` is per-execution and the decision would be re-made on every `Gather` worker and every re-Open; also `preserveCTIDRel` is set between build and Open (C9) |

Building without mutating gives three properties for free:

1. **EXPLAIN is unaffected.** `explainNodeLabel` / the child walker
   (`operators_explain.go:1380-1600`) walk the *plan*, which still contains k `*planner.Join`
   nodes. Fusion is invisible to `EXPLAIN`. This is the entire plan-shape-parity benefit,
   obtained by construction rather than by discipline.
2. **The kill switch is trivial** — one boolean at the top of `tryFuseHashCascade`
   ([10](10-rollback-and-kill-switches.md)).
3. **The plan can be cached/shared** across executions and workers, because it is never
   written.

## 3. The candidate shape

`tryFuseHashCascade(p)` walks **left-only** from `p`:

```
p (level k-1)          Right = build side k-1
└─ Left = Join         Right = build side k-2
   └─ Left = Join      Right = build side 0
      └─ Left = <probe subtree>          ← any node; not required to be a base scan
```

Notes on deliberate differences from today's packer:

- The **probe** (deepest left) subtree is **not** required to be a `SeqScan`. Today's
  `collectMultiHashTables` only accepts `*SeqScan` leaves (`bushy.go:1531-1534`) and bails on
  `Filter` / `Project` (`:1540-1554`). The fused operator does not care what the probe is —
  it only pulls slots from it. Allowing `Filter(SeqScan)`, `IndexScan`, `Gather`-free
  subtrees etc. widens applicability at zero semantic cost.
- The **build** side of each level may likewise be any subtree. It is drained with
  `drainRowsBounded` and hashed; its internal structure is irrelevant.
- Because the walk is left-only and every level's build side is whatever the plan put on the
  right, **there is no join graph, no BFS, no spanning tree, and no tree/cycle test.** The
  plan already fixed the shape. Everything MHJ's `mhjSubsetQualifies` machinery had to
  rediscover is given.

This is the single largest simplification the "third option" buys, and it is worth stating
loudly: *fusing a plan is strictly easier than packing a plan*, because the plan is already a
decision.

## 4. Structural assertions — width is necessary, element-wise identity is what matters

> **Corrected after review (finding F1) — this is the most important correction in the set.**
> The first draft claimed the width identity was "the assertion that replaces name
> resolution". It is not sufficient. `Join.Output()`
> (`internal/planner/plan.go:889-897`) returns the **cached** field `n.schema` for every
> non-semi/anti join — it does not recompute `Left.Output() ++ Right.Output()`. The doc
> comment directly above it (`plan.go:869-886`) records that this cache can go stale as
> *"a stale **permutation** of the real layout"*, which is exactly what produced the
> M0125-0008 wrong-rows bug. **A permutation has the same length**, so a width-only check
> passes on precisely the corrupted input it was supposed to reject.

For a left-deep chain, `planner.Join.Output()` is *intended* to be (for INNER)
`Left.Output() ++ Right.Output()`. Writing `W_i` for level *i*'s right-side width and
`W_probe` for the probe subtree's width:

```
width(level i output) == W_probe + W_0 + W_1 + … + W_i
offset(build side i)  == W_probe + W_0 + … + W_{i-1}
```

**Build-time assertion (mandatory, fail-closed):** for every level *i*,

```go
if len(levels[i].plan.Output()) != len(levels[i].plan.Left.Output())+len(levels[i].plan.Right.Output()) {
    return nil, false
}
if len(levels[i].plan.Left.Output()) != runningWidth { return nil, false }
```

**Element-wise assertion (mandatory, and the one that actually protects against a stale
permutation):** for every level *i* and every output column *c*,

```go
lw := len(j.Left.Output())
for c, col := range j.Output() {
    var want planner.Column
    if c < lw { want = j.Left.Output()[c] } else { want = j.Right.Output()[c-lw] }
    if !sameColumn(col, want) { return nil, false }   // name + type + table identity
}
```

`sameColumn` must compare whatever identity a `planner.Column` carries (name, type, source
table/alias) — **not** just the name, because two columns of the same name in different
positions is precisely the permutation case.

Only if **both** the width identity and the element-wise identity hold at every level does
column *c* of the top join's output map to `(source, col)` by arithmetic over the offsets —
**no `findScanByColName`, no `posMap`, no `remapKeyToLayout`**. If either fails, the plan has
been rewritten by something this design does not know about, and we build the cascade.

Cost: `O(total width)` per plan build, once, on candidate cascades only. That is nothing
against the cost of a silent wrong-answer bug, and it is the *only* mitigation in this design
that addresses [08 R2](08-risk-register.md) at its root.

The historical source of the wrong-column bugs is name-based *resolution*; here name
information is used only to **verify**, never to decide. That asymmetry is the point.

## 5. The data structure

```go
type fusedLevel struct {
    plan     *planner.Join   // for instrumentation identity (C12) and EXPLAIN
    buildOp  Operator        // the right subtree, built normally
    probeKey planner.Expr    // plan.LeftKey  (probe side)
    buildKey planner.Expr    // plan.RightKey (build side)
    residual planner.Expr    // plan.Predicate minus the canonical key equality
    width    int             // len(plan.Right.Output())
    offset   int             // absolute offset of this build side in the top schema

    // populated at Open
    ht       map[string][]Row
    intHT    map[int64][]Row
    htIsInt  bool
    slot     *MaterializedSlot // .row rebound per match; source of the VirtualSlot

    // per-probe-row odometer state
    matches  []Row
    cursor   int

    rowsOut  *int64  // → instrumentScope stats for plan (C12)
}

type fusedHashJoinOp struct {
    levels   []fusedLevel   // levels[0] = innermost join, levels[k-1] = p
    probeOp  Operator
    probeSlotSrc TupleSlot  // the child's slot, held as a slot — NOT materialised (Stage 0a)
    out      *VirtualSlot   // schema == levels[k-1].plan.Output()
    schema   planner.Schema
    ctx      *Context
    // odometer
    active   bool
    probeEOF bool
}
```

`out` is built once in `Open`: `sources = {probeSlot, levels[0].slot, …, levels[k-1].slot}`
and `cols[c] = {sourceIdx, sourceCol}` derived purely from the offsets of §4. This is the
same `VirtualSlot` mechanism MHJ uses (`multi_hash_join.go:265-310`) and the same one
`joinOp` uses for its own output — reused, not reinvented.

## 6. Open()

```
for i := 0 … k-1:
    open levels[i].buildOp
    bounded := drainRowsBounded(levels[i].buildOp, ctx.WorkMem)     // C8
    close  levels[i].buildOp
    drain bounded into ht/intHT via lazyHashInsertDatum semantics   // C5, C6
      – ctx.Ctx.Err() every 4096 rows                               // C7
open probeOp
```

Build order is levels 0..k-1 (innermost first), matching the cascade's `Open` order, so that
any error raised during a build surfaces from the same level as before.

## 7. Next() — the odometer

```
loop:
  if !active:
      probeSlotSrc = probeOp.Next(); if EOF → EOF
      bind probe slot; reset all cursors; active = true
      lookup levels[0].matches
  descend from the shallowest unsatisfied level:
      at level i: take matches[cursor]; cursor++
                  levels[i].slot.row = match
                  if !eval(residual_i, out) → continue at level i     // C4
                  if i == k-1 → emit out                              // C1
                  else compute key_{i+1} from out, look up matches[i+1], cursor[i+1]=0
      when a level's matches are exhausted → back off to level i-1
  when level 0 is exhausted → active = false
  ctx.Ctx.Err() check every 4096 odometer steps                        // C7
```

Key evaluation reads `out` (the `VirtualSlot`) directly — **which requires a slot-taking
variant of the key evaluator that does not exist yet** (finding F11). Today
`evalHashKeyDatum(keyExpr planner.Expr, row Row)` (`operators_join_agg.go:960-968`) takes a
`Row` and calls `evalExpr`. A slot-taking sibling does exist for predicates
(`evalExprSlot`, `operators_join_agg.go:~1024`, used by `joinPredicateMatchSlot`), so the
work is to extract `evalHashKeyDatumSlot` alongside it. **That extraction belongs to Stage 0b**
(which needs it for the same reason) and is a prerequisite of Stage 1, not a Stage 1 task.
See §8.

`out` is complete for all columns at
offsets `< offset(level i+1)` at the moment level *i+1*'s key is computed, because the
odometer binds strictly left-to-right. Columns not yet bound are never referenced by a level's
key or residual — guaranteed by the telescoping identity plus the whitelist in
[05](05-qualification-predicate.md) clause Q5 (key/residual column indices must fall within
the already-bound prefix). **That check is mandatory and fail-closed**; it is the fused
analogue of MHJ's `partitionFilters` and it is much simpler because the plan already ordered
the levels.

## 8. What is NOT reimplemented

To keep the divergence surface minimal, the fused operator calls the existing helpers rather
than copying them:

| concern | reused symbol | file |
| --- | --- | --- |
| key evaluation to a `Datum` | `evalHashKeyDatum` — **must first be split into a slot-taking `evalHashKeyDatumSlot` (F11); Stage 0b scope** | `operators_join_agg.go:960` |
| int64 key derivation | `datumToInt64Key` | `operators_join_agg.go` (int64 fast path) |
| string key derivation | `datumKey` | `operators_join_agg.go` |
| build-side insert + int64 downgrade | `lazyHashInsertDatum` / `lazyHashFinalize` | `operators_join_agg.go:975-1000` |
| residual evaluation against a slot | `joinPredicateMatchSlot` | `operators_join_agg.go:~1031` |
| bounded drain + spill | `drainRowsBounded` | `spill.go:342` |
| slot composition | `NewVirtualSlot` / `SlotFromRow` | `slot.go:132`, `slot.go` |

If any of these are not directly callable with the fused operator's argument shapes, the
correct move is to **extract a small shared helper from `joinOp`**, not to copy the body.
A copy is a future silent divergence.

## 9. Why not fuse at the plan level with a "fusable" annotation?

Considered and rejected: annotating each `Join` with `Fusable bool` in the planner and having
the executor honour it. It reintroduces a planner-side decision that must be kept in sync
with executor capability (the doc-15 failure mode), it makes the plan non-identical to PG's
in a field that could leak into EXPLAIN or plan snapshots, and it makes the kill switch a
planner change rather than an executor one. The executor already has everything it needs to
decide.
