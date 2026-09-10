# R47 slice 2 — measurement report (2026-09-10)

Slice-2 implementation: `SLICE2.md` (reviewed APPROVE-WITH-NOTES, notes
applied). Code: `internal/optimizer/upperorderedgrouping.go` (new) +
`planner.go` normal ORDER BY arm (loop-first, fall through on decline).
Tests: 6 new pins in `upperordered_test.go`, green.

Corpora (pinned `work_mem='64MB'`, `max_parallel_workers_per_gather=4`):
- TPC-H: `/tmp/pp2/oc-tpch-s2.txt` vs slice-1 `oc-tpch-s1.txt`
  (byte-identical to `oc-tpch-r46c.txt`, 0 diff lines — slice-1 verified
  no-op on both corpora before this measurement).
- TPC-DS (SF0.5 clone `/tmp/pp2/ds05oc`): `/tmp/pp2/oc-ds05-s2.txt`
  vs `oc-ds05-s1.txt` (≡ r46c modulo capture-pid noise).

## Outcome: 16 flips, all toward PG, ZERO EXTRA

Every flip is `Sort → HashAggregate` becoming `GroupAggregate → Sort(input)`
— the no-sort sorted election the slice was built to measure. PG 18.3 oracle
uses (Finalize) `GroupAggregate`/`Group` at every one of these 16 stations.

TPC-H (2): Q7, Q8. TPC-DS (14): Q7, Q10, Q15, Q17, Q25, Q26, Q29, Q37, Q40,
Q45, Q50, Q66, Q69, Q82. (Q37/Q82: PG uses `Group`, the sort-ordered
grouping without aggregates — goopg has no `Group` node; `GroupAggregate`
is the toward-PG strategy pick. Q40/Q45: PG uses Finalize GroupAggregate;
goopg elects serial GroupAggregate over a parallel join — strategy matches,
parallel shape does not.)

Q4 — the nominal target — did NOT flip, and the census says why (below).
Its shape was already PG's (`Sort → HashAggregate`).

Cost-only moves, shapes byte-identical: TPC-H Q4, Q10, Q13, Q16, Q21, Q22
(top Sort line); TPC-DS Q3, Q19, Q33, Q35, Q42, Q52, Q54, Q55, Q56, Q60,
Q71, Q72, Q76, Q85, Q91, Q93 (Sort/Limit lines, ±rounding to +1.1% on Q76).
No other section moves in either corpus.

## §3.3 census (GOOPG_PGSHAPED_DP_TRACE=1, s2 binary)

- Q4: loop RAN. Grouping survivors hashed (rows=5, 1426.65/1426.71) +
  translated sorted (5649.63/6077.69, pathkeys=1). Ordered: hashed+Sort
  1426.78 beats no-sort sorted 6077.69 — dominance, not a tie. No flip,
  correctly: goopg's serial input-sort over 57066 estimated rows (~4650)
  dwarfs hashed (1426). The TDD pin's near-tie numbers (70122/69911) are
  PG-oracle-scale costs (PG Q4 `Finalize GroupAggregate 70094.27..70122.64`,
  parallel, at PG's row estimates: semi-join out 3439 vs goopg's 57066);
  production goopg-scale is 6077/1426. Pin/production scale mismatch
  recorded — the pin stays valid as a unit election probe, not as a
  production prediction.
- Q7: gathered-hashed+Sort vs gathered-sorted-as-is tie at total 363546.82
  to the penny; startup tie-break (363326.40 < 363524.78) elects no-sort.
  The pin's predicted mechanism, playing out at production scale.
- Q8: same pattern (tie at 162836.43; startup 162836.40 < 162836.43).
- Split (PathFinalizeAgg) was dominated-and-pruned at grouping before the
  loop in Q7/Q8 (gathered-hashed strictly cheaper on both startup and
  total; `addToPathlist` `removeOld` verified in `path.go:974+`). The
  Finalize gate therefore never fired on a live rel — its correctness
  concern (electing what grouping lost to Finalize) is moot where the
  split already lost to a surviving PathAgg rival. No rel-level Finalize
  presence was observed at loop time in any traced query.
- Memoize census Q4 before/after: 0 / 0.

## Surfaced skew (pre-existing, not slice-2-created): node vs path agg cost

`costAgg` charges hash-computation per input row for hashed (`groupCmpPerTuple
× tuples`: Q4 +142.68 = 57066×0.0025); `createAggPlan`'s legacy display cost
omits it (Q4 node 1284.03 vs path 1426.71). Slice-1 never built Sorts from
paths, so the skew was latent; the loop builds loop-elected Sorts from path
costs while children keep node costs, moving 6+16 Sort/Limit EXPLAIN lines.
Elections are unaffected (all loop comparisons are path-vs-path, PG-faithful
basis — PG's `cost_agg` charges per-row hashing for AGG_HASHED). The
presentational unification (`createAggPlan` display ≡ `costAgg`) is a
separate R-item (filed: TODO R47 follow-up); slice-2 deliberately does not
touch it.

## Gates (SLICE2.md §4)

- Units + pre-commit suite (`RALPH_PRECOMMIT_SCOPE=units`): green, no FAIL.
- TPC-H spotcheck (tree-built binary): Q12 rows=2, Q13 rows=34 — PASS.
- SF0.5 sweep: `PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=4`
  (skips oracle-side); its plan-diff channel lists exactly the 30 queries
  this report adjudicates (14 flips + 16 cost-only).
- `match` count not a criterion (conjunction rule); judged by category
  movement (16× aggregation-strategy toward PG) + zero EXTRA: held.
- Q15a capture footnote: `/tmp/parity-r0/q15a.sql` was missing from shared
  scratch at capture time (present for the r46c baseline); reconstructed
  from `queries/tpch/Q15.sql`'s view body in `to_date` form, re-captured on
  s2 byte-identical to baseline (no ORDER BY → loop correctly never runs).
- No estimate re-baseline: Q4's shape did not move (archival r0-r8 snapshots
  are records, not live gates).

## Firing-rule status

The R47 firing micro-rule remains UNIDENTIFIED (rev3 honesty preserved):
slice-2 is measurement infrastructure that recovers PG's strategy at 16
stations through cost-fair adjudication, not a pick rule. The Q4 gap is now
localized to row estimates below the agg (semi-join out 57066 vs PG 3439),
not to ordering machinery.
