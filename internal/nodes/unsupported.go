package nodes

// unsupported.go — the all-or-nothing shape check (02e §3). A canonical
// pg_node_tree must be emitted for the WHOLE expression or not at all: a real
// PG18 relcache FATALs on a pg_node_tree it cannot fully parse, so a
// partial/best-effort emit is worse than falling back to SQL text. SupportsExpr
// answers "would ResolveExpr produce a complete canonical tree?" without the
// caller having to keep the resolved node; ResolveExpr itself is already
// all-or-nothing (any unsupported sub-node aborts the whole resolution with
// ErrUnsupported), so this is a thin predicate wrapper over it.

import "github.com/goopg/goopg/internal/parser"

// SupportsExpr reports whether e (a column DEFAULT or statistics expression for
// a value of type targetType) is fully representable as a canonical scalar
// pg_node_tree. When false, the writer stores the expression as SQL text and a
// view additionally keeps relhasrules=false. targetType has the same meaning as
// in ResolveExpr (0 = unknown/any).
func SupportsExpr(e parser.Expr, targetType uint32) bool {
	_, err := ResolveExpr(e, targetType)
	return err == nil
}
