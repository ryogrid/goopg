# EX3-04 — Sort spill runs and merge discipline (TODO_ALL E-01)

Status: **DESIGN — verdict first, implementation scoped to what the verdict
leaves standing.** Base `c67051743` (B-01c slice (c)).

Item text (`docs/design/not_ralph/minimize_datum/TODO_ALL.md`, E-01):

> Run formation on `flushChunk`, tape-style merge-back (logtape analogue,
> take3 10 §9). Spill thresholds are batching geometry: on pre-EX1 widths
> this is premature by rule (take3 13 §8.2, EX-P7).
> *design: take3 13 §5; gate: spilling-sort shapes; values + pin.*

The parent design (take3 13 §5, EX3-04) says the same thing with one extra
word: run formation and tape-style merge-back **"replacing whole-chunk
spill"**.

---

## 0. Verdict, stated before the evidence

**The mechanism E-01 asks to be built already exists at HEAD, and it was
built before the row was written.** `sortOp.flushChunk` sorts the chunk
*before* writing it — that is run formation — and `initMerge`/`popMerge`
run an N-way heap merge over the runs plus the unspilled tail. "Whole-chunk
spill" is not what the tree does. The row's WORK clause is therefore
**stale**, and no part of it is buildable as written.

What the row's remaining clause names — *"spill thresholds are batching
geometry"* — **is** live, and it is not a performance item. It is a
**planner/executor disagreement about the same number**:

| | threshold | merge fan-in | multi-pass |
|---|---|---|---|
| PG 18.3 `tuplesort` | `work_mem` | `tuplesort_merge_order(work_mem)`, `MINORDER 6 … MAXORDER 500` | yes (`logtape` polyphase) |
| goopg **cost model** (`cost_funcs.go:306-327`) | `cp.workMem` | `tuplesortMergeOrder(cp.workMem)` (`:427`, a line-for-line port incl. MINORDER/MAXORDER) | yes — charges `logRuns` passes |
| goopg **executor** (`operators.go:897-903`) | hard-coded `sortChunkBytes = 256 MiB` | unbounded — every run file open at once | no — always one pass |

