# R21 (index-leaf) — DECLINED: no witness (census-measured)

*Round close-out, 2026-09-09. Status: implementation WRITTEN, unit-pinned,
A/B-measured, then REVERTED — nothing landed. This is a verified
out-of-scope verdict, which the goal permits, not an unfinished round.*

## 0. Verdict in one paragraph

The R21 charge (qual count for prebuilt index leaves in
`baseSeqScanCostInputs`) is unit-correct and production-dead: an
instrumented census over BOTH corpora counts **1282 leaf pricings, zero
of them index leaves** (TPC-H: 100/100 `*SeqScan`; TPC-DS SF0.5:
1107 `*SeqScan`, 51 `*CTEScan`, 17 `*Project`, 7 `*SetOp`). A
pre/post-R21 A/B of full TPC-H captures is **byte-identical (22/22,
costs included)**. Landing an unconditional cost change no production
path executes would be maintenance surface for zero effect, so the
change was reverted in full (working tree clean at HEAD
`0d67ced34`). The K6 hole is therefore NOT where the TODO row put it —
§4 re-files what the census actually showed.

## 1. What was built and measured

- Implementation: `baseSeqScanCostInputs` returns the local conjunct
  count (from the same `localFilter` the SeqScan arm counts) for
  `*IndexScan`/`*IndexOnlyScan`/`*BitmapHeapScan` leaves, fallback
  pages/rows untouched. Unit test
  `TestBaseSeqScanCostInputsIndexLeafCountsQuals` pinned: count,
  fallback preservation, filter-less zero, table-less zero — PASSED,
  then reverted with the change.
- A/B: HEAD-with-R21 vs HEAD-without (stashed), same private clone
  (`/tmp/pp2/tpch`), verified binaries (`launch-verified.sh`),
  GUCs pinned — **zero byte changes across 22 queries**.
- Census: temporary `Printf` in `baseSeqScanCostInputs` (leaf class +
  `localFilter` presence), probe binary, all 22 TPC-H EXPLAINs + all 99
  TPC-DS EXPLAINs, GUCs pinned — 100 + 1182 lines, result above.
  Probe reverted; no instrument remains in the tree.

## 2. Why the arm never fires (measured + read, not assumed)

Prebuilt leaves reaching `buildInitialRels` are bare `*SeqScan`
nodes (or CTE/Project/SetOp wrappers). Rule-based index choices do not
arrive with Filter chains:

- `rewriteScanInputsWithSingleTablePredicates` (`planner.go:1557`)
  turns SeqScan→IndexScan by ABSORBING conjuncts into probe bounds and
  splicing out emptied Filters (C-02c) — read, not inferred;
- single-table statements divert at `isSimpleSingle` and never reach
  the search at all.

So there is no Filter chain for either R1's `localQualOpCount` or
R21's `localFilter` count to read on an index leaf — consistently, the
Q12 displayed index cost (`0.00..60475.14`) is stable across R0, R1
and R21 for the same reason. Both rivals price zero quals there;
currency holds, at zero.

## 3. What this means for the TODO row and DESIGN §5

- The row's premise ("the winning scans are prebuilt index leaves
  priced at 0 quals") is falsified as stated: the winning scans'
  prebuilt leaves are SEQSCAN leaves, priced WITH full qual counts
  already. K6's observation (displayed cost stable, join above rose)
  stands; its attribution to an uncharged index leaf does not survive
  the census.
- DESIGN §5 suspect #1 (index-qual *operator* cost) is unaffected and
  becomes the sharper hypothesis: what is uncharged anywhere is PG's
  `index_qual_cost` for ABSORBED bounds (probe keys/SAOP/range bounds
  that never appear as Filter conjuncts). That needs its own design —
  counting Filter chains cannot reach it by construction — and is
  filed as follow-up, not attempted here.

## 4. Gates

- Suites: optimizer green at HEAD (post-revert); executor untouched
  (no production change remains).
- Values: vacuous — nothing landed, so no values risk exists. Stated
  explicitly rather than left implicit.
- Parity: the A/B IS the parity evidence (22/22 byte-identical).
- Timing: not taken — nothing changed, so there is nothing to time.
  (Taking one would manufacture evidence of diligence, not signal.)

## 5. Review record

Subagent delegation unavailable (Task cancelled at R0; TODO.md log).
Review as adversarial second pass by the author:

- **Falsification check passed and reported against interest:** the
  design predicted Q12's inner Index Scan cost would move; it did not,
  and instead of re-reading the prediction as success the round
  instrumented the premise and killed it. The census (1282 pricings)
  is the evidence; the A/B (22/22 identical) is the corroboration.
- **K4 compliance:** the absorbed-leaf mechanism was READ
  (`scan_input_rewrite.go`, C-02c note, `planner.go:1557` ordering),
  not inferred from the zero-movement alone.
- **K22 guard:** this report states its limitation — the census covers
  the two corpora at HEAD; a future rewrite that starts emitting
  Filter-wrapped index leaves would create the witness this round
  lacked. Resume condition written down (§3 follow-up), headline
  ("declined") scoped to it.
