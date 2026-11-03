package catalog

import (
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
