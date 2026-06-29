# 0119-0004 — `NULLS NOT DISTINCT` uniqueness *enforcement* at INSERT/UPDATE

Status: **implemented** (plain INSERT/UPDATE — §1–§7 — the ON CONFLICT /
upsert-arbiter follow-up — §8 — and the `CREATE [UNIQUE] INDEX` build over
NULL-keyed data — §9 — landed; the reordered-partition arbiter remains
deferred, ledger)
Source task: M0119-0004 (deferral-ledger backlog consumption; surfaced by
M0110-0001 / DU-002 slices 134–138). Milestone
`docs/milestones/0119-deferral-ledger-backlog-consumption.md`.

## 1. Problem

PostgreSQL `CREATE UNIQUE INDEX … NULLS NOT DISTINCT` (and the table/column
`UNIQUE NULLS NOT DISTINCT` constraint forms) make NULL key values **collide**
with one another for uniqueness purposes: a second row whose key column(s) are
NULL (matching an existing row's NULL pattern, with equal non-NULL columns) is a
duplicate and must raise `23505`.

> *PostgreSQL 18.3 — `postgres/official_docs_in_md/sql-createindex.md`:*
> "By default, null values in a unique column are not considered equal, allowing
> multiple nulls in the column. The `NULLS NOT DISTINCT` option modifies this and
> causes the index to treat nulls as equal."

goopg already round-trips the **dump-fidelity** layer (M0110-0001 slices
134–138): the parser captures the clause across all five surface forms
(`CREATE UNIQUE INDEX`, table-level anonymous/named `UNIQUE`, inline
anonymous/named column `UNIQUE`), the executor threads it into
`catalog.Index.NullsNotDistinct` (`internal/catalog/catalog.go:1312`), and
`pg_index.indnullsnotdistinct` + `pg_get_indexdef`/`pg_get_constraintdef`
re-emit it. **The flag is dumped but not honoured at runtime** — goopg never
treats two NULLs as a duplicate, so an NND-unique index silently permits
duplicate-NULL rows that PG 18.3 rejects.

Root cause: `encodeIndexKeyFromCols` (`operators_storage.go:6083`) and its
sibling `encodeExprIndexKey` return `nil` the moment **any** key column is NULL:

```go
if v.IsNull() {
    return nil, nil // NULLs don't participate in unique constraints
}
```

Every uniqueness caller treats `key == nil` as "skip this index" (insert-maintain
`maintainUniqueIndexesForInsert`, check `checkUniqueIndexesForInsert` /
`checkUniqueIndexesForUpdate`). So NULL-containing rows are neither indexed nor
checked. This is correct for a default (NULLS DISTINCT) index but wrong for NND.

## 2. Constraints / non-goals

* **Zero blast radius outside NND indexes.** `catalog.Index.NullsNotDistinct`
  defaults `false`; every existing index (all of TPC-H, pgbench, every primary
  key) must be byte-for-byte unchanged on every path (Hard-won Rule #1/#2). The
  hot INSERT/UPDATE unique-check path is shared, so the new behaviour must be
  gated strictly on `idx.NullsNotDistinct`.
* **No btree key-encoding change.** A NULL-sentinel key encoding was the
  originally-deferred resume point, but it is hazardous: the sentinel must stay
  byte-consistent across insert-maintain, unique-check, **and** the index-scan
  probe key builders (`lookupKey`/`lookupKeys`/`lookupRangeBounds` in
  `operators_index.go` + `operators_indexonly.go`, `encodeArbiterKey`,
  `updateViaIndex`). goopg's btree stores **raw concatenated key bytes** with no
  null bitmap; for fixed-width columns (int4/int8/float8/timestamp/date) a
  sentinel cannot be made provably non-aliasing against real encodings without a
  per-column presence tag, and a presence tag would have to be mirrored into all
  ~6 scan-probe sites or equality SELECTs on NND indexes would silently return no
  rows. That is exactly the multi-encoding-site hot-path risk the ledger flagged.
  **This design avoids key encoding entirely.**
* Partial NND indexes (`… WHERE pred`) and expression NND indexes are handled by
  reusing the existing predicate / column-resolution machinery where already
  present; no new expression evaluation is added beyond what the check path
  already does. (No upstream NND spec exercises a partial/expression NND index in
  the ported suites; covered defensively, not as a first-class goal.)

### 2.1 Uncovered surfaces (this slice) — follow-up ledger row

This slice closes the **plain `INSERT` and `UPDATE`** enforcement path. An agent
review surfaced three additional surfaces that this slice intentionally does NOT
cover; each is recorded as a deferral so the milestone's acceptance is
unambiguous:

* **`ON CONFLICT` / upsert arbiter (behavioral divergence, P0).**
  `encodeArbiterKey` (`operators_upsert.go:1414`) returns `nil` on any NULL key
  column and never consults `idx.NullsNotDistinct`, so
  `INSERT … ON CONFLICT (nndcol) DO UPDATE|NOTHING` with a NULL key sees
  `conflicted=false` and **inserts a duplicate** where PG would route to the
  conflict action — a *wrong DML outcome*, not merely a missing `23505`. Closing
  it requires the NND check to return the conflicting tuple's `ItemPointer` so
  the upsert executor can target `DO UPDATE` (or skip for `DO NOTHING`); that is a
  larger integration with the arbiter machinery and is a **follow-up M0119-0004
  slice** (ledger). The new `checkNullsNotDistinctViaHeapScan` helper is written
  to return the conflicting `ItemPointer` precisely so the follow-up can reuse it.
* **`CREATE UNIQUE INDEX` build over existing NULL-keyed data (pre-existing
  bug, P1).** Both build paths (`collectBTreeEntries` / `backfillBTree` via
  `encodeCompositeBTreeKey`, `operators_ddl.go`) raise `42804` ("column is null
  and cannot be indexed") for any NULL key column instead of admitting NULLs as
  distinct (default) or deduping them (NND). This is a pre-existing divergence
  independent of enforcement; out of scope here, listed for tracking.
* **`COPY` / logical-apply-worker** deliberately bypass the unique check
  (apply-worker skip-on-duplicate is correct for replication); unchanged.

## 3. Design — heap-scan fallback for the NULL case only

Key observation: the index-scan probe paths **never build a NULL key** — a
`col = NULL` predicate is SQL-unknown (no equality probe is planned) and
`col IS NULL` does not use the btree equality path. Therefore the only place a
NULL key for an NND index needs to be *matched* is the uniqueness **check**, and
the only writers of such keys would be insert-maintain. We exploit this to keep
the btree and all scan/probe paths **completely untouched**:

1. **Maintain (unchanged).** `maintainUniqueIndexesForInsert` /
   `maintainNonArbiterIndexes*` continue to skip NULL-containing rows
   (`key == nil`). NND NULL rows are simply *not* stored in the btree — exactly
   as today. The btree thus contains only non-NULL entries, so every equality /
   range scan and every non-NULL unique probe is byte-for-byte identical to
   today.

2. **Check (new gated branch).** In `checkUniqueIndexesForInsert` and
   `checkUniqueIndexesForUpdate`, after `encodeIndexKeyFromCols` returns
   `key == nil`:
   * Today: `continue` (skip the index).
   * New: **if `idx.NullsNotDistinct` and the candidate row actually has ≥1 NULL
     in this index's key columns**, run a dedicated
     `checkNullsNotDistinctViaHeapScan` instead of skipping. (If `key == nil` for
     any other reason — expression column, arity mismatch — fall through to the
     existing `continue`, unchanged.)

   `checkNullsNotDistinctViaHeapScan` seq-scans the table's heap (mirroring the
   existing `checkGistOverlapExclusion` pattern at `operators_storage.go:6380`:
   `Pool.NBlocks` → per-page `PageGetHeapTuple` → `isLiveForUniqueCheck` →
   `DecodeRowIntoMctxPGTuple`) and, for each **live** tuple, reports a `23505`
   conflict when the existing row's index-key columns match the candidate's
   **NULL pattern and non-NULL values**:

   * for each index key column `c`: `(cand[c] IS NULL AND exist[c] IS NULL)` OR
     `(both non-NULL AND values byte-equal under the column's index encoding)`.
   * value equality reuses `encodeBTreeKeyForColumn` per non-NULL column so it
     matches the exact normalisation (collation/type) the btree would use — no
     new comparison semantics.
   * The helper returns the conflicting tuple's `ItemPointer` (for the
     `ON CONFLICT` follow-up slice, §2.1) in addition to the boolean; the plain
     INSERT/UPDATE path only needs the boolean to raise.

   **UPDATE self-conflict** is handled by the *existing* call ordering, not by a
   new exclusion mechanism. Both UPDATE call sites stamp the old tuple's `xmax`
   with `effectiveWriterXID` **before** calling `checkUniqueIndexesForUpdate`
   (`operators_storage.go:3761` / `:4434`); `isLiveForUniqueCheck`'s
   `xmax == selfXID` short-circuit (`:6786`) then classifies the prior version as
   **dead**, so the heap scan skips it automatically — exactly the mechanism that
   already prevents the pgbench teller-contention false 23505. No `ItemPointer`
   parameter is threaded into the check for self-exclusion. (Note: the existing
   `indexKeyColumnsChanged` skip at `:6318` does NOT protect a NULL→NULL UPDATE —
   nil keys make it report "changed" — so the xmax-stamp ordering is the load-
   bearing guarantee here.)

3. **Error shape.** Raise the same `ExecError` the btree path raises:
   `Code 23505`, `Message duplicate key value violates unique constraint "<name>"`,
   `Detail Key (<cols>)=(<vals>) already exists.` A new detail builder renders a
   NULL key column as `null` (PG prints `Key (a)=(null) already exists.`);
   `Datum.Format()` returns `""` for `KindNull`, so the detail builder maps NULL
   → `null` rather than calling `Format()` directly for those columns.

### Why heap-scan and not a sentinel key

* **Correctness is structural, not type-fragile.** No reasoning about whether a
  reserved sentinel can alias a fixed-width encoding; the comparison is a direct
  per-column NULL/value test on decoded rows.
* **Zero change to btree, scan probes, arbiter, EPQ, WAL.** The blast radius is
  two `if idx.NullsNotDistinct && hasNullKeyCol { … }` branches plus one new
  helper — all dead code for every non-NND index.
* **Acceptable cost.** The O(n_pages) scan fires *only* on an INSERT/UPDATE into
  an NND index where the new row has a NULL key column — a rare combination
  (NND indexes are rare; NULL-keyed rows in them rarer). Default-NULLS-DISTINCT
  indexes never reach it. A future optimisation (presence-tagged sentinel keys
  shared with the scan paths) is noted as out of scope.

## 4. Touch points

| File | Change |
|------|--------|
| `internal/executor/operators_storage.go` | `checkUniqueIndexesForInsert` / `checkUniqueIndexesForUpdate`: replace the `key == nil` `continue` with a gated NND heap-scan branch. New `checkNullsNotDistinctViaHeapScan` + `rowHasNullKeyColumn` + `nndDetail` helpers. |
| (tests) `internal/executor/nulls_not_distinct_test.go` (new) | Unit: single-col + multi-col NND index, duplicate-NULL INSERT → 23505, distinct non-NULL-tail no conflict, NULLS-DISTINCT control admits duplicate NULLs, no-key-change UPDATE no self-conflict. |
| (tests) `internal/testport/…` | Extend an existing pg_dump/connsetup or a focused round-trip test to assert the enforcement against PG 18.3 semantics (a duplicate-NULL INSERT errors). |

No parser, catalog, planner, codec, or WAL changes.

## 5. Gating / verification

Per the WAL/MVCC practice card this is **not** a WAL/replication change (no
visibility/format change; heap read-only scan + existing 23505 raise), but it
*does* touch the shared INSERT/UPDATE unique-check path, so:

* `go build ./...` clean.
* `go test ./internal/executor/` (new NND unit tests + existing unique/upsert
  regression: `TestPort_*`-free unit tier).
* `go test -run 'Unique|Upsert|Conflict|NullsNotDistinct' ./internal/executor/`.
* **Fresh-server TPC-H Q12/Q13 spot-check** (`scripts/tpch-spotcheck.sh`,
  canonical Q12=2/Q13=33 via `postgres@postgres`) — mandatory because the change
  is on the INSERT/UPDATE path even though no TPC-H index is NND (proves zero
  regression on the default path).
* **pgbench smoke** (pre-commit hook) — TPC-B `UPDATE` contention path must stay
  0-failed (the no-key-change-UPDATE self-skip parity is the specific risk).
* `internal/catalog` unchanged → no catalog test needed beyond build.

## 6. Oracle

Mirrors PG's `_bt_check_unique` NULLS-NOT-DISTINCT handling
(`postgres/src/backend/access/nbtree/nbtinsert.c`: when
`!itup_key->anynullkeys || index->rd_index->indnullsnotdistinct` the scan does
not treat NULLs as always-distinct). goopg emulates the *outcome* (NULLs equal
under NND) via the heap-scan check rather than the nbtree null-key path, because
goopg's byte-key btree has no per-attribute null bitmap.

## 7. Risks

* **Self-conflict on UPDATE** — prevented by the existing "stamp old `xmax`
  before check" ordering + `isLiveForUniqueCheck`'s `xmax == selfXID` arm (see
  §3 step 2); covered by a NULL→NULL no-key-change UPDATE regression test + pgbench
  smoke. No new exclusion mechanism is introduced.
