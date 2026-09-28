package catalog

import "strings"

// M0146-0028f — built-in function flags the planner's pull-up gates need.
//
// PG decides `hasTargetSRFs` and `contain_volatile_functions` from pg_proc's
// proretset and provolatile. goopg's built-in functions are dispatched by name
// in the executor and have no pg_proc rows carrying those flags, so the two
// name sets are generated from PG 18.3's pg_proc.dat
// (cmd/gen-pg-proc-data -flags → builtin_proc_flags_gen.go). A name is
// flagged when ANY of its overloads is, the conservative reading for a gate
// that declines on a hit.

var (
	builtinSetReturningProcs = nameSet(builtinSetReturningProcNames)
	builtinVolatileProcs     = nameSet(builtinVolatileProcNames)
	builtinProcs             = nameSet(builtinProcNames)
)

func nameSet(names []string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// BuiltinProcReturnsSet reports whether any built-in overload of name is
// set-returning (pg_proc.proretset). Case-insensitive.
func BuiltinProcReturnsSet(name string) bool {
	return builtinSetReturningProcs[strings.ToLower(name)]
}

// BuiltinProcIsVolatile reports whether any built-in overload of name is
// volatile (pg_proc.provolatile = 'v'). Case-insensitive.
func BuiltinProcIsVolatile(name string) bool {
	return builtinVolatileProcs[strings.ToLower(name)]
}

// IsBuiltinProcName reports whether name is a PG 18.3 built-in function
// (any overload in pg_proc.dat). Case-insensitive.
func IsBuiltinProcName(name string) bool {
	return builtinProcs[strings.ToLower(name)]
}
