# Parallel sorting in goopg — design

**Status**: design, pre-implementation.
**Scope**: make `Sort` participate in parallel query execution, following
PostgreSQL 18.3's internal design. Oracle: `./postgres/` (read-only).

## 1. Objective

TPC-H q16 currently plans as `Sort → Gather → … → Parallel Index Only Scan`
(M0134-0189): the scan and joins are parallel, the sort is serial in the
leader. A CPU profile of that query puts **34 % of the run in
`sortOp.lessRows` / `sortTailWithCTIDs`**, and goopg is 1.3 s against PG's
0.3 s on it. The sort is now the dominant term on every parallel plan that
ends in `ORDER BY`.

The task: let sorting run in parallel, matching PG's design rather than
inventing one.

## 2. What PostgreSQL actually does — checked, not assumed

**PG has no parallel Sort *node*.** Grepping the oracle for the parallel
tuplesort entry point:

```
tuplesort_initialize_shared  ->  access/nbtree/nbtsort.c:1527, :1544
                                 access/brin/brin.c:2480
                                 access/gin/gininsert.c:1038
```

Every caller is an **index build**. No executor node — not `nodeSort.c`, not
`nodeIncrementalSort.c` — ever creates a `Sharedsort`. PG's parallel tuplesort
(`Sharedsort`, `worker_nomergeruns`, `leader_takeover_tapes`) exists solely so
`CREATE INDEX` can spread its sort across workers.

**PG's query-level parallel sort is Gather Merge over per-worker Sorts.**

```
Gather Merge
  -> Sort                 (one per worker, each sorting its own partition)
       -> Parallel <scan>
```

`nodeGatherMerge.c` binary-heap-merges the already-ordered worker streams. The
sort work is divided; the merge is the leader's only ordering cost. This is
the entire mechanism, and goopg already has the shape: `findPartialSubtree`'s
P7 arm.

**PG precomputes the FIRST sort key only, and deliberately not the rest.**
`SortTuple.datum1` is "value of first key column" (`utils/tuplesort.h:151`),
filled at *put* time (`tuplesortvariants.c:767`, `:809`). But
`tuplesort.h:128-133` states the limit explicitly:

> "We could extract and save all the key columns not just the first, but this
> would increase code complexity and overhead, and wouldn't actually save any
> comparison cycles in the common case where the first key determines the
> comparison result."

`comparetup_heap` compares `datum1`, then delegates ties to
`comparetup_heap_tiebreak`, which calls `heap_getattr` for keys 2..k **on every
tied comparison**. PG's extraction count is therefore
`N + 2·(k−1)·(comparisons that tie on key 1)`, not `k·N`.

PG also abbreviates the leading key (`sortKey->abbreviate = (i == 0 && …)`,
`tuplesortvariants.c:233`; the abbreviated value replaces `datum1`,
`tuplesort.c:1193-1194`) and *abandons* abbreviation for tuples read back from
tape (`tuplesort.c:2024-2032`) — the same constraint §5.4 records for goopg's
spill path.

## 3. Why goopg's per-worker sort is too slow to use

goopg has the P7 arm, and M0134-0189 had to **switch it off** for index-only
driving scans (`sortPartialRootPays`) because per-worker sorting measured
*slower* than one leader-side sort: q16 1.5 s → 2.3 s, q13 4.2 s → 6.8 s.
That was the right call on the measurement and the wrong conclusion about the
cause. The cause is this, in `sortOp.lessRows`:

```go
for _, k := range o.keys {
    av, err := evalSortKeyValue(k.Expr, a, o.ctx)   // evaluated per COMPARISON
    bv, err := evalSortKeyValue(k.Expr, b, o.ctx)
    ...
}
```

The key expressions are evaluated **inside the comparator**, so every
comparison re-derives both operands. PG re-derives too — for keys 2..k, on tied
comparisons (§2) — so the gap is **not** the number of extractions. It is the
**cost of one extraction**: PG's is `heap_getattr` against a deformed tuple;
goopg's is a full interpreted `evalExpr` dispatch.

Two corrections to an earlier draft of this section, both from review:

- A closed-form `2·k·N·log₂N` model is not usable. `sort.SliceStable` is
  `symMerge`-based (O(n log²n)); measured at n=20 480 it performs **371 439**
  comparisons, 1.27× the `N·log₂N` figure. And `lessRows` short-circuits on the
  first differing key (`operators.go:979-980`), so the per-comparison factor is
  between 2 and 2k depending on leading-key cardinality.
- TPC-H q16 is the bad case for both engines: its leading key is
  `count(distinct ps_suppkey)`, a handful of small integers over 20 480 rows,
  so key 1 ties almost always and *both* engines fall into their multi-key
  path constantly.

The honest statement, and the one the implementation is measured against: goopg
performs a comparable NUMBER of key extractions to PG on this query and pays
far more per extraction. Precomputing keys removes the per-comparison
evaluation entirely, which is a larger change than PG makes — see §4.1.

**Amdahl ceiling.** The sort is 34 % of q16's run, so a comparator that became
free would take 1.3 s to ≈0.86 s — a 1.5× whole-query bound, still ~2.9× off
PG's 0.3 s. Neither stage below closes that alone, and this design does not
claim to.

## 4. Design

Three stages, smallest first, each independently verifiable.

### Stage 1 — precomputed sort keys (goopg's `SortTuple.datum1`)

Evaluate every key expression **once per row**, at the point the row enters the
sort, and store the resulting `Datum`s beside it. The comparator then compares
stored datums and evaluates nothing.

**This is a deliberate DIVERGENCE from PG, and the doc says so because §4's
own decision rule is PG-fidelity.** PG stores only key 1 and re-extracts the
rest (§2); goopg stores all k. The tradeoff differs because the unit cost
differs: `heap_getattr` is cheap enough that PG's complexity argument wins,
an interpreted `evalExpr` is not.

- `sortOp` gains a parallel `keyvals [][]Datum` (row-major, `len(keys)` wide),
  built in `Open` as rows are pulled — the same place `ctids` is already kept
  in lockstep. It must be populated through **`evalSortKeyValue`**, not
  `evalExpr`, or the reg*-OID family (`operators.go:931`) sorts differently.
- **`keyvals` must be truncated at the spill point.** `Open` does
  `o.rows = o.rows[:0]` and drops `ctids` after `flushChunk`
  (`operators.go:842-849`); a `keyvals` that is not reset there is offset by
  every previously spilled row and every later comparison reads the wrong key.
  Silent wrong results, and the most likely way this lands broken.
- **`sortChunk` is NOT permutation-based** (`operators.go:861-865` is a bare
  in-place `sort.SliceStable`); only `sortTailWithCTIDs` is. Converting
  `sortChunk` to permutation form is part of this stage, not a given.
- `lessRows` becomes `lessKeys(i, j int)` over stored datums.
- Evaluation errors move from the comparator (where `sortErr` exists precisely
  because a comparator cannot return one) to the pull loop, where an error can
  simply be returned. `sortErr` stays for the spill/merge path.

This is stage 1 *because it is worth doing whether or not anything becomes
parallel*: it speeds the serial sort by the same factor.

### Stage 2 — the same for `gatherMergeOp`, and a latent NULL-ordering bug

`gatherMergeOp.lessRows` has the identical per-comparison evaluation, and it
runs on the leader for every row the merge emits.

It also **disagrees with `sortOp` about NULL ordering**:

| | NULL placement |
|---|---|
| `sortOp` | `return k.NullsFirst` |
| `gatherMergeOp` | `return k.Desc` |

These coincide only for PG's *defaults* (`NULLS LAST` for ASC, `NULLS FIRST`
for DESC, i.e. `NullsFirst == Desc`). Under an explicit `ORDER BY x ASC NULLS
FIRST` the worker sorts and the leader merge order NULLs oppositely, so a
Gather Merge would emit misordered rows. **This is live on HEAD, not latent** — `sortPartialRootPays`
(`parallel.go:334-337`) declines only for `*IndexOnlyScan`, so P7 fires today
for seq- and bitmap-driven sorts. Reproduced against the oracle before any
change here:

```
select nullif(l_linenumber,1) from lineitem order by 1 asc nulls first
  plan : Gather Merge -> Sort (NULLS FIRST) -> Seq Scan
  goopg: a NULL appears at row 1183498, AFTER non-NULLs
  PG   : correctly ordered
```

