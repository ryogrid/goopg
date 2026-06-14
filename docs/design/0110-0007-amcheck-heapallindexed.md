# 0110-0007 — amcheck heapallindexed verification tier

Status: accepted (partial)
Milestone: M0110-0003
Date: 2026-06-14

> Scope note: this is the last B-tree verification tier of the amcheck verify
> engine tracked by [0110-0005](0110-0005-verify-heapam-engine.md). It consumes
> the Bloom filter primitive landed in [0110-0006](0110-0006-amcheck-bloom-filter.md)
> and lands the fingerprint+probe **core** of `heapallindexed` as a pure
> function. The lazy heap scan + index-tuple formation and the SQL surface that
> drive it remain deferred (see "Deferred"), still blocked on a clean working
> tree. Follows the same engine-first/wire-later pattern as the rest of
> `internal/amcheck`.

## Goal

Port the heapallindexed logic of upstream amcheck's `bt_check_every_level()` /
`bt_tuple_present_callback()` (`postgres/contrib/amcheck/verify_nbtree.c`) into
`internal/amcheck` as `heapallindexed.go` (`VerifyBtreeHeapAllIndexed`). This is
the only verification logic `bt_index_check(index, heapallindexed => true)`
performs that the goopg engine still lacked after the structural tiers
(loops #55–#59) and the Bloom primitive (loop #60).

## Why this is the right next slice

After 0110-0006 every *ingredient* of heapallindexed exists in
`internal/amcheck` except the algorithm that ties them together: the
fingerprint phase (add every index leaf entry to the filter) and the probe
phase (for each heap tuple, form the index tuple it should produce and assert
the filter does not lack it). Both phases are deterministic functions of two
entry sets, so the *core* can land as a pure function with zero coupling to the
catalog/SQL surface — which remains blocked on the separate manual session's
uncommitted gen-column WIP across parser/planner/executor/catalog. What the core
cannot do on its own — walk the live leaf level, run a snapshot-consistent heap
scan, and re-form an index tuple from each heap tuple via the index `TupleDesc`
— is exactly the plumbing the wire-later SQL surface owns. Splitting here keeps
this loop in new, additive files only.

## What upstream does

1. **Fingerprint** (`verify_nbtree.c:1487-1518`). As the structural walk reads
   each leaf page, every non-dead leaf `IndexTuple` is normalized
   (`bt_normalize_tuple`) and added to the filter (`bloom_add_element`).
   Posting-list tuples are exploded into one "plain" tuple per heap TID
   (`bt_posting_plain_tuple`) and each is fingerprinted separately.
2. **Probe** (`verify_nbtree.c:2781 bt_tuple_present_callback`).
   `table_index_build_scan` drives one callback per live heap tuple: it forms
   the `IndexTuple` a fresh `CREATE INDEX` would emit (`index_form_tuple`),
   normalizes it, and probes with `bloom_lacks_element`. A lacking result raises
   `heap tuple (b,o) from table "T" lacks matching index tuple within index "I"`.

The check is sound because the Bloom filter has **no false negatives**: an added
element is never reported absent, so a heap tuple that genuinely has a matching
index entry is never falsely flagged. False positives only ever *mask* a small
fraction (<2% at bloomCreate's 2-bytes/element sizing) of genuinely-missing
entries — the check never invents corruption. Upstream randomizes the seed per
run so a different masked subset is caught on re-check.

## Design

`VerifyBtreeHeapAllIndexed(indexLeafEntries, heapEntries []btree.LeafEntry, indexName, tableName string, seed uint64) []BtreeReport`

- Builds a filter sized to `len(indexLeafEntries)` (the exact count — the slice
  is in hand, so no `RelationGetNumberOfBlocks`-based estimate is needed),
  `bloomCreate(n, 64MB, seed)`.
- Fingerprints every index leaf entry, then probes every heap entry; emits one
  `BtreeReport` (verbatim upstream message + the heap block) per lacking entry.
- Returns findings, never a Go error, like the rest of the package: an empty
  index over a non-empty heap correctly reports every heap row; an empty heap
  reports nothing.

Both phases run entries through one `fingerprintLeafEntry(e)` — `TID.Block` (4 B,
big-endian) ++ `TID.Offset` (2 B) ++ raw key bytes. The single load-bearing
invariant is that the fingerprint and probe phases encode an identical logical
`(key, TID)` to identical bytes; they do, because both consume the same
`btree.LeafEntry` shape (the sibling-path discipline, `pattern_sibling_paths_must_agree`).

A new exported `btree.PageLeafEntries(p)` is the canonical on-disk reader the
wire-later fingerprint phase uses: it returns each leaf line pointer as a
`btree.LeafEntry{Key, TID}`, **expanding** posting-list items to one entry per
TID (unlike `PageItemKeys`, which collapses them to one separator key for the
item-order tier). Decoding through this single source of truth — beside
`PageItemKeys`/`PageDownlinks` — guards against the v3→v4 inline-item layout
drift.

## goopg / upstream divergences

- **Fingerprint unit.** Upstream fingerprints raw normalized `IndexTuple` bytes;
  goopg leaf items have no PG `IndexTuple` header, so the fingerprint is a
  deterministic `(TID, key)` encoding. Equivalent for the membership contract.
- **No normalization.** `bt_normalize_tuple` exists only to undo a TOAST
  compression representation mismatch between `index_form_tuple` and `btinsert`.
  goopg does not TOAST index keys, so the leaf key bytes and the heap-formed key
  bytes are already one canonical form (the same `EncodeXxxKey` output); the
  normalization step is a documented no-op and is omitted.
- **Pure function vs scan callback.** The lazy heap scan, snapshot registration,
  and HOT-chain root-TID remapping `table_index_build_scan` performs are
  caller/wire-later concerns; this tier ports the fingerprint+probe core over
  two slices.
- **Seed.** A parameter rather than a per-run `pg_prng_uint64`, so the wire-later
  caller randomizes while tests pin it for determinism.

## Tests

`internal/amcheck/heapallindexed_test.go` (6):

- `NoFalseNegatives` (load-bearing): n=100k healthy entries → 0 reports (runs the
  filter in its real ~0.5-density regime, not the over-provisioned 1MB-floor
  regime).
- `DetectsMissingHeapTuple`: one index entry removed → exactly one report, exact
  message + block.
- `DistinguishesByTID`: same key, different TID → reported (proves the TID is in
  the fingerprint, not just the key).
- `EmptyIndex` / `EmptyHeap`: boundary outcomes.
- `SharedKeyDistinctTIDs`: posting-list semantics at the engine level — three
  entries sharing one key, dropping the middle TID flags exactly that one row.

`internal/access/btree/posting_test.go` (1): `TestPageLeafEntries` builds a leaf
page with a plain item + a 3-TID posting item and asserts the reader expands to
4 entries with the right keys/TIDs.

## Index-side relation walk (loop #65)

The fingerprint phase's leaf-level enumeration is now ported as a driver behind
the same `PageSource` seam the cross-page / cross-level B-tree tiers take
(`internal/amcheck/heapallindexed_relation.go`), making it symmetric with the
heap engine's `VerifyHeapRelation` (loop #63). Two entry points:

- `CollectBtreeLeafEntries(src PageSource) ([]btree.LeafEntry, error)` — reads
  the metapage, descends `Root` → leftmost leaf following the slot-1
  (negative-infinity) downlink at each internal level (`leftmostLeafBlock`), then
  walks `btpo_next` across the leaf level collecting `btree.PageLeafEntries`
  (posting items expanded to one entry per TID). Fully deleted leaf pages are
  skipped (deleted-page exemption, matching the per-page tiers). A `Root` of the
  metapage / `InvalidBlockNumber` (no key level — a real goopg tree always roots
  at block 1) and an empty leaf both yield zero entries with no error; a read
  error, unparseable page, or sibling/downlink **cycle** is returned as an error
  rather than silently fingerprinting a truncated set (a truncated set would
  manufacture spurious "lacks matching index tuple" reports — the heapallindexed
  soundness invariant). Both the descent and the sibling walk are bounded by
  visited-sets so a corrupt cycle terminates.
- `VerifyBtreeHeapAllIndexedRelation(idxSrc, heapEntries, indexName, tableName,
  seed)` — composes `CollectBtreeLeafEntries` (fingerprint set) with the
  caller-supplied `heapEntries` (probe set) through the pure
  `VerifyBtreeHeapAllIndexed` core.

**Scope boundary** (where loop #63's heap driver and upstream draw the line): the
heap scan + `index_form_tuple` that produce `heapEntries` are catalog- and
`TupleDesc`-coupled (they need the index's key columns, expressions, and
collations to re-form each heap tuple's would-be index tuple), so they stay at
the wire layer and are passed in as a slice — the shape
`VerifyBtreeHeapAllIndexed` already consumes. What this file ports is exactly the
part that is a deterministic function of the index's page bytes.

Tests (`heapallindexed_relation_test.go`, 10): single root-leaf, multi-level
descent-then-sibling walk (order asserted), no-key-level, empty leaf,
deleted-leaf-skipped, sibling cycle (errors), descent read error; plus the
composing driver clean / missing-entry (exact upstream message + block) /
index-walk-error-propagates. New helpers `makeLeafPage` / `btLeafRaw` /
`makeMetaWithRoot` / `makeDeletedLeaf` self-check through the real readers.

## Deferred (resume points)

- **Heap scan + index-tuple formation** (the `heapEntries` producer). The
  wire-later caller runs a snapshot-consistent heap scan that re-forms each live
  heap tuple's index tuple via the index `TupleDesc` (the `index_form_tuple`
  analog), feeding the result to `VerifyBtreeHeapAllIndexedRelation`. Needs the
  heap relation + index `TupleDesc` — catalog coupling. (The index-side leaf walk
  is now done — see above.)
- **SQL surface.** `CREATE EXTENSION amcheck` + `bt_index_check(index,
  heapallindexed => true)` wiring; promotes `AC-002`. Blocked on a clean tree.
- **Hash unification** (carried from 0110-0006): substitute the shared Jenkins
  hash for `bloomHash64` once it can leave `internal/executor` without
  cross-package entanglement. Distribution-only; no contract change.

## References

- `postgres/contrib/amcheck/verify_nbtree.c` —
  `bt_check_every_level` (398), fingerprint site (1487-1518),
  `bt_tuple_present_callback` (2781), `bt_normalize_tuple` (2850).
- [0110-0005](0110-0005-verify-heapam-engine.md),
  [0110-0006](0110-0006-amcheck-bloom-filter.md).
