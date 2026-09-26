# Working set — loop 16 (2026-09-27)

Task: M0146-0005 slice 23 / M0146-0005v — PG18 btree skip scan
(index quals on non-leading columns, witness Q82). Implementation +
docs staged; awaiting fireset tail before commit.

Files (staged):
- internal/optimizer/pathparamindex.go — pickIndexSkipRun /
  indexSkipNullSafe / indexSkipClauses / skipScanDescents /
  addOneParameterizedSkipPath (emitted ALONGSIDE prefix candidate)
- internal/optimizer/costindex.go — numSAScans clamp + rint division +
  boundSelectivity (skip-revert bound set)
- internal/optimizer/createplanindex.go, createplannl.go — SkipPrefix
  validation + shifted clause positions; Keys-only contract
- internal/optimizer/path.go, plan.go — IndexSkipPrefix/SkipPrefix
- internal/optimizer/unnest.go — harvestKey SkipPrefix+i offset
- internal/executor/operators_index.go — rescanSkip + skipNextGroup
  (lazy cursor enumeration, tuple+blob formats); skip dispatch BEFORE
  SAOP
- internal/executor/operators_explain.go — skip Index Cond rendering
- tests: skip_index_test.go, skip_param_index_test.go (new);
  joinpathsnli_test.go, saop_index_test.go, partial_lateral_test.go
  (updated for new admission/arithmetic)
- analysis/m0146/m0146-0005/slice23/ (README, gates.txt, plans/diff/
  class, fireset/) — created but NOT staged yet

Key findings:
- Q82 elects NL -> Index Scan inventory_pkey (inv_item_sk probe);
  values verified vs PG oracle + positive 1570-row probe.
- Census delta (fireset): jointree-search 23->21, join-method 45->43,
  join-order 73->72; moved {Q16,Q37,Q72,Q82,Q94} all skip elections.
- INCIDENT fixed: refusing SkipPrefix in lateralProbeIsPartialProbe
  broke the path/node twin agreement -> gatherChildPlan panic on
  Q16/Q72. The skip probe IS a legal per-worker rescan (PG's own
  Gather>NL>IndexScan shape); pin added in partial_lateral_test.go.
- Q72 runtime 0s->168s on the new parity shape — report-only delta,
  PG elects the same probe chain.

Gates run: units PASS, tpch-spotcheck PASS (Q12=2/Q13=33),
tpcds-sf025 sweep PASS 96/0-err (second run), tpch-acceptance-arm
PASS 24/24, parity capture match=14, fireset running.

Next step: wait for tpcds-fireset-gate tail (SF0.25 candidate
executes in progress; SF1 corpus still queued), record verdict in
slice23 gates.txt, stage analysis+docs+fix_plan+ledger, commit +
push. Commit style: `optimizer(M0146-0005v): btree skip scan for
parameterized index probes` (check `git log -5` for exact style).

In-flight: `scripts/tpcds-fireset-gate.sh m0146-0005v
analysis/m0146/m0146-0005/slice23/fireset` — log
tmp/m0146-0005v-fireset.log. SF0.25 arm done (baseline 5/5, candidate
executes PASS so far); SF1 corpus pending. If killed, re-run the same
command (it resumes via FIRESET_RESUME=1 semantics / re-clones).
Private clone tmp/m0146-0005v-data-tpcds-sf025 kept, server stopped.

== CONTINUATION (fireset tail + work bounds, 2026-09-27) ==
SF1 first fireset FAIL: candidate Q72 TIMEOUT. Root cause: the skip
election's NL(cs-arm -> inv-skip) won by ~8% in goopg's model (probe
cost 109.84 under loopCountFor({cs})=1.44M amortization) but executes
~9.5k rescans x ~980ms (~2.6h). PG files the same path and its model
rejects it (~52k vs goopg's ~60k for the prefix-order subtree = ~8k
join-cost drift, join-order/costing family). Fix inside the slice:
two executor-capability admission bounds in pathparamindex.go —
maxSkipProbeRows=600 (per-execution probe rows; airtight across outer
relsets since the same fat probe escapes through any req) and
maxSkipProbeLifetimeRows=5e8 (rows x loopCount). Q72 SF1 re-elects
the d2-first order (plan byte-identical to baseline, ~5s); SF0.25's
527-row probe stays admitted (PG-matching election, 168s). New test:
TestParameterizedSkipPathWorkBounds; joinpathsnli fixture gained
table stats so its {supplier} skip path stays thin (12 rows).

Gates (final): units PASS, tpch-spotcheck PASS, sf025 sweep PASS
96/0-err, tpch-acceptance-arm PASS 24/24, FIRESET PASS at BOTH
scales — SF0.25 fires {16,37,72,82,94} 5/5+5/5, SF1 fires
{16,37,82,94} 4/4+4/4, introduced=none everywhere. SF1 census:
join-method 53->50, parameterisation 44->43, D3 29->26.

Next step: stage explicit paths (code+tests+slice23 analysis+design
doc+fix_plan+working_set), commit
`optimizer(M0146-0005v): btree skip scan for parameterized index
probes`, push. Leftover private clones: tmp/m0146-0005v-data-tpcds-sf1
(:5591) and -sf025 (:5590) — servers still running, stop before
deleting.
