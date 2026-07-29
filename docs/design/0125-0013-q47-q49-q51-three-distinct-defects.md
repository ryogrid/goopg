# 0125-0013 — TPC-DS Q47, Q49, Q51: three distinct defects one label had conflated

Status: draft
Date: 2026-07-29
Milestone: **M0125-0013 (Q47), M0125-0014 (Q49), M0125-0015 (Q51)** — one document, three
tasks, because the load-bearing finding is that they are *not* one defect.
Source: ledger row `tpcds-round2 q47-q49-q51` (2026-07-29) — the only source covering all
three. §13.4 item 2 covers **Q47 and Q51 only**; §5a covers **Q49 only**.

## Why one document for three tasks

Through round 2 these three were tracked as one "RC-1b family" — the provisional hypothesis
being that one mispushed MHJ filter accounted for all three wrong answers. (Note the
hypothesis was never that all three returned **zero**: Q49 has always returned 30, not 0.)
RC-1b (`5db0a067`) then landed and **falsified the family**:

| query | before RC-1b | after RC-1b | what that proves |
|---|---|---|---|
| Q47 | `OK 17 s` / 0 rows | `OK 142 s` / 0 rows, **CTE body 661,185 = PG** (was 0) | RC-1b fixed the *input*; a **second** defect sits downstream |
| Q49 | 30 rows | **30 rows — unchanged** | **Disproves** its provisional RC-1b-family attribution |
| Q51 | 0 rows | **0 rows — unchanged** | Read as a **third** distinct defect — but see § Q51 for the reservation the later measurements kept |

The ledger row states the consequence directly: "**All three are separate fix_plan items,
not one.**" It also records why that finding was worth the round's budget — *an inherited
wrong label is more expensive than an open unknown*: while the three were one line, any fix
attempt on one of them was implicitly a claim about the other two.

This document therefore holds the shared evidence and three independent sections. **Nothing
here should be read as implying a shared fix.**

### ⚠️ Gate visibility is NOT uniform across the three — measured 2026-07-29

All three differ from PG in row count, but that does **not** make them detectable. Against
`bench/tpcds/runtime_goopg/tpcds-results-sf05/sweep-20260729-093056.txt` (HEAD),
`…/oracle.txt` and `ci/batch/tpcds-row-anchors.csv`:

| | SF0.5 gate at HEAD | SF0.5 value acceptance | nightly anchor |
|---|---|---|---|
| Q47 | **sees it** — `MISMATCH 43s goopg=0 oracle=100` | **no** — `47\|OK\|100\|n/a\|2` | **absent** |
| Q49 | `PASS 25 rows`, checksum matches — **blind** | yes (`ck=ccb0983b810cf1d0`) | **absent** |
| Q51 | `PASS 100 rows` — **blind** | **no** — `51\|OK\|100\|n/a\|1` | **absent** |

**Q49 and Q51 flipped `MISMATCH` → `PASS` at SF0.5 the moment M0125-0009 landed**
(`sweep-20260729-004730` at `7a7a2639` → `sweep-20260729-033758` at `3fbce36a`, still PASS
at HEAD). No completion note and no ledger row records that side effect, and **neither has
been re-measured at SF=1 since**. That is why § Q49 and § Q51 below begin with a
measurement, not a diagnosis.

The anchor CSV pins 61 queries and holds **none of these three**, so closing any of them
means **adding** an anchor rather than re-pinning one. None depends on M0124-0005's checksum
for *acceptance* (each differs in rows), but Q47 and Q51 cannot be value-accepted at SF0.5
at all.

---

## § Q47 — M0125-0013: the second defect, above the CTE

### Problem

`OK 142 s / 0 rows` against PG's `OK 3 s / 100 rows`
(`analysis/tpcds-sf1-resweep-20260728/RESULTS.md` row 47; classified in
`analysis/tpcds-sf1-goopg-20260728.md` §3, projection 6 — CONFIRMED).

RC-1b made Q47's CTE body **exactly correct**: 661,185 rows = PG, previously 0, by stopping
a mispushed predicate from silently zeroing the scan input. The full query still returns 0.
Therefore a second, independent defect sits in the **windowed self-join layers above the
CTE** — layers which, until RC-1b, had never received non-empty input and had therefore
never been exercised at all.

### Resume point

Start **below** the CTE, at the `v1` → `v2` window / self-join layers of
`bench/tpcds/runtime_goopg/tpcds-data/queries/query47.sql` — the `rank()` / `avg()` over the
partitioned windows. Reproducible for the first time, because the input is non-empty.

**Iterate at SF0.5 for rows, SF=1 for values.** SF0.5 reproduces the gap in **43 s**
(`MISMATCH goopg=0 oracle=100`) against 142 s at SF=1 — but Q47's oracle entry is
`47|OK|100|n/a|2`, so the SF0.5 gate can never *value*-accept the fix.

### A documented contradiction this task must settle

