# `Parallel Hash`: a shared table built from a PARTIAL inner (M0140-0007)

Status: blocked — owner decision required (2026-09-22). No production change.
Implementation must not start until the owner chooses a faithful partial-inner
barrier protocol or closes the task: its central premise was checked and is
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

## The measurement (2026-09-21): all 7 witnesses are SeqScan-rooted — and 3 of them still will not match

Step 1 of the slicing plan, done. Two findings, and the second is the one that
would have cost an implementation loop.

### Finding 1 — the existing builder can reach every witness

goopg's own hash-join build side, read off the fresh parallel-mode TPC-H
capture, for all seven family-A queries:

| query | goopg build side |
|---|---|
| Q3 | `Seq Scan on orders` |
| Q9 | `Seq Scan on partsupp`, `Seq Scan on lineitem` |
| Q10 | `Seq Scan on nation`, `Seq Scan on orders` |
| Q14 | `Seq Scan on part` |
| Q16 | `Seq Scan on part` |
| Q18 | `Seq Scan on orders` |
| Q21 | `Seq Scan on nation`, `Seq Scan on orders`, `Seq Scan on lineitem l1` |

Every one is a bare `Seq Scan`, so `coopDrivingScan` reaches all of them.
**There is no bucket (ii) on this corpus**: the real worker-inserted build +
barrier port is not required for family-A parity, and the existing cooperative
builder could drive every witness.

That keeps the caveat from the recon above, now applying to the whole family:
doing it this way produces PG's plan SHAPE with a different EXECUTION model
(leader-inserted, not worker-inserted). It must be labelled as that.

### Finding 2 — for Q9, Q21 and half of Q10, the node is not the only difference

PG's `Parallel Hash` build sides on the same queries:

| query | PG build side |
|---|---|
| Q3 | `Parallel Seq Scan on customer` |
| Q14 | `Parallel Seq Scan on part` |
| Q16 | `Parallel Seq Scan on part` |
| Q18 | `Parallel Seq Scan on customer` |
| Q10 | `Parallel Seq Scan on orders` AND `Parallel Hash Join` |
| **Q9** | **`Nested Loop`** |
| **Q21** | **`Hash Join`** |

For Q9, Q21 and one of Q10's two hash joins, PG builds its hash table over a
JOIN, while goopg builds over a scan. Those queries therefore differ from PG in
**join order / build-side choice as well as in the parallel-hash node**, and
adding `parallel_hash = true` alone will not make them match. Q3/Q14/Q16/Q18
build over the same relation as PG and are the genuinely reachable subset —
though note even there goopg and PG pick different tables in Q3 and Q18
(`orders` vs `customer`), so "same shape" needs checking per query before any
parity claim is made.

### Revised recommendation

1. **Scope any first implementation to Q14 and Q16**, the two witnesses where
   goopg and PG build over the SAME relation (`part`) and the build side is a
   bare scan on both sides. They are the honest clean witnesses; Q14 was
   already named as such by the task.
2. Q3 and Q18 need their build-side relation checked against PG's before being
   claimed as parity wins — goopg builds over `orders`, PG over `customer`.
3. Q9, Q21 and half of Q10 are NOT parallel-hash problems alone and should not
   be counted as family-A wins by this task; their join-order divergence is
   prior and belongs to whichever task owns join-order parity.
4. The worker-inserted build + barrier port stays unbuilt and unneeded for this
   corpus. It should be filed as its own task only when a witness appears that
   `coopDrivingScan` cannot reach.

### Method note

Both tables are read from the SAME fresh capture pair
(`tmp/q17-attrib/jt-off.plans.txt` goopg, `tmp/q17-attrib/jt-on-pg.plans.txt`
PG 18.3), taken in parallel mode against the same SF1 data. The checked-in
`bench/tpch/plans-pg/` fixtures were NOT used — they are stale and serial
(ledger `parity-reference-fixtures-are-invalid`).

## Step 2 STOPPED before implementation (2026-09-21): the cooperative build already runs

The previous section's plan was to implement, scoped to Q14 and Q16. Reading
the execution path before writing the planner change stopped it, and the reason
retires the implementation as framed.

### The shared cooperative build is already active

`prebuildSharedHashJoins` calls `joinOp.buildLazyHashTable`, which routes to
`parallelBuildLazyHashTable` whenever `parallelBuildEligible` holds
(`operators_join_agg.go:668-673`). That gate is permissive — its composite-key
decline was retired in E-18 slice 3 — and it excludes only FULL/RIGHT, a
LEFT-with-build-on-left, and a CTID-preserving `FOR UPDATE` build.

Instrumented and run on the SF1 clone, Q14 and Q16 together:

```
COOPBUILD entered buildLeft=false scanTable=part
```

The cooperative build fires, over `part` — the build relation of both
witnesses. **One shared hash table is already built by N producer goroutines
splitting the build scan.** The executor capability the task describes as
absent is, for this corpus, present.

### What is actually still different from PG

Two things, and neither is the capability gap the task's framing implies:

1. **Where the build happens.** PG builds inside the Gather: each participant
   produces a SLICE of the inner (a partial path) and inserts into the shared
   table behind a barrier. goopg builds BEFORE fan-out: the leader runs the
   cooperative build over the COMPLETE inner, then shares the finished table.
   Both divide the inner SCAN across goroutines; they differ in who inserts and
   when, not in whether the table is built cooperatively.
2. **The plan label.** PG renders `Parallel Hash`; goopg renders plain `Hash`.

### Why the planned implementation was NOT done

Filing `parallel_hash = true` while the inner remains a COMPLETE path would
move goopg's plan text toward PG's without changing execution at all. That is
a label asserting an execution model the engine does not use — strictly worse
than the current honest divergence, and precisely the kind of
"agree with PG's answer while computing it differently" this milestone has
already been criticised for twice.

Making the inner genuinely partial is the real port, and its benefit on this
corpus is small: the inner scan is already divided across goroutines by the
existing builder, so what a partial inner buys is the barrier-based
per-participant insert, not the parallelism itself.

### Recommendation (owner decision)

The measured state does not justify the task as filed. Either:

1. **Re-scope to the label + model alignment as an explicit fidelity item**,
   accepting that it buys plan text rather than throughput, and sequencing it
   behind something that needs the partial inner for a real reason; or
2. **Close it**, recording that family-A's shared-build capability exists, that
   the residual divergence is where-the-build-happens plus the node label, and
   that three of the seven witnesses (Q9, Q21, half of Q10) are join-order
   divergences this task could never have fixed anyway (Finding 2 above).

The loop does not choose. It declines to ship a label-only change.

## 2026-09-22 escalation — no selectable faithful implementation

The source path remains decisive: `prebuildSharedHashJoins` reaches
`parallelBuildLazyHashTable` for Q14 and Q16, so goopg already cooperatively
scans the complete `part` inner into one shared table. This is not PG's
`parallel_hash = true` model: PG has a partial inner whose participants insert
into the shared target before a build-completion barrier releases probes.

Changing only the planner label would falsely assert that model. The faithful
alternative needs an explicitly owner-approved re-scope covering partial-inner
execution, shared publication, barrier/error behaviour, and a
parallel-versus-serial identity test. The root is therefore `[!]`; its expected
measured movement is limited to Q14/Q16 leaving the TPC-H `parallelism`
category, not a promised plan match.
