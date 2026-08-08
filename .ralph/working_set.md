Task: M0129-S8.3 DONE — cmax stamping applied, S8.3 milestone closed

Files:
- internal/executor/operators_storage.go: cmax stamping added to:
  stampUpdaterXmaxNonHOT (both Multi and plain xmax paths),
  3 fallback PageSetHeapTupleXmax sites (UPDATE index-driven,
  UPDATE updateWithFrom, DELETE deleteOp), and tryApplyHOTUpdate
  (both PageStampHotOldTupleMulti and PageStampHotOldTuple paths)

Key symbols:
- stampUpdaterXmaxNonHOT, PageSetHeapTupleCmax, GetCurrentCommandId
- PageSetHeapTupleXmax, PageStampHotOldTuple, PageStampHotOldTupleMulti
- tryApplyHOTUpdate

Hypothesis/Findings:
- All cmax stamping sites now covered: the central wrapper
  (stampUpdaterXmaxNonHOT), the 3 direct-PageSetHeapTupleXmax fallbacks,
  and the HOT old-tuple path.
- The "second writeHeapRowReturning cmin stamp" referenced in the ledger
  appears to have been resolved by S8.3g — all heap-write paths
  (writeHeapRowReturning, buildCatalogPGHeapTuple, tryApplyHOTUpdate,
  toast.go) already have SetCmin.
- S8.3 is now fully complete.

Next step: Pick next M0129 task from fix_plan.md. M0129-S9
(reduce_outer_joins residuals) is the next unchecked item.

Gates run:
- go build ./...: PASS
- Full executor suite (serial): PASS (5.7-5.8s)
- CTE DML + HOT tests: PASS
- mvcc tests: PASS
- storage tests: PASS
- SPOT (Q12/Q13): PASS (2/35 rows, 29.7s)
- pgbench smoke: PASS (500/692/13820 tps, 0 failures)
- make ralph-state-guard: PASS (auto-repaired)

In-flight: none
