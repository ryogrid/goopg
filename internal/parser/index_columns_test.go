package parser

import "testing"

// TestIndexColumnSurfaceParity — the CREATE INDEX key-column surface.
//
// index_col was a bare `ColId` returning a string, so expression keys
// (`USING btree (f())`, `((a + b))`), per-column opclass (`seqno float8_ops`),
// COLLATE, ASC/DESC and NULLS order, `ON ONLY`, and `WITH (fillfactor = ...)`
// were all syntax errors — ~42 fragments across the must-pass regress cases.
// CreateIndexStmt has carried ColExprs / ColOrders / OnOnly / Fillfactor all
// along as slices PARALLEL to Columns, with Columns[i]=="" marking an
// expression key.
//
// Three grammar decisions here were forced by measurement, not taste:
//   - the key is `name_or_call` or `'(' a_expr ')'`, not a bare a_expr: a
//     trailing opclass ColId after an a_expr is ambiguous with everything that
//     CONTINUES an expression (VARYING, FILTER, WITHIN, YEAR, SECOND) — 14 S/R.
//   - the opclass is IDENT, not ColId: ColId also admits FILTER and WITHIN,
//     which are exactly the tokens that continue a function-call key — 4 S/R.
//   - COLLATE is handled by index_col itself rather than via
//     `a_expr COLLATE ColId`, because legacy records it as
//     ColOrders[i].Collation, not as a CollateExpr node.
//
// DESC implies NULLS FIRST unless the clause overrides it, matching PG.
func TestIndexColumnSurfaceParity(t *testing.T) {
	for _, q := range []string{
		// expression keys
		"CREATE INDEX i ON t USING btree (f())",
		"CREATE INDEX i ON t ((a + b))",
		"CREATE INDEX i ON t (a, (b + 1))",
		// opclass / collation / ordering
		"CREATE INDEX i ON t USING btree (seqno float8_ops)",
		"CREATE INDEX i ON t (a DESC)",
		"CREATE INDEX i ON t (a ASC)",
		"CREATE INDEX i ON t (a NULLS LAST)",
		"CREATE INDEX i ON t (a text_pattern_ops DESC NULLS FIRST)",
		`CREATE INDEX i ON t (a COLLATE "C")`,
		// table-level options
		"CREATE INDEX i ON ONLY t (a)",
		"CREATE INDEX i ON t (a) WITH (fillfactor = 10)",
		// combinations with what already worked
		"CREATE INDEX i ON t (a) WHERE a > 1",
		"CREATE INDEX i ON t (a) INCLUDE (b)",
		"CREATE UNIQUE INDEX i ON t (a, b)",
		"CREATE INDEX CONCURRENTLY ON t (a)",
		"CREATE INDEX i ON t USING gist (a)",
		"CREATE INDEX i ON s.t (a)",
		"CREATE INDEX i ON t (a)",
	} {
		assertParity(t, q)
	}
}
