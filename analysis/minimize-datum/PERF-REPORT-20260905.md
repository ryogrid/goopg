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
