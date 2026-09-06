# Planner + Executor refactor — performance report

Scope: the `docs/design/not_ralph/minimize_datum/TODO_ALL.md` workstream.
Branch `plan-narrowing-and-etc`. Dates 2026-09-05 and 2026-09-06.

This report states what changed, what it cost or bought, and — with equal
weight — what could not be measured and what got worse. Every number below
comes from a run recorded in this repository; nothing is modelled.

Reading order, if you want the argument rather than the list: §2.5 (how the
27% was found), §5.6 and §5.9 (the two structural findings, which between
them relocated the blocker for a whole track twice), §5.8 (the executor's
own win), then §7 — which is the part most likely to change how you read
the rest.

## 1. Headline

**TPC-H SF=1 serial: 138.58 s → 100.79 s, a 27% reduction, from a
one-constant cost-model calibration — with plan parity against PostgreSQL
improving at the same time.**

| Q | before | after | factor |
|---|---|---|---|
| Q5 | 21.60 s | 4.07 s | 5.3x |
| Q7 | 15.72 s | 5.86 s | 2.7x |
| Q3 | 6.25 s | 2.67 s | 2.3x |
| Q9 | 13.17 s | 7.06 s | 1.9x |
| **suite (21 labels)** | **138.58 s** | **100.79 s** | **1.38x** |

Values 24/24 MATCH; TPC-DS SF0.5 PASS=95 MISMATCH=0 CKMISMATCH=0; no query
regressed outside the noise band. Plan parity moved
`match=5 shapediff=15` → `match=6 shapediff=14`, so goopg's plans moved
*toward* PG's shapes, not merely toward faster ones.

The constant is `indexProbeCostMultiplier`, and the finding is less about
the number than about how it was found — see §2.5.

**And one measurement result that matters more than any single
optimisation: the benchmark harness could not previously tell a real change
from a re-run.**

| | before | after |
|---|---|---|
| A/A plan capture, same binary, estimate lines differing | 455 | **0** |
| A/A plan capture, same binary, plan-SHAPE lines differing | 27 | **0** |
| `make plan-gate` in `MODE=costs` (cost-exact) | not reachable | **22/22 MATCH** |

The work items that landed in this session are **values-neutral and
timing-neutral by design** (they change where a qual is evaluated, not what
the query computes). The measurable deliverable is therefore the gate
itself, plus two items closed as already-satisfied and two closed as
not-worth-doing on evidence.

## 2. The measurement problem, and why it dominated the session

Two captures of the **same binary**, taken back to back on fresh servers
over the same data, disagreed on 455 estimate lines and 27 plan-shape
lines — including whole join-method flips (TPC-H Q3 Nested Loop vs Merge
Join, Q9 hash spine vs merge spine). A/B noise between two *different*
binaries measured 420/14, i.e. **smaller than the A/A noise**.

Under those conditions the plan pin reports changes no commit caused, and a
real regression is indistinguishable from a re-run. It cost a wrong
conclusion in this session before it was found: C-02c appeared to double
TPC-H Q9, 12.7 s to 26.4 s. Re-measured against pinned statistics the same
comparison is 11.71 s vs 11.54 s. **The change was innocent; the instrument
was broken.**

Three independent causes, all statistical rather than logical:

1. The capture harness re-ANALYZEs every table per capture (goopg
   statistics are per-connection, so `estimate-audit -warm-stats` defaults
   on), with a wall-clock-seeded reservoir sample.
2. The autovacuum launcher re-ANALYZEs every 60 s, so statistics moved
   between the arms of an A/B and even between two queries of one arm.
3. goopg's ANALYZE updates the **persisted** statistics, so a capture
   depends on whether anyone ANALYZEd the data directory earlier.

Fixed by `GOOPG_ANALYZE_SEED` (mixed with the relation OID so per-table
reservoirs stay independent), `autovacuum = off` written by the **tracked**
cluster generator, and a matching warm-stats step in `plan-snapshot` so
capture and diff normalise identically. Unset, the seed keeps upstream
behaviour bit-for-bit.

Commits: `870732855`, `c5241fecb`, baseline `58313f0b2`.
Design: `docs/design/planner-gate-reproducibility/DESIGN.md`.

## 2.5. How the 27% was found: by reading a comment against its own code

`indexProbeCostMultiplier` exists because — in its own comment's words —
PG's constants under-cost goopg's NL-index probe, since goopg materialises
the whole TID list eagerly per probe, and the cost-driven search would
therefore pick "ruinous PG-shaped NL plans". The comment ends: *"the
calibrated default is set once a value is validated on SF1."*

**It shipped at 1.0 — the exact value it was created to replace.** The knob
was created, documented, and left at the wrong value because the validation
it was waiting for was never run. The item in the plan (C-20d) proposed to
*retire* the flag, which at 1.0 would have made the mis-costing permanent.

Measured at 1, 2 and 4; 2 and 4 select the same plans on the probed queries
and 4 is marginally worse on Q7, so 2 is the smaller departure from PG's
constants that buys the whole win. The knob is kept, not retired: it is
load-bearing at 2.0, and its comment expects another recalibration once the
underlying execution defect is fixed. A validated default beats both an
unvalidated one and a hard-coded one.

The general lesson, which is why this is in the report rather than only in
the commit: **a documented-but-unapplied calibration is invisible to every
gate.** Values pass, plans pass, tests pass — the tests pinned 1.0 as if it
were a decision. Only reading the comment against the constant it describes
surfaces it.

## 3. TPC-H SF=1, serial, current state

Regime: fresh capped server per arm, GOGC=100 / GOMEMLIMIT=12 GiB,
S-cold, `work_mem` 64 MB, statistics pinned, port 65433 `tpch@tpch`.
Two baseline arms bracketing the change arms, so drift is visible.

| Q | s | Q | s | Q | s |
|---|---|---|---|---|---|
| Q1 | 7.15 | Q9 | 13.17 | Q17 | 0.55 |
| Q2 | 0.97 | Q10 | 2.59 | Q18 | 31.93 |
| Q3 | 6.25 | Q11 | 0.14 | Q19 | 1.95 |
| Q4 | 1.56 | Q12 | 12.71 | Q20 | 1.32 |
| Q5 | 21.60 | Q13 | 5.16 | Q21 | 12.80 |
| Q6 | 0.68 | Q14 | 0.44 | Q22 | 0.67 |
| Q7 | 15.72 | Q16 | 0.77 | | |
| Q8 | 0.45 | | | | |

Total over the 21 timed labels: **138.58 s** (repeat arm 136.21 s, so
run-to-run drift is ~1.7%). **This is the PRE-calibration series**; after
C-20d the same 21 labels total **100.79 s** (§1).

**This total is NOT comparable to the 235 s recorded in the A-04 baseline**
(`analysis/planner-refactor-take3/a04-baseline-20260905/README.md`). That
figure was taken under the old regime — autovacuum on, sampler unpinned —
so it measured a different statistics state, and it includes a Q15b label
this arm does not. The honest statement is that the two numbers were
produced by different instruments, not that the suite got 1.7x faster.
Establishing a comparable series is what section 2's work makes possible
going forward.

## 4. What landed, and its measured effect

| item | effect on values | effect on plans | effect on time |
|---|---|---|---|
| **C-20d index-probe calibration 1.0→2.0** | **24/24 MATCH** | **95 shape lines; parity 5/15→6/14** | **suite −27%** |
| C-02c qual MOVE on proven all-INNER paths | 24/24 MATCH | byte-identical | within noise |
| C-02d qual MOVE across preserved-side outer links | 24/24 MATCH | byte-identical | within noise |
| gate reproducibility (3 commits) | n/a | makes plans reproducible | n/a |
| C-19a/b consider_parallel + priced partial seq-scan paths | 24/24 MATCH | byte-identical (nothing consumes them yet) | within noise |
| C-10a grouping-sets cardinality | 24/24 MATCH | byte-identical | within noise |
| D-05 prereq #1 executor entry width | 24/24 MATCH | byte-identical | within noise |
| hash `Memory Usage:` includes buckets | 24/24 MATCH | byte-identical | n/a (EXPLAIN text) |
| **E-09a shared SPILLING hash build** | 24/24 MATCH | byte-identical | **Q9 8.85 s → 7.85 s (−11%)** |
| C-19c parallel eligibility for plain index scans | 24/24 MATCH | byte-identical | within noise (shape not yet chosen) |
| E-11 prefetch-depth knob (instrument only) | 24/24 MATCH | byte-identical | n/a (default unchanged) |
| E-09b load-once-per-batch | forced-shape values green | n/a | n/a — **memory**: 1 live batch table where there were 4 |
| C-19d `PathGather`/`PathGatherMerge` priced | 24/24 MATCH | byte-identical (ships default-OFF) | none by construction |
| E-11 AIO `ReadStream` | n/a | n/a | **DROPPED** — no depth wins; the mechanism is inert where it ships |
| C-19f partial hash-join path | 7/7 MATCH x 42 runs | TPC-DS 99/99 identical at default | **Q21 −51%**, Q9 +94%, Q10 +30%; default stays off |
| E-14 keyed inner spill frames | 24/24 MATCH | byte-identical | neutral; retires `buildKeyOfRow` |
| rowest B1 + A1 (estimate fixes) | 24/24 MATCH | shapes unchanged, 6 cost/rows lines moved | suite −1.4%, **Q2 −55%** |
| coop parallel deform-bound fix | **corrects a silent wrong answer** | n/a | n/a |

C-19a/b are the direct response to §5.6: the search now HAS partial paths
and prices them with `cost_seqscan`'s parallel arm, but by construction
nothing consumes them until C-19d. Review found the parallel-safety
classifier fail-open in four places; all four were fixed in the same
commit, since each becomes a wrong answer the moment a partial path is
chosen.

C-02c and C-02d remove a **double evaluation**: the pass previously copied
each pushed conjunct onto the join input while leaving the original in the
residual Filter, so the executor evaluated it twice and the plan carried
Filters PG never builds. Both are values-neutral and produced no plan-shape
movement on either suite, which is the expected outcome — the conjunct was
already being evaluated below; what changes is that it is no longer *also*
evaluated above. The TPC-H corpus has few shapes where the residual sits on
a hot path, so no timing win was claimed and none was measured.

TPC-DS SF0.5 for C-02d: **PASS=95, MISMATCH=0, CKMISMATCH=0, ERROR=0,
TIMEOUT=0.**

## 5. What was closed without code, on evidence

- **D-01 TupleDesc descriptor fields** — landed, additive, no consumer yet.
  Values 24/24, plans byte-identical, plan-gate PASS. Its agreement test
  spans two of the four in-tree pg_type.dat transcriptions, so they cannot
  drift further silently.
- **F-03 pointer-free `Datum`** — dropped under rule 3. The 2x arithmetic
  is real (`Datum` measures 48 B; `Buf` is exactly the 24 B slice header;
  only 18 non-test references), but `Buf` is the detach target that gives a
  retained value a lifetime independent of a resettable producer arena.
  Removing it leaves only unbounded alternatives, and the one prior attempt
  returned 0 rows on seven queries. The win is also dominated on the same
  sites by the packed-row path D-02 just cleared.
