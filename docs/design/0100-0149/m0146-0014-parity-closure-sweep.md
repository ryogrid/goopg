# M0146-0014 — parity-closure sweep

Status: open (the milestone's exit report). Sweep M0146-0014a done
2026-10-09.

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

## Closing condition

M0146-0014 closes when a sweep finds every record routed **and** the filed
tasks are closed or owner-waived.