* **Live-tuple classification** reuses `isLiveForUniqueCheck`, so MVCC visibility
  matches the btree path exactly (no new visibility logic).
* Performance on a large NND table with frequent NULL-keyed writes — acceptable
  given rarity; documented as a future sentinel-key optimisation.

## 8. Follow-up LANDED (2026-06-29, loop #15) — `ON CONFLICT` / upsert arbiter

Closes the P0 behavioral divergence first listed in §2.1: a NULL-keyed
`INSERT … ON CONFLICT (nndcol) DO UPDATE|NOTHING` against an NND arbiter index
was **inserting a duplicate** (the arbiter probe saw `conflicted=false`) instead
of routing to the conflict action.

**Mechanism — reuse the §3 heap-scan, no new probe semantics.** `encodeArbiterKey`
still returns a `nil` key on a NULL conflict-key column (NULLs distinct by
default), so `probeArbiterWaiting` correctly reports no btree conflict. The
upsert `Next` loop now adds one gated fallback right after that probe, on the
non-reordered path (`partLeaf == nil`):

```
if !conflicted && partLeaf == nil {
    nndPtr, nndRow, nndFound := o.probeArbiterNND(rel, writeTbl, cols, inserted)
    if nndFound { conflicted = true; conflictPtr = nndPtr; conflictRow = nndRow }
}
```

