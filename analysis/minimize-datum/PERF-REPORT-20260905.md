# Planner + Executor refactor — performance report

Scope: the `docs/design/not_ralph/minimize_datum/TODO_ALL.md` workstream.
Branch `plan-narrowing-and-etc`. Date 2026-09-05.

This report states what changed, what it cost or bought, and — with equal
weight — what could not be measured and what got worse. Every number below
comes from a run recorded in this repository; nothing is modelled.

## 1. Headline

**No performance regression, and one measurement result that matters more
than any single optimisation: the benchmark harness could not previously
tell a real change from a re-run.**

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
run-to-run drift is ~1.7%).

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