- **E-08 parallel filter compilation** — dropped by dependency on E-04's
  measurement. **E-07** re-scoped: two of its three justifications died
  with E-04 and E-08, and the third was already satisfied.

- **F-02 probe-seam re-materialisation** — already satisfied in tree.
  M0127-P1.1's probe-side slot chaining is default ON, and the in-tree
  benchmark (which keeps the old seam runnable behind a kill switch)
  measures `chained` **432 ns/op, 0 allocs/op** against `off` **1115 ns/op,
  10 allocs/op**. The pool round-trips the item was filed against are gone,
  not reduced.
- **D-02 derived-column type fidelity** — verdict **PROCEED**. Census over
  both corpora: 0 declining columns of 160,302; 0 plan nodes of 5,876; 0
  retention sites of 985. The load-bearing result was a *design*
  correction: the allow-list definition in `04-target-design.md` §3.1 would
  have declined every text column in both suites and produced a false STOP.

## 5.5. The measurement that stopped a 900-line slice

**D-04 (MD-03.5)** exists to decide D-05 before ~900 LOC is sunk, and it
was allowed to return a negative result. It did.

| number | result |
|---|---|
| batch count | **4 → 4, unchanged** |
| retained bytes | −14.2% (join accounting), −24.4% (live heap) |
| wall time | **+6.8%**, n=7 per arm, distributions barely overlapping |
| allocation count | **+39%** |
| values | MATCH |

Stopping rule 05 §6, in its own words: *"batches unchanged → the model in
D-3 is wrong. Fix the model before touching another site."*

Two measured reasons, both independent of packing:

- **`avgVarBytes` is ~62% too high** — the model says 194 B/row where
  retention measures 120 — and the excess is in a term packing cannot
  touch. **Correcting it alone takes the batch count 4 → 2 with no packing
  at all**, which is the outcome D-05 was going to claim.
- **The model prices rows and ignores the table.** Peak live heap is 506 MB
  of hash-map buckets against 296 MB of rows. The largest memory consumer
  in this join is not the retention format.

The stale premise matters more than the verdict. The bundle was scoped
against 1098 B/row; EX1 narrowing has since taken this build half to
**120 B/row over two columns**, so the ~5× width premise is **1.9×**, on
about 14% of the join's peak. And 05 §6's prediction that allocation count
is "unchanged by construction" is wrong for this tree: the encoder costs
about six allocations per packed row against one for the legacy retain.

Two harnesses came out of it: the query-level live-heap sampler that 05 §6
records as not existing, and a separately ledgered finding that each of
five parallel workers builds all 1.5 M rows privately, a 5× multiplier
nothing in this bundle addresses.

## 5.6. Five measurements that relocated Track D's blocker

Track D's premise is that goopg retains rows too wide, so packing them will
cut spilling. Four measurements tested that, each disproving the previous
one's prediction. **None of them landed a performance win, and together
they are the most valuable result in this report**, because they moved the
target from "pack the rows" to a specific, previously unnamed cost-model
defect.

| # | change | prediction | measured |
|---|---|---|---|
| 1 | pack the Q9 build side (D-04) | batches down | **batches 4→4**, time +6.8%, allocs +39% |
| 2 | fix `avgVarBytes` (executor side) | batches 4→2 | **batches 4→4**; entry 194→120 correct but inert |
| 3 | honest `MapSlotBytes` (48 is 2× low) | less memory | **bucket heap −51%**, and **+10.4%** time: Q14 flips to a nested loop |
| 4 | narrow the COST side too | fixes #3's flip | **it does** — and costs **+10.3%** on its own |
| 5 | charge what a build actually costs (5× private copies) | fixes #4's build-side flips | **+22.3%**: Q5/Q9/Q10 lose *parallelism*, not a build side |

**The finding, revised by measurement 5.** Measurement 4 makes the cost model say exactly what the
executor does, keeps values at 24 MATCH, passes TPC-DS clean, and moves
plan parity *toward* PG. It still loses 10%, because Q5, Q7, Q9 and Q10 all
flip **which side is built**. `hashJoinCost` under-prices *building* a
large hash table — it charges only `cpu_tuple_cost × rows` plus the child
cost, modelling neither the five private per-worker copies nor the table
memory. **The un-narrowed build width was an accidental deterrent doing
that job.**

Measurement 5 charged that build honestly — a 5× participant multiplier on
a spilling build, which is the executor's own sharing-decline rule, confirmed
on a live witness where all five participants scan `orders` privately. It
costs 22.3%, and **Q5, Q9 and Q10 did not lose a build-side choice. They
lost parallelism.** A Merge Join on the driving path makes the whole plan
serial, because goopg's `drivingScan` admits only a hash join under a
Gather, and `MaybeAddGather` runs *after* the search.

**goopg's cost model has no parallel dimension.** PG's is parallel-aware end
to end — partial paths, `get_parallel_divisor`, `create_gather_path`,
`parallel_hash` in the sizing. Three individually-correct cost corrections
have now each failed on exactly this mechanism: every one transferred work
away from a hash join, and a hash join is the only join a Gather can sit on.
Until the search can prefer a plan *because* it will parallelise — which is
Phase 5, items C-19a through C-19h — any term that makes a hash join dearer
trades a real 5× speedup for a modelled saving.

**So Track D's D-05 is blocked on Phase 5, not on its own cost terms**, and
the plan now says so. All three patches are preserved with a guard test.
Two cheaper facts survive: the bucket-array cost term decides nothing on
TPC-H and is dropped from the prerequisite list, and the narrowing patch
narrows the inner only where PG narrows both sides of `page_size`.

Two things measurement 3 established that outlive it: `MapSlotBytes = 48`
was a hand-derived guess and is 2× low (go1.25's swisstable costs 96.1 B
per `map[string][]Row` slot), and `Memory Usage:` omits the bucket array,
which is why this whole line of work was flying blind — it was reporting
the smaller of the join's two memory terms.

## 5.7. Following the evidence: the entry-width fix, and its refutation

D-04's stopping rule said "fix the model first", and named `avgVarBytes` as
prerequisite #1. That fix was implemented and measured.

**The defect was real.** The entry model was half priced on the narrowed
row and half on the full one: `ncols` came from the build child's schema,
already cut by the narrowing work, while `avgVarBytes` was summed over
every column of the *table*. On Q9's `orders` build those 74 bytes are
`o_comment` + `o_clerk` + `o_orderpriority` + `o_orderstatus`, all columns
the build drops. Model 194 B/row against the executor's own accounting of
120.2. Fixed; the model now reads 120.0.

**And it bought nothing.** D-04 predicted the correction alone would halve
the batch count. It does not: `nbatch` is **non-monotone** in entry size,
because a smaller entry buys more buckets and the bucket array is charged
too. Two batches need ≤111.8 B/row, and two retained Datums plus their
slice header are already 120 — so **D-04's own "ideal packed ~63 B/row"
lands back on 4 batches**, the bucket array having taken back more than the
rows gave up.

The lever on this witness is therefore the bucket array
(`MapSlotBytes = 48`), not the row format. That is the finding: two
successive measurements, each disproving the previous one's prediction,
have moved the target from "pack the rows" to "charge the table".

The fix was kept rather than reverted because it is correct, costs nothing
(timing-neutral, values 24 MATCH, plans byte-identical, TPC-DS
PASS=95 CKMISMATCH=0), and errs high. A larger divergence it exposed is
ledgered rather than bundled: the planner's **cost** side still prices the
un-narrowed build, at 530 B/row and 8 batches where the executor runs 4.

## 5.8. The second real win: removing four redundant hash builds

The 27% of §2.5 came from a cost constant. The second measured win came
from the executor, and it is worth recording because it was found by
reading an `EXPLAIN ANALYZE` line that had been visible all along.

goopg partitions a parallel scan by block, so five participants share one
probe side. Until E-09a it did **not** share the build side whenever the
build spilled: `captureSharedBuild` published only a NON-spilling hash
table, and declined the moment the build overflowed `work_mem` into
batches. Every worker then rebuilt the entire inner side privately. On
TPC-H Q9 that is five independent builds of the same 1.5M-row `orders`
scan, and it was legible in every plan capture:

```
HEAD    Seq Scan on orders ... Worker 0..4: rows=1500000.00 loops=1
        Build Time: 4307.315 ms          Execution 8.85 s
E-09a   Worker 0..4:            rows=0.00 loops=0
        Batches: 4 ...          Build Time: 2978.957 ms   Execution 7.85 s
```

`rows=1500000.00 loops=1` per worker on a side no worker should be
scanning is the whole diagnosis. The fix (Variant A) publishes an
IMMUTABLE batch descriptor — `nbatch`, `bucketBits`, `nbuckets`,
`buildIsLeft`, read-only inner files — with growth frozen after prebuild,
which is PG's own rule; each participant reloads a batch through its own
reader into its own map. It introduces **no new synchronisation**, which
is why it was separable from the harder half.

Two honest qualifications. First, −11% on one query is close enough to
this suite's ±17% per-query noise band that it is the WITNESS, not the
timing, that carries the claim: the worker rows going 1.5M → 0 and the
build count going 5 → 1 are not noise. Second, the memory multiplier is
NOT fixed — each participant still materialises its own copy of a
reloaded batch, so D-04's 506 MB live map is still five maps. That is
E-09b (`sync.Once` per batch + refcount + a cancellation-aware wait), and
it is deliberately a separate item because it introduces the executor's
first cross-worker wait, whose failure mode under a LIMIT above a Gather
is a deadlock or a silently partial join.

## 5.9. The parallel dimension exists now — and arithmetic says it cannot pay yet

§5.6 established that goopg's cost model had no parallel dimension:
`MaybeAddGather` was a post-planning size rule, `PartialPathlist` had no
reader, and only a hash join could carry a Gather. Three correct cost
fixes each regressed the suite 10–22% by moving work off a hash join and
losing a 5-worker Gather.

C-19a/b built partial paths, C-19c added partial index scans, and C-19d
landed `generateUsefulGatherPaths` — `PartialPathlist`'s first reader —
with `cost_gather`/`cost_gather_merge` and `createPlanNode` arms. The
machinery is complete and it ships **default-OFF**, for a reason that is
arithmetic rather than caution:

