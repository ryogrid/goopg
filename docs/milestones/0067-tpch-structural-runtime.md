# Milestone 0067 — TPC-H Structural Runtime Improvements

## Goal

Build on M0066 PIVOT's runtime allocation reductions with
structural changes that close Q5 / Q20 / Q21. Target: 22 / 22
OK at SF=1 with `cancel-after=1200s`.

## Context

M0066 PIVOT (commit `55432e2`) eliminated 99.23 % of Q5
allocations via MHJ `SetBorrow`, removed `time.Parse` from hot
loops, and disabled proactive GC. CPU samples on Q5's 60 s
pprof window dropped 67 %. But Q5 still cancels at 600 s
because `runtime.duffcopy` / `memclr` / `memmove` together
consume ~60 % of CPU — the row-at-a-time materialization cost.

## Sub-tasks

- **M0067-0001 Milestone doc + fix_plan.** This file +
  `.ralph/fix_plan.md` updates.
- **M0067-0002 1200s baseline sweep.** Establish a new
  baseline at the larger budget before structural work.
- **M0067-0003 Q9 composite-NLI.** Diagnose why Q9 NLI #2
  picks `partsupp_supplier_fkidx` (single-key) instead of
  `partsupp_pk` (composite). Fix so Q9 is correct (was
  silent FALSE NEGATIVES per M0064 NLI walker bisect).
- **M0067-0004 Q21 NLI walker re-attempt.** Re-add the
  `reresolveNLIByName` infrastructure once Q9's composite-
  NLI absorbs the cardinality.
- **M0067-0005 Projection narrowing (stretch).** Add a
  `pruneUnusedColumnsInMHJ` pass to inject a `Project`
  selecting only needed columns between MHJ and parent.
  Hard cap 3 h; defer to M0068 if exceeded.
- **M0067-0006 Final 22-query SF=1 sweep + report.**

## Acceptance

- **Hard**: 22-query OK count ≥ 19 (no regression). Row-count
  parity for previously-OK queries (Q9's row count may
  legitimately CHANGE if composite-NLI exposes the canonical;
  documented).
- **Soft**: 20–22 / 22 OK at `cancel-after=1200s`.

## Out of scope

- Datum struct shrink (architectural; M0068).
- Columnar storage (M0069+).
- Re-baselining at scale factors > 1.
