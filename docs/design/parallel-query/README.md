# Intra-Transaction Parallel Query — Design Bundle

| field | value |
| --- | --- |
| status | draft — **DESIGN ONLY**, implementation not started |
| date | 2026-07-21 |
| branch | `parallel-query` (base `66c3c482`) |
| scope | parallel *query execution* within one transaction: Parallel Seq Scan, Gather / Gather Merge, Partial + Finalize aggregation, Parallel Hash Join |
| non-goals | parallel maintenance (CREATE INDEX / VACUUM), parallel index scan, parallel DML, parallel query under SERIALIZABLE |
| baseline | PostgreSQL 18.3 (`postgres/` submodule, read-only) |

## Why this bundle exists

goopg executes every query on a single goroutine. PostgreSQL does not.

On the SF1 TPC-H workload used for every prior goopg-vs-PG comparison,
PostgreSQL 18.3 runs **20 of the 23 captured plans in parallel** with
`Workers Planned: 2` (Q11, Q13 and Q20 are the serial ones), using a small,
countable set of node families:

| PG parallel node | occurrences in the TPC-H set |
| --- | ---: |
| `Parallel Seq Scan` | 28 |
| `Gather` | 11 |
| `Gather Merge` | 11 |
| `Parallel Hash` / `Parallel Hash Join` | 7 + 7 |
| `Partial …` / `Finalize …` aggregate variants | 11 + 11 |
| `Parallel Index Scan` | **0** |
| `Parallel Index Only Scan` | 1 (Q16, on `partsupp_pk`) |

Source: `analysis/tpch/goopg-pg-tpch-plan-compare-260718/raw/pg_explain.txt`
on `origin/master`.

That inventory is the whole scope argument for this bundle: it names what to
build, and it names what not to bother with.

One honest qualification on the last row. `Parallel Index Scan` really is
absent, but there is a single `Parallel Index Only Scan` (Q16). So "PG never
parallelises index access here" would be false; the accurate statement is that
parallel index access appears **once in 23 plans**, which is too thin a return
to justify the machinery in v1. The exclusion is a cost/benefit call, not an
observation that the shape never occurs.

The motivation is sharper still when set against goopg's measured position.
Per [`analysis/tpch-csq-evolution-baseline-to-round3-20260721.md`](../../../analysis/tpch-csq-evolution-baseline-to-round3-20260721.md),
goopg's worst remaining ratios versus PG are Q5 (1221×), Q19 (880×), Q7
(350×), Q14 (183×) and Q9 (105×) — and **every one of those queries is one PG
runs in parallel**. Part of each of those gaps is nothing more subtle than
leader-plus-two-workers versus one goroutine. Parallelism will not close them
on its own (Q5 is join-order-bound, Q19 is a probe-count problem), but no
amount of planner work closes the parallel component either.

Meanwhile every parallel GUC is *already registered* with PG-correct names,
contexts and (mostly) defaults — and every one is explicitly inert.
`internal/config/defaults.go:593-600` says so in as many words: "v0 doesn't
honour any of these semantically — the planner / executor ignores the values".
The knobs to request parallelism exist; nothing reads them.

## Guiding principle

**PostgreSQL semantics, Go mechanism.**

Where PG's behaviour is *observable* — result sets, error semantics, GUC names
and units, EXPLAIN node labels and counters, which plans are refused — goopg
matches it. Where PG's behaviour is a *consequence of process isolation* —
dynamic shared memory, tuple serialisation through `shm_mq`, worker fork cost,
serialize/deserialize functions for aggregate transition state — goopg is free
to do something cheaper, and this bundle argues each such divergence
explicitly rather than drifting into it.

Every chapter therefore carries a **"Divergence from PostgreSQL"** section
stating what PG must do, what goopg does instead, and what that choice costs.
The load-bearing divergences are collected in
[03](03-concurrency-substrate.md); the largest are that tuples never leave the
address space, that a hash-join build table can simply be shared, that
aggregate transition state needs no serialisation at all, and that a goroutine
costs approximately nothing to start — which moves the threshold at which
parallelism pays well below PG's.

