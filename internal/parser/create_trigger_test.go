package parser

import "testing"

// TestParseCreateTriggerUpdateOf pins capture of the optional `UPDATE OF col1,
// col2` column list of a column-specific UPDATE trigger. pg_get_triggerdef →
// pg_dump reconstruct the ` OF <cols>` clause from this list; before DU-002
// slice 326 the parser tripped on the `OF` keyword (it was treated as the start
// of the ON clause). The list must round-trip while other events are unaffected.
func TestParseCreateTriggerUpdateOf(t *testing.T) {
	cases := []struct {
		name       string
		sql        string
		wantEvents []string
		wantCols   []string
	}{
		{
			name:       "update of single column",
			sql:        "CREATE TRIGGER t1 BEFORE UPDATE OF a ON tbl FOR EACH ROW EXECUTE FUNCTION f()",
			wantEvents: []string{"update"},
			wantCols:   []string{"a"},
		},
		{
			name:       "update of multiple columns",
			sql:        "CREATE TRIGGER t2 AFTER UPDATE OF a, b, c ON tbl FOR EACH STATEMENT EXECUTE FUNCTION f()",
			wantEvents: []string{"update"},
			wantCols:   []string{"a", "b", "c"},
		},
		{
			name:       "insert or update of columns",
			sql:        "CREATE TRIGGER t3 AFTER INSERT OR UPDATE OF a, b ON tbl FOR EACH ROW EXECUTE FUNCTION f()",
			wantEvents: []string{"insert", "update"},
			wantCols:   []string{"a", "b"},
		},
		{
			name:       "plain update has no column list",
			sql:        "CREATE TRIGGER t4 BEFORE UPDATE ON tbl FOR EACH ROW EXECUTE FUNCTION f()",
			wantEvents: []string{"update"},
			wantCols:   nil,
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
			s, ok := stmts[0].(*CreateTriggerStmt)
			if !ok {
				t.Fatalf("got %T, want *CreateTriggerStmt", stmts[0])
			}
			if !eqStrs(s.Events, tc.wantEvents) {
				t.Errorf("Events = %v, want %v", s.Events, tc.wantEvents)
			}
			if !eqStrs(s.UpdateColumns, tc.wantCols) {
				t.Errorf("UpdateColumns = %v, want %v", s.UpdateColumns, tc.wantCols)
			}
		})
	}
}
