# Working set — loop 20 (2026-09-27), CLOSED

Tasks landed this loop (two commits, both gates PASS-stamped against
their staged index):

1. `dbf9422e0` — executor(M0146-0015f): bpchar blank-insensitive
   equality + hash parity. The padding-correct SF0.25 reload had put
   the sweep at 70/26-red: scalar `=` ran byte-exact against
   bpchareq's bcTruelen semantics, and every hash/dedup key path
   hashed the padded image. `comparisonOperandsAsBpchar` +
   `trimStringDatum` now apply declared-type-driven trailing-blank
   normalisation at every comparison boundary (scalar ops, IN/CASE/
   IS-DISTINCT, compiled exprnode twin via payload-serialised
   typmods + bare-literal flags) and every key boundary
   (evalSortKeyValue sort/merge keys, hash-join buildKeyTrim/
   probeKeyTrim + batch routing, merge-join stream, aggregateOp
   gkTrims incl. COUNT(DISTINCT), distinctOp/distinctOnOp, SetOp +
   recursive-CTE rowKeyTrimmed, window partition/order/peer keys,
   subPlanRowHash multi-col IN, corrSubqHashMap inner+outer — the
   ExecParamRef arm is what Q41 needed — and ANALYZE frequency
   bucketing). Stored/output values keep padding.
   Gates: units PASS; tpch-spotcheck Q12=2/Q13=33; acceptance arm
   24/24 MATCH; sf025 sweep PASS=96 MISMATCH=0 CKMISMATCH=0
   (sweep-20260927-130805.txt), plans same=99 changed=0.
   Design docs/design/0100-0149/m0146-0015f-bpchar-hash-semantics-parity.md;
   evidence analysis/m0146/m0146-0015f/.
   Ledgered: op ANY(array-datum) element typing.
2. `cf7c5397b` — executor(M0146-0015e): explicit default-opclass
   index restart durability (banner item 2a, S2 wrong-rows).
   createBTreeIndex normalises a default-equivalent explicit opclass
   to "" so live CREATE and indclass reload produce the same catalog
   entry — the key-format decision no longer flips across restart.
   Regression:
   TestExplicitDefaultOpclassIndexSurvivesCheckpointedRestart
   (initdb, count(*)=49 before AND after restart).
   Gates: units PASS; tpch-spotcheck PASS; acceptance arm 24 MATCH;
   sf025 sweep PASS=96/0/0 (sweep-20260927-132708.txt), plans
   same=99. Ledgered: pre-fix blob-format images need REINDEX (no
   on-disk format marker).
   Design docs/design/0100-0149/m0146-0015e-explicit-default-opclass-restart.md;
   evidence analysis/m0146/m0146-0015e/.

Notes for next loop:
- The two commits were ordered bpchar-first because the 0015e code
  tree alone could not stamp a green sf025 (the bpchar divergence
  was pre-existing at HEAD); the staged-set dance was stash(0015e
  code) -> gate -> commit 0015f -> pop -> re-gate -> commit 0015e.
- Probe server on :5533 (tmp/bpchar2-data) left running under scope
  bpchar2-probe — stop it (`./tmp/goopg-probe-bin stop -D
  tmp/bpchar2-data`) or reuse.
- Parity capture for the 0015f commit body:
  tmp/m0146-0015f-parity/ (CATEGORIES-EXCL-MATCH unchanged — the
  change is executor-semantics only, plan channel same=99).
- Lineage-guard advisory seen twice (non-blocking):
  M0145-0008y `[!]` cites blocker M0146-0012 which is now `[ ]` —
  re-check whether the hold should re-open or cite STALE-OK.

Next loop: fix_plan banner order. Residual candidates surfaced:
the ledgered `ANY(array-datum)` bpchar element typing, the
non-aggregate `subquery_push_qual` arm (ledgered under 0005x),
M0146-0010 Materialize, the sorted-input `Partial GroupAggregate`
arm (0016 residue), and the filed testport SSI divergence.
