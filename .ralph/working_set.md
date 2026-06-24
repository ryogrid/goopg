(idle — nothing in flight)

Loop #37 COMPLETE: M0118-0009 `predicate-hash.spec` PROMOTED `failed`→`pass`
(all 40 permutations byte-identical, strict TestPort_IsolationPredicateHash).
Design 0118-0099.

What landed — page (bucket) level predicate locking in a hash index:
- catalog/catalog.go: new in-memory `Index.DeclaredHash` (Method stays "btree";
  catalog/pg_am/pg_dump/WAL unchanged).
- executor/operators_ddl.go (execCreateIndex): set DeclaredHash after the btree
  build for `USING hash`.
- executor/ssi.go: `ssiHashBucket` (FNV 31-bit bucket of encoded key);
  `ssiRecordHashBucketRead` (PageLockTag(db,indexOID,bucket) SIREAD);
  `ssiConflictOutTupleRead`/`ssiConflictOutOnWriters` (conflict-out only, no heap
  SIREAD — avoids heap-page coarsening false positive); `ssiRecordHashIndexInsert`
  (bucket conflict-in on INSERT path).
- executor/operators_index.go + operators_indexonly.go: for a declared-hash
  equality probe, take the bucket SIREAD instead of the relation-grain lock and
  use conflict-out-only per-tuple reads.
- executor/operators_storage.go: call ssiRecordHashIndexInsert after
  ssiRecordTupleWrite in both INSERT paths.
- docs/design/0118-0099 + README index; target-inventory CSV failed→pass + regen
  upstream-isolation-coverage.md; fix_plan + tally.

Root cause found via debug: `CREATE INDEX … USING hash` was silently rewritten
to btree in the catalog (operators_ddl.go:4709), so the equality scan took a
relation-grain SIREAD → over-aborted 30 vs PG's 18. DeclaredHash flag (not a
catalog Method change) keeps blast radius off pg_am/dump/planner.

Gates: TestPort_IsolationPredicateHash strict PASS (18 failures matching PG, was
30); 6 pass-required SSI specs + predicate-lock-hot-tuple/partial-index/
simple-write-skew PASS no regression; executor/mvcc/planner/catalog units +
`-race` SSI/predicate tests PASS; build+vet clean; gofmt only pre-existing
M0111 misformat (version mismatch, untouched). pgbench smoke = pre-commit hook.
State guard repaired→consistent.

Isolation failed-spec count now 12 (was 13). Remaining M0118-0009 failed:
intra-grant-inplace (pg_class row locks — heavy), horizons (JSON `->` + EXPLAIN
json), stats (pg_stat infra), prepared-transactions{,-cic} (2PC).
predicate-gin/gist still failed (AM-specific predicate-lock granularity).

NEXT candidate: horizons likely cheapest front-end (JSON `->` operator parse +
explain_json + EXPLAIN FORMAT json Heap Fetches) — probe-rank first.
