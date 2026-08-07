Task: M-NIGHTLY — regress/truncate nondeterministic FK-dependency ordering
  (AI-20260807-004620-001). FIXED.

Files:
  - internal/executor/operators_ddl.go: `truncateTableEntry` (package-level type),
    `sortedTruncateTableSet` (deterministic sort helper), moved `nameOrder`
    declaration to function scope, replaced ALL `range tableSet` iterations
    (FK check, CASCADE expansion, validation, locks, triggers, truncation,
    stats, sequences) with `sortedTruncateTableSet(tableSet, nameOrder)`.

Key symbols: truncateTableEntry, sortedTruncateTableSet, execTruncate

Hypothesis/Findings:
  - Root cause: `for _, entry := range tableSet` iterated over a Go map;
    when multiple tables in the set each had FK references from outside
    tables, which error fired first depended on map iteration order.
  - Fix: sort entries in statement order (s.Names position), then
    CASCADE-added tables by name — matches PG's range-table-order behavior.
  - Also fixed the same nondeterminism in CASCADE partition expansion,
    validation, lock acquisition, triggers, truncation, stats, and sequence
    restart loops — all now deterministic.

Next step: Decide between (a) merge m018 to master, (b) next M-NIGHTLY item
  (regress diff capture or suite-wedge), or (c) close remaining M0125 absorbed
  items and move to M0123.

Gates run: truncate regress 20/20 PASS (10+10), executor unit tests PASS,
  ralph-state-guard PASS (auto-repaired), build OK.

In-flight: none
