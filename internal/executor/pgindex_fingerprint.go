package executor

import "github.com/goopg/goopg/internal/catalog"

// ---------------------------------------------------------------------------
// M0130-S11.4 slice 3b-2c-ii-B2-c-viii — the fingerprint funnel.
//
// B2-c-iii .. B2-c-vii gave every TREE-KEY producer a name that states its
// role: `indexSearchKey` (scan), `indexRowProbeKey` / `indexEntryKey` (runtime
// writers), `indexBuildEntryKey` (bulk build), `arbiterProbeKey` /
// `arbiterEntryKey` (upsert). Each resolves `pgIndexKeyDesc` and switches
// format. What is left calling the raw blob encoders is a different kind of
// caller entirely, and this file is its name.
//
// A FINGERPRINT is an encoding of a value (or of a row's key values) that is
// only ever compared with, or hashed alongside, ANOTHER fingerprint computed
// the same way. It is never handed to a btree: nothing descends with it,
// nothing is stored under it. There are two shapes and six call sites:
//
//   - whole-key (`indexKeyFingerprint`):
//     `indexKeyColumnsChanged` — did an UPDATE touch this index at all?
//     `ssiRecordHashIndexInsert` — which SSI bucket page tag does this row hash
//     into? (must match what `ssiRecordHashBucketRead` computed for a reader)
//
//   - per-column (`indexColumnFingerprint`), all on the NULLS NOT DISTINCT
//     paths, where a NULL key column means the row has no btree entry at all so
//     uniqueness has to be decided by a heap scan:
//     `nndKeyColumnsEqual` (old vs new row version),
//     `resolveNNDKeyColsFromRow` (the candidate's per-column bytes) and
//     `scanNNDLiveMatches` (each scanned live row's, vs the candidate's).
//
// Why they must NOT follow the flip: a tuple-format key carries the row's heap
// TID inside the image (`t_tid`, heapkeyspace's last key attribute), and every
// one of these six compares bytes derived from DIFFERENT heap tuples. Route
// them and `indexKeyColumnsChanged` reports "changed" for every UPDATE ever
// (re-probing every unique index), an SSI writer hashes into a bucket no reader
// holds, and the NND heap scan stops finding its duplicates — a unique
// constraint that silently admits a second NULL-keyed row. None of that is a
// parse failure; all of it is wrong rows or lost conflicts.
//
// So the policy is: goopg computes a key TWO ways for a describable index after
// the flip — the tuple image for the tree, the blob concatenation for the
// fingerprints — and `encodeIndexKeyFromCols` / `encodeBTreeKeyForColumn`
// therefore survive the flip rather than being deleted by it. That policy was
// previously stated only in comments on the individual callers (B2-c-iv's
// ledger row asked for exactly this helper); it is stated here once, and
// `pgindex_fingerprint_test.go` pins the six call sites so a later flip cannot
// route one by hand without deleting the guard.
//
// The equivalence a fingerprint relies on is worth naming, because it is not
// the same equivalence the tree uses. Blob column encodings are injective and
// order-preserving per type, so "equal blob bytes" means "equal values under
// the type's normalisation" — which is what these six callers actually want.
// The tuple format instead answers equality with
// `btree.ComparePGIndexTupleKeyAttrs` over per-column datums. The two agree for
// every type `buildPGIndexKeyDesc` accepts today (bytewise collations only);
// a non-deterministic collation would be the first place they could diverge,
// and `buildPGIndexKeyDesc` refuses those. See the ledger row for B2-c-viii.
//
// See docs/design/0130-0011-nbtree-pg-on-disk-format.md.
// ---------------------------------------------------------------------------

// indexKeyFingerprint encodes idx's key columns from row into bytes that are
// only ever compared with, or hashed together with, another fingerprint of the
// same index. It is deliberately format-INDEPENDENT: it takes no `*Context`,
// resolves no `pgIndexKeyDesc` and accepts no `storage.ItemPointer`, so it
// cannot acquire a heap TID even by accident.
//
// nil bytes with a nil error keeps `encodeIndexKeyFromCols`'s meaning — this
// row has no entry in this index (a NULL key column, or an expression key the
// projection cannot make) — and each caller decides what that means for it
// (`indexKeyColumnsChanged` falls back to the NULL-pattern comparison under
// NULLS NOT DISTINCT; `ssiRecordHashIndexInsert` skips the index).
func indexKeyFingerprint(idx *catalog.Index, cols []catalog.Column, row Row, cat catalog.Catalog) ([]byte, error) {
	return encodeIndexKeyFromCols(nil, idx, cols, row, cat)
}

// indexColumnFingerprint encodes ONE key column's value the way that column
// would be encoded inside a blob index key, for comparison against another
// column fingerprint of the same column. Same contract as
// `indexKeyFingerprint`: no context, no descriptor, no TID.
//
// The `pos` the underlying encoder takes is an error position only, and every
// fingerprint caller reports its own error (or, on the NND paths, treats an
// encode failure as "structurally undecidable" and falls back), so it is fixed
// at 0 here rather than threaded.
func indexColumnFingerprint(v Datum, col *catalog.Column) ([]byte, *ExecError) {
	// No ctx: fingerprint runs from a heap-decoded numeric-OID string, which
	// regIdentifierInput's numeric passthrough resolves (see the reg* arm's
	// nil-ctx contract). A name reaching this path errors rather than being
	// silently stored.
	return encodeBTreeKeyForColumn(nil, v, col, 0)
}
