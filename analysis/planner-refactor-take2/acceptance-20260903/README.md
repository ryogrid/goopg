# planner_refactor_take2 — interim verdict, 2026-09-03

**Scope.** This is NOT the acceptance document 09 §6.6 specifies for the finished
refactor. Phases 4 and 5 have not started, so the bars in 09 §7 (A1–A5, B5,
C1–C4) cannot be evaluated. It is the honest roll-up for the work that *did*
land, written to the same shape, and it states what got worse as well as what
got better.

**Engines.** goopg at `671485c80` vs PostgreSQL 18.3, TPC-H SF=1
(`bench/tpch`, port 65433 / 65432) and TPC-DS SF=0.5 (`bench/tpcds`, the
git-tracked oracle). `work_mem = 64MB`, `hash_mem_multiplier = 2`,
`GOGC=100 GOMEMLIMIT=12GiB`, fresh server per arm.

---

## 1. Headline

TPC-H SF=1, 24 timed items, every arm verified with `cmd/tpch-runner -digest`
on **values** (ordered + unordered digests + column signature), never on row
counts:

| stage | TPC-H | note |
|---|---:|---|
| start of this bundle's session | 245.71 s | |
| parallel post-pass fix | 242.91 s | |
| settings propagation | 258.28 s | **+6.3 %, and correct** — see §4 |
| merge-join cost (`mergejointuples`) | 240.73 s | −6.8 % |
| merge-join dropped clause | 239.72 s | correctness, timing-neutral |
| `enable_*` wired | 242.38 s | |
| unique-index clamp | 237.34 s | |
| **ndistinct two-form fix** | **215.62 s** | **−8.1 % in one change** |
| `mergejoinscansel` | 215.01 s | |
| `RelSet` widened | 213.84 s | |
| FROM-reorder removed | 221.29 s | inside the band — see §5 |

Net **245.71 s → ~215 s, about −12 %**, with the caveat in §5 about the
variance band at the end of the run.

TPC-DS SF=0.5 held **PASS=95, MISMATCH=0, CKMISMATCH=0, ERROR=0, TIMEOUT=0**
across every sweep in the session.

---

## 2. What actually produced the gain

One change produced most of it. `ColumnStats` stores upstream's single signed
`stadistinct` as two fields — `NDistinct` (absolute) and `NDistinctFrac`
(relative) — and both `eqSelectivityForColumn` and `resolveBaseColumn` read only
the absolute one. Every column whose distinct count *scales with the relation*,
which is most key columns, resolved to ndistinct **zero** and fell to
`DEFAULT_EQ_SEL`:

| | goopg before | after | PG |
|---|---:|---:|---:|
| `p_partkey IN (1,2,3,4,5)` | 5,000 | **5** | 5 |
| `l_orderkey IN (1,2,3)` | 90,018 | **14** | 51 |

5000 is exactly `5 × DEFAULT_EQ_SEL × 200000`, which is what made it
identifiable: the statistics were present and simply not being read.

---

## 3. Correctness (the part that matters more than the timing)

**Five bugs, four of them silent. Three returned the CORRECT ROW COUNT while
computing the wrong answer.**

1. **Merge join dropped an equi-clause entirely.** `generateMergeJoinPaths`
   trimmed its merge-clause list to the groups the outer's ordering serves and
   passed the original residual through, so the dropped clauses were evaluated
   nowhere. TPC-H Q9 returned **175 rows — the correct count** — summing
   30,270,658,609.88 against the oracle's 7,528,869,517.19, a factor of 4.02.
   Pre-existing; reproduced at the session-start commit. Fixed in two sites
   (`13d53603f`, `a96c65978`), the second found only by writing the regression
   test the first lacked.
2. **The parallel post-pass ran only on cached plans**, so any cache-bypassing
   session lost all parallelism.
3. **Planner settings stopped at the top of the plan** — 30 `newResolveContext`
   sites re-defaulted them, so a subquery planned under the hardwired defaults
   whatever the session said.
4. **`enable_hashjoin` / `mergejoin` / `nestloop`** were accepted, shown in
   `pg_settings`, and did nothing.
5. **Five GEQO knobs and `enable_memoize`** likewise; `enable_memoize` was worse
   than inert — it wrote a process global, so one session's `SET` steered every
   other session.

**A row-count gate cannot see class 1.** `spotcheck_expected.env` and
`ci/batch/tpch-row-anchors.csv` compare row counts and are structurally blind to
it. `tpch-runner -digest` is what caught it.

---

## 4. What got worse, and why it was kept

