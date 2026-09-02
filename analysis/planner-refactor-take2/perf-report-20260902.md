# Planner refactor take 2 — performance report, 2026-09-02

Covers `ec220754b` … `f87b248cd`: 49 commits, 66 files, +4134/−349 lines.

**Status: the refactor is NOT complete.** 33 TODO items closed, 71 open. Phases
3, 5, 6 and 7 are untouched; Phase 4 has its first slice only. This report
states what was measured, what moved, what did not, and — at some length,
because it is the more useful half — what turned out to be wrong about the plan
this work was following.

---

## 1. Headline numbers

All TPC-H SF=1, `cmd/tpch-runner`, 24 timed items, fresh server per arm, same
server age, `GOGC=100 GOMEMLIMIT=12GiB`.

| # | change | total | vs its own control |
|---|---|---|---|
| 1 | pg_statistic decode fix (`f07c20b1f`) | 288.10 → **257.75 s** | **−10.5 %** |
| 2 | P1-13 range-bound pairing | 253.51 → 254.65 s | +0.45 % (noise) |
| 3 | P1-14 + P1-25 | 246.53 → 248.71 s | +0.88 % (noise) |
| 4 | **P0-12 cluster alignment** | 248.71 → **403.27 s** | **+62.2 %** |
| 5 | Phase 1 batch (10 items) | 395.53 → 399.33 s | +0.96 % (noise) |

**Row counts identical in every arm of every A/B.** TPC-DS SF0.5 gate: **95
PASS, 0 MISMATCH** (4 skips, all oracle-side).

Row 4 is not a regression and is the most important number here — see §3.

## 2. The one change that moved time, and why

**Three bugs in the `pg_statistic` physical-tuple decoder**, each silent, each
masking the next:

1. `decodeTextArray` advanced by each element's *unpadded* length; PG aligns
   array elements to the element `typalign` and the writer pads to 4.
2. `readVarlena` assumed the 4-byte varlena header; the writer emits PG's
   1-byte short header under 128 bytes.
3. `readVarlena` aligned everything to 4, but `stavalues*` is `anyarray` with
   `typalign 'd'`.

Effect: **ANALYZE histograms did not survive a restart.** Every range predicate
fell to `DEFAULT_INEQ_SEL`; twelve of 22 TPC-H queries carry one. Q5 −32.2 %,
Q7 −17.2 %.

The consequence for the record is larger than the 10.5 %: the benchmark
lifecycle restarts the server and never runs an in-session ANALYZE, so **every
previously recorded goopg figure — the 227 s / 9.9× headline included — was
measured on a planner with no histograms.**

## 3. The measurement that was wrong from the start

The PG reference cluster set `work_mem = 64MB`; the goopg cluster set nothing
and sat at its 512MB default. **Every recorded goopg-vs-PG figure was taken with
goopg holding 8× the hash memory.**

Aligned (P0-12), goopg goes 248.71 → 403.27 s. So the honest ratio against PG's
recorded 22.9 s is nearer **17.6×**, not 9.9×. A benchmark that gives one engine
eight times the memory is not measuring the engines.

This could only be done after P2-01/P2-02, because until then `work_mem` reached
the executor but not the planner; setting it earlier would have made the two
disagree.

## 4. Wins that TPC-H cannot see

Four A/Bs on estimator work came back neutral. That is a real finding — but
"no change on TPC-H" is not "no effect", and three of these are large:

| change | effect | why TPC-H is blind to it |
|---|---|---|
| **P1-20** equivalence-class constants | `a = b AND a = 42` now gives `b = 42`. Q-shape cost **32249 → 68.74, a 470× cut** | TPC-H contains no query of that shape |
| **P1-08** `analyze_mcv_list` | `l_orderkey` carried **100 MCV entries** for a 1.5 M-distinct key; now 0, matching PG. `l_returnflag` 1 → 3, matching PG | affects estimates, not the chosen plans here |
| **P1-26** stats-resolver collapse | an index-probed leaf resolved to **no statistics at all**; every clause over it took a default | the corpus's hot paths were already seq scans |
| **P1-11b** `convert_timevalue_to_scalar` | date estimate error **3.22 % → 0.04 %** | error was already small |

Also fixed, and not visible in any timing: VACUUM's `reltuples`/`relpages` were
lost on restart (P1-03); TRUNCATE left the planner estimating **50 000 rows for
an empty table** (P1-03b); ANALYZE did not invalidate cached plans (P1-03b);
`n_distinct` overrides were written where nothing read them (P1-07); index
`relpages`/`reltuples` reported `0`/`-1` in `pg_class` (P1-01).

## 5. The diagnosis that redirected the work

The cost instrument built in Phase 0 (`P0-02`) immediately showed
`rows = 6001255/3` — `DEFAULT_INEQ_SEL` — which led to §2. Later, the `DPPATH`
provenance trace (`P0-11`) settled where the *remaining* gap lives. For Q14:

