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

// TestParseCreateUserMapping verifies CREATE/DROP USER MAPPING parses to a
// CompatNoopStmt/DropCompatStmt carrying the (user, server) pair, while a plain
// CREATE USER (role) still fails to parse so the server-layer role-DDL path
// handles it. DU-002 slice 377.
func TestParseCreateUserMapping(t *testing.T) {
	stmts, err := Parse("CREATE USER MAPPING FOR um_role SERVER goopg_srv")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	ns, ok := stmts[0].(*CompatNoopStmt)
	if !ok {
		t.Fatalf("Expected *CompatNoopStmt, got %T", stmts[0])
	}
	if ns.ObjType != "user mapping" {
		t.Errorf("ObjType = %q, want %q", ns.ObjType, "user mapping")
	}
	if ns.ObjName.Name != "um_role" {
		t.Errorf("user = %q, want um_role", ns.ObjName.Name)
	}
	if ns.TableName.Name != "goopg_srv" {
		t.Errorf("server = %q, want goopg_srv", ns.TableName.Name)
	}

	// OPTIONS clause is captured as "name=value" elements (in clause order) so the
	// mapping's umoptions round-trip through pg_dump. DU-002 slice 379.
	stmts, err = Parse("CREATE USER MAPPING FOR um_role SERVER goopg_srv OPTIONS (user 'x', password 'y')")
	if err != nil {
		t.Fatalf("Parse error with OPTIONS: %v", err)
	}
	ns, _ = stmts[0].(*CompatNoopStmt)
	if ns == nil || ns.ObjName.Name != "um_role" || ns.TableName.Name != "goopg_srv" {
		t.Fatalf("OPTIONS form mis-parsed: %+v", stmts[0])
	}
	wantOpts := []string{"user=x", "password=y"}
	if len(ns.Options) != len(wantOpts) {
		t.Fatalf("mapping Options = %+v, want %+v", ns.Options, wantOpts)
	}
	for i := range wantOpts {
		if ns.Options[i] != wantOpts[i] {
			t.Errorf("mapping Options[%d] = %q, want %q", i, ns.Options[i], wantOpts[i])
		}
	}

	// DROP USER MAPPING FOR <user> SERVER <server>: Names = [user, server].
	stmts, err = Parse("DROP USER MAPPING FOR um_role SERVER goopg_srv")
	if err != nil {
		t.Fatalf("Parse error (drop): %v", err)
	}
	ds, ok := stmts[0].(*DropCompatStmt)
	if !ok {
		t.Fatalf("Expected *DropCompatStmt, got %T", stmts[0])
	}
	if ds.ObjType != "user mapping" {
		t.Errorf("drop ObjType = %q, want %q", ds.ObjType, "user mapping")
	}
	if len(ds.Names) != 2 || ds.Names[0].Name != "um_role" || ds.Names[1].Name != "goopg_srv" {
		t.Errorf("drop Names = %+v, want [um_role goopg_srv]", ds.Names)
	}

	// DROP USER MAPPING IF EXISTS sets the flag.
	stmts, _ = Parse("DROP USER MAPPING IF EXISTS FOR um_role SERVER goopg_srv")
	if ds, _ := stmts[0].(*DropCompatStmt); ds == nil || !ds.IfExists {
		t.Errorf("DROP USER MAPPING IF EXISTS did not set IfExists: %+v", stmts[0])
	}

	// A plain CREATE USER (role DDL) must NOT parse here — it returns an error so
	// the server-layer role-DDL intercept handles it.
	if _, err := Parse("CREATE USER alice"); err == nil {
		t.Errorf("CREATE USER alice parsed successfully; it must fall through to the role-DDL path")
	}
}

