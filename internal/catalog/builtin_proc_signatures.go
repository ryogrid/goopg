package catalog

import (
	"sort"
	"strings"
	"sync"
)

// BuiltinProcSignatures returns the input argument type names (pg_proc.dat
// proargtypes typnames, e.g. "text", "_text", "anyelement") of every PG 18
// built-in function or aggregate named name, one slice per overload — the
// pg_proc rows FuncnameGetCandidates scans. Built once from the generated
// pgProcNamesByOID / pgProcArgTypeNamesByOID indexes (M0148-0001f3).
func BuiltinProcSignatures(name string) [][]string {
	builtinSigsOnce.Do(func() {
		builtinSigs = make(map[string][][]string, len(pgProcNamesByOID))
		for oid, n := range pgProcNamesByOID {
			args, ok := pgProcArgTypeNamesByOID[oid]
			if !ok {
				continue
			}
			builtinSigs[n] = append(builtinSigs[n], args)
		}
	})
	return builtinSigs[strings.ToLower(name)]
}

var (
	builtinSigsOnce sync.Once
	builtinSigs     map[string][][]string
)

var (
	builtinProcOIDsOnce sync.Once
	builtinProcOIDs     map[string][]uint32
)

// BuiltinProcOIDsByName lists every pg_catalog function OID named exactly
// name (each overload; the caller case-folds an unquoted name), ascending. Pair with RegprocedureNameParts for the
// argument typnames. M0149-0001h1a32.
func BuiltinProcOIDsByName(name string) []uint32 {
	builtinProcOIDsOnce.Do(func() {
		builtinProcOIDs = make(map[string][]uint32, len(pgProcNamesByOID))
		for oid, n := range pgProcNamesByOID {
			builtinProcOIDs[n] = append(builtinProcOIDs[n], oid)
		}
		for _, oids := range builtinProcOIDs {
			sort.Slice(oids, func(i, j int) bool { return oids[i] < oids[j] })
		}
	})
	return builtinProcOIDs[name]
}
