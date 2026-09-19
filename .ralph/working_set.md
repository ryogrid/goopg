# Working Set (Loop #27)

Task: M0119-0006 (br) — wire amcheck `heapallindexed` tier: HeapEntryFormer adapter
Files:
- internal/executor/operators_bt_index_check.go — new btIndexHeapAllIndexed +
  heapChainRootOffset; wired into evalBtIndexCheck after unique tier; import sort
- internal/executor/operators_bt_index_check_test.go — 6 HeapAllIndexed tests
- docs/design/0100-0149/0119-0006br-heapallindexed-former-wiring.md + README row
- .ralph/fix_plan.md — progress bullets under M0119-0006
Key symbols: btIndexHeapAllIndexed, heapChainRootOffset, eachHeapChainMember,
  indexBuildEntryKey, TupleVisibleSubxact, amcheck.CollectHeapIndexEntries /
  VerifyBtreeHeapAllIndexedRelation
Hypothesis/Findings: heapallindexed was the last accepted-but-never-ran arg.
  Former mirrors collectBTreeEntries recipe; HOT members emit chain-ROOT tid
  (index entry lives there); snapshot = ctx.Snap via TupleVisibleSubxact (NOT
  isLiveForUniqueCheck — that probes in-flight xmin, a false-positive window).
  Heap pages retained per-block in PageSource wrapper so root lookup reads the
  same bytes the engine scanned.
Next step: none for this slice — task done; next loop picks next banner item
  (M0119 milestone remains open — rootdescend tier, 003/004 remaining arms are
  feature-blocked: non-btree AMs, TOAST layout, per-db catalogs).
Gates run: executor pkg + amcheck pkg + all TestPort_PgAmcheck* PASS; 6 new
  tests PASS; units PASS; tpch-spotcheck PASS (Q12=2 Q13=34); live
  pg_amcheck --heapallindexed[+--rootdescend] exit 0 (5000 rows, 3000 HOT
  members, partial+expr idx). sf025 sweep runs post-commit for clean stamp.
In-flight: none
