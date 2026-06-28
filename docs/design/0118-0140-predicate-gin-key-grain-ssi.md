# 0118-0140 — `predicate-gin.spec` PROMOTED: GIN per-key predicate locking via key-grain SIREAD (M0118-0002)

Status: accepted

## Summary

`predicate-gin.spec` `failed`→`pass`: all permutations now byte-identical to
PostgreSQL 18.3, promoted to `runIsoSpecStrict` in
`TestPort_IsolationPredicateGin`. This closes the last *actionable* gap in the
M0118-0002 predicate-lock-granularity group (and in the isolation suite at
large): the only remaining `failed` spec is **deadlock-parallel**, which needs a
parallel-query lock-group abstraction goopg has no subsystem for (infeasible
without a parallel executor). Isolation tally now **120 pass / 1 failed**.

This builds directly on two prior enablers — 0118-0138 (`int4[]` array column
storage round-trip) and 0118-0139 (`anyarray` `@>`/`<@`/`&&` operators) — which
moved the spec's first divergence from global setup to a genuine SSI granularity
divergence, and reuses the granularity-emulation pattern from 0118-0099
(hash bucket SIREAD) and 0118-0137 (GiST grid-cell SIREAD).

## Problem

The spec tests **page-level (per-key) predicate locking in a GIN index**. The
base data is `array[1]` × 8192 rows plus `array[g]` (g=2..800) one row each, with
a `USING gin(p) WITH (fastupdate = off)` index. Two SERIALIZABLE transactions:

- **s1** scans the array column with `p @> array[K]` (`K` ∈ {1, 2, 800, 2000})
  then INSERTs into another table (`other_tbl`);
- **s2** reads `other_tbl` then INSERTs an array value `array[K']` into `gin_tbl`.

A scan for key `K` and an insert of key `K'` form an rw-conflict **iff K==K'**
(they touch the same part of the index — the same posting tree). Combined with
s2's read of `other_tbl` and s1's write of `other_tbl`, that closes a dangerous
SSI structure → the loser aborts with 40001. When `K≠K'` the scan and insert
touch *different parts* of the index, so there must be **no** conflict (the
reduced-false-positive half). A non-existing key (`K=2000`, matched by no tuple)
must still lock its key and conflict with an insert of `array[2000]`.

With `fastupdate = on` the whole index is buffered through a single pending list
that is predicate-locked as one unit, so the engine cannot distinguish particular
keys — **every** key conflicts (the spec's `fu` permutations). `fastupdate` is
toggled at runtime via `ALTER INDEX ginidx SET (fastupdate = on)`.

PostgreSQL's GIN AM descends the index for the search key and takes a
`PredicateLockPage` on the posting-tree page(s) it visits, so disjoint keys lock
disjoint pages. goopg has **no native GIN access method** — a `USING gin` index
is catalog-only (no physical posting tree) — so with `enable_seqscan=off` the
`p @> array[K]` queries fall back to a **seq scan**, which under SERIALIZABLE took
a **relation-grain** SIREAD on `gin_tbl`. Against that whole-relation lock *every*
concurrent insert into `gin_tbl` conflicts regardless of key → goopg over-aborted
every disjoint-key (`K≠K'`) permutation with a spurious 40001.

## Fix

Emulate GIN per-key leaf-page granularity with a synthetic **key page**: each GIN
search key (an array element in its canonical PG text form) maps to a stable
pseudo-page on the *index*, so two scans of disjoint keys lock disjoint pages
while a scan and an insert of the same key share a page.

### Granularity primitives (`internal/executor/ssi.go`)

- `ssiGinKeyPage(key string) storage.BlockNumber` = FNV-1a of the key text, masked
  to 31 bits (never `InvalidBlockNumber`, never the sentinel). Equal keys → equal
  page; distinct keys collide only with ~2⁻³¹ probability (a collision merely
  re-introduces a false positive — the safe over-abort direction).
- `ssiGinSentinelPage` — a fixed reserved page standing in for the **whole index**
  when `fastupdate = on` (all keys map to it).
- `ssiGinIndexForTable(ctx, tbl, cols)` → `(idxOID, colIdx, fastUpdate, ok)`:
  finds a `Method=="gin"` index on the table, the position of its key column, and
  the index's current fastupdate state (default PG `on` when the reloption is
  unset; the spec creates it `off`).
- `ssiRecordGinKeyRead(ctx, dbOid, idxOID, keys, fastUpdate)` — the **read** hook.
  With `fastupdate=off` it takes a per-key SIREAD `AcquirePredicateLock` on each
  search key's page; with `fastupdate=on` it takes a single SIREAD on the
  sentinel page.
- `ssiRecordGinIndexInsert(ctx, tbl, cols, row, dbOid)` — the **write** hook (the
  `ssiRecordGistIndexInsert` twin). For each GIN index it conflicts-in
  (`CheckForSerializableConflictInReportingFailure`) on **every element** of the
  inserted array's key page **and** on the sentinel page, installing the rw-edge
  from a matching per-key reader (fastupdate=off) *or* a sentinel reader
  (fastupdate=on) and aborting the insert in place (40001) when it closes a
  dangerous structure to a committed pivot.

