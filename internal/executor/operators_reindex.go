package executor

// operators_reindex.go — REINDEX statement executor (M0097-0023).
//
// REINDEX INDEX name validates the index exists; REINDEX TABLE validates
// the table. Physical reindex is a no-op in goopg v0 (no physical btree
// rebuild). Raises 42P01 for nonexistent targets matching PostgreSQL.

import (
	"fmt"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

type reindexOp struct {
	stmt *parser.ReindexStmt
	ctx  *Context
	done bool
}

func newReindexOp(s *parser.ReindexStmt) *reindexOp { return &reindexOp{stmt: s} }

func (o *reindexOp) Schema() planner.Schema { return nil }

func (o *reindexOp) Open(ctx *Context) error {
	o.ctx = ctx
	return nil
}

func (o *reindexOp) Close() error { return nil }

func (o *reindexOp) Next() (TupleSlot, error) {
	if o.done {
		return nil, EOF
	}
	o.done = true

	if o.stmt.Name == "" || o.ctx == nil || o.ctx.Catalog == nil {
		return nil, EOF
	}

	name := parser.ObjectName{Name: o.stmt.Name}
	// Try schema-qualified form.
	if dotIdx := indexOfDot(o.stmt.Name); dotIdx >= 0 {
		name.Schema = o.stmt.Name[:dotIdx]
		name.Name = o.stmt.Name[dotIdx+1:]
	}

	switch o.stmt.ObjectType {
	case "INDEX":
		if _, ok := o.ctx.Catalog.LookupIndex(name); !ok {
			// Try unqualified.
			if _, ok2 := o.ctx.Catalog.LookupIndex(parser.ObjectName{Name: name.Name}); !ok2 {
				return nil, &ExecError{
					Code:    "42P01",
					Message: fmt.Sprintf("relation %q does not exist", o.stmt.Name),
				}
			}
		}
	case "TABLE":
		if _, ok := o.ctx.Catalog.LookupTable(name); !ok {
			if _, ok2 := o.ctx.Catalog.LookupTable(parser.ObjectName{Name: name.Name}); !ok2 {
				return nil, &ExecError{
					Code:    "42P01",
					Message: fmt.Sprintf("relation %q does not exist", o.stmt.Name),
				}
			}
		}
	}
	// Physical reindex is a no-op in v0.
	return nil, EOF
}

// indexOfDot returns the index of '.' in s, or -1 if not present.
func indexOfDot(s string) int {
	for i, c := range s {
		if c == '.' {
			return i
		}
	}
	return -1
}