| term | per row |
|---|---|
| charge to cross a Gather (`parallel_tuple_cost`) | **0.1** |
| saving from 4 workers (`cpu_tuple_cost`'s share) | **≈ 0.0075** |

With only BASE-REL partial paths, the whole relation crosses the
boundary, so the charge exceeds the entire scan cost and `add_path`
correctly dominates every base-rel Gather **at any relation size**. This
is PG's own arithmetic. PG escapes it not by pricing differently but by
having far fewer rows cross: in PG the join and the aggregation happen
BELOW the Gather, so what crosses is the join's output, not its input.

So the blocker named in §5.6 has moved and narrowed. It is no longer
"goopg has no parallel costing" — it is **C-19f**, the partial join path,
which is what puts a join below the boundary. D-05's re-measurement needs
C-19d *and* C-19f; a corrected hash cost with no parallel alternative to
move onto still has nowhere to go.

Two by-products worth recording because both are latent wrong-answer
classes rather than cost issues:

- `MaybeAddGather` now stands down on an already-gathered tree. That is a
  correctness stop, not tidiness: `findPartialSubtree` descends through
  terminating single-child nodes, so the post-pass would have nested a
  second Gather below the costed one — N workers each launching N.
- `gatherMergeOp` attaches only `attachParallelScan` and never the
  index/bitmap claim set, so a Gather Merge over a partial INDEX path
  would return N copies of every row. The producer therefore admits
  seq-scan-driven subpaths only, which means no GatherMerge path is
  produced in production today.

## 5.10. E-09b: the memory multiplier E-09a left behind

§5.8 closed with the qualification that E-09a shared the BUILD but not the
reloaded batch, so D-04's 506 MB live map was still five maps. E-09b
closes that, and it is the first cross-worker wait in goopg's executor.

Measured on a 4-participant, 7-batch fixture that holds every participant
at the same batch (probe keys arrive in runs, so "all four reach every
batch" is a property of the data, not the scheduler):

| | Variant A (E-09a) | Variant B (E-09b) |
|---|---|---|
| batch loads | 28 | **7** |
| max live loads | 4 | **1** |
| max live bytes | ~563,100 | **140,775** |

Mutation-checked: stubbing the release path to a no-op takes max live
loads to 7 and the gate fails.

The cancellation protocol is worth one paragraph because the obvious
implementation is wrong. `sync.Once` marks its slot done when the function
RETURNS, so a loader that returned early — cancelled, or having recovered
a panic — would publish an EMPTY map and every waiter would silently probe
nothing. That is precisely this item's wrong-answer class. Instead each
batch carries an explicit `done` channel closed by `defer` on every exit
including panic, with a pre-set error a successful load overwrites; the
loader itself never waits and never observes cancellation, so `done`
closes in finite time and depends on the filesystem rather than on a peer
— no cycle, hence no deadlock. Waiters select on `done` and the
participant's context, returning 57014.

## 5.11. A whole class of items has no witness in either corpus

C-13a (bounded top-N sort) was stopped by its own go/no-go census before
a line of it was written, and the census result generalises well past that
item.

`EXPLAIN ANALYZE` over all 99 TPC-DS SF0.5 queries, reading each Sort's
input off its CHILD (a Sort under a Limit stops at 100 rows, so its own
`actual rows` says nothing):

| | goopg | PG 18.3 |
|---|---:|---:|
| `Sort` that is a `Limit`'s direct child | **77** | 54 |
| of those, input ≥ 100 000 rows | 3 | 0 |
| **sorts that spilled** | **0 of 100** | — |
| **total time in all 77 bindable sorts** | **≤ 119.8 ms of 802 s = 0.015%** | — |

The structural hypothesis was confirmed — goopg really does stack a
bindable full Sort where PG avoids one, 77 against 54 — and it buys
nothing, because 54 of the 77 sort an aggregate or window output whose
rows have already collapsed. Median input is **145 rows**. The design's
strongest argument, that a bound removes a spill outright, has **no
witness at all**: nothing spilled, and the largest footprint was 26 MB
against the 256 MiB threshold.

Combined with §7's finding that the TPC-H corpus contains no `LIMIT`
whatsoever, the conclusion is corpus-level rather than item-level:
**no sort-side item has a runtime witness here.** All sorting is under
0.2% of corpus wall time. C-14 (Incremental Sort) is not the better
alternative either — goopg's Q67, which is PG's own incremental-sort
case, runs 1.0 ms over 115,150 rows.

This is why the item was deferred rather than cancelled: the mechanism is
cheap and correct, the *evidence* is missing. It should be reconsidered on
a top-N-over-raw-rows corpus (`ORDER BY <ungrouped column> LIMIT k` over a
large scan or join), which neither suite provides — TPC-H has no LIMIT and
TPC-DS's LIMITs sit over grouped output.

### 5.11.1 And the census found something larger than the item

Est-vs-actual on the same 100 Sort nodes exposes two unrelated
row-estimation defects, both 3–5 orders of magnitude, neither previously
filed (ledger `take3-tpcds-rowest-3-to-5-orders`):

| | example | est | actual |
|---|---|---:|---:|
| collapse to 1 | Q78 Hash Left Join | 1 | 245,587 |
| | Q47 CTE Scan | 1 | 43,626 |
| | Q28 Seq Scan on `store_sales` (×6) | 1 | ~15,400–18,400 |
| over-estimate | Q99 HashAggregate | 720,657 | 90 |
| | Q62 HashAggregate | 359,432 | 150 |
| | Q22 HashAggregate (5 grouping sets) | 9,460,201 | 11,987 |

22 of 100 Sort inputs are estimated at exactly 1 against 100+ actual rows.
The Q28 cases matter most because they are plain base-relation scans — a
Seq Scan estimated at one row is a selectivity collapse, not a grouping
artefact.

**Diagnosed since (`6b987aeab`, `docs/design/planner-rowest-collapse/`),
and two things stated above turned out to be wrong.** Four mechanisms,
each confirmed by a probe binary A/B'd on the real dataset rather than
inferred:

- **A1, the null double-exclusion** — `conjunctionSelectivity` computes
  `s2 = hibound + lobound - 1.0` and omits PG's next line,
  `s2 += nulltestsel(IS_NULL)` (`clausesel.c:292-294`). Both bounds
  already exclude NULLs, so the subtraction double-excludes them. On
  `ss_quantity`: 0.955955 + 0.040251 − 1 = **−0.003794**, which falls into
  the small-negative guard and becomes 1e-10, and 1,439,608 × 1e-10 clamps
  to **1**. TPC-DS fact tables are ~4.4% null nearly everywhere, so *every
  predicate narrower than the null fraction is destroyed*. Probe fix:
  1 → 14,932 against 15,410 actual. This is Q28.
- **B1, the aggregate over-estimate** — `resolveBaseColumn` has **no
  `*NestedLoopIndexJoin` arm**, so every column above an NLI resolves to
  nothing, every grouping var prices at `DEFAULT_NUM_DISTINCT = 200`, and
  the product saturates until `estimateNumGroups` returns its *input* row
  count. Isolated to the LIMIT: Q99 estimates **90** without
  `ORDER BY … LIMIT` and **720,657** with it, because the limit flips the
  shape to NLI+Memoize. Q22's 9,460,201 decomposes exactly as
  `1 + 200 + 200² + 2×4,710,000`. Probe: Q99 → 90 exact, Q62 → 150 exact,
  Q22 → 72,001 against PG's 71,857. Seventeen lines.
- **A2** (latent) — `rangeOpSelectivity` returns `DEFAULT_INEQ_SEL` when
  the histogram has fewer than two entries, never reaching the MCV loop
  ten lines below; PG's `scalarineqsel` reads MCVs *first*.
- **A3** — `LEFT JOIN … WHERE d.id IS NULL` is not converted to an anti
  join, and prices from `stanullfrac = 0`; the identical `NOT EXISTS`
  becomes a Hash Anti Join and estimates correctly. This is Q78.

**Correction 1:** my filed guess that B1 lived in the per-set
independent-ndistinct product "where PG leans on extended statistics" is
**refuted**. `estimateNumGroups` is PG-faithful and exact once given real
ndistinct; extended statistics are not involved.

**Correction 2:** Q47 and Q57 are **not defects**. PG 18.3 emits `rows=1`
on the identical node — seven mutually-implied equi-conditions multiplied
independently and floored by `clamp_row_est`, and upstream has no
equivalence-class de-duplication at estimate time either. Q81 and Q89 are
the same class. They come off the list.

This belongs in a performance report because of what it implies about
everything else in it: **a cost model cannot rank plans on estimates that
are wrong by five orders of magnitude.** C-13b's `limit_tuples` branch
conditions compare against these numbers, C-19f's parallel crossover is a
cost comparison, and the spill-cost work prices bytes as rows × width. And
no gate in this repository catches it — the values gate compares results,
plan parity compares shapes, and neither one looks at `rows=`.

## 5.12. E-11: a five-way A/A, and the defect it exposed

E-11 asked whether goopg should wire an AIO `ReadStream` with a depth
policy. Five depths x three reps on one binary, fresh capped server per
arm, values byte-identical across all fifteen arms x 24 queries:

| depth | median suite total | vs control |
|--:|--:|--:|
| 0 | 138.35 s | +1.32% |
| **4 (shipped default)** | **136.55 s** | — |
| 16 | 141.17 s | +3.38% |
| 64 | 142.37 s | +4.26% |
| 128 | 134.14 s | −1.76% |

The five medians span 6.1% **with no ordering in depth** — the deepest arm
is the fastest, and d64 the slowest. The observed control-vs-control band
was 40.2% worst / 12.0% median per query, and the depth-4 totals alone
span 14.2%. Nothing wins, and nothing could: this is a five-way A/A.

**Why it is an A/A is the finding.** `refillPrefetchWindow` returns early
for a parallel scan by design, and **every TPC-H plan at bench settings is
parallel** — captured live, Q6 renders `Gather` / `Workers Planned: 4` /
`Parallel Seq Scan`. So the prefetch window is not exercised by the suite
at all. Any future measurement of a serial-scan-path change against the
TPC-H suite at default settings will measure nothing, in exactly this way.

Forced serial (`-parallel-workers 0`, 7 scan-heavy queries), where the
window is live, depth 0 is **−12.1%** on the subset and **−35.0%** on Q6,
with the s0 and s4 repetition ranges disjoint. More prefetch measures
*worse* where the mechanism actually runs.

### 5.12.1 `Pool.Prefetch` allocates a buffer and throws it away

The allocation arm names the cause. Over 8 serial Q6 executions,
`Pool.Prefetch` is **63.8% of allocation objects and 90.1% of allocation
bytes**; depth 4 costs 14.96M objects / 9.92 GB against depth 0's 5.29M /
1.00 GB — 2.85x the object count, 9.9x the bytes, 50.1 s against 32.9 s
wall, all far outside the 8.6%-wall / 1.9%-object control band.

`Pool.Prefetch` (`internal/storage/bufpool.go:1346`) is nine lines:

```go
buf := make([]byte, BlockSize)
_, _ = p.mgr.PrefetchBlock(tag.Rel, tag.Block, buf)
```

The buffer is never installed into the pool. I verified the surrounding
facts rather than taking the report on trust: `io_method`'s BootVal is
`worker`, so an AIO engine *is* attached in production and the read is
genuinely issued — this is not a synchronous double-read. But since the
result is discarded, the only effect that survives is warming the OS page
cache, and the following `Pin` still performs a full read. PG's equivalent
installs the buffer it will return (`StartReadBuffers`/`WaitReadBuffers`,
`postgres/src/backend/storage/buffer/bufmgr.c`, driven from
`aio/read_stream.c`), so no read is wasted.

**The default was deliberately not flipped to 0**, and that restraint is
the right call: all the evidence is warm-cache, where page-cache warming
is worth nothing and only the allocation cost remains. Zeroing the
constant would bank a warm-cache-only win and hide the actual bug. Ledger
rows `take3-E-11-readstream-declined` and
`take3-E-11-prefetch-discards-buffer`.

## 5.13. C-19f: a Gather becomes choosable, and the first plan it wins is one the post-pass could never reach

§5.9 left the parallel dimension complete but unusable: crossing a Gather
costs 0.1/row against a 4-worker saving of ~0.0075/row, so with only
base-rel partial paths the whole relation crosses and `add_path` correctly
dominates every Gather **at any relation size**. C-19f files a partial
hash join into the joinrel's `PartialPathlist`, which is what puts a join
below the boundary.

The crossover, pinned through the named constants in both directions:

```
(1 − 1/d)·(cpu_tuple_cost + cpu_operator_cost·k)·N
    > parallel_setup_cost + (parallel_tuple_cost − (1 − 1/d)·cpu_tuple_cost)·J
```

— i.e. **N > 106,667 + 9.87·J** at 4 workers. A base rel could not satisfy
this at any size and a single FK join still cannot (J ≈ N), but a join
*tree* can, because partial paths now propagate upward.

TPC-H SF=1, `off` vs `top`, 3 repetitions, values **7/7 MATCH in all 42
runs**:

| | off | top | Δ |
|---|---:|---:|---:|
| **Q21** | 17.33 s | 8.42 s | **−51%** |
| Q9 | 7.26 s | 14.06 s | **+94%** |
| Q10 | 2.75 s | 3.57 s | +30% |
| suite | 46.06 s | 41.14 s | −10.7%, inside its own spread |

**Q21 is the case only a path model can reach.** At HEAD it gets no Gather
at all: its root is a Nested Loop Anti Join, and `terminatesPartial` stops
`findPartialSubtree` before the post-pass can look inside. C-19f gathers
the hash join *within* it. That is the structural argument for the whole
Phase-5 track, demonstrated rather than asserted.

**Q9 is not the foreclosure the design predicted.** The parallel scan
moves from `lineitem` (6M) to `orders` (1.5M) and — the part that matters —
*which relation is BUILT* flips, so a 6M-row hash table is now built
undivided at startup while the model called the plan 21% cheaper. The lead
is the **build** term, not the Gather term: `spillPages` over-states, by
its own documented caveat, and the correction for that is the
spill-cost item's Cut 3. Q10's +30% comes with a **byte-identical executed
shape** and is recorded open rather than attributed.

**The default does not move**, and no new flag was added — C-19f rides
`GOOPG_GATHER_PATHS`, whose default is off, so the slice is provably inert
at the default. TPC-DS SF0.5 at the default: PASS=95, MISMATCH=0,
CKMISMATCH=0, and PLAN-SHAPE **99/99 identical** — inertness measured
rather than claimed.

**On the cost model that was supposed to describe the executor**: the
build is charged **once, undivided**, which after E-09a/E-09b is what the
executor actually does and what `initial_cost_hashjoin` charges. The
reverted `d05p4` 5× participant multiplier is now refuted by a test that
fails if anyone reintroduces it. goopg has neither of PG's two parallel
hash joins — its executor pre-builds once in the leader and shares by
pointer, which is upstream's `parallel_hash=false` variant with the N-fold
replication removed; `parallel_hash=true` is refused for want of an
executor.

### 5.13.1 The consumer test found two bugs that could not previously exist

The item required an *executor* consumer check, not a planner-only test —
a fixture where the path wins must actually execute as a parallel hash.
Writing it surfaced two latent defects in C-19d's own `createPlan` arms,
both unreachable until a Gather could win at a search root:

- `createGatherPlan` built `&Gather{…}` as a struct literal instead of
  `NewGather`, so the schema was never set and `createPlanAtSearchRootRange`
  panicked "layout is 5 columns but its output is 0";
- `*Gather`/`*GatherMerge` did not embed `searchedTree`, so
  `markSearchedTree` panicked — the tag whose absence lets the legacy
  posmap family permute a searched subtree twice.

This is the third time in this workstream that **an unwinnable path turned
out to be an untested path**. Budget for it: when a cost fix first makes a
shape reachable, expect execution bugs in it, and force the shape in a test
rather than trusting the planner to arrive there.

## 5.14. The estimate fixes, and what a correct estimate is worth

§5.11.1 diagnosed two row-estimation defects. Both are now fixed, and
because an estimate change is a cost change, both needed a full arm.

- **B1** — `resolveBaseColumn` gained its missing `*NestedLoopIndexJoin`
  arm. Every column read above an NLI had resolved to nothing, so every
  grouping variable priced at `DEFAULT_NUM_DISTINCT = 200` until the
  independence product saturated and `estimateNumGroups` returned its own
  *input* row count.
- **A1** — `conjunctionSelectivity` now adds PG's
  `s2 += nulltestsel(IS_NULL)` (`clausesel.c:292-294`). Without it both
  range bounds excluded NULLs and the subtraction excluded them twice.

Measured against the pre-change tree, fresh capped server per arm, pinned
statistics:

| | result |
|---|---|
| TPC-H values (`-diff`, by value) | **24/24 MATCH** |
| TPC-H plan **shapes** | unchanged — 0 nodes added, 0 removed |
| plan lines changed | 6, all of them `cost=`/`rows=` |
| `make plan-gate` | exit 0 (Q9 MATCH) |
| plan parity vs PG | 6/14, unchanged |
| TPC-DS SF0.5 | PASS=95 MISMATCH=0 CKMISMATCH=0 |
| suite timing | 106.51 s → 105.02 s, **−1.4%** |
| Q2 | 1.67 s → 0.75 s, **−55%** |

The plan diff is the interesting artifact, because it is exactly what a
correct estimate fix should look like — no shape moved, and the estimates
that moved moved toward the truth:

```
Q4  HashAggregate (cost=8672.02..8674.02 rows=200 …)
 →  HashAggregate (cost=8672.02..8672.07 rows=5   …)
```

`rows=200` is `DEFAULT_NUM_DISTINCT` verbatim. That line is B1's signature
in the plan, and it had been sitting in the committed TPC-H plans the whole
time.

Two honest caveats. The −1.4% suite figure is inside this suite's own
run-to-run spread and is not a claim; **Q2's −55% is outside it and is**.
And the fixes landed ungated because both agents were terminated by a
session limit mid-gate — the arm above is the gate, run afterwards, and it
is why this section exists rather than a commit message.

## 5.15. The benchmark was measuring the wrong thing on TPC-DS

Found by a direct question about buffer residency, and it is the kind of
defect that invalidates measurements rather than code.

goopg turns `shared_buffers` into pool slots as `shared_buffers / 8`. Both
TPC-DS `postgresql.conf` files **left `shared_buffers` commented out**, so
both clusters ran on the `128MB` GUC BootVal — 16,384 slots — while TPC-H
had been explicitly tuned to `2048MB` (262,144 slots) since its setup
script was written, and the PG TPC-DS reference runs `2GB`.

Measured with `pg_stat_io`, not assumed:

| cluster | pool | working set | evidence |
|---|---:|---:|---|
| TPC-H goopg | 2048 MiB | 1.9 GiB | 3 scans of 6M-row `lineitem`: reads **136,393**, evictions **0**, reads unchanged from scan 2 onward |
| TPC-DS SF0.5 goopg | 128 MiB | 1.113 GiB (75 rels) | 2 scans of `store_sales`: reads **59,522**, evictions **43,138** |

`store_sales` alone is 232 MiB — **1.8× the entire pool** — so nothing was
ever resident and every scan re-read from the OS. Against PG's 2 GiB
reference this also made any goopg-vs-PG TPC-DS timing a **16× unfair
comparison on memory**.

The two consequences are different and must not be conflated:

- **No values gate was affected.** `tpcds-sf05-regression.sh` compares row
  values against a git-tracked PG oracle; residency cannot change an
  answer. Every `PASS=95 CKMISMATCH=0` in this report stands.
- **TPC-DS timing taken before 2026-09-06 is I/O-bound.** The only such
  numbers published here are the C-13a census wall times, and its
  conclusion *strengthens*: the 802 s denominator was inflated by I/O the
  fix removes while the sort times are CPU work, and the no-go rested on
  median sort input 145 rows, largest sort 1.9 ms, and zero spills — none
  of which residency touches.

Fixed to `2048MB` on both clusters, with a start-time warning in
`bench/tpcds/server.sh` so it cannot silently regress. The general lesson
is the one §5.12 already taught in a different costume: **before believing
a benchmark result, confirm the configuration under which it was taken.**
That is twice in one day — once where the code never ran (every TPC-H plan
being parallel made a prefetch sweep a five-way A/A), and once where the
memory was 16× off.

### 5.15.1 And the same audit on TPC-H found the asymmetry pointing the other way

Asked the obvious follow-up — what is the PG side set to — the answer was
`shared_buffers = 512MB` against a 2.1 GiB dataset, while goopg ran
`2048MB` against 1.9 GiB. So on TPC-H **goopg had 4× the buffer memory**,
the mirror image of the TPC-DS defect and in goopg's favour.

Both are now aligned, and the alignment was verified live rather than by
reading the config files — both engines report `shared_buffers = 262144`
8 kB slots:

| knob | goopg 65433 | PG 65432 | before |
|---|---|---|---|
| `shared_buffers` | 2048MB | **2048MB** | PG was 512MB |
| `autovacuum` | **on** | on | goopg was off |
| `work_mem` | 64MB | 64MB | already matched |
| `effective_cache_size` | 2GB | 2GB | already matched |

`effective_cache_size` matching already mattered more than the rest, since
that is the knob PG's index-cost model reads — **plan-parity and cost
comparisons were on fair footing all along**; the asymmetry was purely in
execution.

**The autovacuum alignment costs something real, and it is recorded rather
than glossed.** `autovacuum = off` was one of the three fixes in §2 that
took A/A plan-capture noise from 455 estimate lines and 27 shape lines to
zero — the thing that made `make plan-gate MODE=costs` reachable. Turning
it on re-exposes that drift, and the two needs are kept separate instead:
a goopg-vs-PG *timing* arm runs autovacuum on so both engines share one
maintenance policy; a goopg-internal *plan-pin* arm pins statistics with
`GOOPG_ANALYZE_SEED` plus an explicit in-session `ANALYZE <table>`. One
thing improves — autovacuum sets visibility-map bits again, so index-only
paths stop being priced pessimistically.

**What none of this invalidates:** every timing figure in this report is
goopg-vs-goopg on one cluster, so the buffer asymmetry never entered any
of them. The only cross-engine numbers published here are plan parity
(structural) and row estimates (`Q22 → 72,001 against PG's 71,857`),
neither of which depends on buffer residency. What the setup permitted was
a goopg-vs-PG wall-clock comparison that would have been silently unfair —
and now does not.

## 5.16. C-04a: TPC-H passed, and the second gate caught a wrong answer

This section exists because it is the cleanest demonstration in the whole
workstream of why the gate protocol has two suites, and because the first
measurement of it was wrong in a way worth recording.

C-04a admits `LEFT JOIN` links into the join search — the item that turns
C-03's inert jointype machinery on. On TPC-H it looked like a clean win:

| | pre-C-04a | C-04a |
|---|---:|---:|
| values (`-diff`) | — | **24/24 MATCH** |
| plans moved | — | 1 (Q13) |
| Q13 | 5.87 s | **5.05 s (−14%)** |
| suite | 106.85 s | 109.27 s (+2.3%, inside spread) |

The one plan that moved, moved the right way: Q13's `Hash Left Join`
became a `Merge Left Join`, the row estimate went from 2,358,304 to
1,499,850 — the true `orders` cardinality — and the width narrowed
1072 → 96. The LEFT join was *in* the search, which is the item's entire
purpose.

**Then the TPC-DS SF0.5 gate returned `MISMATCH=1 TIMEOUT=1`** against a
prior `PASS=95`:

- **Q72 returned 84 rows; the oracle says 100.** Both `Nested Loop Left
  Join`s in the plan had become plain `Nested Loop`s — the admitted LEFT
  links lost their jointype, the null-extended rows were dropped, and the
  ON-condition surfaced as a post-join `Filter` on an inner join. Q72 is
  the design's own named witness for this item.
- **Q78 went from 15 s to a >328 s timeout.** Its LEFT joins survived,
  but `d_year = 1998` was dropped from the `date_dim` scan (149 rows →
  73,049, unfiltered), so three hash joins consumed the dimension 490×
  larger. The per-qual delay had misclassified a non-nullable-side
  restriction as one that must rise above the outer join.

Neither shape exists in TPC-H. A values gate on one suite is a statement
about that suite's shapes, and no more.

**The measurement that was wrong.** My first timing of C-04a showed
**+13.1%** with Q9 +36% and Q7 +31% — taken while my own TPC-DS captures
and a PG cluster were running. On a quiet host, same session, fresh server
per arm, it was **+2.3%** with per-query moves mixed in sign (Q1 went from
−33% to +17.6% between the two runs). That swing is the tell. I had
written the host-load trap into every agent prompt this session and then
walked into it myself; the number reported to the agent was wrong and had
to be corrected.

### 5.16.1 Both fixed — and Q78 took three wrong hypotheses to reach

Q72 was the straightforward one: the admitted LEFT links lost their
jointype through the collapse-limit sub-problem split, fixed by a
per-problem SJI remap (`fb6550266`), pinned.

Q78 was not. The agent that fixed Q72 had already diagnosed it correctly
by trace — the search admits the outer `ss LEFT JOIN ws LEFT JOIN cs`
problem with every path at `rows=1` and takes an epsilon Nested Loop
victory (**3.07 vs Hash 3.09**) over three full CTE outputs — and had
written the right instrument, a firewall that declines an outer join over
statistics-less derived inputs to the syntactic fallback. The firewall was
wired at the right point, its unit tests passed, and **it never fired.**

I then went through three hypotheses, each plausible from the code, each
refuted by a single trace line:

| hypothesis | refuted by |
|---|---|
| the `d_year = 1998` qual was dropped from the scan | the committed fix had already restored it — the plan showed it on all three `date_dim` scans |
| a `base != 0` coordinate decline on nested spine links | the seam trace showed the outer chain was **admitted**, not declined; the edit is reverted |
| the firewall's `table == nil` leaf test | closer — `with.go` gives every CTE binding a synthesised non-nil table — but fixing it *still* changed nothing |

The real root, read off an instrumented firewall: `scan=*optimizer.Filter
table=true` for all three leaves, `derived=[false false false]`. A CTE
output with a pushed-down predicate reaches the search as
`*Filter{Child: *CTEScan}`, and a type switch on the top node sees only
the Filter. The fix classifies by node type **and descends through
wrappers**. Both halves are mutation-checked: remove the unwrap and only
the wrapped-CTE pin fails; remove the node-type switch and only the
CTE pins fail.

Q78: 327 s timeout → **19 s, checksum-verified**, and its top-level plan is
byte-identical to pre-C-04a from the outer join down.

**And the timing had to be read three times before it was trustworthy.**
A first TPC-H arm on the fixed tree read **+9.0%** — taken while the full
TPC-DS sweep ran on the same host. A quiet re-run read **+5.4%** against
a three-hour-old baseline. A same-session A/A control on the *unchanged*
pre-C-04a binary then showed the baseline itself had drifted **+6.3%**
over those three hours. Same-session A/B: **−0.8%**, every query within
±1.5% except **Q13 −17.4%**, the intended improvement. C-04a is
timing-neutral with one real win, and two of the three numbers I could
have reported were wrong.

Final gates: TPC-H 24/24 by values; TPC-DS SF0.5 `PASS=95 MISMATCH=0
CKMISMATCH=0 TIMEOUT=0`, plan shapes 99/99, total delta +0.0%; plan-gate
22/22 in both modes, re-pinned with the Q13 hunk as the only diff.

## 5.17. Phase 4: the upper planner becomes a path search

Five items landed in sequence overnight, each gated on both suites. They
are grouped here because individually none of them moves a number, and
together they are the structural half of the plan — the point at which
goopg's upper planner stops being a chain of post-passes and becomes a
path search PG would recognise.

| item | what landed | gate |
|---|---|---|
| **C-11** | upper-rel registry (`fetch_upper_rel`), **inert** | TPC-H 24/24; plan-gate 22/22 both modes |
| **C-12** | ORDERED upper rel gets a real `PathSort` | 24/24; costs move on Sort lines only; TPC-DS PASS=95 |
| **C-13b** | `cost_tuplesort`'s `limit_tuples` middle branch | 22/22 both modes; TPC-DS PASS=95, total −1.0% |
| **C-15** | GROUP_AGG upper rel, `cost_agg` paths, **three aggregate rules retired** | TPC-DS PASS=95, zero shape change |
| **C-16** | DISTINCT upper rel via DistinctOn reuse | 22/22 both modes, timing flat, shapes 99/99 |

The one that changes the most is C-12, and what it changes is honesty
rather than speed. Before it, `costSortRun` had exactly one production
caller — merge-join input sorts — so **every top-level Sort in the suite
was priced at zero** by `DeriveLegacyDisplayCost`. Q18's
`Sort (rows=1565307 width=204)`, the largest in the corpus and in its
slowest query, contributed nothing to any path comparison. C-12 makes it
cost something; the costs move on Sort lines and the plans do not.

C-15's retirement of three aggregate rules is the deletion half of the
same story: the rules existed because there were no grouping paths to
compare, and once `cost_agg` prices them the rules have nothing left to
decide.

**None of these is a speed result and none is presented as one.** The
suite totals across the five range from −1.0% to +2.5%, all inside the
run-to-run spread. What they buy is that Phase 5 and Phase 6 have
something to delete *into* — C-19g replaces `splitAggregate` with paths
because C-15 made grouping paths exist, and C-20a can delete the legacy
estimators only once every consumer reads the path model.

## 5.18. C-19g: partial aggregation makes the Gather pay

§5.9 established the arithmetic that kept the parallel path off: crossing
a Gather costs `parallel_tuple_cost` = 0.1/row against a 4-worker saving
of ≈0.0075/row, so with only base-rel partial paths **the whole relation
crosses** and `add_path` correctly dominates every Gather at any size.
C-19f put the join below the boundary and got Q21 −51%, but Q9 +94% left
the default off.

C-19g puts the **aggregation** below it, and that changes the arithmetic
by orders of magnitude rather than by a factor. goopg's Partial node emits
**zero rows** — group-states cross through a mutex-guarded accumulator —
so TPC-H Q1 at 4 workers crosses **16 group-states, not 5,901,255 rows**:
1.6 against 590,000 charged at `parallel_tuple_cost`, plus ~110,000 saved
on per-worker CPU. The boundary term essentially disappears.

Measured, three alternating passes per arm, medians:

| | off | on | |
|---|---:|---:|---|
| **Q1** | 8.57 s | **4.14 s** | **−51.7%** |
| suite | — | — | −4.3% |

The off-arm spread was 1.2%, so Q1's move is **50× outside the noise
floor** — the largest single-query result in this workstream after the
27% calibration, and the first one the parallel track has produced.

Two things make it more than a number:

- It **replaces five invented constants with a path tournament.**
  `splitAggregateIsProfitable` weighed `cXfer 2.0`, `cTrans 1.0`,
  `cHash 0.25`, `cMerge 4.0`, `cOut 1.0`, self-described as "calibrated
  against one query". C-19g prices two candidate paths with `costAgg` +
  `gatherCost` and lets `addPath`/`setCheapest` adjudicate — no new cost
  function and no new constant, so the verdict is
  `compare_path_costs_fuzzily`'s, fuzz band included.
- It **reaches queries the old rule could not.** Before this slice the
  only TPC-H aggregates that split were the three *ungrouped* ones,
  because an ungrouped aggregate is the single case `groupsToRowsRatio`
  can answer without statistics.

**It ships default-OFF** (`GOOPG_PARTIAL_AGG_PATHS`), and that is not
timidity: flipping it moves three TPC-H plans and so requires re-pinning
`plan_snapshots/` in the same commit — a shared artefact two peer agents
were A/B-ing against all session. The flip is fully specified and is the
next action, not an open question.

Honest limits, recorded because the result is good enough to be
over-read: `MODE=costs` is **not evaluable** on a restarted private clone
(cold-stats drift moves even the mode-OFF control 21/22), so it is not
claimed; Q5 +7.2% and Q14 +10.6% (0.47→0.52 s) sit inside their own arms'
spreads and are not counted either way; and the upper-rel-resident half of
`create_partial_grouping_paths` is genuinely unfinished, so the item is
`[~]`, not `[x]`.

## 5.19. C-18 and C-17: two items that correctly show nothing

Both landed, both are worth keeping, and **neither moved a plan on either
corpus**. Recording them properly matters more than usual, because the
easy mistake here is to read "no movement" as "no value" — or to go
hunting for a number until one appears.

**C-18** files WINDOW and SETOP upper rels, pricing two node types that
were previously free. It cannot move a plan by construction: a window
spec-group chain becomes ONE stacked candidate `add_path`ed once, which is
`create_one_window_path`'s own shape (planner.c:4620), and set-ops have a
single candidate.

**C-17** threads `tuple_fraction` to every upper rel. Its census found
four real gaps — `ctx.tupleFraction` stamped in two per-arm places, so a
WHERE-less `… ORDER BY a LIMIT 10` told every upper rel that all rows were
wanted; two producers passing a literal `0`; and a set-op statement's
trailing ORDER BY reaching the executor as a bare `&Sort{}` with **no
ORDERED rel at all** — the last top-level sort still priced at zero after
C-12, and the one shape that could never reach C-13b's bounded arm.

And it shows nothing on either suite, for a reason worth stating: **TPC-H
has zero set operations, zero window functions, and a WHERE on every
query**; TPC-DS's UNIONs all sit inside subqueries whose ORDER BY belongs
to the outer SELECT. The raw SF0.5 plan capture — costs included, not
shape-normalised — is byte-identical between the two binaries, 0 diff
lines.

So the witness is a direct probe rather than a suite number
(`lineitem UNION ALL orders ORDER BY 1`, 2,000,672 rows):

| Sort startup cost | base | C-17 |
|---|---:|---:|
| `… ORDER BY 1 LIMIT 10` | 344,423.57 | **178,266.51** |
| `… ORDER BY 1` | 344,423.57 | **433,323.57** |

Both directions are the correction: the first is C-13b's bounded heap arm
firing on this shape for the first time, the second is the external-merge
charge for a spill the bare `&Sort{}` never paid.

**Neither is credited with a performance number.** C-18 measured −0.2% and
C-17 +1.5%, both on byte-identical plans, i.e. noise — and in the C-18
pair Q9 read +70.9% on an identical plan while the *same* query read 4.16 s
and 7.16 s across two arms of the same binary. That spread is the reason
this report keeps refusing single-arm numbers.

Two by-products worth keeping. A real defect surfaced while implementing:
`fetch_upper_rel(SETOP, 0)` shares one rel across a chain, so
`A INTERSECT B EXCEPT C` had the outer node answer with the inner node's
candidate — wrong rows, caught by the executor's set-op precedence suite.
PG keys that rel by relids (prepunion.c:805). And the design's proposed
`costSortRun(cp, rows, nkeycols, …)` put the **key count** where the **row
width** belongs, which would have modelled a 2-column row and suppressed
the disk charge entirely; the agent declined that line and pinned the
distinction instead.

## 5.20. Track E closed: three drops, one delivered, all measured

The four remaining executor items were adjudicated rather than assumed.
Three are dropped and one delivered, and **the executor carries no
behaviour change from any of them** — the instruments built to decide them
were reverted and preserved as a patch.

**E-07 (Gather slab parity) — dropped.** Its own resume condition was
"measure the dispatch delta on a parallel witness shape FIRST". Measured
in-process, both arms in one binary, over the exact three-node chain a
worker subtree has: legacy **27.18 ms/iter**, slab **27.84 ms/iter** — the
slab is **2.2% SLOWER**, ranges overlapping, allocations equal. Per-row
work is 544 ns, so three saved interface calls cap the effect at ~1.8%
before a line is written. Two of its three justifications were already
void.

**E-13 (owned-row tightening) — dropped.** Its gate was "only if a later
alloc arm shows a residual". Whole-suite census: **509,824 dead Datum
cells of 228,212,960 — 24.5 MB of 10,954 MB, 0.22%** — from four small
sites, because every multi-million-row build side already carries
`bound=deformBoundFull` from P4-01's Project. Second and stronger: E-13's
mechanism *is* the prefix truncation the EX1-04 review declined, and a
prefix is a special case of Cut B's keep-set.

**E-14 Cut A — dropped; Cut B quantified.** TPC-H has **4 Semi/Anti hash
builds against 43 INNER**, and all four are already at `buildWidth = 1`
— P4-01 got there first, exactly as the design's §8b predicted. Total
Semi/Anti retained rows suite-wide: **14,747 rows = 0.7 MB against
10,954 MB, one part in 15,000.** Cut B is where the mass is and now has a
number for the first time: 99.99% of the 10,954 MB is INNER, largest
single site **6,001,255 rows × 16 cols = 4,609 MB, 288 MB per dead
column**. It stays deferred for a *structural* reason rather than the
previous circumstantial one — its geometry half is forfeited until
`hashsize.EntryBytes` prices the retained width, so landing it first buys
the memory and none of the `nbatch` win E-12 and D-05 are waiting on.

**E-09c — delivered.** It is a measurement item, and here is the
measurement (300k-row cooperative build, 3 reps):

| producers | build wall | producer blocked (each) | consumer blocked |
|---|---:|---:|---:|
| 2 | 161.9 ms | 32.9 ms (20%) | 6.0 ms |
| 4 | **139.6 ms** | 83.5 ms (60%) | 0.0 ms |
| 8 | 172.0 ms | 132 ms (77%) | 0.0 ms |

**The cooperative build is consumer-bound and saturates at ~4 producers**:
4 → 8 loses 23% of build wall for 3.2× the stall, and past 4 the consumer
never waits at all. **Skew does not move the bottleneck** — it is already
entirely at the consumer, which is the skew half's answer stated as the
negative result it is.

### 5.20.1 The finding worth more than the items

`parallelBuildLazyHashTable` sets `maxProducers = ctx.MaxParallelWorkers`
with a floor of 2 and **no ceiling**, so an 8-worker cluster spawns 8
producer goroutines and 8 arenas for a build that stops improving at 4.
Routed rather than changed — it is a default and needs its own A/B on a
quiet host plus a GUC decision. The structural fix is PG's shape:
`nodeHash.c`'s `PHJ_BUILD_HASH_INNER` has **every participant** call
`ExecParallelHashTableInsert` behind a barrier, with no single-consumer
funnel at all. Ledger `take3-E-09c-consumer-bound`.

**Measurement honesty note.** The host was held by three peer agents at
load 18–30 throughout, so every cluster wall-clock number from that window
is void as timing and no verdict rests on one. Both timing verdicts
(E-07, E-09c) were taken **in-process, both arms in the same binary and
the same run**, which is what makes them survive the contention.

## 5.21. The gate that never ran now runs — and the estimates it scores

§5.11.1 diagnosed two row-estimation defects and noted that **no gate in
this repository catches them**: the values gate compares results, plan
parity compares shapes, and neither looks at `rows=`. The gate four
TODO_ALL items *cited* — the EA ratchet — had never executed.

C-20a built one. `make ea-ratchet` runs `EXPLAIN ANALYZE` over TPC-DS
SF0.5 and scores estimated against actual, keyed by relation set so
base-rel and joinrel granularity come out of one keying — the old tool was
joinrel-only, which is why Q28's base-rel `rows=1` was not a candidate
even in principle. The bar is **PG-relative**, `qerr > max(10, PG_qerr×2)`,
which is the only bar that correctly passes Q47 (PG emits `rows=1` there
too) and fails Q99. It ratchets on finding *identities*, not a count.
First run: 99/99 queries, 844 nodes scored, 178 findings pinned.

Scored against the c13a census baseline, the estimate fixes land like
this:

| query | before | after | actual |
|---|---:|---:|---:|
| **Q99** | 720,657 (8007×) | **72** (1.25×) | 90 |
| **Q62** | 359,432 (2396×) | **120** (1.25×) | 150 |
| **Q22** | 9,460,201 (789×) | **71,857** (6.0×) | 11,987 |
| Q12 | 107,310 | 5,066 | 932 |
| Q28 | 1 (15,410× low) | — | 15,410 |
| Q78 | 1 | 1 (unchanged) | 245,587 |

Q78 is unchanged and expected: the A3 mechanism is firewalled, not fixed
(§5.16.1), and its 8 findings are the firewall showing through.

Two parser defects the ratchet exposed on itself are worth recording,
because either would have made the gate lie in the confident direction:
**`loops=0` is not zero rows** — goopg annotates hash-join build sides
that way, and a *correctly* estimated 464,390-row scan headed the findings
table at a fictitious q-error of 464,390 — and goopg prints the **alias**
where PG prints the relation, leaving 662 of 1131 nodes unmatched until
`_<digits>` normalisation brought it to 369.

### 5.21.1 EXPLAIN reports a different estimator than the planner chooses with

C-20a's consumer census turned up something that changes what every
estimate figure in this report *means*.

`PlanCost.PlanRows` already carries the **winning path's** row count on
every search-produced node, through one funnel (`stampPlanCost`). **And
EXPLAIN never reads it.** `explainCostFields` takes Startup, Total and
Width off that carrier and then computes `rows=` as
`optimizer.EstimateRows(rowSrc)` at all four sites.

So on a searched plan the planner **chooses** with `calcJoinrelSize` and
EXPLAIN **reports** `estimateJoin` — two independent estimators. And
everything that reads a plan reads the reported one: plan-gate
`MODE=semantic-cost`, `estimate-audit`, the c13a census figures above, and
the new ratchet.

They are not known to disagree, and `cardinality_two_estimators_test.go`
now pins agreement on the control and composite-superkey shapes through
entirely separate code. But nothing had ever checked. The practical
consequence for reading this report: **an estimate-accuracy number here
describes the reporting estimator**, and a plan-choice consequence has to
be traced to `calcJoinrelSize` separately rather than assumed to follow
from it.

### 5.21.2 C-20a itself deleted nothing, and that is the result

The item was "delete the legacy `estimateJoin`/`EstimateRows` plus the
`joinkeyproof.go` mirror; everything reads `calcJoinrelSize`". The census
says it is not executable as written:

- `EstimateRows` has **28 live call sites across 15 files**, three of them
  in `internal/executor/` (EXPLAIN, hash-table geometry, correlated-
  subquery cache budget) where no `RelOptInfo` exists or ever will.
  `calcJoinrelSize` is a `searchCtx` method over `*RelOptInfo`;
  `EstimateRows` walks the plan `Node` tree. That is a coordinate-space
  problem, not a call-site migration.
- **`joinkeyproof.go` is not a mirror** and is struck from the item. Only
  `superkeyJoinEstimate` belongs to `estimateJoin`; `resolveBaseColumn`
  serves three other callers, `uniqueKeyColumnSets` serves eight scan
  sites, and `columnsSubset` is a live dependency of C-05's own
  `joinrelsize.go`.

All three deletion targets measured as load-bearing, so nothing was
deleted — which is the repo's deletion discipline working as intended
(the same rule that correctly kept `GOOPG_INDEXKEY_HARVEST` when its flip
failed its own byte-identical-plans gate).

## 5.22. Three more adjudicated, and a pattern across all of them

**C-14 (Incremental Sort) — dropped, 1.26 ms.** Its stated blocker was
already gone (E-15 published the presorted-prefix contract precisely to
break the C-14/E-03 wait), so this was build-or-drop. Re-measured on a
private clone: **Q67 — PG's own canonical Incremental Sort case — sorts
in 1.259 / 1.312 / 1.247 ms over 376,552 rows**, `quicksort Memory:
553kB`, no spill, against 13.5–15.2 s Execution Time. That is **0.008% of
the query**.

The number is *stronger* than the census's earlier 1.0 ms over 115,150
rows: 3.3× the input rows and still about a millisecond, because
`sortOp.keyvals` already precomputes the keys. Both of PG's mechanisms
lack a witness — early-rows-under-a-bound (TPC-H has zero `LIMIT`,
re-verified independently across all 25 committed query files) and spill
avoidance (0 of 100 sorts spill).

**E-03 resolved by the same verdict**, not left dangling: its condition is
"file ONLY if C-14 activates", and it will not be met. E-15's contract
stays landed and inert, so reopening C-14 reopens E-03 with its
prerequisite already paid.

**C-10b (`remove_useless_joins`) — dropped. One removable join in 121
queries, and removing it buys nothing.** Census against `analyzejoins.c`:
TPC-H has **1** LEFT join in total (Q13), failing both preconditions.
TPC-DS has 21; **all 21 pass uniqueness, exactly one passes
`attr_needed`** — q72's `left outer join catalog_returns`, corroborated by
its absence from the committed PG plan. Hand-A/B'd that one join against
the exact rewrite the optimisation performs: **arm B was 0.2–0.6%
slower**, values byte-identical (md5 equal both arms), which also
empirically confirms the census's uniqueness proof.

**E-09 — closed.** Bookkeeping: the parent states no requirement outside
E-09a/b/c, and the 5× private-build multiplier that motivated it is
exactly what E-09a removed.

### 5.22.1 The pattern: real PG optimisations with no witness here

C-14 is the **third sort-side item refuted by the same underlying fact**,
after C-13a and the parallel-sort design's stage 3. Every one of them
measured against a sort that the `keyvals` work (M0134-0191) had already
made cheap. When a subsystem has been optimised once, the items queued
behind it inherit its result — and the plan did not know that.

C-10b and C-09 land in the same family from a different direction, and the
agent checked rather than assumed the connection: **C-09's decline does
not transfer.** Both of C-09's grounds fail for C-10b — join *removal*
needs no reorder and cannot move a row estimate. C-10b falls on an
entirely independent ground, corpus incidence. What the two share is only
the shape of their evidence: a real PG optimisation with no witness in
goopg's benchmarks.

Two corrections came out of this work, and both were mine to make:

- **I stated a premise wrongly in the task.** PG 18 has **no** unique-inner
  INNER-join removal; `innerrel_is_unique` is used only to set
  `extra.inner_unique`, an execution shortcut. The only two removal
  mechanisms are LEFT-join removal and self-join removal — which shrinks
  the scope of any future port. Corrected in the ledger.
- **Uniqueness is never the binding precondition.** 21 of 21 candidates
  prove unique; 20 of 21 die at `attr_needed`. goopg's missing half is a
  per-relation needed-set — it has only a whole-statement *name* set, safe
  purely because it is decline-biased. Any future item reasoning about
  "unused above" hits that same wall.

## 5.23. C-04c lands with the session's second real win; C-06 blocked on its own gate

**C-04c** admits below-inner and non-first-comma outer links, completing
the C-04 cut. It produced the second measured TPC-DS improvement of the
workstream:

| | before | after |
|---|---:|---:|
| **Q40** | 1.50 s | **0.92 s** |
| **Q80** | 13.54 s | **10.57 s** |

Both are LEFT-below-inner, C-04c's exact subject; TPC-DS plan shapes moved
on those two queries and no others (97 of 99 identical). TPC-H is
byte-identical and +0.06% — no movement, as expected, since its corpus has
one LEFT join in total.

The mechanism is worth stating because it is a *widening*, not a deletion.
`extractSearchLeaves`' `onSpine` flag was doing two jobs at once: an INNER
join preserves both inputs, so descending one should carry the flag, and
C-04a cleared it anyway. What actually has to clear it is descending into
a side that some link null-extends. And the `base != 0` coordinate decline
— which the file header called unsafe to lift, correctly, because
`shiftColumnRefsBy` handles 13 of 32 Expr types and `return e`s the rest —
is now lifted by a re-baser that is **exhaustive over all 32 types by a
build-time gate and fail-closed on an unknown one**. That is the property
the old rewriter lacked, and building it is what made the lift safe rather
than merely possible.

**Two shapes stay declined, and both were measured rather than inherited.**
The second is the one I got wrong.

### 5.23.1 A prediction I made that the measurement refuted

I told the agent that C-04b's decline pin
(`TestSeamDeclinesAnOuterLinkUnderARightLinksNullableSide`) was "expected
to flip to admit — its comment says so". It admitted the shape, and it
**returned wrong rows**:

```
nsj_t LEFT JOIN nsj_p ON t.id=p.id RIGHT JOIN nsj_q ON t.id=q.id
  goopg (admitted):  NULL, NULL, c
  PG:                   3, NULL, c
```

Root cause, read off the plan rather than guessed:
`buildJoinRelRestrictList` classifies the *lower* link's own ON clause as
an outer-join filter clause for the *upper* link — its relids are a subset
of the upper SJI's nullable hand — and re-applies it at the upper join,
filtering exactly the rows that join exists to null-extend. Upstream
cannot reach this because an applied clause is removed from the per-rel
`joininfo` lists; goopg re-scans one flat list per pair.

So the pin stays as C-04b wrote it, **contrary to its own comment**, with
two executor cases pinning the correct rows through the fallback. Ledger
`c04c-nested-outer-refilters-lower-on-qual`. That is the third time this
session a plausible expectation of mine was refuted by a measurement the
agent ran anyway.

**C-06 (retire `GOOPG_PGSHAPED_COLLAPSE`) — blocked on its own gate, and
nothing was deleted.** The flip is not byte-identical: TPC-H **Q13 moves**,
and the OFF plan is the PG-parity one — `Index Only Scan using
customer_pk` + `Hash Left Join` at cost 66,218, against the ON path's
`Index Scan` + `Merge Left Join` at 338,223. Runtime is a wash (5.40 s vs
5.47 s, same session, identical digests). Retiring the flag would delete
the only reachable spelling of a PG-shaped Q13 for no measurable gain —
the `GOOPG_INDEXKEY_HARVEST` precedent exactly. The item's premise is also
unmet: the collapse=0 regime is fully alive after C-04c. Ledger
`c06-collapse-flip-moves-q13` names the real question — why the search
wins a Merge Left Join at 5× the cost.

One open observation recorded and deliberately not tuned: Q40's printed
cost *rises* 24 k → 393 k while its runtime *falls* 1.50 → 0.92 s.

## 5.24. C-19e lands cost-driven; C-19h reveals the parallel track's keystone

**C-19e (P5-05)** replaces `sortPartialRootPays`' hard-coded decline with
a two-candidate path tournament — `Gather Merge -> Sort -> partial`
against `Sort -> Gather -> partial`, the two plans the post-pass actually
builds — priced by `costSortRun` + `cost_gather`/`cost_gather_merge` and
adjudicated by `addPath`/`setCheapest`. The accounting worth stating:
**the rule it replaces had one hard-coded type switch; the replacement has
none.** It rides `GOOPG_PARTIAL_SORT_PATHS`, default off, delegating to
the retired switch unchanged, so the default and serial control arms stay
bit-identical.

The item pre-authorised recording a "permitted divergence" if goopg's
costs still chose leader-side sorting. **That case did not occur, so
nothing was recorded.** goopg's costs choose the worker-side sort,
disagreeing with the old rule, and the measurement backs the costs:
exactly one TPC-H plan moves — q16, the query the rule's own note cites —
with a median over five paired observations of 0.82 s off / **0.70 s on**.
The historical regressions that motivated the decline (q16 1.5 -> 2.3 s,
q13 4.2 -> 6.8 s, M0134-0189) **do not reproduce**: they predate E-10's
Gather-Merge claim set, and q13's plan no longer moves at all. Suite
totals sit inside their own spread in both directions, so no suite claim
is made.

### 5.24.1 A latent EXPLAIN bug found only because both arms were run

`rebuildWithGather`'s merge arm stamped `stampParallelScan(root)` with
`root` being the `*Sort` — which has no arm there — so the call fell
through and returned the Sort unchanged. The scan under a Gather Merge
therefore rendered **without its `Parallel ` label while the workers were
splitting it**: EXPLAIN under-reporting the one thing it exists to report.

It is label-only (the flag is read in `operators_explain.go` and nowhere
else) and has been latent since P7 **because the shape was unreachable** —
`sortPartialRootPays` declined every index driver, and no TPC-H plan
reached it with a seq-scan driver either. C-19e's cost verdict makes it
reachable, and the bug surfaced immediately. This is the third instance
this session of **an unwinnable path being an untested path**, and the
second where turning a decline into a priced decision exposed code that
had never executed.

### 5.24.2 C-19h: blocked, and not on the thing everyone assumed

C-19h (retire `MaybeAddGather`) was expected to be gated on flipping
`GOOPG_GATHER_PATHS` to on. The measurement says otherwise, and the real
blocker had never been named as a prerequisite.

At the engine defaults, **the post-pass is the only producer of
parallelism at all**: `generateUsefulGatherPaths` and `addPartialAggPaths`
are the sole producers of `PathGather`/`PathGatherMerge` and both return
at their first line when their knob is off — the default for both.

| arm | queries with a Gather | TPC-H suite |
|---|---:|---:|
| post-pass live (default) | **12 / 22** | 232.35 s |
| post-pass retired | **0 / 22** | **467.03 s (+100.0%)** |
| stand-down at `GP=all PA=on` | 7 / 22 | — |

Q18 goes 43 -> 154 s, Q21 17.7 -> 61.1 s, Q19 2.6 -> 25.3 s. And the
conditional retirement — the salvage the item permits — fails too: at
`GOOPG_GATHER_PATHS=all` + `GOOPG_PARTIAL_AGG_PATHS=on` a stand-down
build reaches only 7 of 22. It **gains Q21** (C-19f's win, the case only a
path model reaches) and **loses Q1, Q6, Q14, Q15a, Q16 and Q19**.

Why Q1 is lost is the whole finding: **C-19g replaced the split verdict
but not the construction.** `partialAggSplitPays` "returns only a boolean
and constructs no node", while `splitAggregate` in `parallel.go` still
builds `Finalize -> Gather -> Partial` inside the post-pass. So C-19g's
own headline win — Q1 8.57 -> 4.14 s — is *delivered through the very
post-pass C-19h wants to delete*, and dies with it. That is precisely
what C-19g's `[~]` row meant by "the upper-rel-resident half is
unfinished"; it had simply never been connected to C-19h.

Two process points. The conditional stand-down was **written and then
reverted rather than shipped**: landing it would serialise six queries in
the exact arm on which the default flip is to be judged, and a staging
position worse than both endpoints is not staging. And the item's own
double-Gather requirement was **verified rather than assumed** — across
every arm measured, including `GP=all PA=on` with the post-pass live, no
plan carries more than one Gather on any root-to-leaf path.

The sequencing this establishes: finish C-19g's upper-rel half -> re-run
the census -> retire conditionally on `all` -> flip the default (needs the
`plan_snapshots/` re-pin) -> only then delete. **Only the last step is
C-19h as written.** This also retires my own standing plan to flip
`GOOPG_PARTIAL_AGG_PATHS` as an independent step: the flip is not the
finish line, the construction port is.

## 5.25. Four adjudications made from evidence already in hand

Four items were closed this session without new engineering, because the
evidence that decides them had already been gathered and was sitting
unread in their own rows. Recording them together, because the pattern is
the point: **a deferred item accumulates its own verdict, and nobody goes
back to read it.**

**B-06 (CTE-agg statistics) — out of scope.** Removing the guard is a
*regression, not a gain*: Q74 goes 14 s -> 99 s. And PG has no answer
either — single-key uniqueness against a 4-key GROUP BY — so a faithful
port cannot supply one. What remains is beyond-PG work with no safe
ratchet-moving increment. The synthesis design and its inert
implementation slice (pure functions, 16 tests, no consumers wired) stay
landed as the resume point.

**B-07 (index-endpoint probe + MCV widening) — out of scope.** The
endpoint probe is architecturally blocked, and **PG itself keeps it
`#ifdef NOT_USED`**, so fidelity does not demand it. The reachable pure
half predicts ~0 ratchet movement on this corpus: the endpoints are fresh
and every literal is in-bounds, so there is no witness to improve.
Reopening needs a demonstrated out-of-range case — new evidence, not new
effort.

**C-10d (FROM-subquery pull-up) — decided: the boundary is permanent.**
The item's own census refutes the port's structural justification. A full
`pull_up_subqueries` port removes **18 of 46** ABOVE-BLOCKING boundaries
(39%), leaving 28 — so C-11's upper rels had to support boundary-crossing
construction *regardless*. The port is an optimisation on top of that
support, never an enabler of it, and ~400-700 LOC of high-risk change
moving ~46 queries' values to remove 39% of a problem you must solve
anyway is the wrong order. The decision was in any case already taken by
what shipped: C-11 and C-12 both landed treating the boundary as
permanent, with `relfromjoinlist.go` documenting it as deliberate and
ledgering its two costs. The caveat is kept visible rather than buried —
**Q9, P4-01's own witness, IS in the pullable set** — so the successor is
filed to be judged on its own measured plan-quality evidence starting
from Q9, not on the argument the census refuted.

**C-10a — one condition discharged and verified, one still owed.** The
"must land before C-15" finding was that `pg-plan-parity-diff.py` could
not *see* the grouping-sets divergence at all: goopg's label and PG's
`MixedAggregate` were both unknown node kinds, so every such divergence
was mis-filed as `join-order` and the `aggregation-strategy` bucket that
a P4 exit criterion reads was **empty of the only 8 queries guaranteed to
be in it** — a gate that could not fail. It is fixed (`0194551f6`), and I
verified it three ways rather than by reading the code: self-test 17/17;
a synthetic pair in the real divergence shape now files as
`aggregation-strategy` and names both labels; and C-15's own run observed
the bucket populated and *moving* (13 -> 14), which is the production
confirmation. The corpus facts also re-check exactly against the tracked
PG fixtures with no cluster: 11 real grouping-sets queries (the twelfth
is the `query_0` junk concatenation), 8 measurable after three dsqgen
SKIPs, and PG picking 3 MixedAggregate + 5 GroupAggregate.

What C-10a still owes is one measurement: the SF=1 memory behaviour of
Q22/Q67, on which Decision 2's AGG_HASHED pin is explicitly conditional.
It was **deliberately not run today** — it is a memory-exhaustion
measurement, and four peer agents held benchmark servers with 12 GB
already in swap. Running it there would distort the answer and risk
OOM-killing their arms. A late number beats a contaminated one; that
lesson was learned twice the hard way earlier in this workstream.

## 5.26. The last flag retirements fail their gate; C-10a's pin earns its evidence

**C-20f and C-20g — both blocked, nothing deleted.** Both were "retire a
flag" items whose gate is *byte-identical plans for the flip*, and both
fail it, on a private clone with an A/A control that came back
byte-identical (so every diff below is signal).

`GOOPG_NLI_COSTGATE` moves exactly one query, and moves it hard:

| Q4 arm | join | cost | runtime |
|---|---|---:|---:|
| default (cost gate) | Nested Loop Semi Join over the fk index | 8,672 | **1.60 s** |
| `=legacy` | Hash Semi Join, Seq Scan on lineitem | 105,657 | **18.30 s** |

11.4x — the same Q4 semi-join class the design records at 12.5x.
`GOOPG_PGSHAPED_DP` is worse: **17 of 22 queries move**, 587 diff lines,
and all 17 change top-level cost. The `=0` path is a whole second planner
— no search, syntactic order, legacy rewrites — not dead weight.

C-20f produced a genuine decision rather than a verdict, and the agent
escalated it instead of taking it, which was right. Unlike C-06, **here
the losing arm is the flag's own off path**, so retiring the hatch would
change no plan production reaches. That makes deletion defensible — but
as a deliberate *exception* to the gate, not a pass of it. I decided
against: an exception granted once is a precedent the other retirement
items inherit; deletion is irreversible against a branch that costs
little to keep; and the hatch's value is escape from a misfiring cost
gate *on data we have not seen*, which a measurement on this corpus
cannot speak to. The 11.4x says the gate is right on **this** corpus —
not the same claim.

### 5.26.1 C-07: the widening works, and still moves nothing

C-07's second half had been blocked on C-11/C-12. Both landed, so it was
re-opened — and then **implemented and instrumented rather than
re-argued**, which is what produced a real answer.

The widening works at the producer: the useful set goes `[w]` -> `[w x]`
and a real `index.ordered` path is added (pathlist 1 -> 2). And **no plan
moves** — not on cost, not with `enable_seqscan = off`, byte-identical
across five join shapes. The reason is the seam, confirmed three ways:
`planJoinlistSearch` still returns a Node, C-12's only Node->Path bridge
leaves `Pathkeys` nil, so `pathkeysContainedIn(nil, keys)` is false and
the Sort arm is the only arm production can take. **C-11's `ORDERED` rel
exists but has nothing ordered to receive.**

A second, independent blocker turned up that nobody had filed:
`addOrderedIndexPaths` runs only inside the PG-shaped join search, which
declines at `nrels < 2` — so `SELECT ... FROM t ORDER BY t.pk`, the
canonical shape the widening exists to serve, never reaches the producer
at all.

The agent also hit the exact trap this report has now recorded three
times: the whole optimizer suite passed *with the widening applied*,
because the shared fixture builds a `baseLeaf` with a nil schema, so a
rel-membership filter reading `Output()` is invisible to it. The verdict
came only from end-to-end probes.

### 5.26.2 C-10a's pin stops being a promise

C-10a's AGG_HASHED pin was explicitly conditional on an SF=1 memory
measurement of Q22/Q67 that had never run. It has now run, under a
cgroup cap with per-session ANALYZE:

| query | grouping-sets node | hash memory | batches | result |
|---|---|---:|---:|---|
| Q22 | `HashAggregate (4 keys, 5 grouping sets)` | 24.3 MB | **1** | 24.95 s |
| Q67 | `HashAggregate (8 keys, 9 grouping sets)` | 6.6 MB | **1** | 26.01 s |

**`Batches: 1` on every hash table in both plans — nothing spilled.** The
risk the condition guarded against was that hashing every grouping set
would exhaust memory where PG's MixedAggregate/GroupAggregate would not;
at SF=1, on the two queries the decision itself named, it does not come
close. The pin stands on evidence now.

The measurement was held for several hours and run late on purpose: four
peer agents held benchmark servers with 12 GB already in swap, and **a
memory measurement taken under memory contention answers a different
question**. Earlier in this workstream two timing conclusions were
contaminated exactly that way; waiting cost hours and bought a number
that means what it says.

## 6. What was dropped, and what it cost to find out

**E-04 (EX4-01) `filterOp` predicate compilation — dropped.** Three
variants were implemented and measured: compile-per-Open, slab cached
across re-Opens, and adapter-root declined.

| Q | base A | base B | v1 | v2 | v3 |
|---|---|---|---|---|---|
| Q18 | 31.93 | 31.87 | 34.62 | 34.48 | 34.82 |
| Q1 | 7.15 | 7.18 | 7.68 | 7.12 | 7.33 |
| Q12 | 12.71 | 12.57 | 13.07 | 13.12 | 12.84 |

No query improved repeatably, and **Q18 regressed 8.5% in all three
variants** against two baselines agreeing to 0.2%. The non-result is
structural, not a bad attempt: the item's own predicted effect is ~0.33
percentage points, an order of magnitude below the protocol's noise band,
and the mechanism overlaps `seqScanOp`'s prefilter, which already compiles
the same predicate before deformation — so a `filterOp` above such a scan
only ever sees survivors.

The unexplained Q18 regression is recorded as a finding in its own right,
not written off: `analysis/executor-refactor/e04-filterop-compile-20260905/`.

## 7. What is NOT measured

Stated plainly, because the gaps bound every claim above:

- **No allocation arm was run this session.** Ground rule 4 asks for timing
  and allocator arms together; the items that landed are plan-placement
  changes with no expected allocation effect, but that expectation is not
  measured.
- **Single samples.** Each arm is one sweep. Repeat baselines bracket the
  change arms so drift is visible (~1.7% on the total, up to ~3% per
  query), but no query has a confidence interval.
- **The row-weighted half of D-02 is formally unmeasured** — the in-process
  fixture catalogs carry no statistics, so every PlanRows reads 1.0. Any
  weighting of an empty declining set is zero, so the verdict does not turn
  on it, but the number D-02 asked for does not exist.
- **The TPC-H corpus cannot witness anything LIMIT-dependent.** goopg's
  suite is HammerDB `pgolap.tcl`'s templates, not the TPC-H spec: none of
  the 22 query strings contains a `LIMIT`, and none of the 23 committed PG
  plans contains a `Limit` node. So bounded/top-N sort, `cost_sort`'s
  `limit_tuples` arm and `tuple_fraction` end-to-end have **no witness in
  the numbers above** and must be measured on TPC-DS, where 81 of 99 PG
  plans are `Limit`-rooted. Quoting the spec's `LIMIT 100` on Q18 is a live
  trap — it is not in this corpus.
- **Top-level sorts are priced at zero.** `costSortRun` has one production
  caller, `sortPathFor`, so goopg costs merge-join input sorts and nothing
  else. Q18's `Sort (rows=1565307 width=204)`, the largest in the suite and
  in its slowest query, contributes nothing to any path comparison. Any
  reasoning about "goopg's sort cost model" from suite behaviour is
  reasoning about a strictly smaller thing than it appears.
- **No S-warm arm, and no parallel arm.** Everything here is S-cold serial.
- **TPC-DS timing is not tracked**, only values (PASS/MISMATCH/CKMISMATCH).

## 8. Standing conclusion

The session's durable contribution is that a planner or executor change can
now be *measured*: plan captures are byte-reproducible, the cost-exact pin
passes, and an A/A arm is available to check the instrument before trusting
an A/B. Two items were closed by measuring rather than by building, one was
dropped by measuring, and the two that landed are provably neutral. That is
a smaller list of optimisations than the plan anticipates, and a larger
correction to how the plan's remaining items must be judged.
