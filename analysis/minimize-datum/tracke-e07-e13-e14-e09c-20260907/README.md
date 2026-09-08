# Track-E residual: E-07, E-13, E-14 (Cut A/B), E-09c — measurements and verdicts

Session 2026-09-07. Four items, all of them explicitly evidence-gated. This
directory holds the measurements each verdict rests on.

Bench isolation: a PRIVATE clone of the TPC-H SF=1 cluster
(`cp -a bench/tpch/runtime_goopg/data tmp/e14-tpch-data`, 2.0 GB) served on
**port 5535** by a private binary, driven by `private-arm.sh` (a port/data-dir
variant of `scripts/tpch-acceptance-arm.sh`). The shared 65433/65437 clusters
were never touched. Every arm header carries `# engine-binary:` and was
checked before its numbers were read.

**Host caveat, stated up front:** three peer agents held the machine for this
whole session (load average 18–30 on a 16-thread box; one peer ran a full
TPC-H sweep concurrently). Every WALL-CLOCK number taken on the cluster in
this directory is therefore void as a timing, and none of the verdicts below
rests on one. What the cluster runs are used for is STRUCTURAL: plan shapes,
join types, retained row counts and retained widths, which contention does not
move. The two verdicts that do rest on timing (E-07, E-09c) were taken
in-process, both arms in the same binary and the same run, where contention
hits both arms equally and the comparison survives it.

---

## E-07 — EX5-01 slab parity for `Gather`: DROPPED

The item's own resume point: "measure the dispatch delta on a parallel witness
shape FIRST … and only implement the `Gather` arm if it clears the noise band".

Witness: `internal/executor/e07_worker_dispatch_bench_test.go`. It drives ONE
plan — `SELECT id + 1, label FROM items WHERE id > 0` over a 50,000-row heap,
which compiles to `OpSeqScan → OpFilter → OpProject`, i.e. exactly the three
migrated node kinds a TPC-H worker subtree contains (verified by walking the
slab) — through both builders in the same process:

| arm | builder + drain | median ns/iter | range | allocs/iter |
|---|---|---|---|---|
| legacy | `Build` + `Operator.Next` | **27,178,000** | 26.86–28.82 ms | 150,969 |
| slab | `BuildFast` + `opNext` | **27,837,000** | 27.37–28.30 ms | 150,974 |

8 runs of 30 iterations each per arm. The slab is **+2.2% slower**, ranges
overlap, allocation counts differ by 5 parts in 150,969.

Ceiling check, which is the part that generalises: per-row work on this shape
is 27.18 ms / 49,999 rows = **544 ns/row** (heap read + deform + expression
eval). Three interface dispatches saved at 2–4 ns each caps the whole effect at
~1.8%, below the ±17% per-query band and below the measured same-binary A/A
drift. This is E-04's structural non-result repeating: a predicted effect an
order of magnitude below the noise floor, and here it does not even have the
right sign.

The item's other two justifications were already void — E-08 was dropped by
dependency, and `buildNode` already threads the EX1-01 deform bound into
worker trees. Nothing survives. Ledger `take3-E-07-dropped`.

---

## E-09c — cooperative-stall measurement under skew + worker-count scaling

E-09c is a MEASUREMENT item, so delivering the measurement closes it. The
apparatus is in-process (host contention hits every arm equally): a temporary
instrument charges blocked time to whichever half of the cooperative parallel
hash build actually waited —

* `producerBlockedNs` — summed over producers, time parked on `ch <- batch`
  (the consumer is behind);
* `consumerBlockedNs` — time the single inserting leader parked on `<-ch` with
  the channel empty (the producers are behind), plus `recvWaits`, the number of
  times that happened at all.

Fixture: a 300,000-row build side (`cs_dim`) under a 330,000-row probe, driven
through `parallelBuildLazyHashTable` at `work_mem` = 1 GiB (single batch — the
coop path declines when `NBatch > 1`), at 2 / 4 / 8 producers, with a uniform
build-key distribution and a 10:1 skewed one. 3 repetitions × 2 distributions
= 6 observations per producer count; raw log in
`e09c-stall-scaling-3reps.txt`.

