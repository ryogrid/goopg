package pgnodes

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// benchLogResolver stubs the catalog metadata for the bench_log table used by
// the S3 query-tree goldens (query_roundtrip_test.go):
//
//	CREATE TABLE bench_log(client int, src text);   -- relid 16384
//
// client is int4 (attno 1, non-collatable), src is text (attno 2, default
// collation 100), exactly as a live PG18.3 catalog reports them.
type benchLogResolver struct{}

func (benchLogResolver) LookupRelation(schema, name string) (*RelationInfo, bool) {
	if (schema != "" && schema != "public") || name != "bench_log" {
		return nil, false
	}
	return &RelationInfo{
		Relid:   16384,
		Relname: "bench_log",
		Relkind: 'r',
		Columns: []ColumnInfo{
			{Name: "client", Attno: 1, TypeOID: OidInt4, Typmod: -1, Collation: 0},
			{Name: "src", Attno: 2, TypeOID: OidText, Typmod: -1, Collation: DefaultCollationOid},
		},
	}, true
}

func parseSelect(t *testing.T, sql string) *parser.SelectStmt {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse %q: %v", sql, err)
	}
	if len(stmts) != 1 {
		t.Fatalf("parse %q: got %d stmts, want 1", sql, len(stmts))
	}
	sel, ok := stmts[0].(*parser.SelectStmt)
	if !ok {
		t.Fatalf("parse %q: stmt type %T, want *SelectStmt", sql, stmts[0])
	}
	return sel
}

// TestResolveViewQuery is the forward-direction gate: resolving each view's
// goopg SELECT AST must produce a Query that serializes byte-for-byte to the
// exact pg_rewrite.ev_action PostgreSQL 18.3 stored for the same DDL. This is
// the query-tree analogue of resolver_expr_test.go's canonical-Out pins.
func TestResolveViewQuery(t *testing.T) {
	for _, tc := range []struct {
		name   string
		sql    string
		golden string
	}{
		{
			name:   "v_with_where",
			sql:    "SELECT client, src FROM bench_log WHERE client > 0",
			golden: goldenViewV,
		},
		{
			name:   "v2_funcexpr_no_where",
			sql:    "SELECT upper(src) AS us FROM bench_log",
			golden: goldenViewV2,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, err := ResolveViewQuery(parseSelect(t, tc.sql), benchLogResolver{})
			if err != nil {
				t.Fatalf("ResolveViewQuery: %v", err)
			}
			got := OutRuleAction([]Node{q})
			if got != tc.golden {
				t.Fatalf("ev_action mismatch:\n got=%s\nwant=%s", got, tc.golden)
			}
		})
	}
}

// TestResolveViewQueryRoundTrip closes the loop through the S3 codec: the
// resolved Query -> OutRuleAction -> ReadRuleAction -> OutRuleAction must be
// stable, proving the resolver emits a shape the reader accepts and re-emits
// identically.
func TestResolveViewQueryRoundTrip(t *testing.T) {
	q, err := ResolveViewQuery(parseSelect(t, "SELECT client, src FROM bench_log WHERE client > 0"), benchLogResolver{})
	if err != nil {
		t.Fatalf("ResolveViewQuery: %v", err)
	}
	first := OutRuleAction([]Node{q})
	nodes, err := ReadRuleAction(first)
	if err != nil {
		t.Fatalf("ReadRuleAction: %v", err)
	}
	if got := OutRuleAction(nodes); got != first {
		t.Fatalf("re-Out mismatch:\n first=%s\n reOut=%s", first, got)
	}
}

// TestResolveViewQueryStructure spot-checks the resolved IR so a byte-identical
// but semantically wrong resolution can't slip through: the selectedCols bias,
// the Var source-column provenance (resorigtbl/resorigcol), and the computed
// target's zeroed provenance.
func TestResolveViewQueryStructure(t *testing.T) {
	q, err := ResolveViewQuery(parseSelect(t, "SELECT client, src FROM bench_log WHERE client > 0"), benchLogResolver{})
	if err != nil {
		t.Fatalf("ResolveViewQuery: %v", err)
	}
	pi := q.RtePermInfos[0].(*RTEPermissionInfo)
	if len(pi.SelectedCols) != 2 || pi.SelectedCols[0] != 8 || pi.SelectedCols[1] != 9 {
		t.Errorf("selectedCols = %v, want [8 9]", pi.SelectedCols)
	}
	te0 := q.TargetList[0].(*TargetEntry)
	if te0.Resname != "client" || te0.Resorigtbl != 16384 || te0.Resorigcol != 1 {
		t.Errorf("target0 = {%q %d %d}, want {client 16384 1}", te0.Resname, te0.Resorigtbl, te0.Resorigcol)
	}

	// A computed target carries no source-column provenance.
	q2, err := ResolveViewQuery(parseSelect(t, "SELECT upper(src) AS us FROM bench_log"), benchLogResolver{})
	if err != nil {
		t.Fatalf("ResolveViewQuery v2: %v", err)
	}
	te := q2.TargetList[0].(*TargetEntry)
	if te.Resname != "us" || te.Resorigtbl != 0 || te.Resorigcol != 0 {
		t.Errorf("computed target = {%q %d %d}, want {us 0 0}", te.Resname, te.Resorigtbl, te.Resorigcol)
	}
	if _, ok := te.Expr.(*FuncExpr); !ok {
		t.Errorf("computed target expr type = %T, want *FuncExpr", te.Expr)
	}
}

// TestResolveViewQueryUnsupported confirms the resolver returns ErrUnsupported
// (the writer's "store SQL text, keep relhasrules=false" signal) for shapes
// outside the single-base-relation SELECT subset, rather than emitting a wrong
// canonical tree.
func TestResolveViewQueryUnsupported(t *testing.T) {
	// Each shape is outside the single-base-relation SELECT subset: GROUP BY,
	// ORDER BY, LIMIT, DISTINCT, an aliased FROM (deferred), a bare star, two
	// relations, an unknown column, an unknown relation, and a set operation.
	for _, sql := range []string{
		"SELECT client FROM bench_log GROUP BY client",
		"SELECT client FROM bench_log ORDER BY client",
		"SELECT client FROM bench_log LIMIT 1",
		"SELECT DISTINCT client FROM bench_log",
		"SELECT client FROM bench_log b",
		"SELECT * FROM bench_log",
		"SELECT client FROM bench_log, bench_log b2",
		"SELECT missing FROM bench_log",
		"SELECT client FROM nope",
		"SELECT client FROM bench_log UNION SELECT client FROM bench_log",
	} {
		if _, err := ResolveViewQuery(parseSelect(t, sql), benchLogResolver{}); err == nil {
			t.Errorf("ResolveViewQuery(%q) = nil error, want ErrUnsupported", sql)
		}
	}
}