// TestParseCreateServerOptions verifies that CREATE SERVER captures both the
// FOREIGN DATA WRAPPER association and an OPTIONS (name 'value', …) clause as
// "name=value" elements, so the server's options round-trip through pg_dump
// (pg_foreign_server.srvoptions → dumpForeignServer). DU-002 slice 378.
func TestParseCreateServerOptions(t *testing.T) {
	// Bare CREATE SERVER (no OPTIONS) carries the FDW association and nil Options.
	stmts, err := Parse("CREATE SERVER goopg_srv FOREIGN DATA WRAPPER goopg_fdw")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	ns, ok := stmts[0].(*CompatNoopStmt)
	if !ok {
		t.Fatalf("Expected *CompatNoopStmt, got %T", stmts[0])
	}
	if ns.ObjType != "server" || ns.ObjName.Name != "goopg_srv" || ns.TableName.Name != "goopg_fdw" {
		t.Errorf("bare server mis-parsed: %+v", ns)
	}
	if ns.Options != nil {
		t.Errorf("bare server Options = %+v, want nil", ns.Options)
	}

	// CREATE SERVER … OPTIONS (…) captures the options in OPTIONS-clause order.
	stmts, err = Parse("CREATE SERVER goopg_srv FOREIGN DATA WRAPPER goopg_fdw OPTIONS (host 'localhost', dbname 'mydb')")
	if err != nil {
		t.Fatalf("Parse error with OPTIONS: %v", err)
	}
	ns, _ = stmts[0].(*CompatNoopStmt)
	if ns == nil {
		t.Fatalf("Expected *CompatNoopStmt, got %T", stmts[0])
	}
	if ns.TableName.Name != "goopg_fdw" {
		t.Errorf("FDW association lost with OPTIONS: %q", ns.TableName.Name)
	}
	want := []string{"host=localhost", "dbname=mydb"}
	if len(ns.Options) != len(want) {
		t.Fatalf("Options = %+v, want %+v", ns.Options, want)
	}
	for i := range want {
		if ns.Options[i] != want[i] {
			t.Errorf("Options[%d] = %q, want %q", i, ns.Options[i], want[i])
		}
	}
}

// TestParseCreateServerTypeVersion verifies that CREATE SERVER captures the
// TYPE 'x' / VERSION 'y' clauses (string literals) so they round-trip through
// pg_foreign_server.srvtype / srvversion (dumpForeignServer re-emits TYPE/VERSION).
// DU-002 slice 381.
func TestParseCreateServerTypeVersion(t *testing.T) {
	// TYPE and VERSION before FOREIGN DATA WRAPPER, with a trailing OPTIONS clause.
	stmts, err := Parse("CREATE SERVER s1 TYPE 'oracle' VERSION '12.2' FOREIGN DATA WRAPPER goopg_fdw OPTIONS (host 'h')")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	ns, ok := stmts[0].(*CompatNoopStmt)
	if !ok {
		t.Fatalf("Expected *CompatNoopStmt, got %T", stmts[0])
	}
	if ns.ServerType != "oracle" || ns.ServerVersion != "12.2" {
		t.Errorf("TYPE/VERSION = %q/%q, want oracle/12.2", ns.ServerType, ns.ServerVersion)
	}
	if ns.TableName.Name != "goopg_fdw" {
		t.Errorf("FDW association lost: %q", ns.TableName.Name)
	}
	if len(ns.Options) != 1 || ns.Options[0] != "host=h" {
		t.Errorf("Options = %+v, want [host=h]", ns.Options)
	}

	// TYPE only (no VERSION) leaves ServerVersion empty.
	stmts, err = Parse("CREATE SERVER s2 TYPE 'pgsql' FOREIGN DATA WRAPPER goopg_fdw")
	if err != nil {
		t.Fatalf("Parse error (TYPE only): %v", err)
	}
	ns, _ = stmts[0].(*CompatNoopStmt)
	if ns == nil || ns.ServerType != "pgsql" || ns.ServerVersion != "" {
		t.Errorf("TYPE-only mis-parsed: %+v", ns)
	}

	// Bare CREATE SERVER leaves both empty.
	stmts, _ = Parse("CREATE SERVER s3 FOREIGN DATA WRAPPER goopg_fdw")
	ns, _ = stmts[0].(*CompatNoopStmt)
	if ns == nil || ns.ServerType != "" || ns.ServerVersion != "" {
		t.Errorf("bare server should have empty TYPE/VERSION: %+v", ns)
	}
}

