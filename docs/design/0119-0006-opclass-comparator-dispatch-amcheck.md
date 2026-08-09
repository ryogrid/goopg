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

1. every key column is a plain (non-expression) column resolvable in
   `idx.Table.Columns`;
2. **at least one** key column declares an explicit operator class
   (`idx.ColOpClasses[i]`) that resolves through seam 2 to a routine
   (`Routines().LookupByOID`) taking two arguments.

The comparator walks the composite key **column by column**, which is the
contract of upstream `_bt_compare`: it iterates the scan key's attributes in
order and stops at the first non-equal one. Each column is decoded from the
stored key bytes with the executor's shared
`decodeIndexKeyColumn` — the same inverse-of-`encodeBTreeKeyForColumn` decoder
the index-only scan uses, covering `int4`/`int8`/`float8`/`date`/
`timestamp(tz)`/text-like/enum — and the column's own comparator decides:

- a column that resolved to a user routine is compared by calling it through
  `executeStoredRoutine` (the same path the hash-partitioning custom-opclass
  hash function already uses, `expr.go`, M0097-0027);
- a column that did not keeps `btree.CompareKeys` over **that column's** encoded
  bytes — the built-in class's order by construction.

Decoding returns the byte width consumed, which is what lets the walk advance
past a leading variable-length column (e.g. `text`) to reach a later key column
whose opclass was damaged.

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

**2026-08-10 (second slice).** The first slice restricted dispatch to a single
`int4` key column, because it inverted key bytes with a hand-rolled
`btree.DecodeInt4` call. That restriction is lifted: the walk now delegates to
`decodeIndexKeyColumn`, so every type that decoder inverts, in any position of a
composite key, dispatches to its declared opclass.

**2026-08-10 (fourth slice — `INCLUDE`).** Covering indexes are no longer
declined. The earlier slices assumed goopg's covering-key layout was outside the
decoder's contract; it is not — goopg builds a stored B-tree key from the **key**
columns alone (`encodeCompositeBTreeKey` / the unique-index key builder in
`operators_storage.go` both walk `idx.Columns`, and no non-key attribute is ever
appended; goopg's index-only scan likewise decodes only key columns). A covering
index's key bytes are therefore exactly the column-by-column walk above, which is
also the upstream rule: `_bt_compare` stops at
`IndexRelationGetNumberOfKeyAttributes`. The guard was one line
(`len(idx.IncludeColumns) > 0`) and is gone; the `checkunique` tier inherits the
widening for free, since `btIndexCheckUnique` runs under the same comparator.

Still out of scope:

- **Expression key columns** (`Columns[i] == ""`): there is no catalog column
  whose type drives the decode.
- **Types the key decoder cannot invert** (e.g. `NUMERIC`, whose encoding is
  documented as deliberately one-way, and `box`/`int4range`/`int4[]`). These hit
  the per-column decode-failure fallback and keep byte order for that column, so
  they are never a false positive — only a missed detection. Recorded in
  `.ralph/deferral_ledger.md`.
- **The `--checkunique` tier**, tracked separately under M0119-0006.

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
- Second slice (2026-08-10):
  `TestBtIndexCheck_OpClassDamageDetectedText` (user opclass on a `text` key
  column) and `TestBtIndexCheck_OpClassDamageDetectedComposite` (two-column
  `(text, int4 int4_fickle2_ops)` index, damage on the *trailing* column) —
  both shapes the first slice declined outright and therefore reported clean.
  Each keeps the clean-then-damaged pair, so the no-false-positive half is
  asserted for the widened surface too.
- Fourth slice (2026-08-10):
  `TestBtIndexCheck_OpClassDamageDetectedInclude` — a user opclass on the key
  column of an `(i int4_fickle3_ops) INCLUDE (payload)` index. Clean-then-damaged
  pair as above; confirmed **non-vacuous** (restoring the `IncludeColumns` guard
  makes the damaged covering index report clean and the test fail).
- `go test ./internal/amcheck/... ./internal/catalog/...` and the existing
  `TestBtIndexCheck_*` suite PASS unchanged.
