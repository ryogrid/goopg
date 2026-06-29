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

// TestParseCreateConstraintTrigger pins capture of CREATE CONSTRAINT TRIGGER and
// the optional `[NOT] DEFERRABLE [INITIALLY {IMMEDIATE|DEFERRED}]` clause that
// follows the ON-table name. pg_get_triggerdef → pg_dump reconstruct
// `CREATE CONSTRAINT TRIGGER ... NOT DEFERRABLE INITIALLY IMMEDIATE` from these
// flags (DU-002 slice 327). A plain (non-constraint) trigger must keep
// IsConstraint=false.
func TestParseCreateConstraintTrigger(t *testing.T) {
	cases := []struct {
		name             string
		sql              string
		wantConstraint   bool
		wantDeferrable   bool
		wantInitDeferred bool
	}{
		{
			name:           "constraint trigger default deferrability",
			sql:            "CREATE CONSTRAINT TRIGGER c1 AFTER INSERT ON tbl FOR EACH ROW EXECUTE FUNCTION f()",
			wantConstraint: true,
		},
		{
			name:           "constraint trigger explicit not deferrable",
			sql:            "CREATE CONSTRAINT TRIGGER c2 AFTER UPDATE ON tbl NOT DEFERRABLE FOR EACH ROW EXECUTE FUNCTION f()",
			wantConstraint: true,
		},
		{
			name:             "constraint trigger deferrable initially deferred",
			sql:              "CREATE CONSTRAINT TRIGGER c3 AFTER DELETE ON tbl DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION f()",
			wantConstraint:   true,
			wantDeferrable:   true,
			wantInitDeferred: true,
		},
		{
			name:           "constraint trigger deferrable initially immediate",
			sql:            "CREATE CONSTRAINT TRIGGER c4 AFTER INSERT ON tbl DEFERRABLE INITIALLY IMMEDIATE FOR EACH ROW EXECUTE FUNCTION f()",
			wantConstraint: true,
			wantDeferrable: true,
		},
		{
			name:           "plain trigger is not a constraint trigger",
			sql:            "CREATE TRIGGER c5 AFTER INSERT ON tbl FOR EACH ROW EXECUTE FUNCTION f()",
			wantConstraint: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stmts, err := Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.sql, err)
			}
			s, ok := stmts[0].(*CreateTriggerStmt)
			if !ok {
				t.Fatalf("got %T, want *CreateTriggerStmt", stmts[0])
			}
			if s.IsConstraint != tc.wantConstraint {
				t.Errorf("IsConstraint = %v, want %v", s.IsConstraint, tc.wantConstraint)
			}
			if s.Deferrable != tc.wantDeferrable {
				t.Errorf("Deferrable = %v, want %v", s.Deferrable, tc.wantDeferrable)
			}
			if s.InitDeferred != tc.wantInitDeferred {
				t.Errorf("InitDeferred = %v, want %v", s.InitDeferred, tc.wantInitDeferred)
			}
		})
	}
}

// TestParseCreateTriggerReferencing pins capture of the REFERENCING clause's
// `OLD TABLE AS <name>` / `NEW TABLE AS <name>` transition-relation names.
// pg_get_triggerdef → pg_dump reconstruct `REFERENCING OLD TABLE AS … NEW TABLE
// AS …` between the ON-table name and FOR EACH ROW from these (DU-002 slice 328).
// Either or both clauses may appear, in any order, with an optional AS keyword.
func TestParseCreateTriggerReferencing(t *testing.T) {
	cases := []struct {
		name    string
		sql     string
		wantOld string
		wantNew string
	}{
		{
			name:    "new table only",
			sql:     "CREATE TRIGGER t1 AFTER INSERT ON tbl REFERENCING NEW TABLE AS nt FOR EACH STATEMENT EXECUTE FUNCTION f()",
			wantNew: "nt",
		},
		{
			name:    "old table only",
			sql:     "CREATE TRIGGER t2 AFTER DELETE ON tbl REFERENCING OLD TABLE AS ot FOR EACH STATEMENT EXECUTE FUNCTION f()",
			wantOld: "ot",
		},
		{
			name:    "both old and new",
			sql:     "CREATE TRIGGER t3 AFTER UPDATE ON tbl REFERENCING OLD TABLE AS ot NEW TABLE AS nt FOR EACH STATEMENT EXECUTE FUNCTION f()",
			wantOld: "ot",
			wantNew: "nt",
		},
		{
			name:    "new before old (any order)",
			sql:     "CREATE TRIGGER t4 AFTER UPDATE ON tbl REFERENCING NEW TABLE AS nt OLD TABLE AS ot FOR EACH STATEMENT EXECUTE FUNCTION f()",
			wantOld: "ot",
			wantNew: "nt",
		},
		{
			name:    "optional AS keyword omitted",
			sql:     "CREATE TRIGGER t5 AFTER INSERT ON tbl REFERENCING NEW TABLE nt FOR EACH STATEMENT EXECUTE FUNCTION f()",
			wantNew: "nt",
		},
		{
			name: "no referencing clause",
			sql:  "CREATE TRIGGER t6 AFTER INSERT ON tbl FOR EACH ROW EXECUTE FUNCTION f()",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stmts, err := Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.sql, err)
			}
			s, ok := stmts[0].(*CreateTriggerStmt)
			if !ok {
				t.Fatalf("got %T, want *CreateTriggerStmt", stmts[0])
			}
			if s.OldTransitionTable != tc.wantOld {
				t.Errorf("OldTransitionTable = %q, want %q", s.OldTransitionTable, tc.wantOld)
			}
			if s.NewTransitionTable != tc.wantNew {
				t.Errorf("NewTransitionTable = %q, want %q", s.NewTransitionTable, tc.wantNew)
			}
		})
	}
}
