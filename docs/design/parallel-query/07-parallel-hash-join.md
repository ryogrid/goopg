# 07 — Parallel Hash Join

| field | value |
| --- | --- |
| status | draft (DESIGN ONLY) |
| date | 2026-07-21 |
| depends on | [03](03-concurrency-substrate.md), [04](04-parallel-scan.md), [05](05-gather-and-gather-merge.md) |

`Parallel Hash` and `Parallel Hash Join` appear 7 times each in the TPC-H
reference set — always together, since PG's `Parallel Hash` is the shared-build
variant that `Parallel Hash Join` probes. This is the chapter where goopg's
shared address space produces the largest structural simplification in the
bundle.

## 1. What PG has to do, and why

PG offers two parallel hash joins:

- **Non-shared**: every worker builds its *own* complete copy of the hash
  table from the (non-partial) inner side, then probes its share of the outer.
  Simple, but costs N× the build time and N× the memory.
- **Shared** (`Parallel Hash`): workers cooperatively build **one** hash table
  in dynamic shared memory, synchronise on a barrier, then each probes its
  share. This needs DSA (dynamic shared area) allocation, a barrier protocol
  with well-defined phases, and careful handling of batch spilling in shared
  memory — a substantial amount of machinery.

PG built the shared variant because the non-shared one wastes too much on large
builds. Both exist because shared memory is expensive to manage.

## 2. What goopg does instead

**One build, shared by pointer, published behind a barrier.**

```
Open (leader):
    build the hash table exactly as today, serially
    publish it (see §3)
    fan out N workers, each with:
        - a read-only reference to the same table
        - its own probe-side operator subtree (partial: parallel seq scan)
        - its own per-probe scratch
```

The correctness argument is the ordinary Go one: **a map is safe for unlimited
concurrent reads provided no writer runs concurrently**. A publish barrier
(the goroutine-start happens-before edge) is sufficient, and no lock is needed
on the read path.

That single sentence replaces PG's entire DSA + barrier-phase apparatus. There
is no third design to choose between: the non-shared variant has no advantage
here, since sharing costs nothing.

## 3. The build/publish boundary

Today `openLazyHashJoin` (`internal/executor/operators_join_agg.go:474-613`)
mutates `o.lazyHash map[string][]Row` (declared `:44`, allocated `:565`/`:609`)
in place and closes the build child immediately after draining. There is no
point at which the table is declared complete.

This needs a **two-phase `Open` protocol**, not merely a hook — an earlier draft
of this chapter understated it. `openLazyHashJoin` today drains the build child,
closes it, and *then* opens the probe child (`:541-543`, `:598-600`) in one
call. Under parallelism those halves must be separable:

1. **Leader, build phase**: open and drain the build child, populate the map,
   close the build child. Do **not** open the probe child.
2. **Publish**: after the last write, the map is frozen. Nothing writes to it
   afterwards.
3. **Worker, probe phase**: each worker's `joinOp` skips the build entirely,
   takes the shared map by reference, and opens only its own (partial) probe
   child.

This also means [05](05-gather-and-gather-merge.md) §3's `Open` sequence needs
an extra step: **pre-execute shared build subtrees in the leader before
fan-out**. A Gather whose partial subtree contains a hash join cannot simply
launch workers.

Beyond the map itself, several **build-computed scalar fields** must be
propagated into every worker's `joinOp` instance — they are per-instance fields,
not part of the shared map, so "shared read-only along with the table" is not
automatic: `antiBuildRows` / `antiBuildHasNull` (`:89-90`), `lazyHashCTID`, and
the build/probe width fields.

Two invariants the implementation must not violate:

- **No lazy insertion after publish.** Any code path that would add to
  `lazyHash` during probing (there is none today, but the field is not marked
  read-only) becomes a data race the moment workers exist.
- **The `Row` values in the table must be owned.** They come from
  `drainRowsBounded` (`:504`,`:589`), which materialises into its own storage,
  so this holds today — but it is a property to assert, not assume, since the
  rows are read by every worker and are exactly the kind of thing an arena
  optimisation would later break ([03](03-concurrency-substrate.md) §3).

### 3.1 Which side is the build side

`o.plan.BuildLeft` selects it, forced to the right for Semi/Anti
(`:496-499`). Parallelism does not change that choice: the **build side stays
serial and non-partial**, and the **probe side is the partial one**. This
mirrors PG's plan shape, where `Parallel Hash Join`'s outer (probe) side is the
parallel scan.

A cooperative parallel *build* — N workers inserting into one table — is
deliberately **not** designed here. It would require a concurrent map or
sharded locking, and it optimises the part of the join that is usually not the
bottleneck at TPC-H scale (the build side is the small dimension by
construction of the planner's `IsSmallDimensionSide` logic,
`internal/planner/cardinality.go:167`). Recorded in [10](10-roadmap.md) as a
possible later phase, with the honest note that it is the one place PG's design
is more advanced than what this bundle proposes.