Q47's runtime moved `OK 17 s` → `OK 142 s` (8.4×), reproduced standalone at 143 s
(`analysis/tpcds-sf1-resweep-20260728/diag-q47-rerun.txt`) and shown to be query-specific
(Q44/Q46/Q48 in the same chunk, on the same server at the same age, all within ±2 s of the
prior sweep — so it is neither noise nor GC/sweep-tail collapse).

**Two primary sources disagree on what that means:**

| source | verdict |
|---|---|
| `analysis/tpcds-sf1-resweep-20260728/RESULTS.md`, chunk 49–56 | "**Q47 is NOT a regression** … An 8.4× runtime increase is the **expected cost** of [the CTE body going 0 → 661,185], not a performance regression." Chunk 41–48's opposite reasoning is explicitly retracted there |
| ledger row `tpcds-round2 RC-1b`, residual (a) | Same reading — "14s->143s confirms real work" |
| `analysis/tpcds-sf1-goopg-20260728.md` §3.2 **and** §6 item 2 | "this is a slowdown *on top of* an unfixed wrong answer, **not the cost of a newly-correct plan**" … "bounded but **unattributed**" |

The merged deliverable reproduces chunk 41–48's superseded reasoning and is internally
inconsistent with `RESULTS.md`.

**Deliverable for this task — and it is STEP 0, before any planner edit.** An `EXPLAIN` diff
of Q47 against set A's plan, with the verdict written back into whichever document is wrong.
The ordering is not cosmetic: once the fix moves plan shape, the diff against set A is
confounded and the question becomes unanswerable. Leaving two committed reports contradicting
each other about an 8.4× movement is a bookkeeping defect in its own right — strictly it is a
repair of M0124-0001's deliverable rather than engine work, so it may be split out if it
grows beyond a diff and a paragraph.

### Acceptance

**By value.** Q47 = 100 rows = PG **and** values equal PG **at SF=1** (SF0.5 carries
`ck=n/a`, so it cannot value-accept). Plus the step-0 runtime attribution. The SF0.5 gate
sees the row gap today and will register the fix; the nightly anchors will not — **add** a
Q47 row to `ci/batch/tpcds-row-anchors.csv` on close.

---

## § Q49 — M0125-0014: 30 rows where PG returns 34, and 30 is suspiciously 3 × 10

### Problem

`OK 79 s / 30 rows` against PG's `OK 1 s / 34`. Unchanged by RC-1b.

**⚠️ STEP 0 — that SF=1 number is stale, and the task starts by refreshing it.** Q49 went
`MISMATCH 24/25` → `PASS 25` at SF0.5 when **M0125-0009** landed and still passes at HEAD
with a matching checksum. Nothing records that. Q49 has **not** been re-measured at SF=1
since -0009/-0010/-0011. Measure it first: if SF=1 now returns 34 = PG by value, this task
closes as *measured-and-already-fixed* — a legitimate completion, with an UPDATE on the two
ledger rows naming M0125-0009 — and everything below is moot. The precedent for a task that
may close on measurement alone is M0124-0004 ("recover **or classify**").

Q49 is three `UNION ALL` branches (web / catalog / store), each ranking a derived ratio and
filtering `return_rank <= 10 or currency_rank <= 10`. goopg returns **exactly 30** — exactly
3 × 10, which is the arithmetic a collapse of the two-rank `OR` into a *single* rank filter
would produce. That is a hypothesis worth testing first, not a conclusion.

### Ruled out by probe — do not re-test

`rank()` peer-tie handling. `rank`, `dense_rank` and `row_number` over a tied `ORDER BY` are
**byte-identical to PG** (`1,1,3,3,3,6` on both engines), so the `<= 10` filter is not
silently degrading to `row_number` semantics (§5a, ledger row `tpcds-round2 Q49`).

### Remaining candidates (§5a)

1. The `decimal(15,4)` division producing `return_ratio` / `currency_ratio` — a precision
   difference reorders ties and changes which rows sit **at** rank 10. Note goopg has a
   known numeric-division scale gap (result scale on an exactly-zero quotient) and a known
   `stddev_samp`/`sqrt_var` last-digit divergence; neither is obviously this, but the
   numeric path is the first place to look.
2. The mixed `store_sales sts LEFT OUTER JOIN store_returns sr … , date_dim` shape — the
   outer-join-plus-comma-join form that §2.3 flags as unverified **for Q72** (§5a's own
   wording; the limitation to Q72 matters, it was never a general finding).

### ⚠️ The cheap reproduction is GONE

§5a's one-row SF0.5 gap (24 vs 25) was the recommended bisect target and the reason the
milestone's interleaving rule once called Q49 the cheapest item. **It no longer reproduces**
— see STEP 0. SF0.5 is now a *regression* gate for Q49, not a *detection* gate, which is the
same distinction M0125-0011 established by measurement (from the opposite direction: there,
a row-count change that the gate could not see). Any new minimal reproduction has to be
constructed at SF=1, not inherited.

### Acceptance

**By value.** 34 rows = PG **at SF=1**, values equal. **"25 rows at SF0.5" is not an
acceptance signal — HEAD already satisfies it.** Add a Q49 row to
`ci/batch/tpcds-row-anchors.csv` on close; there is none today.