// TestParseCreateFDWOptions verifies that CREATE FOREIGN DATA WRAPPER captures an
// OPTIONS (name 'value', …) clause as "name=value" elements (and skips any
// HANDLER/VALIDATOR clauses preceding it), so the wrapper's options round-trip
// through pg_dump (pg_foreign_data_wrapper.fdwoptions → dumpForeignDataWrapper).
// DU-002 slice 380.
func TestParseCreateFDWOptions(t *testing.T) {
	// Bare CREATE FOREIGN DATA WRAPPER (no OPTIONS) carries nil Options.
	stmts, err := Parse("CREATE FOREIGN DATA WRAPPER goopg_fdw")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	ns, ok := stmts[0].(*CompatNoopStmt)
	if !ok {
		t.Fatalf("Expected *CompatNoopStmt, got %T", stmts[0])
	}
	if ns.ObjType != "foreign-data wrapper" || ns.ObjName.Name != "goopg_fdw" {
		t.Errorf("bare FDW mis-parsed: %+v", ns)
	}
	if ns.Options != nil {
		t.Errorf("bare FDW Options = %+v, want nil", ns.Options)
	}

	// CREATE FOREIGN DATA WRAPPER … OPTIONS (…) captures options in clause order,
	// even when an unrelated NO HANDLER / VALIDATOR clause precedes them.
	stmts, err = Parse("CREATE FOREIGN DATA WRAPPER goopg_fdw NO HANDLER OPTIONS (debug 'true', delimiter ',')")
	if err != nil {
		t.Fatalf("Parse error with OPTIONS: %v", err)
	}
	ns, _ = stmts[0].(*CompatNoopStmt)
	if ns == nil {
		t.Fatalf("Expected *CompatNoopStmt, got %T", stmts[0])
	}
	if ns.ObjName.Name != "goopg_fdw" {
		t.Errorf("FDW name lost with OPTIONS: %q", ns.ObjName.Name)
	}
	want := []string{"debug=true", "delimiter=,"}
	if len(ns.Options) != len(want) {
		t.Fatalf("Options = %+v, want %+v", ns.Options, want)
	}
	for i := range want {
		if ns.Options[i] != want[i] {
			t.Errorf("Options[%d] = %q, want %q", i, ns.Options[i], want[i])
		}
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

// TestParseCreateOperatorExtendedClauses verifies the DU-002 slice 407
// extension of the CREATE OPERATOR key-value scanner: COMMUTATOR/NEGATOR
// (both the bare-symbol and pg_dump-emitted OPERATOR(schema.op) forms),
// RESTRICT/JOIN function references, and the bare MERGES/HASHES flags.
func TestParseCreateOperatorExtendedClauses(t *testing.T) {
	sql := `CREATE OPERATOR public.=== (
        FUNCTION = int4eq, LEFTARG = int4, RIGHTARG = int4,
        COMMUTATOR = OPERATOR(public.===),
        NEGATOR = !==,
        RESTRICT = eqsel, JOIN = eqjoinsel,
        MERGES, HASHES
    )`
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
	if ns.OpCommutatorName.Schema != "public" || ns.OpCommutatorName.Name != "===" {
		t.Errorf("OpCommutatorName = %+v, want {public ===}", ns.OpCommutatorName)
	}
	if ns.OpNegatorName.Name != "!==" {
		t.Errorf("OpNegatorName = %+v, want {!==}", ns.OpNegatorName)
	}
	if ns.OpRestrictFuncName.Name != "eqsel" {
		t.Errorf("OpRestrictFuncName = %+v, want {eqsel}", ns.OpRestrictFuncName)
	}
	if ns.OpJoinFuncName.Name != "eqjoinsel" {
		t.Errorf("OpJoinFuncName = %+v, want {eqjoinsel}", ns.OpJoinFuncName)
	}
	if !ns.OpCanMerge {
		t.Error("OpCanMerge = false, want true")
	}
	if !ns.OpCanHash {
		t.Error("OpCanHash = false, want true")
	}
}

// TestParseCreateOperatorUnary verifies a LEFTARG-omitted (prefix/unary)
// CREATE OPERATOR parses with an empty ArgTypes[0], matching PG's grammar
// (RIGHTARG is always required — postfix operators were removed in PG14).
func TestParseCreateOperatorUnary(t *testing.T) {
	sql := `CREATE OPERATOR @@- (FUNCTION = int4um, RIGHTARG = int4)`
	stmts, err := Parse(sql)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	ns, ok := stmts[0].(*CompatNoopStmt)
	if !ok {
		t.Fatalf("Expected *CompatNoopStmt, got %T", stmts[0])
	}
	if len(ns.ArgTypes) != 2 || ns.ArgTypes[0] != "" || ns.ArgTypes[1] != "int4" {
		t.Errorf("ArgTypes = %v, want [\"\" int4]", ns.ArgTypes)
	}
}

// TestParseAlterOperatorSet verifies the ALTER OPERATOR ... SET (...) form
// (AlterOperator, operatorcmds.c) parses into AlterOperatorSetStmt with all
// six option kinds, closing the slice-407 ledger follow-up.
func TestParseAlterOperatorSet(t *testing.T) {
	sql := `ALTER OPERATOR public.=== (int4, int4) SET (
        RESTRICT = eqsel, JOIN = eqjoinsel,
        COMMUTATOR = OPERATOR(public.===),
        NEGATOR = !==,
        MERGES, HASHES = false
    )`
	stmts, err := Parse(sql)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(stmts) != 1 {
		t.Fatalf("Expected 1 stmt, got %d", len(stmts))
	}
	s, ok := stmts[0].(*AlterOperatorSetStmt)
	if !ok {
		t.Fatalf("Expected *AlterOperatorSetStmt, got %T", stmts[0])
	}
	if s.Name.Schema != "public" || s.Name.Name != "===" {
		t.Errorf("Name = %+v, want {public ===}", s.Name)
	}
	if s.LeftType != "int4" || s.RightType != "int4" {
		t.Errorf("LeftType/RightType = %q/%q, want int4/int4", s.LeftType, s.RightType)
	}
	if !s.RestrictSet || s.Restrict.Name != "eqsel" {
		t.Errorf("Restrict = set=%v name=%q, want set=true name=eqsel", s.RestrictSet, s.Restrict.Name)
	}
	if !s.JoinSet || s.Join.Name != "eqjoinsel" {
		t.Errorf("Join = set=%v name=%q, want set=true name=eqjoinsel", s.JoinSet, s.Join.Name)
	}
	if !s.CommutatorSet || s.Commutator.Schema != "public" || s.Commutator.Name != "===" {
		t.Errorf("Commutator = set=%v %+v, want set=true {public ===}", s.CommutatorSet, s.Commutator)
	}
	if !s.NegatorSet || s.Negator.Name != "!==" {
		t.Errorf("Negator = set=%v name=%q, want set=true name=!==", s.NegatorSet, s.Negator.Name)
	}
	if s.Merges == nil || !*s.Merges {
		t.Errorf("Merges = %v, want true", s.Merges)
	}
	if s.Hashes == nil || *s.Hashes {
		t.Errorf("Hashes = %v, want false", s.Hashes)
	}
}

// TestParseAlterOperatorSetRestrictNone verifies `SET (RESTRICT = NONE)`
// clears the estimator (RestrictSet true, Restrict zero-valued) and that a
// unary operator's NONE left-arg parses.
func TestParseAlterOperatorSetRestrictNone(t *testing.T) {
	sql := `ALTER OPERATOR @@- (NONE, int4) SET (RESTRICT = NONE)`
	stmts, err := Parse(sql)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	s, ok := stmts[0].(*AlterOperatorSetStmt)
	if !ok {
		t.Fatalf("Expected *AlterOperatorSetStmt, got %T", stmts[0])
	}
	if s.LeftType != "" || s.RightType != "int4" {
		t.Errorf("LeftType/RightType = %q/%q, want \"\"/int4", s.LeftType, s.RightType)
	}
	if !s.RestrictSet || s.Restrict.Name != "" {
		t.Errorf("Restrict = set=%v name=%q, want set=true name=\"\"", s.RestrictSet, s.Restrict.Name)
	}
}

// TestParseAlterOperatorSetImmutableAttr verifies LEFTARG/RIGHTARG/FUNCTION/
// PROCEDURE raise a syntax error inside ALTER OPERATOR SET (immutable after
// CREATE, mirroring AlterOperator's own rejection).
func TestParseAlterOperatorSetImmutableAttr(t *testing.T) {
	for _, attr := range []string{"leftarg", "rightarg", "function", "procedure"} {
		sql := `ALTER OPERATOR === (int4, int4) SET (` + attr + ` = int4eq)`
		if _, err := Parse(sql); err == nil {
			t.Errorf("attr %q: expected parse error, got none", attr)
		}
	}
}

// TestParseAlterOperatorOwnerToIsNoop verifies ALTER OPERATOR ... OWNER TO
// still parses as the pre-existing no-op compat stub (goopg does not track
// per-operator ownership changes at ALTER time), and ALTER OPERATOR
// CLASS/FAMILY are unaffected by the new SET(...) branch.
func TestParseAlterOperatorOwnerToIsNoop(t *testing.T) {
	for _, sql := range []string{
		`ALTER OPERATOR === (int4, int4) OWNER TO someone`,
		`ALTER OPERATOR === (int4, int4) SET SCHEMA other`,
		`ALTER OPERATOR CLASS int4_ops USING btree OWNER TO someone`,
		`ALTER OPERATOR FAMILY int4_ops USING btree RENAME TO int4_ops2`,
	} {
		stmts, err := Parse(sql)
		if err != nil {
			t.Fatalf("%q: Parse error: %v", sql, err)
		}
		if _, ok := stmts[0].(*AlterTableStmt); !ok {
			t.Errorf("%q: Expected *AlterTableStmt (no-op stub), got %T", sql, stmts[0])
		}
	}
}