The insert always conflicts-in on the sentinel as well as the per-key pages: a
reader only ever takes the sentinel lock when `fastupdate=on`, so in the
non-fastupdate permutations no reader holds it and the extra check is a pure
no-op (no false positives). This handles the "fastupdate turned on concurrently"
permutation too — a reader that locked per-key (fu off at read time) is still
caught because the insert also conflicts per-key.

### Scan wiring (`internal/executor/operators_storage.go`)

The `Filter`-over-`SeqScan` predicate is threaded down at build time into
`seqScanOp.ssiGinPred` in **both** build paths (`Build` and the live
`buildRec`/`BuildFastIterator` in `executor.go` — `pattern_sibling_paths_must_agree`),
the same site that already feeds `ssiGistPred`. In `Open` (after the GiST
resolution, gated `gistSSIIdxOID==0`):

1. resolve the GIN index + fastupdate via `ssiGinIndexForTable`;
2. extract the search keys from the `<gincol> @> <const array>` predicate via
   `extractGinSearchKeys` (evaluates the constant `@>` right operand → array
   elements). Keys come from the **query**, not matched tuples, so a non-existing
   key still locks its page; an unsupported predicate shape returns `ok=false` →
   the scan keeps relation-grain locking (never under-locks);
3. take the key SIREADs via `ssiRecordGinKeyRead` and **suppress** the
   relation-grain SIREAD (`ssiRecordRelationRead`, gated on
   `ginSSIIdxOID==0`) — taking both would re-coarsen to the relation grain.

In the `Next` loop, GIN mode (`ginSSIIdxOID != 0`) **suppresses** the heap
per-tuple SIREAD (`ssiRecordTupleRead`) and the relation-wide invisible-tuple
conflict-out — the reader→inserter rw-edge is formed write-side by the insert's
key conflict-in, so a tuple/relation-grain read lock would re-introduce the false
positives the per-key locking removes. (All spec permutations read before any
concurrent insert commits, so no read-side conflict-out is exercised.)

### Insert wiring

`ssiRecordGinIndexInsert` is called at both INSERT sites in `insertOp.Next`
(partition-routed and non-partitioned), immediately after the existing
`ssiRecordGistIndexInsert` call.

### `ALTER INDEX … SET (fastupdate = …)` (parser + executor)

`fastupdate` was already parsed/stored on CREATE INDEX (`catalog.Index.FastUpdate`,
DU-002 slice 220) but `ALTER INDEX name SET (...)` was a parser no-op. Added:

- parser (`internal/parser/ddl.go`): an `ALTER INDEX name SET (param = value, …)`
  branch (directly after the index name, distinct from `ALTER COLUMN … SET`)
  emits one `AlterIndexSetReloptions` action carrying the parsed `With` map;
- executor (`internal/executor/operators_ddl.go`, the ALTER-on-index branch):
  for `fastupdate`, parses the bool via the existing `parseReloptionBool` and
  updates `idx.FastUpdate`. Other options are accepted and ignored (matches the
  CREATE INDEX WITH path). The reloption change is immediately visible to other
  sessions through the shared in-memory catalog, so the read/insert hooks observe
  the live fastupdate state at each step — which is exactly what the permutation
  ordering (e.g. read-before-`fu` vs `fu`-before-read) requires.

## Blast radius

Bounded behind `ginSSIIdxOID != 0`, which is 0 for every non-GIN scan: the whole
GIN-SSI path is skipped unless a SERIALIZABLE scan resolves a GIN index on the
predicate's array column. Catalog `Method`/pg_am/pg_dump/WAL are unchanged; the
`ALTER INDEX SET` change only mutates the advisory `FastUpdate` field already
rendered in `pg_class.reloptions`. RC/RR and bootstrap contexts short-circuit on
`ssiActive`.

## Oracle

Mirrors `src/backend/access/gin/ginget.c` / `ginbtree.c` predicate-lock points
(`PredicateLockPage` on the visited posting/entry pages) and
`src/backend/access/gin/ginfast.c` (fastupdate pending list locked as one unit),
reduced to a synthetic per-key page because goopg has no physical GIN tree.
Behavior compared byte-for-byte against `./postgres/local_install` PG 18.3 across
all spec permutations.

## Tests / gates

- **Strict** `TestPort_IsolationPredicateGin` — all permutations byte-identical to
  PG 18.3 (previously over-aborted every disjoint-key interleaving).
- `TestSsiGinKeyPage` — key-page determinism, distinctness of the spec's four
  keys, no sentinel collision.
- `TestParseAlterIndexSetReloptions` — `ALTER INDEX … SET (fastupdate=on|off)`
  parses to one `AlterIndexSetReloptions` action.
- Non-GIN SSI regression: `predicate-gist`, `predicate-hash`, `partial-index`,
  `index-only-scan` strict PASS (granularity siblings unaffected).
- `-race` executor + mvcc on the SSI/array path PASS; `internal/executor` +
  `internal/parser` unit suites PASS; `go build ./...` clean.
- pgbench CI-parity smoke 0 failed (pre-commit hook).