---

## § Q51 — M0125-0015: 0 rows where PG returns 100, third distinct defect

### Problem

`OK 587 s / 0 rows` against PG's `OK 1 s / 100`. Unchanged by RC-1b.

**"Third distinct defect" is the leading hypothesis, not a settled fact.** §13.4 item 2 does
call it that on the strength of RC-1b leaving it untouched, but the later measurements
deliberately kept the question open, and this document should not close it by restatement:

- `RESULTS.md` chunk 49–56 — its RC-1b family membership "stays ***probable, unproven*** —
  the fix that corrected Q50 did not move Q51's rows".
- ledger row (M0124-0001, 2026-07-28) — "so **either Q51 is a different defect or it shares
  Q47's downstream one**".
- `analysis/tpcds-sf1-goopg-20260728.md` §3 — §13.4 item 2's conclusion is "**untouched by
  this sweep**", i.e. neither confirmed nor refuted.

`RESULTS.md` also warns why "rows did not move" is weak evidence here: RC-1b's effect "is
simply **not uniform across the family**, which is why Q49/Q51 look untouched".

**⚠️ STEP 0 — the SF=1 number is stale, exactly as for Q49.** Q51 went `MISMATCH 0/100` →
`PASS 100` at SF0.5 when **M0125-0009** landed and still passes at HEAD; unrecorded. One
~590 s SF=1 observation may close this task outright, the same "resolve **or classify**"
shape as M0124-0004.

**No mechanism is claimed.** The only characterisation on record is a *shape* — "a wrong
answer that had been hiding behind a timeout" (`docs/design/tpcds-round2-fixes/README.md`
**§13.3**; the phrasing is §13.3's and the ledger's, not §13.4 item 2's), the same shape
M0124-0004 names for Q35. The ledger's resume point is deliberately modest: *re-measure at
SF=1 against M0124-0001's sweep row before assuming a mechanism.*

### ⚠️ Budget-marginal on the `OK` side

Q51 completes with **13 s of headroom** under the 600 s cut — the **narrowest** `OK` margin
in the sweep; Q82's 44 s is the next-narrowest. (`RESULTS.md` and
`analysis/tpcds-sf1-goopg-20260728.md` §3 both call Q82 "the narrowest OK margin of the
sweep", which §5 of the same document then contradicts by listing Q51 at 13 s. 13 < 44, so
§5 is the one to follow.) Two consequences:

1. **Any fix that adds work can flip it to `TIMEOUT` and mask a correct row count.** A
   correct answer that no longer completes reads, on the verdict column, exactly like a
   regression.
2. Therefore: **time it explicitly and report the runtime beside the rows.** If the fix
   crosses the budget, raise the budget for the acceptance run rather than declaring the fix
   a regression — the reporting rule `analysis/tpcds-sf1-goopg-20260728.md` §5 states for
   marginal cells.

Its history also shows the margin moves on its own: set A `OK 597 s` (3 s headroom) →
this sweep `OK 587 s` (13 s), i.e. 10 s *faster* with no relevant change.

### Acceptance

**By value.** 100 rows = PG **at SF=1**, values equal, **and the measured runtime
recorded**. "100 rows at SF0.5" is not an acceptance signal — HEAD already satisfies it —
and SF0.5 cannot value-accept Q51 at all (`ck=n/a`). Closing by measurement, if SF=1 now
matches PG, is a valid outcome; record it and UPDATE the ledger rows. Add a Q51 row to
`ci/batch/tpcds-row-anchors.csv` on close; there is none today.

---

## Shared gate

All three are planner/executor changes → the full pre-commit bar:

- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
- `scripts/tpch-spotcheck.sh` (canonical `Q12=2`, `Q13=35`)
- the TPC-DS SF0.5 gate — **only Q47 is visible to it today** (Q49/Q51 both PASS at HEAD);
  see "Gate visibility is NOT uniform" above. Run it for regression on all three regardless
- `make plan-diff` — with M0125-0004's `r5-default` fallback label until M0124-0002 lands
  `tpcds-round2-head`
- the pgbench smoke, via the commit hook

**Sibling-path audit (Hard-won Rule #2)** applies per task: Q47's window/self-join layers
pair with the aggregate/window evaluator twins; Q49's ranking pairs `rank()` evaluation with
the numeric division producing its ordering key; Q51's is unknown until a mechanism exists.

## References

- `.ralph/deferral_ledger.md` — `tpcds-round2 q47-q49-q51` (2026-07-29),
  `tpcds-round2 Q49` (2026-07-27), `tpcds-round2 RC-1b` (2026-07-27, residual (a))
- `docs/design/tpcds-round2-fixes/README.md` §5a (Q49), §13.4 item 2 (the three-way split)
- `analysis/tpcds-sf1-goopg-20260728.md` §3 (projections 6, 7, 8), §5 (budget-marginal
  reporting rule)
- `analysis/tpcds-sf1-resweep-20260728/RESULTS.md` chunks 41–48 and 49–56 (the Q47 runtime
  class, opened and then closed), `diag-q47-rerun.txt`