| producers | build wall (median) | producer blocked, TOTAL | per producer | consumer blocked | recvWaits |
|---|---|---|---|---|---|
| 2 | 161.9 ms | 65.8 ms | 32.9 ms (20% of build) | 6.0 ms | 8–110 |
| 4 | **139.6 ms** | 334 ms | 83.5 ms (**60%** of build) | 0.0 ms | 0–1 |
| 8 | 172.0 ms | 1059 ms | 132 ms (**77%** of build) | 0.0 ms | 0 |

**The cooperative build is consumer-bound, and it saturates at about four
producers.** Going 2 → 4 producers buys 14% of build wall; going 4 → 8 LOSES
23% and costs 3.2× more producer stall. At 4 producers and above the consumer
never waits at all (`recvWaits` = 0 in 10 of 12 observations, 1 in another):
the single goroutine that evaluates keys and inserts is the whole critical
path, and every producer past the point where it can be kept fed contributes
stall, one `mmgr.Acquire` arena and one rebuilt operator subtree — and nothing
else.

**Skew does not move the bottleneck**, because the bottleneck is already
entirely at the consumer. The 10:1 skewed arms have the same signature
(build 149.0 / 134.8 / 129.4 ms at 2 / 4 / 8 in rep 1) as the uniform ones.
That is a negative result stated as one: this shape has no skew-specific
stall to tune, because the consumer is saturated before skew can matter.

