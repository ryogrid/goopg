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

// relTypeOf is pg_class.reltype for t: its reserved rowtype, else the
// captured system rowtype.
func relTypeOf(t *Table) uint32 {
	if t.RowTypeOID != 0 {
		return t.RowTypeOID
	}
	return BuiltinRelType(t.OID)
}

// LookupTableByRowTypeOID finds the relation whose rowtype (or, with
// array=true, whose rowtype's array type) is oid. OIDs come from one
// counter, so every database namespace is searched.
func (c *InMemory) LookupTableByRowTypeOID(oid uint32) (t *Table, array bool, ok bool) {
	if oid < FirstUserOID {
		return nil, false, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, ns := range c.namespaces {
		for _, tbl := range ns.tables {
			if tbl.RowTypeOID == 0 {
				continue
			}
			if tbl.RowTypeOID == oid {
				return tbl, false, true
			}
			if tbl.RowTypeOID-1 == oid {
				return tbl, true, true
			}
		}
	}
	return nil, false, false
}

// LookupCompositeTypeByRelOID finds a standalone composite type by the OID of
// its implicit relation (relkind 'c'). M0149-0001h1a34.
func (c *InMemory) LookupCompositeTypeByRelOID(oid uint32) (*CompositeType, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, ct := range c.compositeTypes {
		if ct.RelOID == oid {
			return ct, true
		}
	}
	return nil, false
}
