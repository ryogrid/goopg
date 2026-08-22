package parser

import "testing"

// The ON COMMIT {PRESERVE ROWS|DELETE ROWS|DROP} clause on CREATE [TEMP] TABLE
// (M0134-0072). PRESERVE ROWS and the absent clause collapse to OnCommitNone
// (""); DELETE ROWS and DROP are captured so the executor can register the
// end-of-transaction action. temp.sql / tablecmds.c register_on_commit_action.

func TestParseCreateTempTableOnCommitDeleteRows(t *testing.T) {
	stmts, err := Parse("CREATE TEMP TABLE t (a int) ON COMMIT DELETE ROWS")
	if err != nil {
		t.Fatal(err)
	}
	ct := stmts[0].(*CreateTableStmt)
	if !ct.Temporary {
		t.Errorf("Temporary=%v, want true", ct.Temporary)
	}
	if ct.OnCommit != OnCommitDeleteRows {
		t.Errorf("OnCommit=%q, want %q", ct.OnCommit, OnCommitDeleteRows)
	}
}

func TestParseCreateTempTableOnCommitDrop(t *testing.T) {
	stmts, err := Parse("CREATE TEMPORARY TABLE t (a int) ON COMMIT DROP")
	if err != nil {
		t.Fatal(err)
	}
	ct := stmts[0].(*CreateTableStmt)
	if !ct.Temporary {
		t.Errorf("Temporary=%v, want true", ct.Temporary)
	}
	if ct.OnCommit != OnCommitDrop {
		t.Errorf("OnCommit=%q, want %q", ct.OnCommit, OnCommitDrop)
	}
}

func TestParseCreateTempTableOnCommitPreserveRowsNoop(t *testing.T) {
	// PRESERVE ROWS is the default no-op: it collapses to "" (deviation from
	// PG, which keeps it distinct — see the M0134-0072 design doc).
	stmts, err := Parse("CREATE TEMP TABLE t (a int) ON COMMIT PRESERVE ROWS")
	if err != nil {
		t.Fatal(err)
	}
	ct := stmts[0].(*CreateTableStmt)
	if ct.OnCommit != "" {
		t.Errorf("OnCommit=%q, want %q", ct.OnCommit, "")
	}
}

func TestParseCreateTempTableOnCommitNoClause(t *testing.T) {
	stmts, err := Parse("CREATE TEMP TABLE t (a int)")
	if err != nil {
		t.Fatal(err)
	}
	ct := stmts[0].(*CreateTableStmt)
	if ct.OnCommit != "" {
		t.Errorf("OnCommit=%q, want %q", ct.OnCommit, "")
	}
}

// TestParseCTASOnCommit: the CTAS alias-list lookahead admits an optional ON
// COMMIT between the (col) list and AS (gram.y create_as_target: qualified_name
// opt_column_list OptOnCommit OPT_TABLESPACE). The clause must land on the
// statement AND the aliases must still be captured.
func TestParseCTASOnCommit(t *testing.T) {
	for _, tc := range []struct {
		sql       string
		wantOnC   string
		wantAlias []string
	}{
		{"CREATE TEMP TABLE t (col) ON COMMIT DROP AS SELECT 1", OnCommitDrop, []string{"col"}},
		{"CREATE TEMP TABLE t (a, b) ON COMMIT DELETE ROWS AS SELECT 1, 2", OnCommitDeleteRows, []string{"a", "b"}},
		{"CREATE TEMP TABLE t (col) ON COMMIT PRESERVE ROWS AS SELECT 1", "", []string{"col"}},
	} {
		stmts, err := Parse(tc.sql)
		if err != nil {
			t.Fatalf("%s: %v", tc.sql, err)
		}
		ct := stmts[0].(*CreateTableStmt)
		if !ct.Temporary {
			t.Errorf("%s: Temporary=%v, want true", tc.sql, ct.Temporary)
		}
		if ct.OnCommit != tc.wantOnC {
			t.Errorf("%s: OnCommit=%q, want %q", tc.sql, ct.OnCommit, tc.wantOnC)
		}
		if len(ct.ColumnAliases) != len(tc.wantAlias) {
			t.Fatalf("%s: ColumnAliases=%v, want %v", tc.sql, ct.ColumnAliases, tc.wantAlias)
		}
		for i := range tc.wantAlias {
			if ct.ColumnAliases[i] != tc.wantAlias[i] {
				t.Errorf("%s: ColumnAliases[%d]=%q, want %q", tc.sql, i, ct.ColumnAliases[i], tc.wantAlias[i])
			}
		}
	}
}