It is therefore lifted OUT of this staging and landed as its own wrong-results
fix, with `TestGatherMergeExplicitNullOrdering` covering the four
NullsFirst×Desc combinations (the two where they differ fail without the fix).
The same change switches the merge to `evalSortKeyValue`, which it also
lacked — a second live divergence for `ORDER BY x::regclass`.

### Stage 3 — let the planner choose the parallel sort again

With stages 1–2 landed, remove `sortPartialRootPays`'s index-only-scan
exception so P7 takes `Sort` over a partial-capable subtree as the partial
root again, giving PG's shape:

```
Gather Merge -> Sort -> Parallel Index Only Scan
```

The decision stays where it already is (`findPartialSubtree`) and stays a
capability test, not a preference: no query is named anywhere.

### Explicitly NOT in scope

**A bespoke multi-threaded sort operator.** PG has no such node for queries
(§2), so adding one would be a divergence, not parity — and the measured
bottleneck is redundant work, not insufficient cores. Sorting a partition per
worker *is* PG's parallel sort, and stage 1 is what makes it affordable.

## 5. Correctness constraints

Every one of these is currently satisfied and must remain so:

1. **Stability — in-memory only, and that is the pre-existing state.**
   `sort.SliceStable` makes the in-memory tail stable, but `sortHeap.Less`
   (`operators.go:1160-1162`) and `gmHeap.Less`
   (`operators_gather_merge.go:178`) carry **no source tiebreak**, so equal
   keys arriving from different spill chunks or different workers come out in
   heap order. PG offers no stability guarantee either, so this is documented
   rather than fixed — but it must not be asserted as an invariant, and stage 3
   widens the unstable path.
2. **NULL ordering** per key: `NullsFirst`, independent of `Desc` (§4.2).
3. **`ctid` lockstep.** `sortOp.ctids` must stay aligned with `rows` through
   any permutation — a mis-permuted TID stamps a row lock on the wrong row.
4. **Spill path.** Once `flushChunk` runs, `ctidsDisabled` is set and the
   N-way merge reconstructs rows without TIDs. Precomputed keys must either
   be re-derived after a spill or the spilled path must keep using expression
   evaluation; the merge comparator reads rows it decoded from disk.
5. **Evaluator errors** must still surface (`sortErr`), and must not make the
   comparator non-strict-weak-ordered. Moving evaluation to the pull loop makes
   an `ORDER BY a/b` error on a row that would never have been compared — more
   PG-faithful (PG projects below the Sort) but user-visible.
6. **One comparator, not two.** If the tail sorts on precomputed keys while
   `initMerge`/`popMerge` (`operators.go:1074-1102`) re-evaluate, the spilled
   merge compares the tail against spill files with a *different* comparator
   (`initMerge:1088-1090` pushes the tail as a merge input). Any disagreement
   emits out-of-order rows with no error. Either both paths use precomputed
   keys, or neither does.
6. **Serial/parallel row identity.** The union of worker outputs must equal
   the serial result exactly — the check that caught the duplicate-rows bug
   when Gather and the block allocator were first connected.

## 6. Verification

- `go test ./internal/executor/ ./internal/optimizer/` — sort, gather-merge,
  regsort, external-sort and spill suites.
- Explicit `ORDER BY … NULLS FIRST/LAST` over a parallel plan, both
  directions, asserted against PG.
- TPC-H 21-query byte-comparison; time every query whose plan changes, both
  arms, fresh equal-age servers.
- `scripts/tpch-spotcheck.sh`, units precommit, TPC-DS SF0.5 sweep.
- Report per-query TPC-H and TPC-DS timings with PG's measured times and the
  goopg/PG ratio.

## 7. Risks

