# E-09a — publish a SPILLING shared hash build (Variant A: private reload)

Status: accepted for implementation. Owner item: TODO_ALL E-09a (split
from E-09 on 2026-09-06). Feasibility established by static analysis of
`internal/executor/parallel_hash_build.go`, `join_batch.go`,
`operators_join_agg.go` and PG's `nodeHash.c` / `nodeHashjoin.c` /
`sharedtuplestore.c`.

## 1. The defect, measured

`prebuildSharedHashJoins` declines to publish a build whose geometry says
`NBatch > 1` (`sharedBuildWouldSpill`), and additionally drops a build that
turned out to batch after the fact. Every Gather participant then falls
through to a full **private** build. Live witness, TPC-H Q9 with 4 workers
+ leader: `Seq Scan on orders rows=1500000 loops=1` in **all five**
participants; build 2979 of 8062 ms. That 5× multiplier on the whole build
dwarfs everything the minimize-datum bundle proposes to save on the same
query (1.9× on the row format), and it is the source of D-04's 506 MB
live-map figure.

## 2. Why it is declined today — a missing mechanism, not a design choice

The in-memory maps hold **batch 0 only**; everything needed for batches
1..n-1 (inner/outer file slices, `curBatch`, `nbatch`, `bucketBits`, the
replay operator) lives on `hashBatchState`, which `captureSharedBuild`
does not freeze. A worker handed the maps alone would probe batch 0 and
silently return one partition's rows — and never even *save* its probe
rows for later batches, because `routeProbeRow` is guarded on
`bs != nil`. The file header says exactly this. Sharing `hashBatchState`
as-is is also unsafe: `increaseNumBatches` and `loadInnerBatch` mutate maps
and append to inner files, `openReader` nils slots, `discard` unlinks.

## 3. What goopg does NOT need from PG's protocol

goopg's Gather partitions the **probe** by scan blocks, not by batch. Every
participant sees a disjoint slice of the outer relation and processes
**every** batch of it in order. That removes, relative to PG:

- shared **outer** tuplestores (`PHJ_BUILD_HASH_OUTER`) — each participant's
  outer batch files are written and read by it alone, exactly as today;
- batch-level work distribution and the "attach late, jump in" switch;
- the `distributor` counter.

## 4. The minimum port — Variant A

Three parts, none of which introduces a cross-worker wait:

1. **Publish the spilling build.** `captureSharedBuild` carries, beside the
   batch-0 maps, an immutable shared batch descriptor: `nbatch`,
   `bucketBits`, `nbuckets`, `buildIsLeft`, and the **inner** batch files
   1..n-1 (closed for writing; paths only). The prebuild operator must not
   release or unlink them; the statement-end temp-file registry is the
   backstop, plus an explicit release in Gather/GatherMerge `Close`.
2. **Freeze growth after prebuild** — PG's own rule (`nodeHashjoin.c`:
   "completes all changes to the number of batches during the build
   phase"). Growth is still allowed *during* the leader's prebuild; on a
   participant's reload path `growEnabled=false`, so `loadInnerBatch` never
   writes into a shared file and never doubles `nbatch`. If a batch does
   not fit at reload it exceeds budget — the same best-effort PG makes.
3. **Per-participant batch state derived from the descriptor.**
   `applySharedBuild` installs a private `hashBatchState` holding its own
   outer file slice, `curBatch`, replay op, `spaceUsed` and stats sink,
   with `inner` pointing at the shared read-only files. `nextBatch` then
   works unchanged except that the inner table for batch k is obtained by
   each participant opening its **own** `spillReader` on the shared inner
   file and loading into its own fresh map. `resetHashTable` already
   *replaces* rather than clears, so the shared batch-0 map is dropped, not
   mutated.

**Invariants** (each pinned by a test): shared inner files are never
written or unlinked by a participant; growth is frozen on the reload path;
every participant opens each inner file exactly once.

Memory: N × one batch instead of N × the whole build — same per-worker peak
as today on Q9 (one batch resident) but the **build scan and partition
write happen once**. CPU trade: participants re-decode from files instead
of re-scanning the heap and re-keying all of it. That trade is unmeasured
and is the item's gate.

## 5. Variant B, deferred to E-09b

A load-once-per-batch cache on the descriptor (`sync.Once` per batch, a
refcount, `ctx.Done()`-aware waiting) is PG's `PHJ_BATCH_LOAD`/`FREE`
analogue and is what removes the 5× **memory** multiplier. It is the first
real cross-worker wait in the executor and has a cancellation hazard under
LIMIT-above-Gather. It lands on top of A without touching the outer side.

## 6. What this changes elsewhere

- **C-19f** (parallel hash join as a priced path) should land AFTER this,
  because its price must describe the executor that will exist: today the
  honest parallel price is "N full builds" (which is what the reverted
  build-cost patch charged, and why it cost 22%); after E-09a it is "one
  build + N batch reloads", i.e. PG's own shape.
- **D-05** should not be re-measured until this lands: every D-05 number to
  date was taken with the build replicated five times.
- E-09a does NOT depend on E-07 (the slab `Gather` arm): it changes
  `joinOp`/`join_batch.go`, which both build paths execute.

## 7. Gate — this is a wrong-answer class

- Forced-shape unit tests per shareable join type (INNER, SEMI, ANTI incl.
  NOT-IN null handling, LEFT probe-fill) with `work_mem` small enough to
  batch, a Gather with ≥2 workers plus leader participation, values-diffed
  against the serial result. Include an estimate-said-fit-but-didn't case.
- Counter assertions: each inner batch file opened by every participant
  exactly once; a poison writer proving no participant writes `inner[k]`.
- `go test -race` on the parallel hash-join and batch test shapes.
- Cancellation: LIMIT above the Gather, cancelled mid-batch-1, no hang.
- Plans byte-identical (no planner change); TPC-H values 24 MATCH; TPC-DS
  SF0.5 clean; `scripts/tpch-spotcheck.sh`.
- **Acceptance witness:** `EXPLAIN (ANALYZE)` Q9 shows
  `Seq Scan on orders rows=0.00 loops=0` in every worker and ONE
  `Build Time`. Time Q9/Q5/Q10 specifically — a row-count gate cannot see
  a 43× regression.

## 8. Size

~250–470 LOC across 4–5 files in 2–3 commits; tests match or exceed. Risk
is concentrated in part 3 (the adopted-map invariant) and is high because
the failure is a silently partial join.