## 4. What stays per-worker

`joinOp` carries per-probe-row scratch that cannot be shared: `lazyMatches`,
`lazyMatchIdx`, `lazyActive`, `lazyProbeMatched` (`:46-56`), the slot views
`lazyBuildSlot` / `lazyProbeSlot` / `lazyVirtualOut` / `lazyOuterOnlySlot`
(`:76-80`), and the reusable buffers `lazyNullLeft` / `lazyNullRight` /
`lazyKeyRow` (`:65-67`).

Therefore each worker gets its **own `joinOp` instance** — built from the same
plan node, as every worker's tree is ([03](03-concurrency-substrate.md) §1) —
whose `lazyHash` field points at the shared table instead of being built
locally. The build child is opened and drained exactly once, by the leader.

`evalHashKey` (`:818-828`) calls `evalExpr` with `o.ctx`, so each worker must
carry its own worker context per [03](03-concurrency-substrate.md) §2. That is
already the design; restated because the hash-key path is the hottest place
where a shared context would be caught by `race-gate` only under load.

## 5. Spilling under concurrency

`drainRowsBounded` (used at `:504`,`:589`) can spill, and `sortOp` spills too
(`internal/executor/operators.go:797`); both create files via
`newSpillWriter(os.TempDir())` (`internal/executor/spill.go:23`, `:340`).

Under parallelism the build still spills only once (it is serial), but
[05](05-gather-and-gather-merge.md) §4.1 introduces N concurrent worker sorts
below a Gather Merge, so concurrent spilling becomes real. File naming must be
verified collision-free, and cleanup must survive a worker being cancelled
mid-spill. This is a small, concrete verification item rather than a design
question — [09](09-verification-and-measurement.md) makes it a test.

## 6. Multi-way hash join

`multiHashJoinOp` (`internal/executor/multi_hash_join.go:24`) is goopg's N-way
join, with no PG counterpart. It builds several hash tables and probes one
driving table.

The same argument extends: all build tables are constructed serially by the
leader and published read-only; the probe side becomes partial. Nothing about
MHJ is structurally hostile to this.

MHJ was **not** in v1 scope, and the reasoning here was partly wrong — see
[12](12-parallel-multi-way-hash-join.md) §0, which parallelises it. Two
corrections: (1) "parallelising the two-way `Join` first gives the TPC-H
benefit" is false — P8 shipped and changed one plan (Q13) with no measurable
gain, because goopg collapses multi-table joins into `MultiHashJoin`, so the
two-way form the reference set never actually uses. (2) MHJ does **not**
interact with the join-order DP: `rewriteMultiWayChain` runs after the DP
finishes and `MaybeAddGather` is a non-mutating post-pass. The one true reason
was the absent oracle plan, which [12](12-parallel-multi-way-hash-join.md) §4
handles with a stated PG-comparability invariant. The build side being the
small side (§3.1) is what makes the parallel MHJ *simpler* than this two-way
case, not harder.

## 7. Semi / Anti joins

`Semi` and `Anti` force the build side to the right (`:496-499`) and use
early-out probing. Nothing in that is order- or worker-dependent: each probe
row's verdict depends only on the shared build table and that row.

The one caveat is the NOT-IN null-aware path, which tracks `antiBuildRows` and
`antiBuildHasNull` (`:89-90`). Both are computed during the **build**, hence
serial — but they are *scalar fields on the `joinOp` instance*, not entries in
the shared map, so each worker's instance must be given them explicitly (§3).

## 8. Divergence from PostgreSQL

| PG | goopg | Cost |
| --- | --- | --- |
| Two variants (shared and non-shared) because shared memory is expensive to manage | One design: build once, share by pointer | Removes DSA allocation, the barrier-phase protocol, and shared-memory batch spilling |
| `Parallel Hash` cooperatively builds with N workers | Build is serial in the leader | goopg does **not** parallelise the build; PG's design is genuinely more advanced here (§3.1). Acceptable because the build side is the small side by planner construction |
| Build-side tuples copied into shared memory | Rows are heap objects referenced by every worker | No copy; requires the rows to be owned, which `drainRowsBounded` already gives (§3) |
| Barrier with explicit phases (`PHJ_BUILD_*`) | The goroutine-start happens-before edge | Sufficient because the table is never written after publish |

The trade is explicit and worth restating plainly: goopg gets the *sharing*
benefit for free and gives up the *parallel build* benefit that PG's barrier
machinery buys. For the TPC-H shapes in the reference set — where the build
side is a dimension table and the probe side is `lineitem` — the sharing is
almost all of the win.
