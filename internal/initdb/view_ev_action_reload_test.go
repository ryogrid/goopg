package initdb

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/nodes"
)

// benchLogStubResolver mirrors the pgnodes resolver_query_test stub so this test
// can synthesize a real canonical ev_action for the reload discriminator to
// decode. bench_log(client int4, src text), relid 16384.
type benchLogStubResolver struct{}

func (benchLogStubResolver) LookupRelation(schema, name string) (*nodes.RelationInfo, bool) {
	if (schema != "" && schema != "public") || name != "bench_log" {
		return nil, false
	}
	return &nodes.RelationInfo{
		Relid:   16384,
		Relname: "bench_log",
		Relkind: 'r',
		Columns: []nodes.ColumnInfo{
			{Name: "client", Attno: 1, TypeOID: nodes.OidInt4, Typmod: -1, Collation: 0},
			{Name: "src", Attno: 2, TypeOID: nodes.OidText, Typmod: -1, Collation: nodes.DefaultCollationOid},
		},
	}, true
}

// canonicalEvAction resolves the view SELECT into the exact ev_action bytes the
// writer (executor.writeViewRewriteRow) would store.
func canonicalEvAction(t *testing.T, sql string) string {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil || len(stmts) != 1 {
		t.Fatalf("parse %q: %v (n=%d)", sql, err, len(stmts))
	}
	sel, ok := stmts[0].(*parser.SelectStmt)
	if !ok {
		t.Fatalf("parse %q: not a SelectStmt", sql)
	}
	q, err := nodes.ResolveViewQuery(sel, benchLogStubResolver{})
	if err != nil {
		t.Fatalf("ResolveViewQuery %q: %v", sql, err)
	}
	return nodes.OutRuleAction([]nodes.Node{q})
}

// TestRebuildViewFromEvAction is the reload-side gate for M0123-S3 sub-slice 2c:
// loadViewsFromHeap must decode BOTH ev_action forms. A canonical pg_node_tree
// (leading "({") goes through pgnodes.ReadRuleAction -> RebuildViewQuery and
// reports canonical=true (so RuleIsCanonical / relhasrules survive restart); a
// legacy SQL-text SELECT goes through parser.Parse and reports canonical=false;
// garbage in either shape is ok=false so the view degrades to a plain relation
// rather than failing startup.
func TestRebuildViewFromEvAction(t *testing.T) {
	const sql = "SELECT client, src FROM bench_log WHERE client > 0"

	t.Run("canonical", func(t *testing.T) {
		ev := canonicalEvAction(t, sql)
		if len(ev) < 2 || ev[0] != '(' || ev[1] != '{' {
			t.Fatalf("canonical ev_action must open with %q, got %q", "({", ev[:min(2, len(ev))])
		}
		sel, canonical, ok := rebuildViewFromEvAction(ev)
		if !ok || !canonical || sel == nil {
			t.Fatalf("canonical => (canonical=%v, ok=%v, sel!=nil=%v)", canonical, ok, sel != nil)
		}
		if len(sel.From) != 1 || sel.From[0].Name != "bench_log" {
			t.Fatalf("rebuilt FROM = %+v, want single bench_log", sel.From)
		}
		if len(sel.Targets) != 2 {
			t.Fatalf("rebuilt targets = %d, want 2", len(sel.Targets))
		}
	})

	t.Run("sql_text", func(t *testing.T) {
		sel, canonical, ok := rebuildViewFromEvAction(sql)
		if !ok || canonical || sel == nil {
			t.Fatalf("sql text => (canonical=%v, ok=%v, sel!=nil=%v)", canonical, ok, sel != nil)
		}
	})

	t.Run("garbage_canonical", func(t *testing.T) {
		if _, _, ok := rebuildViewFromEvAction("({QUERY :not-valid"); ok {
			t.Fatal("malformed canonical ev_action must be ok=false")
		}
	})

	t.Run("garbage_sql", func(t *testing.T) {
		if _, _, ok := rebuildViewFromEvAction("this is not sql ;;;"); ok {
			t.Fatal("unparseable SQL ev_action must be ok=false")
		}
	})
}