`probeArbiterNND` is gated on `idx.NullsNotDistinct && rowHasNullKeyColumn(...)`,
calls the SAME `checkNullsNotDistinctViaHeapScan` matcher the plain
INSERT/UPDATE check uses, and decodes the conflicting tuple (`PageGetHeapTuple` →
`DecodeHeapTupleRow`) so the existing `DO NOTHING` (silent skip) and `DO UPDATE`
(targets `conflictPtr`/`conflictRow`) branches work unchanged. `arbiterKey`
stays `nil`; `findInProgressConflictKey(nil)` is already a no-op, so the DO
UPDATE re-probe path is safe.

**Self-conflict fix on the DO UPDATE branch.** Unlike a plain UPDATE, the upsert
runs `checkUniqueIndexesForUpdate` **before** `applyUpdate` stamps the old tuple
dead, so the §3-step-2 "xmax == selfXID ⇒ dead" trick does not yet apply and the
NND heap scan would re-find the still-live old version → false `23505`. Fixed by
making the existing unchanged-key skip NND-aware: `indexKeyColumnsChanged` now,
when `encodeIndexKeyFromCols` returns a `nil` key on a `NullsNotDistinct` index,
compares the NULL pattern + non-NULL values of the two versions directly (new
`nndKeyColumnsEqual`) instead of unconditionally reporting "changed". A
no-key-change NULL→NULL UPDATE therefore skips the probe (cannot self-collide),
while a genuine key change (e.g. `5 → NULL` colliding with another NULL row) is
still reported as changed and probed. This also makes the plain-UPDATE
self-skip independent of stamp ordering (more robust; the §3-step-2 note about
`indexKeyColumnsChanged` not protecting NULL→NULL is now obsolete).

