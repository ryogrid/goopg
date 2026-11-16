package catalog

import "sync/atomic"

// fixedLengthGeoTid is the cluster capability "point / lseg / box / line /
// circle / tid columns are stored in PostgreSQL's fixed-length form"
// (internal/utils/adt/fixedgeo), not as goopg's older varlena text. A heap
// value carries no format marker, so the unit is the cluster, as for
// null_keyed_index_entries: initdb of a capable binary lists the feature in
// global/pg_goopg_features and open sets this flag from it. Older clusters
// keep the flag off and keep reading and writing varlena, so no stored value
// changes meaning. PhysicalTypeIsVarlena / PhysicalTypeAlign, the executor's
// heap codec and the pgoutput decoder all consult it. M0149-0001k.
var fixedLengthGeoTid atomic.Bool

// FixedLengthGeoTidFeature is the capability's name in the marker file.
const FixedLengthGeoTidFeature = "fixed_length_geometric_tid"

// SetFixedLengthGeoTid records whether the open cluster has the capability.
// Set once at open; tests set it explicitly.
func SetFixedLengthGeoTid(on bool) { fixedLengthGeoTid.Store(on) }

// FixedLengthGeoTid reports whether the open cluster stores point / lseg /
// box / line / circle / tid in PG's fixed-length form.
func FixedLengthGeoTid() bool { return fixedLengthGeoTid.Load() }

// isFixedGeoTidName reports whether lower is one of the capability's types.
func isFixedGeoTidName(lower string) bool {
	switch lower {
	case "point", "lseg", "box", "line", "circle", "tid":
		return true
	}
	return false
}
