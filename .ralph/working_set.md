(idle — nothing in flight)

Last loop: **M0127-P5.9-h HALF LANDED** — the `rows=1` estimate collapse is
root-caused, fixed and re-measured. Do NOT re-derive:

1. **The bisect P5.9-h specified answered NEITHER branch.** The search's own
   sizing was always right (`addOneOrderedIndexPath` sets
   `Path.Rows = rel.Rows`; `makeJoinRel` sizes off that), and
   `joinsearchlevel.go:324-330`'s `rows < 1` clamp is NOT implicated. The 1 was
   minted AFTER the search by `EstimateRows` (`internal/planner/cardinality.go`),
   which returned **1 for every `*IndexScan`/`*IndexOnlyScan`** on the
   equality-probe convention — wrong for the bound-less full scan P5.4c-ii-b
   introduced. Fix = one arm: no `Key`/`Keys`/`LowKey`/`HighKey` ⇒
   `tableRows(Table)`.
2. **Measured**: `parity_violations` **6 → 0** on the five carrying queries
   (5,7,9,10,12); Q12's `orders` leaf `rows=1` → `rows=1500000` (exactly
   actual), its joinrel est 1 → 46 001 vs 31 354 actual.
3. **§3.5's headline is REFUTED.** Plan shapes and timings are byte-identical
   before and after (Q12 20.83→20.21 s, Q7 27.47→26.85 s). The clause 2/3
   timing gap is NOT the estimate collapse. What remains of -h is a COST
   question: the five still plan a Merge Join over a full ordered index scan of
   `orders` where the OFF arm plans Hash Join over Seq Scan.
4. Blast radius measured, not argued: OFF arm emits **zero** index scans on
   those five queries; DS05 `plans` = `queries=99 same=99 changed=0`. The ≤0.6 %
   `est=` drift on the OFF arm vs run 3 is `--warm-stats` ANALYZE sampling.

Files: `internal/planner/cardinality.go` (+`indexScanRows`),
`cardinality_fullindexscan_test.go` (new), `cardinality_propagation_test.go`
(its Memoize case asserted the old convention on the very shape that changed —
now built with a `Key`). Docs: 09 §3.6 + §4.1 amendment, bundle README,
IMPLEMENTATION-TODO P5.9-h, fix_plan P5.9-h, 2 ledger rows,
`analysis/leftdeep-joins/2026-08-05-p59h-audit-{on,off}.{txt,plans.txt}`.

Gates run: UNITS (green), `go test ./internal/planner/` (green),
`scripts/tpch-spotcheck.sh` PASS (Q12 rows=2, Q13 rows=35), both estimate-audit
arms on queries 5,7,9,10,12, DS05 `plans` capture, pgbench smoke via the commit
hook, `make ralph-state-guard` (repaired a stale progress marker).
**NOT run: the DS05 `sweep` (~1 h) and `make plan-diff`** — ledgered, discharge
at run 4.

Nightly triage 20260805-014309: unchanged run, both items already filed under
M-NIGHTLY, left unchecked per the banner.

Next step: **M0127-P5.9-i** (the 7 TPC-DS `assertSearchedTreeNeedsNoReconcile`
aborts) or the cost half of **-h**. -i is the larger correctness defect and
TPC-H structurally cannot reach it; -h's remainder is a pure cost A/B on Q12.
Run 4 of the bar comes after both, with the DS05 clause no longer optional.

In-flight: none.
