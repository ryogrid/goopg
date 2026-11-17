package catalog

import (
	"strconv"
	"sync"
)

// Name- and OID-keyed indexes over the full pg_operator.dat seed
// (PGOperatorAllEntries), for the regoper / regoperator input and output
// functions (regproc.c regoperin / regoperatorin / regoperout /
// regoperatorout). The hand-curated builtinOperatorsByKey covers only the
// operators goopg's DDL surface names; these cover every seeded row.
// M0149-0001h1a14.

var (
	seedOperatorsOnce   sync.Once
	seedOperatorsByName map[string][]OperatorEntry
	seedOperatorsByOID  map[uint32]OperatorEntry
)

func buildSeedOperatorIndexes() {
	all := PGOperatorAllEntries()
	seedOperatorsByName = make(map[string][]OperatorEntry, len(all))
	seedOperatorsByOID = make(map[uint32]OperatorEntry, len(all))
	for _, e := range all {
		seedOperatorsByName[e.Name] = append(seedOperatorsByName[e.Name], e)
		seedOperatorsByOID[e.OID] = e
	}
}

// BuiltinOperatorsByName returns every seeded pg_catalog operator spelled
// name (operator names are case-sensitive), in seed order.
func BuiltinOperatorsByName(name string) []OperatorEntry {
	seedOperatorsOnce.Do(buildSeedOperatorIndexes)
	return seedOperatorsByName[name]
}

// BuiltinOperatorEntryByOID returns the seeded pg_operator row for oid.
func BuiltinOperatorEntryByOID(oid uint32) (OperatorEntry, bool) {
	seedOperatorsOnce.Do(buildSeedOperatorIndexes)
	e, ok := seedOperatorsByOID[oid]
	return e, ok
}

// UserOperatorsByName returns the defined (non-shell) user operators spelled
// name, in OID order.
func (c *InMemory) UserOperatorsByName(name string) []*UserOperator {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []*UserOperator
	for _, op := range c.userOperators {
		if op.FuncOID != 0 && op.Name == name {
			out = append(out, op)
		}
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].OID < out[j-1].OID; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

var (
	seedOperatorViewOnce sync.Once
	seedOperatorView     [][]string
)

// seedOperatorViewRows renders PGOperatorAllEntries as the virtual
// pg_operator view's text cells (oid, oprname, oprnamespace, oprowner,
// oprkind, oprcanmerge, oprcanhash, oprleft, oprright, oprresult, oprcom,
// oprnegate, oprcode, oprrest, oprjoin). Built once; the rows are shared
// and read-only. M0149-0001h1a19.
func seedOperatorViewRows() [][]string {
	seedOperatorViewOnce.Do(func() {
		u := func(v uint32) string { return strconv.FormatUint(uint64(v), 10) }
		for _, e := range PGOperatorAllEntries() {
			seedOperatorView = append(seedOperatorView, []string{
				u(e.OID), e.Name, u(e.Namespace), u(e.Owner), string(e.Kind),
				boolToPGChar(e.CanMerge), boolToPGChar(e.CanHash),
				u(e.LeftType), u(e.RightType), u(e.ResultType),
				u(e.Commutator), u(e.Negator), u(e.Code), u(e.Restrict), u(e.Join),
			})
		}
	})
	return seedOperatorView
}

// Seed row counts of the generated built-in catalog rows
// (pg_catalog_seed_rows_generated.go), for callers that need to skip past
// them to the user rows. M0149-0001h1a20.
func SeedPgOpfamilyRowCount() int { return len(seedPgOpfamilyRows) }
func SeedPgOpclassRowCount() int  { return len(seedPgOpclassRows) }
func SeedPgAmopRowCount() int     { return len(seedPgAmopRows) }
func SeedPgAmprocRowCount() int   { return len(seedPgAmprocRows) }
func SeedPgCastRowCount() int     { return len(seedPgCastRows) }

// BuiltinFormatTypeName is format_type(oid, NULL) for a pg_catalog type, as a
// PG 18.3 initdb prints it (regtypeout / format_type_be). M0149-0001h1a21.
func BuiltinFormatTypeName(oid uint32) (string, bool) {
	n, ok := builtinFormatTypeNames[oid]
	return n, ok
}
func SeedPgConversionRowCount() int { return len(seedPgConversionRows) }
func SeedPgRangeRowCount() int      { return len(seedPgRangeRows) }

var (
	builtinFormatTypeOIDsOnce sync.Once
	builtinFormatTypeOIDs     map[string]uint32
)

// BuiltinTypeOIDByFormatName is the reverse of BuiltinFormatTypeName: the
// pg_catalog type whose format_type spelling is name (cstring, anyelement,
// int4range, character varying, integer[]). M0149-0001h1a22.
func BuiltinTypeOIDByFormatName(name string) (uint32, bool) {
	builtinFormatTypeOIDsOnce.Do(func() {
		builtinFormatTypeOIDs = make(map[string]uint32, len(builtinFormatTypeNames))
		for oid, n := range builtinFormatTypeNames {
			builtinFormatTypeOIDs[n] = oid
		}
	})
	oid, ok := builtinFormatTypeOIDs[name]
	return oid, ok
}