The corollary is a discipline, not a licence: a thread-parallel executor can
have data races, which PG's process-parallel one structurally cannot. The
repository already recognises this — `Makefile:393-396` justifies the
`race-gate` target as catching "data races specific to goopg's thread-parallel
architecture (as opposed to PG's process-parallel design where data races
aren't possible between backends)". That gate covers `internal/executor` and
`internal/planner` today, and it becomes a first-class acceptance criterion
here.

## What v1 deliberately refuses

Four refusals, each because the substrate cannot currently guarantee
correctness — not because the shape is uninteresting. Each is specified with
its enforcement point in the chapter that owns it.

| refusal | reason | owner |
| --- | --- | --- |
| **SERIALIZABLE isolation** | SSI predicate-lock acquisition is a genuine *write* on the scan read path (`internal/executor/operators_storage.go:1541`, `ssi.go:455`) funnelling through one `ssiMu`. PG itself only allowed parallel query under SERIALIZABLE from v12. | [03](03-concurrency-substrate.md) |
| **Any write / DML statement** | Workers must never assign an XID, mutate the subxact stack, touch the catalog, or release locks — and `lockmgr` release is destructive for the whole transaction (`internal/lockmgr/lockmgr.go:557`). | [03](03-concurrency-substrate.md) |
| **Parallel-unsafe functions** | The `proparallel` marker (`'s'`/`'r'`/`'u'`, default unsafe) is already parsed by `CREATE FUNCTION` and stored — nothing consults it. | [08](08-planner-integration.md) |
| **Non-decomposable aggregates** | `DISTINCT`, `WITHIN GROUP`, ordered `array_agg`/`string_agg`, and user aggregates without `COMBINEFUNC` have no correct partial/final split. | [06](06-parallel-aggregation.md) |

## Documents in this bundle

| Doc | Title | Scope |
| --- | --- | --- |
| [01](01-current-state-and-gap-analysis.md) | Current State and Concurrency Hazard Inventory | Which subsystems are already concurrency-safe (most of them), the eleven blockers that are not, the parallel-GUC audit and two fidelity bugs found while surveying |
| [02](02-pg-target-architecture.md) | PostgreSQL's Parallel Query Architecture (Oracle Reference) | `ParallelContext`, DSM/`shm_mq`, Gather/Gather Merge, partial paths, `compute_parallel_worker()`, parallel safety, transaction integration; the fidelity matrix |
| [03](03-concurrency-substrate.md) | Execution Substrate: Ownership, Lifetime, Failure | The shared/per-worker context split, per-worker arenas, the tuple ownership contract, error and panic propagation, cancellation, worker lifecycle, pin discipline |
| [04](04-parallel-scan.md) | Parallel Sequential Scan | Shared atomic block allocator, per-worker page state, hint-bit writes, prefetch and ring-buffer behaviour under N workers |
| [05](05-gather-and-gather-merge.md) | Gather and Gather Merge | Channel shape and batching, backpressure, leader participation, early shutdown, order-preserving merge |
| [06](06-parallel-aggregation.md) | Partial and Finalize Aggregation | The plan-node mode split, per-aggregate decomposability matrix, `aggRuntime` combine, `COMBINEFUNC` reuse, refusal rules |
| [07](07-parallel-hash-join.md) | Parallel Hash Join | Shared read-only build table after a publish barrier, per-worker probe state, spill behaviour under concurrency |
| [08](08-planner-integration.md) | Planner Integration, GUCs, and EXPLAIN | Gather placement, the worker-count rule without a cost model, per-session GUC plumbing, GUC corrections, PG-faithful EXPLAIN rendering |
| [09](09-verification-and-measurement.md) | Correctness Gates and Measurement Plan | Serial≡parallel identity, race-gate, plan-gate, the aggregate matrix, cancellation and leak tests, honest speedup reporting |
| [10](10-roadmap.md) | Phased Roadmap | P0–P7 with acceptance criteria, kill switches, and per-phase plan-stability predictions |
| [11](11-partial-aggregation-cost-model.md) | A Cost Model for the Partial-Aggregation Split | When splitting an aggregate across a Gather pays and when it does not; `estimate_num_groups` in miniature, the mutex-merge cost goopg's design substitutes for PG's tuple transfer, the memory ceiling, and the two ANALYZE defects the premise does not survive without |
| [12](12-parallel-multi-way-hash-join.md) | Parallel Multi-Way Hash Join | Why the two-way parallel hash join delivered nothing on TPC-H (goopg collapses chains into MultiHashJoin), and the three-change design that parallelises the probe scan; each worker rebuilds the small dimension tables, so no shared-build machinery is needed |

## Reading order

[01](01-current-state-and-gap-analysis.md) and [02](02-pg-target-architecture.md)
are the problem statement and the oracle. [03](03-concurrency-substrate.md) is
the load-bearing chapter — chapters 04–07 all consume the ownership and
lifetime contract it defines, and reviewing them without it will produce
plausible-looking designs that are unsound. [08](08-planner-integration.md)
onwards can be read independently.
