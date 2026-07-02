package parser

import "testing"

// TestParseCreateStatistics pins capture of the optional kinds clause and the ON
// column list, which pg_get_statisticsobjdef → pg_dump rely on to reconstruct the
// object. Before DU-002 slice 314 the parser discarded both. M0119-0004.
func TestParseCreateStatistics(t *testing.T) {
	cases := []struct {
		name     string
		sql      string
		wantName string
		wantFrom string
		wantKind []string
		wantCols []string
		wantExpr bool
	}{
		{
			name:     "default-kinds",
			sql:      "CREATE STATISTICS s1 ON a, b FROM t",
			wantName: "s1",
			wantFrom: "t",
			wantCols: []string{"a", "b"},
		},
		{
			name:     "explicit-kinds",
			sql:      "CREATE STATISTICS public.s2 (ndistinct, mcv) ON a, b, c FROM public.t",
			wantName: "s2",
			wantFrom: "t",
			wantKind: []string{"ndistinct", "mcv"},
			wantCols: []string{"a", "b", "c"},
		},
		{
			name:     "if-not-exists",
			sql:      "CREATE STATISTICS IF NOT EXISTS s3 (dependencies) ON x, y FROM tbl",
			wantName: "s3",
			wantFrom: "tbl",
			wantKind: []string{"dependencies"},
			wantCols: []string{"x", "y"},
		},
		{
			name:     "expression-target",
			sql:      "CREATE STATISTICS s4 ON a, (b + c) FROM t",
			wantName: "s4",
			wantFrom: "t",
			wantCols: []string{"a"},
			wantExpr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stmts, err := Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.sql, err)
			}
			if len(stmts) != 1 {
				t.Fatalf("got %d stmts, want 1", len(stmts))
			}
			s, ok := stmts[0].(*CreateStatisticsStmt)
			if !ok {
				t.Fatalf("got %T, want *CreateStatisticsStmt", stmts[0])
			}
			if s.Name.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", s.Name.Name, tc.wantName)
			}
			if s.FromTable.Name != tc.wantFrom {
				t.Errorf("FromTable = %q, want %q", s.FromTable.Name, tc.wantFrom)
			}
			if !eqStrs(s.Kinds, tc.wantKind) {
				t.Errorf("Kinds = %v, want %v", s.Kinds, tc.wantKind)
			}
			if !eqStrs(s.Columns, tc.wantCols) {
				t.Errorf("Columns = %v, want %v", s.Columns, tc.wantCols)
			}
			if s.HasExpr != tc.wantExpr {
				t.Errorf("HasExpr = %v, want %v", s.HasExpr, tc.wantExpr)
			}
		})
	}
}

func eqStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
