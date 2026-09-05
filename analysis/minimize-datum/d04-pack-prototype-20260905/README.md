# D-04 (MD-03.5) — throwaway packing prototype: **STOP, the model is wrong**

Date: 2026-09-05. Item: `docs/design/not_ralph/minimize_datum/TODO_ALL.md`
D-04, whose whole purpose is to decide D-05 (~900 LOC) before it is built.
The prototype was written, measured, and **deleted**; `git status` on
`internal/executor/` is empty.

## Verdict, in the stopping rule's own words (05 §6)

> **"batches unchanged → the model in D-3 is wrong. Fix the model before
> touching another site."**

Values matched, so this is not the revert row. Wall time rose but by less
than the ±17% band, so the R-3 row does not fire on its literal wording —
although the R-3 mechanism is directly visible in the allocation counts.

## The four numbers

| number | result | status |
|---|---|---|
| **batch count** | **4 → 4, UNCHANGED** (also under a second arm that re-derives the geometry from a packed-entry model) | measured by hand `EXPLAIN (ANALYZE)` |
| **retained bytes** | join accounting 44,026 → 37,788 kB (**−14.2%**); live-heap `inuse_space` at the converted site 180.02 → 136.01 MB (**−24.4%**) | measured — **the harness 05 §6 says does not exist was built**: sample `/debug/pprof/heap?gc=1` once a second at `GOGC=100`, so this is live bytes, not `GOGC=off` garbage |
| **wall time** | off 14.92 s ±0.35 (n=7), on **15.94 s ±0.28 (n=7) → +6.8%** | measured, 7 interleaved reps per arm, one binary, fresh capped server per arm. Inside the ±17% band, but the distributions barely overlap (off max 15.44 vs on min 15.39), so it is a repeatable penalty rather than noise |
| **allocation count** | **+39% process-wide, 137.2 M → 190.8 M objects** | measured. 05 §6 predicts "unchanged by construction"; that prediction is **wrong for this tree** — `EncodeRowPGCtx` alone contributes 44.6 M allocations for 7.2 M packed rows, about 6 per row, against ~1 per row for the legacy retain |
| **values** | **MATCH** on all 7 paired runs, `-diff` PASS ×3 | measured |

## Why the model is wrong — two independent reasons, both measured

Instrumented `buildGeometry` temporarily (removed):

```
D04GEO rows=1500000 width=2 avgVar=74.0 workMem=67108864
  | legacy entry=194.0 nbatch=4 | packed entry=142.0 nbatch=4
```

1. **`avgVarBytes` dominates the entry and packing cannot touch it.** The
   planner says 74 B/row of variable payload for the `orders` build; the
   measured retention is 120.2 B/row = 2×48 (Datums) + 24 (slice header) +
   ≈0 payload. The model's 194 B/row is ~62% too high, and the excess sits
   entirely in the term packing does not affect. **Correcting
   `avgVarBytes` alone takes 194 → 120 and `nbatch` 4 → 2 with no packing
   at all.** Only after that correction could packing (ideal ~63 B/row)
   reach `nbatch` 1.
2. **The model prices rows and ignores the table.** Peak live heap:
   `presizeLazyHash` **506 MB of Go maps** against `retainBuildRowHeap`
   **296 MB of rows** (5 private per-worker builds × 1,048,576 buckets).
   `hashsize` charges per bucket in the *sizing*, but the counter that
   drives growth and the `Memory Usage:` line counts only rows. **The
   largest memory consumer in this join is not the retention format.**

## The premise is stale, which matters more than the verdict

- **Q9 is not the shape D-05's notes describe.** TODO_ALL records the
  pre-state as `Batches: 2→1`, widths 1098 B vs PG's 23 B, 63.8 s. Q9 today
  is a **parallel** plan, 5 workers, ~15 s, and the batching join is
  `orders` at `Batches: 4`.
- **The retained rows are already narrow.** EX1 narrowing has landed for
  this build half: 2 columns, ~0 varlena payload, **120 B/row**. The
  bundle's ~5× premise was priced against 1098 B/row. Best case here — a
  real `[]PackedTuple` with no sentinel wrapper — is ~63 B/row, i.e.
  **1.9×, not 5×**, and only ~14% of the join's own peak memory.
- **The build is not shared.** `prebuildSharedHashJoins` declines to
  publish a spilling build, so each of the 5 workers builds all 1.5 M
  `orders` rows privately. That 5× memory multiplier is untouched by any
  part of the minimize-datum bundle.

## Encode is the cost, and it is asymmetric

Every build row is packed (7.2 M) while only matches are deformed (321 k).
R-3 is real at the encode end. A D-05 that stores `[]PackedTuple` would
need a dense byte arena for the tuples **and** an allocation-free encoder
to get allocations to neutral. Neither is in D-05's current scope.

## Two incidental findings

- **R-1 bit immediately.** `FormPackedTuple` is typed by the descriptor, so
  a schema column with an empty `Type.Name` round-trips an int key through
  the untyped fallback and stops hashing equal — two batch tests dropped
  ~43% of their rows before a type guard was added. Real plans carry types;
  several executor fixtures do not.
- **D-03's guard worked as designed.** `TestPackedSlotHasNoProducer` failed
  the moment the prototype called a constructor, naming the gates a
  producer owes.

## What this means for the plan

D-05 must not proceed as written. The ordered prerequisites are now:

1. **Fix `avgVarBytes`** so the entry model matches measured retention. On
   this witness it alone halves the batch count, which is the outcome D-05
   was going to claim.
2. **Charge the hash table** in the counter that drives growth, not only in
   the sizing — it is the larger consumer here.
3. **Re-measure the premise.** The 5× width claim is 1.9× post-EX1. Whether
   1.9× on ~14% of a join's peak justifies ~900 LOC is a different question
   from the one the bundle was scoped against, and it should be answered
   before D-05 restarts.

## Not measured

- A parallel-half batch count for a *shared* spilling build (worker hash
  counters die with the worker — 05 §6 flags this). It did not block here
  only because this build is declined for sharing.
- A clean split of encode CPU from decode CPU. The +6.8% is attributed to
  encode by the allocation evidence and the 7.2 M-vs-321 k asymmetry, not
  by direct CPU attribution.
- Anything outside the single-key resident lane: the composite/multi-key
  lane, null-key retention, and the FOR-UPDATE ctid build were left on the
  production path.
