Task: M0129-S5.6 COMPLETE — parallel bitmap heap scan. M0129 milestone COMPLETE (all subtasks [x]; S5.7/S5.8 closed blocked-with-reason).

Files:
- internal/planner/parallel.go: drivingSeqScan→drivingScan (supports BitmapHeapScan);
  scanTable helper; BitmapHeapScan added to subtreeHasUnsafeNode, parallelChildren;
  HasBitmapScan for executor pre-build gating.
- internal/executor/parallel_bitmap_scan.go: new — parallelBitmapState
  (shared atomic page allocator + sorted block list from TIDBitmap).
- internal/executor/operators_bitmap.go: pbm field on bitmapHeapScanOp;
  nextSerial/nextParallel split; fetchOneTuple inline (avoids recursive Next()
  across the shared-allocator boundary); ownBitmap flag.
- internal/executor/parallel_scan.go: attachParallelBitmapScan (walks
  filterOp/projectOp/instrumentedOp/joinOp/aggregateOp/sortOp→bitmapHeapScanOp).
- internal/executor/operators_gather.go: pbm field on gatherOp;
  prebuildBitmapScan (leader builds bitmap once before fan-out);
  collectBitmapScans; wired in Open(), runWorker, leader child.
- internal/executor/parallel_bitmap_scan_test.go: 6 unit tests for
  parallelBitmapState (empty, single, multiple, lossy, nil-safety, idempotent).
- .ralph/deferral_ledger.md: S5.7/S5.8 blocked-with-reason rows appended.
- .ralph/fix_plan.md: S5.6 DONE, S5.7/S5.8 CLOSED blocked, S5 parent [x].

Key symbols: parallelBitmapState, drivingScan, HasBitmapScan, scanTable,
nextParallel, fetchOneTuple, attachParallelBitmapScan, prebuildBitmapScan,
collectBitmapScans

Hypothesis/Findings:
- PG's ParallelBitmapHeapState uses DSA (dynamic shared memory) for the
  shared page allocator. goopg shares a Go pointer — the state reduces to
  a sorted block list + an atomic.Int64 counter.
- The leader builds the TIDBitmap once before fan-out (prebuildBitmapScan),
  publishes the sorted blocks in parallelBitmapState, and workers claim
  disjoint pages via nextPage().
- Unlike the serial path (fetchExact calls o.Next() recursively), the
  parallel path inlines the per-tuple fetch (fetchOneTuple) so that an
  invisible tuple advances to the next offset on the SAME page rather than
  accidentally claiming a new page from the shared allocator.
- planner changes: drivingScan generalizes drivingSeqScan to also recognize
  BitmapHeapScan; computeParallelWorkers uses scanTable() to extract the
  *catalog.Table from either scan type. HasBitmapScan gates the executor
  pre-build so we don't build a throwaway tree unnecessarily.

Next step: M-NIGHTLY items (49 new regressions from 20260809-020705). All
M0129 items are [x]; the Current Priority banner should be updated or M-NIGHTLY
selection resumes.

Gates run:
- UNITS: PASS (SCOPE=units precommit)
- SPOT: PASS (Q12=2, Q13=35, 28.1s)
- pgbench smoke: PASS (13,974 TPS, 0 failures)
- RACE: PASS (all packages green)
- ralph-state-guard: REPAIRED+OK

In-flight: none
