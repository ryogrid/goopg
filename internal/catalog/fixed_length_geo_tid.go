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

// fixedLengthMoney is the cluster capability "money columns are stored as
// PG's 8-byte Cash (int64 cents, typbyval, typalign 'd')" rather than goopg's
// older varlena text. Same per-cluster marker mechanism as
// fixedLengthGeoTid. M0149-0001k1.
var fixedLengthMoney atomic.Bool

// FixedLengthMoneyFeature is the capability's name in the marker file.
const FixedLengthMoneyFeature = "fixed_length_money"

// SetFixedLengthMoney records whether the open cluster has the capability.
func SetFixedLengthMoney(on bool) { fixedLengthMoney.Store(on) }

// FixedLengthMoney reports whether the open cluster stores money as Cash.
func FixedLengthMoney() bool { return fixedLengthMoney.Load() }

// fixedLengthAclItem is the cluster capability "aclitem columns are stored
// as PG's 16-byte AclItem (typalign 'd')" rather than goopg's older varlena
// text. M0149-0001k2.
var fixedLengthAclItem atomic.Bool

// FixedLengthAclItemFeature is the capability's name in the marker file.
const FixedLengthAclItemFeature = "fixed_length_aclitem"

// SetFixedLengthAclItem records whether the open cluster has the capability.
func SetFixedLengthAclItem(on bool) { fixedLengthAclItem.Store(on) }

// FixedLengthAclItem reports whether the open cluster stores aclitem as
// AclItem.
func FixedLengthAclItem() bool { return fixedLengthAclItem.Load() }
