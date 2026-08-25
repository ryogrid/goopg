package postmaster

// Production wiring for the parser-rewrite dispatch hook
// (docs/design/not_ralph/03-strangler-migration.md §2): an init() here is
// sufficient because postmaster is linked into every server binary — unlike
// internal/sqlparser, which nothing imports directly and whose init() would
// therefore never run in production.
//
// The hook is INERT at P0: sqlparser.routedStmts is empty, so RouteBatch
// declines every batch (handled=false) and the legacy parser runs unchanged.
// Waves flip by adding entries there, gated per TODO.md.

import (
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/sqlparser"
)

func init() {
	parser.RouteBatch = sqlparser.RouteBatch
}