| risk | mitigation |
|---|---|
| `keyvals` not truncated at the spill point | named explicitly in §4.1; `sort_external_test.go:30,66` pin spill behaviour and must both run |
| Two comparators disagreeing across the spill boundary | §5.6 — one comparator on both paths |
| Sort memory multiplies by worker count | `sortChunkBytes` is 256 MiB **per `sortOp`** (`operators.go:793`), and P7 builds one per worker, so a 4-worker Gather Merge budgets 1.25 GiB where serial budgets 256 MiB. PG has the same per-worker `work_mem` semantics, so parity argues for keeping it — but it must be checked against the gates' cgroup cap before stage 3 widens P7 |
| Counting key datums toward `chunkLimitBytes` changes when spills happen | `sort_external_test.go` `TestM0068SortNoSpillBelowChunk` can flip either way; run it |
| Stage 3 re-enables P7 where it still does not pay | **No mitigation exists yet.** The earlier claim that index-page sizing handles q13 was wrong: `cce5a1bbc` added the sizing and `sortPartialRootPays` in the *same* commit, so the q13 4.2 s → 6.8 s regression (`parallel.go:320`) was measured *with* the sizing already in place. Stage 3 must be gated on a fresh measurement of q13/q16/q22, not on that claim |

## 8. Deliberately out of scope, and why

- **Bounded / top-N sort.** `ORDER BY … LIMIT k` currently sorts each worker's
  whole partition (`parallel.go:282-283`; `parallel_merge_test.go:73` pins that
  P7 fires under a `Limit`). PG uses a bounded sort here and it is likely the
  single largest win available on that shape. Out of scope only because it is
  orthogonal to making sorting parallel; it should be its own item.
- **Incremental Sort.** PG has `nodeIncrementalSort.c` and
  `Gather Merge → Incremental Sort → Parallel <scan>` is a common PG shape.
  goopg has no such node (referenced as deferred at
  `groupagg_indexorder.go:18`). For q16 — whose sort sits over an index-only
  scan with a usable prefix — it may be a better answer than parallelising the
  full sort. Named here so it is a choice, not an omission.
- **Abbreviated keys.** PG's leading-key abbreviation is a large constant
  factor goopg has no analogue for; any goopg/PG per-comparison comparison
  should say so.


## 9. Outcome

Implemented and measured. Results in [`PERF.md`](./PERF.md).

| stage | disposition |
|---|---|
| §4.2 NULL ordering (+ `evalSortKeyValue`) | **SHIPPED** as `6fa1f400d`, standalone — it was a live wrong-results bug, not a prerequisite |
| §4.1 precomputed sort keys | **SHIPPED** — q16 1.4s → 0.9s (−36 %), q01 −10 %, q18 −5 %; 21/21 results byte-identical, no plan moved |
| §4.3 re-enable P7 for index-only driving scans | **REFUTED by measurement, not shipped** |

### 9.1 Stage 3 was refuted, and by its own success

The review flagged that stage 3 had no mitigation (§7). It was implemented
anyway and measured, because the prediction was that a cheap comparator would
make per-worker sorts pay. It does the opposite:

| query | leader-side sort (shipped) | per-worker sorts (P7) |
|---|---:|---:|
| q16 | **0.9s** | 1.6s (+78 %) |
| q10 | **3.0s** | 3.4s (+13 %) |
| q13 | **4.8s** | 5.1s (+6 %) |

Stage 1 is *why* stage 3 fails. The argument for moving the sort into the
workers was that the sort dominated — 34 % of q16's run. Precomputing the keys
removed that dominance, and against a sort that is no longer the bottleneck,
N worker sorts plus a k-way merge cost more than one leader-side sort over a
parallel scan. `sortPartialRootPays` stays.

This is worth stating plainly because the design predicted the opposite: the
two stages were expected to compose, and instead the first one obsoleted the
third.

### 9.2 What this says about the original objective

goopg's parallel sorting is PG's design — `Gather Merge` over per-worker
`Sort`s — and it remains available and correct (it is what q01, q03 and others
use today). What changed is that goopg no longer *needs* it on the shape that
motivated this work. q16's remaining 3.0x against PG is not the sort and not
the scan; both now match PG's plan and its parallelism. Finding what it is, is
the next question, and this document does not answer it.

### 9.3 Still open

- **Bounded / top-N sort** (§8) — untouched, and still the largest single win
  available on `ORDER BY … LIMIT` shapes.
- **Incremental Sort** (§8) — goopg has no such node.
- **Stability on the spill and merge paths** (§5.1) — no source tiebreak in
  `sortHeap`/`gmHeap`; documented, not fixed, matching PG's own non-guarantee.
- **Per-worker sort memory** (§7) — 256 MiB × W remains unbudgeted; it did not
  bite because stage 3 is not shipped.