`costSortRun`'s own doc comment forbids exactly this state of affairs
("two independently calibrated models competing inside one `addPath`
comparison … is what design ch. 04 §1 forbids"). It says that about hash
vs sort. It is equally true of planner-sort vs executor-sort.

So the design splits E-01 into:

- **(A) run formation + merge-back** — already delivered, `[x]` by
  inspection, no work;
- **(B) threshold sourcing** — executor should read `ctx.WorkMem`, the
  number the planner already prices against. Gated on §4's census;
- **(C) bounded merge fan-in** — the `logtape` residue. Gated on (B);
  unreachable without it;
- **(D) the gate the row demands** — ordering assertions over a
  *constructed* spilling witness, because the corpus supplies none.

---

## 1. What is actually in the tree (source, HEAD `c67051743`)

`internal/executor/operators.go`:

- `sortOp.Open` (`:904`) pulls the child, materialising each row and
  computing its ORDER BY key values once (`sortKeyVals`, `:1013`), into
  `rows` / `keyvals` kept in lockstep.
- At `chunkBytes >= limit` (`:960`) it calls **`flushChunk` (`:1315`)**,
  which **`sortChunk`s the chunk first** and only then streams it to a new
  spill file. That is a *sorted run*. It is not a whole-chunk dump.
- After the child is drained, `sortTailWithCTIDs` (`:1124`) sorts the
  residual tail.
- `Next` (`:1367`) takes the merge path when `len(spillFiles) > 0`:
  `initMerge` (`:1400`) opens one `spillReader` per run, primes each with
  one row, and pushes them into `sortHeap`; the unspilled tail is pushed as
  an in-memory `sortSource` **handing over its already-computed keyvals**
  rather than recomputing. `popMerge` (`:1430`) is the classic
  pop-emit-advance-push.
- The merge heap and the in-memory sort share **one comparator**,
  `lessKeyVals` (`:1039`), over **one key-evaluation function**,
  `sortKeyVals`. `sortKeyVals`' doc comment names the hazard this closes:
  *"a tail sorted by one comparator and merged against spill files by
  another emits out-of-order rows with no error"* (M0134-0191).

That is: quicksort runs + N-way merge, with comparator unification already
done. PG 18 forms runs the same way — replacement selection was removed in
PG 10, so `tuplesort` also quicksorts each run. goopg is **not** behind PG
on run formation.

**Provenance.** The run/merge machinery is `M0068-0006` (see `sortOp`'s
struct comment, `:783-789`); the comparator unification is `M0134-0191`.
Both predate the E-01 row's last rewrite, which is how the row came to
describe a tree that no longer existed.

## 2. What PG has that goopg does not (the honest residue)

Oracle: `postgres/src/backend/utils/sort/tuplesort.c`,
`postgres/src/backend/utils/sort/logtape.c` (read-only).

1. **Threshold.** PG spills when the in-memory tuple array exceeds
   `work_mem`. goopg spills at a constant 256 MiB and never reads
   `ctx.WorkMem` — the *only* operator family that does not.
   `materializeOp` (`operators_material.go:83`), `memoizeOp`
   (`:106`), the subquery cache (`subq_cache.go:23`), the hash join
   (`join_batch.go`) and the TID bitmap (`tidbitmap.go:261`) all budget on
   `ctx.WorkMem`. Sort is the outlier.
   Consequence at bench settings (`bench/tpch/setup_goopg.sh:71`,
   `work_mem = 64MB`): the planner charges disk-sort I/O for any sort it
   *estimates* above 64 MB, while the executor sorts up to 256 MiB in
   memory — a 4× band in which the plan is priced for an event that cannot
   occur. goopg's upper-rel row estimates are wrong by up to 789× high
   (`analysis/planner-refactor-take3/c13a-limit-sort-census-20260906`, Q22:
   est 9,460,201 vs actual 11,987), so the band is not hypothetical: the
   estimator routinely puts a sort in it.
2. **Bounded fan-in.** `logtape` merges at most `tuplesort_merge_order`
   tapes per pass and iterates. goopg opens **every** run file
   simultaneously (`initMerge`, `:1403`). At the current 256 MiB threshold
   the run count is small enough that this never bites; it becomes an fd
   and buffer-memory question the moment (B) lowers the threshold.
3. **One tape set vs one file per run.** `logtape` multiplexes runs into a
   single tape set with block-level free-list reuse, so peak temp disk
   approaches the input size once; goopg keeps one `pgsql_tmp` file per run
   until `Close`. Peak *bytes* are comparable; peak *file count* is not.
4. **Top-N bound** (`tuplesort_set_bound`) — **explicitly not this row**.
   It is C-13a, and C-13a is DEFERRED on measurement (0/100 sorts bindable
   *and* large). Named here only so a reviewer does not re-import it.

## 3. Why the B-01c unblock has a NEGATIVE sign for this row

The row was unblocked because B-01c slices (b) and (c) landed, so the
sorted-aggregate sort and the general ORDER BY sort now spill the
**narrowed** row, discharging §8.2's "premature on pre-EX1 widths"
objection. That is true, and it is the right reason to let the row proceed.

But the direction of the effect must be stated: **narrowing makes rows
narrower, so a sort of the same input reaches the spill threshold later, so
FEWER sorts spill.** EX-P7 says batching geometry must be computed over
narrowed widths; computed over narrowed widths, the geometry says there is
*less* spilling than the pre-B-01c census already found. The unblock makes
the item legitimate to attempt and simultaneously makes its witness rarer.
A design that quotes the unblock without quoting its sign is misreading it.

Corollary for §4: re-running the census at HEAD can only lower the
footprints the pre-B-01c census recorded. It is run anyway, because
"can only go down" is an argument, not a measurement, and because the
C-13a capture was taken on a cluster since found to be I/O-starved.

## 4. Measurement instrument, decided BEFORE implementation

### 4.1 The corpus census (does a witness exist?)

Inherited (`c13a-limit-sort-census-20260906`, goopg `00688e96c`, TPC-DS
SF0.5, 99 queries): **0 of 100 sorts spilled**; largest in-memory sort
footprint **26,210 kB (Q1)** = **10% of `sortChunkBytes`**; median direct-
child sort input 145 rows; all sorting ≤ 119.8 ms of 802 s.

Re-check at HEAD, not inherited:

- **TPC-H SF=1, all 22 queries** — never censused for sorts at all, and the
  one corpus with a 5,997,241-row merge-join input sort in its cost model's
  worked example (`costSortRun`'s comment). This is the census that could
  actually falsify the verdict.
- **TPC-DS SF0.5, the sort-heaviest queries** re-measured at HEAD
  (Q1, Q14, Q22, Q47, Q51, Q57, Q59, Q79 — every query whose recorded
  footprint is ≥ 4 MB, plus the largest-input Q78/Q67). The verdict depends
  only on the *maximum*, so the maximum is what is re-measured; the tail is
  not re-run for an hour to reconfirm 145 rows.

Instrument: `EXPLAIN (ANALYZE, VERBOSE OFF)`, reading the `Sort Method:`
line `publishSortStat` (`:988`) emits — `external merge`/`Disk` iff a run
file exists, `quicksort`/`Memory` with `peakBytes` otherwise. Private
clone of the data dir, a 55xx port, own `GOOPG_CG_UNIT`, through
`scripts/goopg-test-run.sh`.

**Decision rule, fixed now.** If max footprint < `work_mem` (64 MB at bench
settings) on both corpora, then (B) cannot change any corpus plan's
behaviour and cannot be scored by either suite; (B) is then a
faithfulness/consistency change to be justified by the planner agreement
argument alone, landed with a `changed=0` plan pin, and **no timing claim
is made**. If any query's footprint lands in the 64 MB–256 MiB band, that
query is the witness and (B) gets a real A/B.

### 4.2 The constructed spilling witness (does the path work?)

The corpus cannot exercise the merge at all, so the merge is exercised
directly: a `sortOp` at `chunkLimitBytes` small enough to force ≥ 8 runs
over an input with

- **DESC** keys, **NULLS FIRST** and **NULLS LAST** keys, and a
  mixed multi-key list (`a ASC NULLS LAST, b DESC NULLS FIRST`);
- ties spanning a run boundary (stability is *not* asserted across the
  merge — the heap is not stable and PG's is not either; ordering is);
- NULLs in the leading key.

Assertion is **ordering, not membership**: for every adjacent output pair
`(prev, cur)`, `!less(cur, prev)`, evaluated by an independent oracle
comparator written from the `SortKey` list, **not** by calling
`o.lessKeyVals`. Membership is asserted too (multiset equality against the
input), because an ordering-only assertion passes on a truncated stream.

This is the gate the row asks for ("gate: spilling-sort shapes") and it is
the one that covers `operators.go:1010-1015`: a merge that disagrees with
the in-memory sort emits out-of-order rows with **no error**. The existing
`sort_external_test.go` asserts spill-file count and row count; it does not
assert order, and it does not cover DESC or NULLS placement.

## 5. Defect found while reading (independent of the verdict)

`sortOp.Close` (`:1345`) clears `rows`, `ctids`, `idx`, `heap`,
`spillFiles` — but **not `keyvals` and not `mergeReady`**. The struct's own
contract (`peakBytes` doc comment, `:1350`) is that rescan is Close+Open.
On a second `Open`:

- `rows` restarts at nil while `keyvals` keeps the previous Open's N
  entries, so `len(keyvals) != len(rows)`. `sortChunk` (`:1073`) detects
  that and falls back to `lessRows` — correct, but O(N log N) interpreted
  `evalExpr` calls, the exact cost M0134-0191 removed.
- If the second Open spills, `initMerge` builds the tail `sortSource` with
  `keyvals: o.keyvals` (`:1417`), whose entries belong to the *previous*
  scan. `sortSource.advance` reads `keyvals[idx]` positionally. That is
  **silently mis-ordered merge output** — the M0134-0191 hazard, re-entered
  through the back door.
- Worse: `mergeReady` survives Close as `true` while `heap` is nil, so a
  second spilling Open skips `initMerge` entirely and `popMerge` calls
  `o.heap.Len()` on a nil `*sortHeap` → nil dereference.

**Reachability at HEAD: none found.** The only Close+Open rescan of a plan
subtree is the correlated-subquery path (`expr.go:10987`), gated on
`planIsIndexScanBased` (`expr.go:11070`), whose whitelist is
IndexScan/BitmapHeapScan/Project/Aggregate/Filter — `Sort` is not admitted.
`materializeOp.Rescan` replays a cache and does not re-Open. So this is
**latent, not live**.

It is fixed anyway, in this row, because it is three lines in `Close`, it
is in the exact function E-01 owns, and the failure mode is the silent
wrong-answer class the row's own gate exists to catch. A latent
wrong-answer bug in the merge path is squarely E-01's business even when
the perf half of E-01 is closed as unwitnessed.

## 6. Scope, in landing order

1. **(D) ordering gate** — `sort_spill_order_test.go`: the §4.2 witness.
   Pure test, no production change. Lands first so the later changes are
   covered by it.
2. **(E) `Close` hygiene** — clear `keyvals`, `mergeReady`, `sortErr`,
   `ctidsDisabled`, `peakBytes` in `Close`; a rescan test that
   Close+Open+drains twice, once in-memory and once spilling, asserting
   order both times. Zero behaviour change on the live paths (§5).
3. **(B) threshold sourcing** — `chunkLimit()` returns `ctx.WorkMem` when
   > 0, else `sortChunkBytes`. **Conditional on §4.1's decision rule** and
   on a `changed=0` plan pin plus a values pass; abandoned rather than
   forced if the census puts a corpus query in the band and the A/B is
   negative.
4. **(C) bounded fan-in** — not attempted in this row unless (B) lands and
   the census shows a run count that makes it matter. Ledgered otherwise.

## 7. Gates

`scripts/tpch-spotcheck.sh` (Q12=2 / Q13=35); TPC-H values 24/24; TPC-DS
SF0.5 `PASS=95`, all-zero; `go test -race ./internal/executor/...`;
plan check as a **same-binary same-commit A/B**, not against
`plan_snapshots/c20a-c06s-plancost-rows-20260907.txt` (drifted — C-19's
base-rel scan repricing landed after that pin was cut);
`plan_snapshots/b01c-narrowsort-20260907.txt` is the fresh pin.

## 8. What would make this row wrong as written

Stated so the next reader does not have to re-derive it:

- "Run formation on `flushChunk`" — **already there** (M0068-0006).
- "tape-style merge-back … replacing whole-chunk spill" — **already
  there** (M0068-0006 + M0134-0191). Nothing is replacing anything.
- The live residue is *threshold sourcing and fan-in bounding*, which the
  row compresses into the single clause "spill thresholds are batching
  geometry".
- The B-01c unblock is real but its sign is **negative** for this row
  (§3).

---

## 9. Review record

*(Two adversarial reviews are recorded here, inline, before implementation
— a source-falsification pass over `internal/` and a PG 18.3 oracle pass
over `postgres/`. Corrections are recorded as corrections, not silently
folded into the text above.)*

### 9.1 Source-falsification pass (`internal/`)

*(pending)*

### 9.2 PG 18.3 oracle pass (`postgres/`)

*(pending)*

## 10. Measurement record

*(pending — §4.1 census at HEAD, §4.2 constructed witness)*
