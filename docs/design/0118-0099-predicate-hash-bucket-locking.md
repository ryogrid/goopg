# 0118-0099 — `predicate-hash.spec` PROMOTED: page (bucket) level predicate locking in a hash index (M0118-0009)

Status: accepted
Date: 2026-06-25
Spec: `postgres/src/test/isolation/specs/predicate-hash.spec`
Test: `TestPort_IsolationPredicateHash` (`runIsoSpecStrict`, internal/testport/isolation_port_test.go)

## Summary

`predicate-hash.spec` `failed`→`pass`, byte-identical to PG 18.3 across all 40
permutations. The spec verifies two complementary SSI behaviours for a hash
index: (1) a SERIALIZABLE index scan and a concurrent index insert that touch the
**same** bucket form an rw-conflict (so an overlapping interleaving aborts the
loser with `40001`); (2) a scan and an insert that touch **different** buckets
do **not** conflict ("reduced false positives").

goopg previously emitted 30 serialization failures where PG emits 18 — it
over-aborted the 12 different-bucket interleavings. Root cause: goopg has no
native hash access method, so `CREATE INDEX … USING hash` was silently rewritten
to a B-tree and the equality query either seq-scanned or index-scanned with a
**relation-grain** SIREAD predicate lock, against which *every* insert into the
table conflicts regardless of value.

## Upstream behaviour (the oracle)

PostgreSQL's hash AM takes a **page-level** predicate lock on the bucket's
primary page during an index scan (`hashgettuple` → `PredicateLockPage`) and
during an insert (`_hash_doinsert` → `CheckForSerializableConflictIn` on the
bucket page). Two transactions whose equality values hash to different buckets
lock different pages and never form an rw-edge; same-bucket scan+insert do. The
scan takes **no heap tuple** predicate lock (it is the index page lock that
matters), so unrelated heap-page co-residency cannot create a false conflict.

## goopg model

goopg builds every index on the B-tree substrate (no hash buckets), so we
emulate PG's bucket page lock with a synthetic bucket derived from the encoded
equality key:

- **`catalog.Index.DeclaredHash`** (new, in-memory only) records that an index
  was created `USING hash`. The catalog `Method` stays `"btree"` — pg_am /
  pg_class.relam / pg_dump are unchanged (the pre-existing hash→btree substrate
  behaviour), so blast radius outside SSI is nil. Set in `execCreateIndex` after
  the physical B-tree build (`operators_ddl.go`). Not persisted to the index-DDL
  WAL record, so it resets after a restart — a known follow-up, no durability
  regression over the prior unconditional hash→btree rewrite.

- **`ssiHashBucket(key)`** (`ssi.go`) maps the encoded index key to a stable
  31-bit pseudo-bucket via FNV-1a (masked so it is never `InvalidBlockNumber`,
  which `mvcc.PageLockTag` rejects). Equal keys → equal bucket; distinct keys
  collide only with ~2⁻³¹ probability, and a collision merely re-introduces a
  false-positive conflict (the safe over-abort direction), never a missed one.

- **`ssiRecordHashBucketRead(ctx, dbOid, indexOID, key)`** acquires a
  `PageLockTag(dbOid, indexOID, bucket)` SIREAD. The **index OID** is the
  predicate-lock relation, so these page tags never collide with the heap
  relation's tuple/page locks. Called from both the index scan
  (`indexScanOp`, `operators_index.go`) and the index-only scan
  (`indexOnlyScanOp`, `operators_indexonly.go`) once the equality probe key is
  encoded (`loBytes`), in place of the relation-grain `ssiRecordRelationRead`.

- **`ssiConflictOutTupleRead(ctx, xmin, xmax)`** is a lock-free variant of
  `ssiRecordTupleRead` (extracted shared `ssiConflictOutOnWriters`). A hash
  bucket scan uses it for each matched heap tuple so it records the
  read→writer conflict-out edges (the write-before-read same-bucket ordering,
  where the reader observes an in-flight inserter's invisible row) **without**
  acquiring a per-tuple heap SIREAD. Acquiring those would coarsen to a
  heap-page lock (`max_pred_locks_per_page = 2`) and then conflict with a
  different-bucket INSERT that merely lands on the same heap page — exactly the
  false positive the spec checks. This mirrors PG's index-only scan taking no
  heap predicate lock.

- **`ssiRecordHashIndexInsert(ctx, tbl, cols, row, dbOid)`** is the write-path
  hook (called after `ssiRecordTupleWrite` in both INSERT paths in
  `operators_storage.go`). For each `DeclaredHash` index on the table it encodes
  the inserted row's key with the same `encodeIndexKeyFromCols` /
  `encodeBTreeKeyForColumn` encoder the scan probe uses, computes the bucket, and
  runs `CheckForSerializableConflictIn` on the bucket page tag — forming the
  rw-edge against any SERIALIZABLE reader holding that bucket's SIREAD and
  aborting the INSERT in place (`40001`) when it closes a structure to an
  already-committed pivot.

## Why the planner already cooperates

Because `Method` stays `"btree"`, `findBTreeIndexForColumn` still selects the
hash index for an equality predicate (with `enable_seqscan = off` the spec forces
this), so the scan runs through `indexScanOp` / `indexOnlyScanOp` where the
bucket hooks live — no planner change was needed.

## Conflict-edge coverage (same bucket)

- **read-before-write:** reader takes the bucket SIREAD; the later same-value
  INSERT's `ssiRecordHashIndexInsert` finds it → `R → W`.
- **write-before-read:** the in-flight inserter's rows are visited by the
  reader's index probe and are invisible to its snapshot; `ssiConflictOutTupleRead`
  (and the existing `ssiRecordInvisibleTupleRead` path) records `R → W` via the
  inserter's xmin.

Different buckets exercise neither edge, so both transactions commit.

## Blast radius

Bounded to SERIALIZABLE equality scans / INSERTs on `DeclaredHash` indexes:
- Non-SERIALIZABLE: every hook short-circuits on `ssiActive`.
- btree / seqscan SSI specs: unchanged (`DeclaredHash` false) — the six
  pass-required SSI specs (`project-manager`, `total-cash`, `two-ids`,
  `receipt-report`, `read-only-anomaly*`, `read-write-unique-4`) and the
  `predicate-lock-hot-tuple` / `partial-index` / `simple-write-skew` anchors all
  still pass.
- Catalog / pg_dump / WAL: untouched (`Method` stays `"btree"`).

## Gates

- `TestPort_IsolationPredicateHash` strict PASS (40/40 permutations
  byte-identical; serialization-failure count 18, matching PG; was 30).
- Pass-required SSI specs above PASS (no regression).
- `internal/executor` + `internal/mvcc` + `internal/planner` + `internal/catalog`
  units PASS; `-race` on the SSI/predicate/serialization tests in
  `internal/executor` + `internal/mvcc` PASS.
- `go build ./...` + `go vet` clean; `gofmt` introduced no new misformat.
- pgbench smoke = pre-commit hook.

## Follow-ups

- `predicate-gin.spec` / `predicate-gist.spec` remain `failed` — they need
  AM-specific predicate-lock granularity (page-range / leaf locking) and are not
  addressed here.
- `DeclaredHash` is not WAL-persisted; a hash index reverts to relation-grain SSI
  locking after a restart. UPDATE/DELETE on a hash-indexed column do not yet take
  the bucket conflict-in (no current spec needs it; the INSERT path is the one
  `predicate-hash` exercises).