```
producer=index.ordered relids={0} rows=6001255 total=657623.09  accepted
producer=mergejoin     relids={0,1}            total=754717.55  accepted
producer=join.hash     relids={0,1}            total=1811944.24 dominated
```

The index scan is **not** under-costed and the merge join is a correct sum of
its inputs. The hash join is costed 2.4× higher — because:

```
goopg  Index Scan on part  rows=200000 width=548  ->  104.5 MB hash
PG     Index Only Scan     rows=200000 width=6    ->   14.6 MB, Batches: 1
```

The query needs two of `part`'s nine columns. PG projects; goopg carries the
whole tuple because it has **no `PathTarget`**. At 64 MB that forces batching,
the cost triples, and a merge join over a full 6 M-row index scan wins — 13.9 s
against PG's 1.08 s.

**So the remaining gap is in the cost model's inputs, not the cardinality
estimator.** That is why four consecutive selectivity A/Bs moved nothing, and it
is a measurement rather than an assertion.

I recorded two wrong diagnoses on the way to that, both publicly corrected: that
the regression was hash *spill* efficiency (neither engine spills — both report
`Batches: 1`), and that the index scan was under-costed (the 66 680 in the plan
text is `DeriveLegacyDisplayCost`'s rendering, not the planner's 657 623).

## 6. Where the plan itself was wrong

Of ~20 TODO items examined closely, a third specified something that should not
be done as written. This is the most transferable output of the session.

| item | as written | actually |
|---|---|---|
| P0-10 | TPC-DS anchors inert | fixed a month earlier by `63056c544` |
| P1-01 | *persist* per-index relpages | read live via `RelNBlocksFunc` — what `get_relation_info` does. Persisting would **add** a staleness class PG does not have |
| P1-06 | adopt PG's block sampling | goopg's full scan gives **exact** `reltuples`; PG's estimates. Declined — it trades planner accuracy for ANALYZE speed in a path not on the query path |
| P1-17 | port the semi MCV arm | already present, including the `nd -= nmatches` discount that looked like a divergence and is upstream's |
| P1-20 | `nconst_ec` double-count fix | premise inverted — constants never propagated at all, so there was nothing to double-count |
| P1-18 | port the jointype switch | the search never *sees* an outer join (`DPTRACE`: `nrels=1, pairs=0`). Blocked on P3-04 or it is dead code |
| P1-21 | delete the cap once P1-15 lands | P1-15 improved the *measured* path; the cap guards the *unmeasurable* one |
| P4-01 | insert a `Project` / narrow the leaf schema | the leaf-schema option **silently disables the join search** (seam offset invariant); the collector it proposed to build already exists |

Two design docs were agent-reviewed before implementation and both had blockers.
The P2-A review caught that my accessor would have read **another session's
GUCs** under concurrent planning. The P4-A review caught the seam invariant and
found a **live planner/executor desync** — paths were costed at their
relation's width while the executor measured the narrowed schema — which
`P4-01a` then fixed.

## 7. What is left, in priority order

1. **P4-01's remaining slice**: narrow an ordinary heap scan to its needed
   columns. `P4-01a` removed the per-rel/per-path blocker, so this is now
   setting two fields and emitting a narrower schema. It is the measured
   bottleneck (§5). Not started here because a half-finished projection is a
   silent wrong-answer class, and it needs value-level verification
   (`tpch-runner -digest`), not row counts.
2. **P3-01…P3-04**: bring outer joins into the search. Also unblocks P1-18.
3. **P5-06**: parallel hash join — PG shares one 14.6 MB table across five
   workers, which is the *second* reason it stays inside 64 MB.
4. P2-02b (`work_mem` BootVal 512MB → PG's 4MB) — now unblocked, but expect it
   to be large: 512→64MB alone cost 62 %.

## 8. Caveats

- One run per arm. Two runs of the **same binary** differed by 1.7 %, which
  bounds any single figure here. Only Q5 (−32 %) and Q7 (−17 %) clear it
  individually; the rest rest on aggregate direction and row-count identity.
- Concurrent test CPU during part of the first A/B's `before` arm. It biases
  *against* the fix, so it cannot manufacture the −10.5 %, but makes it an
  upper bound.
- The 227.0 s baseline in 07 §2 is **not** comparable to anything here:
  different binary, different histogram state, and the cluster was restarted and
  re-ANALYZEd during diagnosis.
- The two TPC-H clusters hold different `lineitem` loads (+0.040 %), and
  `shared_buffers` remains 4× apart by design (goopg's arena is a Go-heap object
  under `GOMEMLIMIT`).
- pg_regress: `limit` and `numerology` fail at HEAD **and** on a worktree at
  `ec220754b`, so this work introduced no regress regressions.
