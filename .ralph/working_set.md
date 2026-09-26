# Working set — loop 18 (2026-09-27), CLOSED

Task: M0146-0016 — presorted split admits non-column group keys
(TPC-DS Q62/Q99 witnesses).

LANDED + PUSHED: `2d66bc4d0` on plan-parity-with-pg-take2-ralph2; all gates
PASS-stamped against staged code_tree
f78f78f5520997e75725f2228f99df1d5ef4775ea98b31f832b7712ae2209b25
(units, tpch-spotcheck Q12=2/Q13=33, sf025 sweep 96 PASS/0 err,
acceptance arm 24/24 MATCH, fireset `introduced=none` both scales,
fires={Q62,Q99}).

Final state:
- `transportGroupSortKeys` (groupclause.go) widened: non-column group
  exprs get a positional `*ColumnRef` {Index:k.Pos, Name/Type/
  SourceTableIdx from `agg.Output()[k.Pos]`}; bare-column path unchanged;
  fail-closed on out-of-range/short-schema/unnamed slot.
- Render was already covered: `sortGroupKeySource` (R66 Arm S) resolves
  position→GroupExprs → `Sort Key: (substr(...))`. No executor change.
- Tests: `TestUpperSplitSortedTransportArmExprKey` (admission +
  positional pathkeys); refusal test re-pinned as the no-schema decline;
  `TestSortKeyTransportExprKeyRendersSource`; `TestPartialEmitSortedIdentity`
  +sorted-gathermerge-exprkey.
- Measured consumers (M0146-0001 PG captures): Q62/Q99 both scales.
  Q76 was already admissible (column keys) — unchanged. Q23 is NOT a
  consumer (PG itself hashes it).
- Residue ledgered: PG's sorted-input `Partial GroupAggregate` arm
  (sorts input per worker, no output Sort) is still unfiled — Q62/Q99
  stay SHAPE-DIFF on `sort-strategy`.
- Files: internal/optimizer/groupclause.go, partialaggupper.go(+test),
  internal/executor/explain_alias_source_keys_test.go,
  parallel_agg_transport_test.go; docs design + index + analysis dir.
- Scratch: tmp/fireset-m0146-0016 (copied to analysis/), tmp/arm-on-
  m0146-0016.txt, tmp/m0146-0016-server.log; :5533 server STOPPED.

Next loop: fix_plan banner order — M0146-0005 umbrella remains `[ ]`;
its child family (a–w) is exhausted, so either file the next child from
the first-divergence census residuals, or take the next unchecked M0146
entry in file order (M0146-0006 Incremental Sort election is sequenced
after 0005 per the milestone table; banner S7 ordering applies).
Adjacents surfaced: `subquery_push_qual` (Q34/Q73), M0146-0010
Materialize, and the sorted-input `Partial GroupAggregate` arm
(this slice's residue).
