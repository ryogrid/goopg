package parser

import (
	"testing"
)

// TestParseGrantTableACL verifies a GRANT/REVOKE on a table records the target
// relation name in CompatNoopStmt.TableACL (design 0118-0109,
// intra-grant-inplace) while a GRANT on a non-table object class or ON DATABASE
// leaves it empty.
func TestParseGrantTableACL(t *testing.T) {
	cases := []struct {
		sql      string
		wantTbl  string
		wantDBup bool
	}{
		{"GRANT SELECT ON intra_grant_inplace TO PUBLIC", "intra_grant_inplace", false},
		{"GRANT SELECT ON TABLE foo TO bar", "foo", false},
		{"REVOKE SELECT ON public.foo FROM bar", "foo", false},
		{"GRANT TEMP ON DATABASE postgres TO PUBLIC", "", true},
		{"GRANT USAGE ON SCHEMA s TO bar", "", false},
		{"GRANT USAGE ON SEQUENCE seq1 TO bar", "", false},
	}
	for _, tc := range cases {
		stmts, err := Parse(tc.sql)
		if err != nil {
			t.Fatalf("Parse(%q) error: %v", tc.sql, err)
		}
		ns, ok := stmts[0].(*CompatNoopStmt)
		if !ok {
			t.Fatalf("%q: expected *CompatNoopStmt, got %T", tc.sql, stmts[0])
		}
		if ns.TableACL != tc.wantTbl {
			t.Errorf("%q: TableACL = %q, want %q", tc.sql, ns.TableACL, tc.wantTbl)
		}
		if ns.DatabaseACL != tc.wantDBup {
			t.Errorf("%q: DatabaseACL = %v, want %v", tc.sql, ns.DatabaseACL, tc.wantDBup)
		}
	}
}

func TestParseCreateOperatorArgTypes(t *testing.T) {
	sql := `CREATE OPERATOR @#@
        (leftarg = int8, rightarg = int8, procedure = int8xor)`
	stmts, err := Parse(sql)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(stmts) != 1 {
		t.Fatalf("Expected 1 stmt, got %d", len(stmts))
	}
	ns, ok := stmts[0].(*CompatNoopStmt)
	if !ok {
		t.Fatalf("Expected *CompatNoopStmt, got %T", stmts[0])
	}
	if ns.ObjType != "operator" {
		t.Errorf("ObjType = %q, want %q", ns.ObjType, "operator")
	}
	if ns.ObjName.Name != "@#@" {
		t.Errorf("ObjName.Name = %q, want %q", ns.ObjName.Name, "@#@")
	}
	if len(ns.ArgTypes) != 2 {
		t.Fatalf("ArgTypes len = %d, want 2: %v", len(ns.ArgTypes), ns.ArgTypes)
	}
	if ns.ArgTypes[0] != "int8" {
		t.Errorf("ArgTypes[0] = %q, want %q", ns.ArgTypes[0], "int8")
	}
	if ns.ArgTypes[1] != "int8" {
		t.Errorf("ArgTypes[1] = %q, want %q", ns.ArgTypes[1], "int8")
	}
}

func TestParseCreateRuleTableName(t *testing.T) {
	sql := `CREATE RULE test_rule_exists AS ON INSERT TO test_exists
    DO INSTEAD
    INSERT INTO test_exists VALUES (1, 'x')`
	stmts, err := Parse(sql)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(stmts) != 1 {
		t.Fatalf("Expected 1 stmt, got %d", len(stmts))
	}
	ns, ok := stmts[0].(*CompatNoopStmt)
	if !ok {
		t.Fatalf("Expected *CompatNoopStmt, got %T", stmts[0])
	}
	if ns.ObjType != "rule" {
		t.Errorf("ObjType = %q, want %q", ns.ObjType, "rule")
	}
	if ns.ObjName.Name != "test_rule_exists" {
		t.Errorf("ObjName.Name = %q, want %q", ns.ObjName.Name, "test_rule_exists")
	}
	if ns.TableName.Name != "test_exists" {
		t.Errorf("TableName.Name = %q, want %q", ns.TableName.Name, "test_exists")
	}
}
