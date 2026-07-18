package catalog

// Forward OID-resolution indexes for the canonical pg_node_tree resolver
// (02e item C S0). goopg ships the full PG18 pg_operator seed (PGOperatorAllEntries)
// and pg_proc name/arg-type reverse maps (pgProcNamesByOID / pgProcArgTypeNamesByOID),
// but only *reverse* (OID → name) lookups. Emitting a canonical OpExpr/FuncExpr
// needs the *forward* direction — (operator spelling + operand type OIDs) → the
// operator row, and (function name + argument type OIDs) → funcid — which these
// lazily-built indexes provide. Built-in OIDs match PG18's bootstrap OIDs, so a
// real PG standby resolves the emitted funcid/opno identically.

import (
	"strconv"
	"strings"
	"sync"
)

var (
	nodeOperatorIndexOnce sync.Once
	nodeOperatorIndex     map[string]OperatorEntry // builtinOperatorKey(name,left,right) → row

	nodeProcIndexOnce sync.Once
	nodeProcIndex     map[string]uint32 // procNodeKey(name,argOIDs) → funcid
)

func buildNodeOperatorIndex() {
	entries := PGOperatorAllEntries()
	nodeOperatorIndex = make(map[string]OperatorEntry, len(entries))
	for _, e := range entries {
		// Binary operators only (the node resolver emits OpExpr for two-operand
		// operators; left-unary/right-unary handling can extend this later).
		if e.Kind != 'b' {
			continue
		}
		key := builtinOperatorKey(e.Name, e.LeftType, e.RightType)
		// pg_operator is unique on (oprname, oprleft, oprright, oprnamespace);
		// all seeded rows are pg_catalog (namespace 11), so keep the first.
		if _, dup := nodeOperatorIndex[key]; !dup {
			nodeOperatorIndex[key] = e
		}
	}
}

// LookupOperatorForNode resolves a binary operator by its spelling and its two
// operand type OIDs to the full pg_operator row (carrying opno=OID, oprcode
// funcid=Code, oprresult=ResultType). Returns false when no such built-in
// operator exists — the caller degrades to SQL text for that node.
func LookupOperatorForNode(name string, leftOID, rightOID uint32) (OperatorEntry, bool) {
	nodeOperatorIndexOnce.Do(buildNodeOperatorIndex)
	e, ok := nodeOperatorIndex[builtinOperatorKey(name, leftOID, rightOID)]
	return e, ok
}

// procNodeKey is the forward-index key for a function: name + parenthesized,
// comma-joined argument type OIDs (e.g. "upper(25)", "now()").
func procNodeKey(name string, argOIDs []uint32) string {
	var b strings.Builder
	b.WriteString(name)
	b.WriteByte('(')
	for i, oid := range argOIDs {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatUint(uint64(oid), 10))
	}
	b.WriteByte(')')
	return b.String()
}

func buildNodeProcIndex() {
	nodeProcIndex = make(map[string]uint32, len(pgProcNamesByOID))
	for funcid, name := range pgProcNamesByOID {
		argNames := pgProcArgTypeNamesByOID[funcid] // nil for zero-arg procs
		argOIDs := make([]uint32, 0, len(argNames))
		ok := true
		for _, tn := range argNames {
			oid := TypeNameToOID(tn)
			// TypeNameToOID falls back to text(25) for names it does not know
			// (pseudo-types like anyrange/anyelement/internal), which would make
			// e.g. lower(anyrange) collide with lower(text). Accept only an EXACT
			// resolution — one that round-trips through OIDToTypeName back to the
			// same name. Anything else (pseudo/unmapped type) makes the proc
			// non-forward-resolvable, so skip it; the resolver degrades to SQL
			// text for such calls.
			if oid == 0 || OIDToTypeName(oid) != tn {
				ok = false
				break
			}
			argOIDs = append(argOIDs, oid)
		}
		if !ok {
			continue
		}
		key := procNodeKey(name, argOIDs)
		// pg_proc is unique on (proname, proargtypes, pronamespace); keep first.
		if _, dup := nodeProcIndex[key]; !dup {
			nodeProcIndex[key] = funcid
		}
	}
}

// LookupProcForNode resolves a function by its name and argument type OIDs to
// its funcid. Returns false when the function is not a forward-resolvable
// built-in (unknown name, unmapped arg type, or an overload not seeded) — the
// caller degrades to SQL text for that node.
func LookupProcForNode(name string, argOIDs []uint32) (uint32, bool) {
	nodeProcIndexOnce.Do(buildNodeProcIndex)
	funcid, ok := nodeProcIndex[procNodeKey(name, argOIDs)]
	return funcid, ok
}