**Settings propagation cost 6.3 % (242.91 → 258.28 s) and was landed anyway.**
Before it, the bench planned at an accidental 1 GB while its conf said 64 MB.
The slowdown *is* the correction: goopg now plans at the budget it was
configured with. Same class as P0-12's earlier cluster alignment.

**`GOOPG_HASH_OUTER_JOIN` was measured and deliberately NOT flipped.** It is
safe now (PASS=95, MISMATCH=0, **CKMISMATCH=0**, answering the recorded worry
that it "keeps every row but changes their ORDER") but it is a wash: 2 of 99
plans change, Q51 14→11 s and Q97 11→15 s, net **+1 s**. Flipping a default on a
wash is churn.

**The per-tuple index qual cost was implemented, measured, and reverted.**
Faithful to `selfuncs.c:7228-7234`, not double-charged, not asymmetric — and
still **+3.3 %** on TPC-DS against a same-day ±0.4 % band. Adding one correct
term to a model missing its neighbours made outcomes worse.

**P2-02b remains unlanded at +23.1 %.** Correcting `work_mem`'s BootVal to PG's
4 MB is now *value-correct* (24 MATCH — it was not, before the merge-join fix),
but costs 215.62 → 265.44 s, entirely Q9 (+40 s) and Q7 (+9 s). Re-measured
after the ndistinct fix and unchanged, which **rules out estimation error as the
cause** and leaves width (P4-01) and the lost Gather (Phase 5).

---

## 5. Measurement honesty

Three methodology problems were found by being wrong first, and each is now
enforced rather than remembered:

- **The TPC-DS gate could not see a broad, shallow regression.** Its runtime arm
  is per-query with a 2× factor; a change moving 60 plans and slowing ten
  queries by 3–5 s each reports `runtime-moves=0`. The +3.3 % above passed it.
  A `TOTAL` aggregate arm was added to `scripts/tpcds-sweep-diff.py`, validated
  against three real sweep pairs — it fires on the regression and stays silent
  on the two clean pairs.
- **A per-query runtime move is only attributable if that query's PLAN changed.**
  Applied repeatedly since: for P2-11, P2-12, P3-12 and P6-06 the queries the
  gate *named* had byte-identical plans, while the real effect sat elsewhere.
  For P3-12 the split was −41 s on the 64 changed plans against +7 s on the 35
  unchanged ones.
- **The TPC-H variance band widened during the session.** P3-10 appeared to cost
  +2.8 % until an A/A control showed the *unchanged* baseline moving 213.84 →
  221.01 s on a re-run. Re-run the baseline before believing a TPC-H delta of
  that size; the late figures in §1 carry this caveat.

---

## 6. Items determined must-not-be-done

Six, each with a measurement, all recorded in `.ralph/deferral_ledger.md`:

| item | finding |
|---|---|
| P6-03 | load-bearing — Q20 6.5×, correlated `Index Cond` becomes `Filter: (true)` |
| P6-04 | load-bearing — Q4 12.5×; P3-11 explains why |
| P6-05 | **not dead** — the oracle for a live wrong-column tripwire on every searched plan |
| P2-08, P2-10, `num_sa_scans` | no consumer exists; blocked on Phase 3/4 |

The pattern is consistent and worth stating: **Phase 6's deletions assume the
search has superseded the legacy passes, and it has not.** Every one is
compensating for a gap the search still has — the same assumption that makes
P2-08 and P2-10 unbuildable. Phase 6 is gated on Phases 3–5 far more tightly
than the bundle's ordering suggests.

---

## 7. The largest remaining gap, measured at last

07 §6's width residual, at **equal cardinality** for the first time — earlier
attempts to claim it compared plans with different row counts, which is why an
earlier P4-A revision had to withdraw it:

| TPC-H Q9, same `work_mem` | PostgreSQL 18.3 | goopg |
|---|---:|---:|
| rows through the join tree | ~319 k | 321,056 |
| tuple widths | 23 / 32 / 54 / 81 B | 1098 / 1642 / 2090 / 3164 B |
| peak hash memory | 38 MB | 97 MB |
| batches | 1 | 8 |
| Q9 | 6.2 s | 63.8 s |

Same rows, **14–39× wider tuples**, because there is no `PathTarget` and so no
projection. That is P4-01, and it is what blocks P2-02b.

---

## 8. Verdict

The bundle is **not complete**. Phases 4, 5 and most of 3 remain, and the bars in
09 §7 cannot be evaluated until they land.

What this session establishes is narrower and, for a from-scratch reimplementation,
more useful: goopg's planner was returning **wrong answers with correct row
counts** on ordinary SELECTs, and the default configuration was hiding it. That
is fixed, the gates that missed it are fixed, and every remaining item carries a
measurement and a resume point instead of an assumption.
