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

## Closing condition

M0146-0014 closes when a sweep finds every record routed **and** the filed
tasks are closed or owner-waived.
