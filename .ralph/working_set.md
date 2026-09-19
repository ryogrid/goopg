# Working Set (Loop #28)

Task: M0141-S2b-15 — residual bookkeeping after `caf858301` landed the
  `overrideRows` code on HOLD release: fresh ea-ratchet + triage + repin + tick.
Files:
- analysis/planner-refactor-take3/c20a-estimator-census-20260915/ea-baseline.txt
  — repinned 76→54
- .ralph/fix_plan.md — S2b-15 [x] + triage block; new task M0141-S2b-17 filed
- .ralph/deferral_ledger.md — row appended for Q94 semi-join divergence
- docs/design/0100-0149/m0141-s2b-15-gather-rows-stamp.md — Status → landed
- docs/design/README.md — s2b-15 row status → landed
Key symbols: scripts/estimate-parity-gate.sh (EA_CAPTURE rescore mode,
  EA_REPIN=1), parity.py, makeGatherPath(overrideRows), splitAggregate,
  NewGather, PathFinalizeAgg.Rows=finalGroups
Hypothesis/Findings: 8 NEW findings on HEAD — 5 are relset-key churn /
  shared PG-formula errors (Q40 goopg 17 vs PG 19 on identical relset;
  Q71 229 vs PG ~181; Q85 `reason`-key swap), all verified vs
  :65438/tpcds025 + plans-pg. 3 real divergences exposed by post-S2b-15
  shape-admission commits (9a2b9d47b NLI+Memoize Gather-driver /
  M0140-0006c): Q62/Q99 Finalize+Partial HashAggregate + Gather all
  render rows=1 (PlanCost never stamped by splitAggregate/NewGather —
  display gap, sibling of S2b-16's scope; ledger row already covers it)
  and Q94 Hash Semi Join 90 vs PG 1 (actual 4) — genuine selectivity
  divergence, filed under M0141-S2b-17 + new ledger row.
Next step: none for this slice — S2b-15 fully closed. Next loop picks per
  banner: open S2b children remain (S2b-3b window wiring, S2b-4 SETOP
  rel-identity, S2b-16 partial-path rows= display, S2b-17 recon) which
  gate M0141-S7; otherwise item 8 → M0119-0006 residual ledger drain /
  M0122+.
Gates run: make ea-ratchet fresh capture on HEAD (~10min) — FAIL 8 NEW
  expected → triage → EA_REPIN re-score → EA-RATCHET PASS 54/54.
  pg_isready :65438 OK. No production-code changes this loop.
In-flight: none
