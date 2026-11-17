package catalog

import "sync"

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
