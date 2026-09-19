Task: M0143-0010 — index probes must resolve the heap update chain (raw
  PageGetHeapTuple on the index ItemPointer reads a dead HOT-chain root).
  Filed + fixed + documented this loop; pending gates/commit at time of
  writing.
Files:
  internal/executor/operators_index.go — new eachHeapChainMember iterator
    (redirect stubs, ItemIDNormal members in chain order, IsHotUpdated/CTID
    links, MaxHeapTuplesPerPage bound).
  internal/executor/operators_fk.go — scanIndexForFKMatch walks the chain
    with TupleVisibleSubxact per member (was raw ptr read — the
    a53c5b807/M0142-0003g regression behind the 3 nightly spec failures).
  internal/executor/operators_storage.go — uniqueCheckWithWait.scanOnce +
    exclusionCheckOnce walk the chain (scanOnce kept its in-flight-xmin
    wait + isLiveForUniqueCheck arms; conflictPtr now = member slot).
  internal/executor/operators_upsert.go — findInProgressConflictKey +
    probeSpeculativeConflict walk the chain (Case1/2/3 and self-skip +
    isLiveForUniqueCheck per member; decode errors still propagate via
    memberErr closure).
  internal/executor/deferred_exclusion.go — recheckDeferredExclusionEq
    counts a chain ONCE if any member is live (per-member counting
    double-counts an in-flight update → false 23P01).
  internal/executor/operators_fk_test.go — TestFKInsertAfterParentHotUpdate.
  internal/executor/insert_unique_constraint_test.go —
    TestUniqueInsertAfterHotUpdate (duplicate PK after HOT update → 23505;
    was a live-verified bypass on scratch :5533 before the fix).
  .ralph/fix_plan.md — M0143-0010 filed/[x] (Movement: none); the three
    M-NIGHTLY items AI-20260917-004357-008/-009/-012 ticked RESOLVED.
  docs/design/0100-0149/m0143-0010-index-probe-hot-chain-resolution.md +
    docs/design/README.md index row.
Key symbols: eachHeapChainMember, scanIndexForFKMatch, uniqueCheckWithWait,
  findInProgressConflictKey, probeSpeculativeConflict, exclusionCheckOnce,
  recheckDeferredExclusionEq; precedents followHOTChainNoCopy /
  resolveDeferredUniqueChainTail.
Hypothesis/Findings: CONFIRMED — every index-driven heap probe that judged
  "is there a live row at this key" by reading the index ptr's slot
  verbatim was broken after any committed HOT update; FK was the newest
  instance (regression), uniqueCheckWithWait the worst (dup PK possible,
  demonstrated live). Exact-ctid refetches (EPQ/rowmark/catalog oldTID)
  deliberately do NOT walk — audited and left alone.
Next step: gates (units + tpch-spotcheck + tpcds-sf025 + acceptance-arm)
  on the staged index, then commit + push.
Gates run: executor pkg suite PASS (13.6s); TestFKInsertAfterParentHotUpdate
  + TestUniqueInsertAfterHotUpdate PASS (both verified red pre-fix);
  upsert/exclusion/deferred set 24/24 PASS; isolation specs
  fk-contention/fk-deadlock/update-locked-tuple all PASS.
In-flight: none (scratch fk-repro.scope on :5533 stopped).