// TestParseCTASImmediateAsNonRegression: `CREATE TEMP TABLE t(col) AS SELECT`
// (M0097-0020) must still parse — the lookahead succeeds when `)` is followed
// directly by AS, with no ON COMMIT involved.
func TestParseCTASImmediateAsNonRegression(t *testing.T) {
	stmts, err := Parse("CREATE TEMP TABLE t (col) AS SELECT 1")
	if err != nil {
		t.Fatal(err)
	}
	ct := stmts[0].(*CreateTableStmt)
	if len(ct.ColumnAliases) != 1 || ct.ColumnAliases[0] != "col" {
		t.Errorf("ColumnAliases=%v, want [col]", ct.ColumnAliases)
	}
	if ct.OnCommit != "" {
		t.Errorf("OnCommit=%q, want %q", ct.OnCommit, "")
	}
}

// TestParseCreateTempTableOnCommitColumnDefsRestored: a plain (non-CTAS) CREATE
// TEMP TABLE with a real column-def list and ON COMMIT must NOT be consumed by
// the CTAS lookahead — the ON COMMIT capture is the plain arm's job.
func TestParseCreateTempTableOnCommitColumnDefsRestored(t *testing.T) {
	stmts, err := Parse("CREATE TEMP TABLE t (a int, b text) ON COMMIT DELETE ROWS")
	if err != nil {
		t.Fatal(err)
	}
	ct := stmts[0].(*CreateTableStmt)
	if len(ct.Columns) != 2 {
		t.Fatalf("columns=%d, want 2", len(ct.Columns))
	}
	if ct.OnCommit != OnCommitDeleteRows {
		t.Errorf("OnCommit=%q, want %q", ct.OnCommit, OnCommitDeleteRows)
	}
}

// TestParseCreateTempTableOnCommitEmptyListInherits: the `()` empty-column-list
// INHERITS child form goes through consumeCreateTableSuffix (ddl.go:3561), which
// must capture ON COMMIT rather than discard it — temp.sql's inheritance ON
// COMMIT tests use exactly this shape.
func TestParseCreateTempTableOnCommitEmptyListInherits(t *testing.T) {
	for _, tc := range []struct {
		sql  string
		want string
	}{
		{"CREATE TEMP TABLE c () INHERITS (p) ON COMMIT DROP", OnCommitDrop},
		{"CREATE TEMP TABLE c () INHERITS (p) ON COMMIT DELETE ROWS", OnCommitDeleteRows},
		{"CREATE TEMP TABLE c () INHERITS (p) ON COMMIT PRESERVE ROWS", ""},
		{"CREATE TEMP TABLE c () INHERITS (p)", ""},
	} {
		stmts, err := Parse(tc.sql)
		if err != nil {
			t.Fatalf("%s: %v", tc.sql, err)
		}
		ct := stmts[0].(*CreateTableStmt)
		if len(ct.Inherits) != 1 {
			t.Errorf("%s: Inherits=%d, want 1", tc.sql, len(ct.Inherits))
		}
		if ct.OnCommit != tc.want {
			t.Errorf("%s: OnCommit=%q, want %q", tc.sql, ct.OnCommit, tc.want)
		}
	}
}