**Worth more than the item** (routed, not made — it is a tuning default, and
E-09c's mandate is to measure): `parallelBuildLazyHashTable` sets
`maxProducers = ctx.MaxParallelWorkers` with a floor of 2 and no ceiling, so a
cluster configured for 8 workers spawns 8 producers plus 8 arenas for a build
that stops improving at 4. PG's own hash build shares one barrier across
participants rather than funnelling through a single inserter; goopg's shape
puts a hard cap on what producer count can buy.

---

## The retained-storage census (evidence for E-13 and E-14)

Both remaining items are claims about what a RETAINING site holds that nobody
reads, so both need the same measurement: for every hash build and every sort
input in the TPC-H suite, the schema width, the EX1-01 deform bound its child
subtree was built with, and the number of rows actually retained.

A temporary instrument (`GOOPG_E13_CENSUS=1`) logs `(width, bound)` at Build
and the retained row count at Close; `census_aggregate.py` joins them by plan
node. Two arms cover the suite (the first died on Q9 at `GOGC=off` under peer
memory pressure and was re-run for Q9–Q22 at `GOGC=100`); raw arm logs are
`tracke-census.arm.txt` and `tracke-census2.arm.txt`.

**Suite totals, hash-build retention:** 16,803,718 retained rows,
**228,212,960 Datum cells = 10,954 MB** of stratum-D cell extent at 48 B/cell.
Largest single site: **6,001,255 rows × 16 columns = 4,609 MB** in one join.

---

## E-13 — EX1-04 Cut 2 (owned-row tightening on Project-declined paths): DROPPED

E-13's gate is written into the item: "**Only if** a later alloc arm shows a
residual." The alloc arm has now been taken, and there is no residual worth
collecting.

**Measured residual: 509,824 dead Datum cells out of 228,212,960 =
24.5 MB out of 10,954 MB = 0.22%** of retained hash-build storage. It comes
from four small build sites in the whole suite (150,000 / 29,824 / 10,000 /
10,000 rows, at `width 8 / bound 7`, `width 8 / bound 7`, `width 7 / bound 6`
and `width 7 / bound 5`), each giving back one or two columns. Every
multi-million-row retained build side — the 6,001,255 x 16, 1,500,000 x 9,
800,000 x 5, 730,895 x 9 and 455,688 x 9 sites, which are 96% of the retained
storage — carries `bound = deformBoundFull`: P4-01's `narrowBuildInput`
Project already narrowed the input to exactly what is read and there is
nothing left for a second truncation to take.

There is a second, independent reason the item should not be resumed as
written, and it is the stronger one: **E-13's mechanism is the one the EX1-04
review declined.** Copying `row[0:bound]` at a retention site makes a row
shorter than the coordinate space its readers address — the failure E-14
DESIGN §1 enumerates (`slotRow`/`Row`/`Materialize` flatten at `len(schema)`;
`nullRow(o.lazyRW)` binds a FULL-width pad into the same slot field as a
retained row; the tail of a truncated row is ABSENT, not NULL). The safe
replacement already exists and is specified: E-14 §3.1's keep-SET gather with
the reader coordinates moved at the `virtualCol` seam, of which a prefix is
merely a special case. So any residual that ever does appear belongs to E-14
Cut B, not to a separate item with a declined mechanism.

Ledger `take3-E-13-dropped`.

---

## E-14 — Cut A and Cut B

### Cut A (Semi/Anti zero-width retention): DROPPED, 0.0065% of retained storage

DESIGN §8b already found Cut A's value shrunk: P4-01's keep-set is
`needed-above ∪ quals-at-and-above`, and `needed-above` over a Semi/Anti build
side is empty by the same `Output() == Left.Output()` structure Cut A's safety
proof rests on — so the planner already narrows those builds to
`keys ∪ residual`, and only the **zero-width** case (dropping the key columns
themselves, now that §4a's keyed inner frames carry the key) was left. §3.3
sized that case at "`24 + w*48 + payload` → `24` bytes per build row" and
named Q4/Q16/Q18/Q20/Q21/Q22 as the shapes that would pay.

The census says otherwise, on the corpus:

* The whole 22-query suite contains **4 Semi/Anti hash builds** (3 Semi,
  1 Anti) against 43 INNER ones.
* **Every one of them already has `buildWidth = 1`** — P4-01 narrowed them to
  the single key column, exactly as §8b predicted.
* Total Semi/Anti rows retained across the suite: **14,747**, i.e. **14,747
  Datum cells = 0.7 MB**.

So Cut A's entire prize on TPC-H is **0.7 MB out of 10,954 MB = 0.0065%** of
retained hash-build storage — one part in 15,000 — and it is a width change on
retained rows, the class whose precedent in this repository is "0 rows on
seven TPC-H queries after passing a five-query gate". The named shapes do not
pay because they are not Semi/Anti hash builds at all in goopg's plans: Q4,
Q18, Q20, Q21 and Q22 produce INNER hash joins here.

This is the C-13a verdict shape — mechanism cheap and correct, evidence
missing — with one difference: C-13a's evidence could improve with a different
corpus, and Cut A's cannot improve without the planner emitting Semi/Anti hash
joins where it currently does not.

### Cut B (INNER keep-set + the above-walk): still the right target, still blocked

Cut B is where all of the mass is. The 10,954 MB of retained Datum cells the
census measures is **99.99% INNER**, and the design's own §2 measurement on the
Q9 fixture puts **6 of 15 retained columns** in the dead-key class — columns
that exist only because the executor evaluates the build key against the build
row, and that are dead the instant `evalHashKeyDatumSlot` returns. Applied to
the measured corpus that is an order of **~4 GB** of retained Datum cells, and
the single biggest site (6,001,255 rows × 16 columns = 4,609 MB) would give
back 288 MB per dead column.

That prize was never quantified before this session — the item said only
"deferred for want of an alloc + batch-geometry arm" — so it is recorded here.
Cut B is NOT taken in this session, for reasons that are now specific rather
than circumstantial:

1. **Its geometry half is forfeited at HEAD, by construction.** DESIGN §5.1:
   `hashsize.EntryBytes` prices the build node's SCHEMA width, so after
   narrowing the planner still chooses batch counts for storage the executor
   no longer holds. The `nbatch` 4→2 that E-12 and D-05 are sequenced behind
   is therefore unreachable until `EntryBytes` prices the RETAINED width —
   which is the D-05 follow-up the design already files. Landing Cut B first
   buys the memory saving and none of the geometry.
2. **Its acceptance is values + batch geometry on SPILLING shapes**, and the
   host was held by three peer agents at load 18–30 for the whole session:
   the one arm that could accept it is the one arm that cannot be taken. A
   row-count gate cannot substitute (21 of 21 byte-identical while Q2 went
   43× slower).

Resume point is unchanged in mechanism (DESIGN §8: generalise
`scan_deform.go:deformJoinBounds` from a prefix bound to a set, threaded
through every `buildNode`/`buildRec` arm, both build paths agreeing; seam is
`ensureLazyVirtual`'s `virtualCol` map with a NULL-pad source) and now carries
the prize above. Ledger `take3-E-14-cutA-dropped-cutB-quantified`.

### A sort-side note the census turned up

The sort half of the same question looks different from the hash half: several
sort inputs carry a LARGE prefix residual — `childWidth=14 / childBound=8`
(6 of 14 columns unread) and `childWidth=11 / childBound=4` (7 of 11) among
them. Those fractions are per-site, not per-byte: the capture behind the E-13
number counts retained rows for hash builds only. See the E-13 row in
TODO_ALL for the follow-up capture.

### The sort half of E-13, quantified

A third census arm (`tracke-census3.arm.txt`) added a retained-row counter to
`sortOp`, because the hash-side capture could not see it. The per-site dead
fractions there ARE large — `childWidth=14 / childBound=8`, `childWidth=11 /
childBound=4` — but the row counts are not: across the suite the largest sort
input measured is **20,451 rows × 8 columns**, then 11,415 × 4, 1,302 × 2,
418 × 28, and everything else is under 40 rows. A sort input of 20,451 rows
at 8 columns is 7.9 MB of Datum cells in total, of which the dead prefix is a
fraction — three orders of magnitude below the hash side's 10,954 MB, and in
the same place C-13a independently put it (median sort input 145 rows, 0 of
100 sorts spilled, TPC-H has no `LIMIT` at all).

So the sort half does not rescue E-13 either: the sites with the biggest
proportional residual are the ones with almost no rows.

---

## Values check on the private clone

The third arm is a full 22-query `-digest` run, so it doubles as a values check
that the private clone and the instrumented tree still compute what HEAD
computes:

```
tpch-runner -diff bench/tpch/baseline-digests.txt tracke-census3.arm.txt
SUMMARY: 23 MATCH, 1 STATUS-DIFF
```

The one non-match is **Q9 exceeding the 600 s per-query budget** — in both
capture arms, under a peer agent's concurrent sweep at load 25–30. It is a
budget-marginal cell in this project's own D6 taxonomy, not a value
difference: Q9 got far enough to build its hash tables in both runs (its
retained-row census lines are present, including the 6,001,255 × 16 site).
Every other label, including all four spilling ones, is byte-identical to the
committed baseline.

## Files

| file | what |
|---|---|
| `instruments.patch` | the two temporary instruments (retention census, cooperative-stall counters) and the E-09c harness, as applied to HEAD. **Reverted before commit** — the executor carries no behaviour change from this session beyond E-07's benchmark. |
| `census_aggregate.py` | joins `(width, bound)` Build sites to retained row counts by plan node and reports the dead-cell totals |
| `private-arm.sh` | port-5535 / private-clone variant of `scripts/tpch-acceptance-arm.sh` |
| `tracke-census{,2,3}.arm.txt` | the three arm logs (Q1–Q8 at `GOGC=off`; Q9–Q22 at `GOGC=100`; full 22 with the sort counter) |
| `e09c-stall-scaling-3reps.txt` | raw E-09c producer/consumer stall log, 3 reps × 2 distributions × 3 producer counts |
