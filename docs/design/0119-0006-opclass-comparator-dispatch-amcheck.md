# 0119-0006 — Operator-class comparator dispatch in amcheck's B-tree verifier

- Milestone / task: M0119-0006 (pg_amcheck server tier), unlock slice for
  upstream `postgres/src/bin/pg_amcheck/t/005_opclass_damage.pl`
- Status: **accepted**
- Date: 2026-08-10
- Related: `docs/design/0110-0008-*` (the amcheck SQL surface), deferral-ledger
  row 2026-08-09 (the `nextVirtualPgAmproc` UPDATE-path prerequisite)

## Problem

`bt_index_check()` verified every index under `btree.CompareKeys` — goopg's
order-preserving key-byte comparison. Upstream amcheck never compares keys
itself: `verify_nbtree.c` builds a `BTScanInsert` via `_bt_mkscankey`, and every
comparison runs through `_bt_compare`, which calls the index's **support
function 1** (`BTORDER_PROC`), resolved from `pg_amproc` for the index's
operator class.

That indirection is not a detail — it is the entire mechanism
`005_opclass_damage.pl` tests. The upstream test builds an index under a custom
operator class whose FUNCTION 1 sorts ascending, then executes

```sql
UPDATE pg_catalog.pg_amproc SET amproc = 'int4_desc_cmp'::regproc
 WHERE amproc = 'int4_asc_cmp'::regproc;
```

and requires that the *physically unchanged* index now reports
`item order invariant violated for index "fickleidx"`. Without comparator
dispatch, goopg reported the index clean: the damage was invisible, because
nothing goopg did during verification ever consulted `pg_amproc`.

The write half of this had already landed (`updateOp.nextVirtualPgAmproc` +
`catalog.InMemory.SetAmProcMemberProc`, commit `06995dd1`), which recorded the
read half as the remaining gap: "the btree AM still has no per-index comparator
dispatch to make a corrupted amproc observable."

## Design

Three seams, each in the layer that owns the concern.

### 1. Engine seam — `internal/amcheck`

```go
type KeyComparator func(a, b []byte) int
func VerifyBtreeItemOrderCmp(p storage.Page, blkno storage.BlockNumber,
    indexName string, cmpKeys KeyComparator) []BtreeReport
```

`VerifyBtreeItemOrder` becomes a nil-comparator delegate, so every existing
caller and unit test is unchanged. Both invariants the function checks — high
key and item order — run under the injected comparator, matching upstream where
one opclass comparator governs every comparison amcheck performs on the index.

A **nil** comparator (= `btree.CompareKeys`) is the *faithful* answer for a
built-in operator class, not a fallback: goopg's B-tree is key-encoding based,
so for a built-in class the encoding **is** that class's order and there is no
catalog function to call.

### 2. Catalog seam — `catalog.InMemory.LookupOpClassSupportProcOID`

```go
func (c *InMemory) LookupOpClassSupportProcOID(
    opClassName string, methodOID, procNum uint32) (uint32, bool)
```

PG's `get_opfamily_proc(opcfamily, opcintype, opcintype, procNum)`. It resolves
the *user-created* class by (name, access method) to its OID, then scans the
live `amProcMembers` store for the matching `ProcNum`. Reading the live store —
not a cached or heap-materialised copy — is what makes a runtime
`UPDATE pg_catalog.pg_amproc` take effect immediately, which is precisely the
corruption-injection channel the upstream test uses.

Built-in (BKI-pinned) classes are deliberately not covered; see the nil-comparator
rationale above.

### 3. Wire seam — `executor.btIndexOpClassComparator`

Called from `evalBtIndexCheck` and threaded through `btIndexVerify`. It returns
non-nil only when all of the following hold:

1. the index has exactly one key column and no `INCLUDE` columns;
2. that column declares an explicit operator class (`idx.ColOpClasses[0]`);
3. the class resolves through seam 2 to a routine (`Routines().LookupByOID`)
   taking two arguments;
4. the key column's type is `int4`.

The comparator decodes both stored keys with `btree.DecodeInt4` and calls the
routine through `executeStoredRoutine` — the same path the hash-partitioning
custom-opclass hash function already uses (`expr.go`, M0097-0027).

Two deliberate fallbacks to byte order inside the comparator:

- **Undecodable key.** An internal page's leftmost downlink carries an empty
  key (negative infinity, `findChildBlock`), and a truncated separator can be
  shorter than a full encoding. Byte order is correct for those, exactly as
  upstream treats its zero-attribute minus-infinity tuple.
- **Comparator error / NULL result.** A support function that fails cannot
  decide an ordering; manufacturing a finding from it would be a false positive,
  and amcheck's contract in this adapter is report-and-continue over the whole
  index.

## Scope and deferral

`int4`, single key column. That is 005_opclass_damage's shape and the only
shape whose stored key bytes can be inverted back to the SQL datum the support
function expects. Wider coverage needs a general encoded-key → `Datum` decoder
per type (the inverse of `encodeBTreeKeyForColumn`), which does not exist;
recorded in `.ralph/deferral_ledger.md`.

Cost: one SQL-function call per key comparison on the pages of an index that
declares a user opclass. Acceptable for a corruption check, and zero for every
index that does not (the comparator is nil and never allocated).

## Verification

- `TestBtIndexCheck_OpClassDamageDetected` (`internal/executor`) reproduces the
  upstream scenario end to end: clean check under the ascending comparator, then
  the verbatim-shaped `UPDATE pg_catalog.pg_amproc`, then the required
  `item order invariant violated for index "fickleidx"`. Confirmed **non-vacuous**
  — with the comparator forced to nil the second check reports the index clean
  and the test fails.
- The no-false-positive half (a healthy index under a user opclass must not
  raise) is the load-bearing assertion of the pair.
- `go test ./internal/amcheck/... ./internal/catalog/...` and the existing
  `TestBtIndexCheck_*` suite PASS unchanged.
