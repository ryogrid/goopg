# M0146-0014 — parity-closure sweep

Status: held [!] 2026-10-13 on the S4 lineage budget (the milestone's exit
report; sweeps 0014a–d done, the last on 2026-10-13). Five findings from
0014d (F1–F5) wait unfiled in the task's escalation for the owner.

## Purpose

Re-run the first-divergence census on both corpora. Prove that every
remaining record is either assigned to a live task or presented to the
owner as a named, measured, waived residual. The acceptance bar is "no
unnamed first-divergence records".

## Method

1. Capture goopg and PG plans at HEAD for TPC-DS at both scales. The fire
   set's captures serve.
2. Run `scripts/pg-plan-first-divergence.py` on each scale.
3. Diff the records against the previous routing. Records whose route is
   still live keep it; records whose target has closed, or whose first
   divergence moved, are re-analysed from plan text.
4. Assign each record a family and a route:
   - COSTTIE, B8, RELPAGES and B-15 are owner residuals.
   - MECHANISM, GSETS, STATS, TRACE and CAPTURE go to filed tasks.
5. Write `ROUTING.md` with every record and file the new tasks.

## 2026-10-09 (M0146-0014a)

`analysis/m0146/m0146-0014/routing-20261009/ROUTING.md` covers 112
records:

| scale | match | divergent |
|---|---|---|
| SF0.25 | 49 | 50 |
| SF1 | 37 | 62 |

- **Owner residuals:**
  - COSTTIE 31, near-ties within about 1%;
  - B8 27, to M0145-0008ag via M0146-0068's option choice;
  - RELPAGES 15, waiting on the SF1 cluster reload;
  - B-15 7, blocked.
- **Filed:** M0146-0130 to 0140. The table in `ROUTING.md` gives the PG
  mechanism behind each.
- **Held:** M0146-0042 (rendering) and M0146-0005dp.

## 2026-10-10 (M0146-0014b)

`analysis/m0146/m0146-0014/routing-20261010/ROUTING.md` covers 106
records at HEAD `05897eb09`:

| scale | match | divergent |
|---|---|---|
| SF0.25 | 53 | 46 |
| SF1 | 39 | 60 |

- **What changed since 2026-10-09.** M0146-0130 to 0145 closed:
  - Q4, Q35, Q66 and Q67 now match at SF0.25, and Q66 and Q67 at SF1;
  - Q36, Q70 and Q86 now plan instead of erroring, so they are records
    for the first time.
- **What was re-analysed.** 84 records kept their earlier route (the first
  divergence and its target were unchanged). The other 22 were re-analysed
  from plan text, each with its cost numbers.
- **Owner residuals:**
  - B8 32 (Q49 ×2, Q92, Q79 and Q18 SF1 joined);
  - COSTTIE 29;
  - RELPAGES 16;
  - B-15 7;
  - RENDERING 5 (Q6's `$0` SubPlan argument; Q58's InitPlan numbering).
- **A new owner class: GEQO-RNG, 2 records (Q64 at both scales).**
  - The first divergence is an exact tie among one-row probes, which
    PG's GEQO random stream (`pg_prng`) settles.
  - goopg's GEQO uses another generator. Even a port of that generator
    would not reproduce PG's tours while any tour cost differs, so the
    recommendation is to waive.
- **Filed:**
  - M0146-0146, a Subquery Scan with a computed resjunk ORDER BY key is
    not trivial (Q36/Q70/Q86 ×2);
  - M0146-0147, the needed-column set for statements with a WITH clause,
    which blocks index-only paths (Q95 ×2);
  - M0146-0148, a sub-joinlist hands its whole pathlist up (Q72 SF1);
    this is the open pathlist half of ledger row M0127-P5.9-a.

## 2026-10-10, second refresh (M0146-0014c)

`analysis/m0146/m0146-0014/routing-20261010b/ROUTING.md` covers 100
records at HEAD `1fd148e3a`:

| scale | match | divergent |
|---|---|---|
| SF0.25 | 56 | 43 |
| SF1 | 42 | 57 |

- **Since 0014b.** Q36, Q86 and Q95 now match at both scales
  (M0146-0146, M0146-0147). Q72 SF1 is COSTTIE (M0146-0148's
  measurement). Every other record carries its route unchanged.
- **Filed: M0146-0149** (Q70 at both scales).
  - PG's `cost_incremental_sort` clamps its input to at least 2 tuples and
    sets the path's rows from the clamped value.
  - That makes Q70's 1-row window subquery 2 rows, so PG hashes it in a
    semi join, where goopg runs it as a 1-row nested-loop outer.
- **Route counts:** B8 32, COSTTIE 30, RELPAGES 16, B-15 7,
  RENDERING 5, PARAM-APPEND 2, GEQO-RNG 2, INCSORT-ROWS 2, and four
  single records.

## 2026-10-13 (M0146-0014d)

`analysis/m0146/m0146-0014/routing-20261013/ROUTING.md` covers 84
records at code `7e6d8b13f`:

| scale | match | divergent |
|---|---|---|
| SF0.25 | 70 | 29 |
| SF1 | 44 | 55 |

- **What changed since 0014c.** Two owner classes resolved: the probe
  multiplier was retired (M0146-0159), and the descent and qual-op terms of
  B-15 were ported (M0146-0172). Their records were re-analysed with the
  moved and new ones: 13 SF0.25 records, in two plan-text passes.
- **SF1 runs on a stale load.** The goopg SF1 cluster stores `char(n)`
  unpadded and `numeric` as text (M0146-0172, M0146-0174).
  - A record that diverges at SF1 only (28) is the owner residual
    STALE-SF1, re-measured after the reload.
  - A record shared with SF0.25 takes the SF0.25 route.
- **Owner residuals:**
  - COSTTIE 17;
  - GEQO-RNG 1;
  - RELPAGES 2: `item` is 1242 pages against PG's 1284 at SF0.25 (hio.c
    bulk extension);
  - ESTIMATE+RELPAGES 1;
  - PARAM-APPEND 1;
  - STALE-SF1 28.
- **Rendering:** Q5 (Var deparse through an Append's SubqueryScan child)
  and Q6 (a PARAM_EXEC printed as `$0`, where PG prints the referent) route
  to M0146-0042.
- **Recorded unfiled (S4 lineage hold on M0146-0014; see its escalation):**
  - 0014d-F1, a correlated SubPlan conjunct applied unpriced above the
    join (Q32, Q92). This is a regression since M0146-0130.
  - 0014d-F2, a full Sort offered beside the Incremental Sort (Q60).
  - 0014d-F3, the index-only-scan heap-fetch term and int4 index density
    (Q24).
  - 0014d-F4, a Hash Semi Join over a pulled subquery rel (Q70).
  - 0014d-F5, a Parallel Append priced below its own child (Q76).

## Closing condition

M0146-0014 closes when a sweep finds every record routed **and** the filed
tasks are closed or owner-waived.