**Scope deferred (ledger):** a *reordered* partition leaf
(`partLeaf != nil`, leaf column order ≠ parent) is skipped — the candidate row
is in parent order while the heap-scan matcher needs leaf-order columns; an
exotic NND-on-reordered-partition combination, ledgered for a later slice.
Concurrent in-flight NULL-keyed NND conflicts (the spec/wait machinery) ride the
single-session heap scan; full speculative-token integration for the NULL case
is not attempted (the btree spec path never sees a NULL key).

**Touch points (follow-up):** `internal/executor/operators_upsert.go`
(`probeArbiterNND` + `Next` fallback), `internal/executor/operators_storage.go`
(`indexKeyColumnsChanged` NND branch + `nndKeyColumnsEqual` helper),
`internal/executor/nulls_not_distinct_test.go` (3 new tests:
`TestNullsNotDistinctOnConflictDoNothing` / `…DoUpdate` /
`TestNullsDistinctOnConflictControlInserts`).

**Gates:** full `internal/executor` suite PASS + `-race` on NND/upsert/conflict
PASS; `TestPort_IsolationInsertConflict*` + `TestPort_IsolationMerge*` (M0118-0006
group) PASS (58 s); zero blast radius outside NND indexes (every new branch
gated on `idx.NullsNotDistinct`; non-NND `indexKeyColumnsChanged` returns the
identical result as before). pgbench smoke via the pre-commit hook.

