package catalog

import "sync/atomic"

// nullKeyedIndexEntries is the cluster capability "tuple-format btree indexes
// store an entry for every heap row, NULL key columns included", as
// PostgreSQL does (index_form_tuple writes a null bitmap). goopg's writers
// used to skip NULL-keyed rows, so an index built before the capability
// existed lacks those entries, and nothing durable records which indexes
// have them. The unit is therefore the cluster: initdb of a NULL-storing
// binary writes the marker file global/pg_goopg_features naming the
// capability, and open sets this flag from it. Older clusters (no marker)
// keep the flag off: writers keep skipping and the planner keeps refusing
// scans that would need the entries. Design:
// docs/design/0100-0149/m-nightly-store-null-keyed-index-entries.md.
var nullKeyedIndexEntries atomic.Bool

// NullKeyedIndexEntriesFeature is the capability's name in the marker file.
const NullKeyedIndexEntriesFeature = "null_keyed_index_entries"

// SetNullKeyedIndexEntries records whether the open cluster has the
// capability. Set once at open; tests set it explicitly.
func SetNullKeyedIndexEntries(on bool) { nullKeyedIndexEntries.Store(on) }

// NullKeyedIndexEntries reports whether tuple-format indexes in the open
// cluster store NULL-keyed entries.
func NullKeyedIndexEntries() bool { return nullKeyedIndexEntries.Load() }

// indexFormatStoresNullKeys reports whether idx is kept in the tuple format
// (the only format whose writers file NULL-keyed entries). The test lives in
// the executor (buildPGIndexKeyDesc needs its type and comparator tables),
// which registers it here so the optimizer — which cannot import the
// executor — can ask through catalog. nil (never registered, e.g. an
// optimizer-only test binary) answers no.
var indexFormatStoresNullKeys func(*Index) bool

// SetIndexFormatStoresNullKeysFunc registers the tuple-format test. Called
// once by the executor package at init.
func SetIndexFormatStoresNullKeysFunc(f func(*Index) bool) { indexFormatStoresNullKeys = f }

// IndexHasNullKeyedEntries reports whether idx holds an entry for every heap
// row, NULL key columns included: the cluster has the null_keyed_index_entries
// capability and idx is tuple-format. Such an index is complete for any scan,
// whatever its key columns' nullability — the planner's NULL-key guard
// (optimizer indexUnboundKeysNullSafe) only has to protect the others.
func IndexHasNullKeyedEntries(idx *Index) bool {
	return idx != nil && NullKeyedIndexEntries() && indexFormatStoresNullKeys != nil && indexFormatStoresNullKeys(idx)
}
