# Planner + Executor refactor — performance report

Scope: the `docs/design/not_ralph/minimize_datum/TODO_ALL.md` workstream.
Branch `plan-narrowing-and-etc`. Date 2026-09-05.

This report states what changed, what it cost or bought, and — with equal
weight — what could not be measured and what got worse. Every number below
comes from a run recorded in this repository; nothing is modelled.

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

## 5.6. Following the evidence: the entry-width fix, and its refutation

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