## 9. Follow-up LANDED (2026-06-29) — `CREATE [UNIQUE] INDEX` build over NULL-keyed data

Closes the P1 pre-existing divergence first listed in §2.1: both index-build
paths (`collectBTreeEntries` / `backfillBTree` via `encodeCompositeBTreeKey`,
`operators_ddl.go`) raised `42804 "column %q is null and cannot be indexed"` for
**any** NULL key column. That rejected `CREATE INDEX`/`CREATE UNIQUE INDEX` over a
table containing **any** NULL-valued key — a divergence from PG, which admits
NULLs in indexes (distinct by default, allowing multiple NULLs) and only collides
them under `NULLS NOT DISTINCT`.

**Mechanism — mirror the runtime maintain path; no stored key change.** goopg's
byte-key btree has no per-attribute null bitmap, and the runtime maintain path
(`maintainUniqueIndexesForInsert`) already **never stores** a NULL-keyed row
(`encodeIndexKeyFromCols` returns `nil`). The build is brought into line:

* `encodeCompositeBTreeKey` now returns a third result `hasNullKey bool` instead
  of raising `42804`. On the first NULL **value** key column it returns
  `(nil, true, nil)`; the row is not stored. (Expression-only columns still
  return `(nil, false, nil)` — the existing `key == nil` skip is preserved and
  kept distinct from the NULL-value case.)
* Both build loops, on `hasNullKey`:
  * **default (NULLS DISTINCT) or non-unique** → skip the row (multiple NULLs
    allowed); the index simply omits NULL-keyed rows, exactly as the maintain
    path and as every equality/range scan expects (no NULL keys are ever probed).
  * **`unique && nullsNotDistinct`** → dedup null-bearing rows among themselves
    via a build-local `seenNull` map keyed by `nndNullKeyDedupKey` (a presence
    byte `0x00`/`0x01` per key column + the btree column encoding for non-NULL
    columns); a repeated NULL-pattern-plus-equal-non-NULL-values row raises
    `23505 "could not create unique index"`. This key is **never stored in the
    btree** (it exists only for build-time dedup), so the §2 "avoid sentinel keys
    in stored/scanned keys" constraint is unaffected — sentinel aliasing against
    real encodings is irrelevant for a build-local map.

The `NullsNotDistinct` flag was previously set on the catalog index **after**
`createBTreeIndex` built it, so the flag is now threaded as a new
`nullsNotDistinct bool` parameter through `createBTreeIndex` →
`bulkBuildBTree[WithPredicate]` → `collectBTreeEntries` / `backfillBTree`. All 16
`createBTreeIndex` call sites pass it: the five NND-capable constraint/index
forms forward the real flag (CREATE [UNIQUE] INDEX `s.NullsNotDistinct`, named
constraint `nc.NullsNotDistinct`, inline column `c.UniqueNullsNotDistinct`,
table-level `s.TableUniqueNullsNotDistinct[i]`, partition-child/LIKE
`parentIdx.NullsNotDistinct`/`idx.NullsNotDistinct`); PK and non-unique forms
pass `false`. The matview-rebuild `bulkBuildBTree` (operators_ddl.go) forwards
`idx.NullsNotDistinct`.

**Zero blast radius for default indexes.** Passing `nullsNotDistinct == false`
(every existing index, all of TPC-H/pgbench) changes the build only in that a
NULL-keyed row is now *skipped* instead of *erroring* — a strict improvement
(building over NULL data now succeeds, matching PG and the runtime maintain
path); no non-NULL row's key bytes or storage change. `seenNull` is `nil` for
every non-NND build.

**Tests:** `internal/executor/nulls_not_distinct_test.go` adds four build-path
tests: `TestNullsNotDistinctBuildAdmitsNullDefault` (non-unique build over NULL
data succeeds, no 42804), `…BuildUniqueAdmitsDuplicateNulls` (default-unique
admits multiple NULLs), `…BuildNNDRejectsDuplicateNulls` (NND build over
duplicate NULLs → 23505), `…BuildNNDAdmitsDistinctTail` (multi-col NND admits a
distinct non-NULL tail, rejects an equal one). Full `internal/executor` suite +
`-race` on Unique/Index/NullsNotDistinct/Upsert/Conflict/DDL/Partition PASS.

**Scope deferred (ledger):** the reordered partition-leaf NND arbiter (§8) is the
sole remaining NND follow-up under M0119-0004.
