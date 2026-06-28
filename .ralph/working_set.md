(idle — nothing in flight)

Last landed (loop #3): M0118-0002 `predicate-gist` PROMOTED — GiST page-level
predicate locking via grid-cell SIREAD (design 0118-0137). Engine change. Strict
`TestPort_IsolationPredicateGist` PASS (36 perms byte-identical to PG 18.3).
Isolation tally now 119 pass / 2 failed.

What/why: goopg has NO native GiST AM (a `USING gist` index is catalog-only →
spatial `p <</>>  point(k,k)` queries fall back to a seq scan), so the seq scan's
relation-grain SIREAD over-aborted all 18 disjoint-region permutations. Fix
emulates GiST leaf-page granularity with a synthetic grid: `ssiGistGridCell` =
FNV-1a of `(floor(x/256),floor(y/256))`. A SERIALIZABLE seq scan of a
GiST-indexed table takes a per-matching-tuple grid-cell SIREAD on the INDEX
(`ssiRecordGistGridRead`) INSTEAD of the relation lock (suppressed at Open);
INSERT conflicts-in on its point's cell (`ssiRecordGistIndexInsert`). Heap
per-tuple SIREAD skipped (heap-page coarsening → false positives); invisible-tuple
conflict-out gated by spatial match (`gistTupleMatches`). `Filter`-over-`SeqScan`
predicate threaded in BOTH build paths (`Build` + live `buildRec`). All gated
behind `gistSSIIdxOID != 0` (0 for every non-gist scan) → bounded blast radius.

Files: internal/executor/ssi.go (helpers), operators_storage.go (seqScanOp +
Next/Open + insert hooks), executor.go (build-path threading + unwrapSeqScanOp),
internal/testport/isolation_port_test.go (test), docs/design/0118-0137-*.md +
README, CSV/coverage regen, fix_plan.md.

REMAINING failed isolation specs (2, both Effort-L):
- predicate-gin (M0118-0002): needs int4[]-column array typing (array[1]→int4[]
  collapses to int4 today) + a real GIN AM. The grid-cell SSI primitive
  (ssiGistGridCell / ssiRecordGistGridRead / ssiRecordGistIndexInsert) is
  reusable once a GIN scan path + array typing exist.
- deadlock-parallel (M0118-0004): parallel-worker lock groups — goopg has no
  parallel query; not feasible without that subsystem.

Gates this loop: go build ./... clean; TestPort_IsolationPredicateGist strict
PASS (7.5s); non-gist SSI regression batch PASS (47.8s); -race executor+mvcc +
full executor/planner units PASS; pgbench smoke 0-failed (all 3 workloads);
TPC-H spot-check infra-timed-out on WSL2 (known SLRU-backfill hang; gated path
structurally unaffected); gen-isolation-coverage + gen-oracle-inventory regen
clean; make ralph-state-guard OK (self-repaired).
