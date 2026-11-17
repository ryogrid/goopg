package catalog

import "sync/atomic"

// builtinRelTypes maps a system relation's OID to the OID of its composite
// rowtype (pg_class.reltype), as a PG 18.3 initdb assigns it. initdb
// registers the captured map at startup (pgClassRowtypeOIDs); the virtual
// pg_class renderer reads it. M0149-0001h1a34.
var builtinRelTypes atomic.Pointer[map[uint32]uint32]

// SetBuiltinRelTypes installs the system relation → rowtype OID map.
func SetBuiltinRelTypes(m map[uint32]uint32) { builtinRelTypes.Store(&m) }

// BuiltinRelType is pg_class.reltype for a system relation: its rowtype OID,
// or 0 when it has none (an index, a sequence, a TOAST table) or the map is
// not installed.
func BuiltinRelType(relOID uint32) uint32 {
	if relOID >= FirstUserOID {
		return 0
	}
	if m := builtinRelTypes.Load(); m != nil {
		return (*m)[relOID]
	}
	return 0
}
