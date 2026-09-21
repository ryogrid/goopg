# `Parallel Hash`: a shared table built from a PARTIAL inner (M0140-0007)

Status: scoping recon, 2026-09-21. No production change. Implementation NOT
started — the task's central premise needed checking first, and it is
understated in a way that changes the slicing.

Task: `.ralph/fix_plan.md` M0140-0007. Kind: impl. Parent: none. Carries ledger
`m0140-0005-q14-parallel-hash-execution-model`, K92, plan-flow doc D3(a).

## What PG does, and what goopg files instead

PG has two partial hash-join variants (`try_partial_hashjoin_path`,
`joinpath.c:1290-1297`, dispatched at `:2418+`):

- `parallel_hash = false` — a partial OUTER probes a COMPLETE inner whose hash
  table each worker holds (or, in goopg, the leader prebuilds and shares);
- `parallel_hash = true` — the inner is itself a PARTIAL path, and the
  participants build ONE shared table cooperatively behind a barrier before any
  of them probes.

goopg files only the first. `joinpathsparallel.go`'s own header says why: "no
goopg executor builds a hash table from a partial inner", and the file never
reads `inner.PartialPathlist`. This floors M0137-0019's **family A** — 7 TPC-H
queries (Q3 Q9 Q10 Q14 Q16 Q18 Q21, Q14 the clean witness).

## The premise check: the mechanism exists, but not in the shape the task implies

The task says the cooperative-build mechanism "ALREADY exists"
(`parallelBuildLazyHashTable`, `parallel_hash_build.go:596`) and that "the gap
is driving it from a partial-inner PATH ... not the mechanism itself".

The first half is true and the second is understated. Reading the function:

- **It is leader-inserted, not worker-inserted.** N producer goroutines
  scan+filter and ship row batches through a channel; the CALLING goroutine —
  one consumer — evaluates the hash keys and does every insert, running "the
  SAME buildLoopRight or buildLoopLeft as the serial path". PG's
  `parallel_hash = true` has every participant insert into the shared table.
- **It is scan-driven, not path-driven.** It requires
  `coopDrivingScan(buildPlan)` to find a `*SeqScan`, reachable only through
  `Filter`/`Project`/(hash `Join`) — anything else returns nil and the build
  refuses (`parallel_hash_build.go:451-473`). The producers then REBUILD that
  subtree from the plan and claim blocks from a shared `ParallelScanState`.

So the existing mechanism parallelises *reading* the build side; it does not
consume a partial inner's per-worker output. Driving it "from a partial-inner
PATH" is therefore not an extension of call sites — the producer model differs.

### Why that distinction matters rather than being pedantry

Two consequences, and they pull in opposite directions:

1. **A cheaper route may exist for some witnesses.** Where a family-A inner
   bottoms out in a `SeqScan` under Filter/Project, the existing mechanism can
   already build one shared table cooperatively. Filing
   `parallel_hash = true` for those and driving the EXISTING builder would
   produce PG's plan SHAPE without the barrier protocol PG uses. That is a
   legitimate increment — but it must be described honestly as a different
   execution model wearing the same label, not as the ported feature.
2. **It does not generalise.** An inner that is a partial aggregate, an
   append, or any non-`SeqScan`-rooted shape has no `coopDrivingScan` and
   cannot use the existing builder at all. Those need the real thing: a
   build input that is itself partial, a shared build target all participants
   publish into, and a build-done barrier.

Which witnesses fall in which bucket is **unmeasured** and is the first thing
the next loop should establish.

## The hard constraint, restated because it binds the design

The refusal exists because a partial build that misses inner rows silently
drops matches. Any implementation must prove the barrier — no probe before the
shared build completes — and a failed build must fail the query loudly rather
than return partial results. This is the same failure class as the 2026-09-21
parallel SEMI defect (`docs/design/parallel-query/09-verification-and-measurement.md`):
values stayed plausible while the row count was wrong, and only a
parallel-vs-serial identity comparison could see it. **The pin for this task
must be an identity test per witness shape, not a predicate test.**

## Recommended slicing

1. **Measure the family-A inner shapes FIRST** (recon, cheap): for each of the
   7 witnesses, record whether the inner path's plan bottoms out in a `SeqScan`
   reachable by `coopDrivingScan`. That splits the work into "existing builder
   suffices" and "needs the real barrier protocol", and the split decides
   everything below.
2. If any witness is in bucket 1, land that narrowly: planner files
   `parallel_hash = true` for those shapes, executor drives the existing
   cooperative builder, pinned by a parallel-vs-serial identity test.
   Record explicitly that the execution model differs from PG's.
3. Bucket 2 is the real port and should be its own task: worker-inserted shared
   build + barrier, sized against `prebuildSharedHashJoins`' call sites.
4. Scope (b) — PG's cost split across participants — only after an executor
   exists for the shape, per scope (d)'s standing rule in M0145-0010
   (verify executor capability by measurement BEFORE admitting a shape).

## Not done here

No production change. The premise check above is the deliverable; the
measurement in step 1 is the next loop's.
