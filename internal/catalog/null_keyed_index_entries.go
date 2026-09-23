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
