M0128-P5.1 COMPLETE — EXPLAIN range-table name dedup landed

Task: M0128-P5.1 — EXPLAIN node-label disambiguation for repeated relation
  names (PG select_rtable_names_for_explain, ruleutils.c)

Files:
  - internal/executor/explain_names.go: added nodeLabels map + nodePtr helper;
    collect now runs a second pass after register to assign per-node
    disambiguated labels (_1, _2, …) for node labels independently of
    SourceTableIdx-based column qualification; added disambiguatedName method
  - internal/executor/operators_explain.go: describePlan/describePlanVerbose
    now accept *explainNames parameter; SeqScan/IndexScan/IndexOnlyScan labels
    use disambiguatedName when available; all call sites updated
  - internal/executor/explain_qualify_test.go: added
    TestExplainNodeLabelDisambiguatesRepeatedTable (SEMI-join self-scan)
  - .ralph/fix_plan.md: M0128-P5.1 checked off
  - .ralph/deferral_ledger.md: clause-6 re-adjudication row for the five
    queries (now adjudicable but not yet measured)

Key symbols: explainNames.nodeLabels, nodePtr, disambiguatedName,
  describePlanVerbose, describePlan

Hypothesis/Findings:
  - The existing explainNames.register() uses seen[src] to guard against
    double-registration (same subtree, cross-level SourceTableIdx collision).
    But two DISTINCT plan nodes can share a SourceTableIdx (e.g. SEMI-join
    outer and inner sides over the same relation). The bySource column-
    qualification table correctly keeps the first, but node LABELS need
    per-node disambiguation — hence the separate nodeLabels map.
  - The fix is a parallel pass in collect: iterate found entries in order,
    count base names across ALL nodes, append _N suffix to non-first
    occurrences. nodeLabels is keyed by fmt.Sprintf("%p", n) — stable within
    a process lifetime.
  - Zero row/checksum deltas in DS05: the change affects only EXPLAIN text,
    never execution.
  - The existing column-qualification system (bySource/taken/seen/cols) is
    untouched — this is purely an addition.

Next step: M0128-P5.2 (Rows Removed by Filter / by Join Filter) or next
  M0128 task per fix_plan.md ordering (P4.1→P5.1→P5.2)

Gates run: UNITS PASS, SPOT PASS (Q12=2/Q13=35), DS05 PASS (95/99, zero
  row/checksum deltas, 45 plan text changes from P4.1 not from this change)

In-flight: none
