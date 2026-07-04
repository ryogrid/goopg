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
// OPTIONS (name 'value', …) clause as "name=value" elements (independently of
// any HANDLER/VALIDATOR clause preceding it — see
// TestParseCreateFDWHandlerValidator for that clause), so the wrapper's
// options round-trip through pg_dump
// (pg_foreign_data_wrapper.fdwoptions → dumpForeignDataWrapper). DU-002
// slice 380.
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

// TestParseCreateFDWHandlerValidator pins the DU-002 (M0119-0004) closure of
// the "HANDLER/VALIDATOR func references are skipped" gap: CREATE/ALTER
// FOREIGN DATA WRAPPER now captures the function names onto
// FDWHandlerFunc/FDWValidatorFunc (schema-qualified or bare), and ALTER's
// paired *Given flags distinguish an absent clause (nil, Given=false, leave
// unchanged) from an explicit `NO HANDLER`/`NO VALIDATOR` (nil, Given=true,
// clear it).
func TestParseCreateFDWHandlerValidator(t *testing.T) {
	stmts, err := Parse("CREATE FOREIGN DATA WRAPPER goopg_fdw HANDLER myschema.my_handler VALIDATOR my_validator")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	ns := stmts[0].(*CompatNoopStmt)
	if ns.FDWHandlerFunc == nil || ns.FDWHandlerFunc.Schema != "myschema" || ns.FDWHandlerFunc.Name != "my_handler" {
		t.Errorf("FDWHandlerFunc = %+v, want myschema.my_handler", ns.FDWHandlerFunc)
	}
	if ns.FDWValidatorFunc == nil || ns.FDWValidatorFunc.Schema != "" || ns.FDWValidatorFunc.Name != "my_validator" {
		t.Errorf("FDWValidatorFunc = %+v, want my_validator", ns.FDWValidatorFunc)
	}

	// Bare CREATE (no HANDLER/VALIDATOR clause at all) leaves both nil.
	stmts, err = Parse("CREATE FOREIGN DATA WRAPPER goopg_fdw2")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	ns = stmts[0].(*CompatNoopStmt)
	if ns.FDWHandlerFunc != nil || ns.FDWValidatorFunc != nil {
		t.Errorf("bare CREATE FDWHandlerFunc/FDWValidatorFunc = %+v/%+v, want nil/nil", ns.FDWHandlerFunc, ns.FDWValidatorFunc)
	}

	// ALTER: an explicit NO HANDLER/NO VALIDATOR sets Given=true with a nil
	// Func (clear); an absent clause (OPTIONS only) leaves Given=false.
	stmts, err = Parse("ALTER FOREIGN DATA WRAPPER goopg_fdw NO HANDLER NO VALIDATOR")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	ns = stmts[0].(*CompatNoopStmt)
	if !ns.FDWHandlerGiven || ns.FDWHandlerFunc != nil {
		t.Errorf("ALTER NO HANDLER: Given=%v Func=%+v, want Given=true Func=nil", ns.FDWHandlerGiven, ns.FDWHandlerFunc)
	}
	if !ns.FDWValidatorGiven || ns.FDWValidatorFunc != nil {
		t.Errorf("ALTER NO VALIDATOR: Given=%v Func=%+v, want Given=true Func=nil", ns.FDWValidatorGiven, ns.FDWValidatorFunc)
	}

	stmts, err = Parse("ALTER FOREIGN DATA WRAPPER goopg_fdw OPTIONS (ADD x 'y')")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	ns = stmts[0].(*CompatNoopStmt)
	if ns.FDWHandlerGiven || ns.FDWValidatorGiven {
		t.Errorf("ALTER OPTIONS-only: HandlerGiven=%v ValidatorGiven=%v, want false/false", ns.FDWHandlerGiven, ns.FDWValidatorGiven)
	}

	stmts, err = Parse("ALTER FOREIGN DATA WRAPPER goopg_fdw HANDLER new_handler")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	ns = stmts[0].(*CompatNoopStmt)
	if !ns.FDWHandlerGiven || ns.FDWHandlerFunc == nil || ns.FDWHandlerFunc.Name != "new_handler" {
		t.Errorf("ALTER HANDLER new_handler: Given=%v Func=%+v", ns.FDWHandlerGiven, ns.FDWHandlerFunc)
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

// TestParseCreateOperatorFamily verifies CREATE OPERATOR FAMILY name USING
// method captures ObjType/ObjName/OpFamilyMethod (DU-002 slice 408). Unlike
// CREATE OPERATOR CLASS, the grammar has no AS clause.
func TestParseCreateOperatorFamily(t *testing.T) {
	stmts, err := Parse("CREATE OPERATOR FAMILY myschema.op_family USING btree")
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
	if ns.ObjType != "operator family" {
		t.Errorf("ObjType = %q, want %q", ns.ObjType, "operator family")
	}
	if ns.ObjName.Schema != "myschema" || ns.ObjName.Name != "op_family" {
		t.Errorf("ObjName = %+v, want {myschema op_family}", ns.ObjName)
	}
	if ns.OpFamilyMethod != "btree" {
		t.Errorf("OpFamilyMethod = %q, want %q", ns.OpFamilyMethod, "btree")
	}
}

// TestParseCreateOperatorFamilyUnqualified verifies a bare (unqualified)
// family name parses with an empty Schema, mirroring the other CREATE
// OPERATOR FAMILY test. DU-002 slice 408.
func TestParseCreateOperatorFamilyUnqualified(t *testing.T) {
	stmts, err := Parse("CREATE OPERATOR FAMILY op_family USING gist;")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	ns, ok := stmts[0].(*CompatNoopStmt)
	if !ok {
		t.Fatalf("Expected *CompatNoopStmt, got %T", stmts[0])
	}
	if ns.ObjName.Schema != "" || ns.ObjName.Name != "op_family" {
		t.Errorf("ObjName = %+v, want {\"\" op_family}", ns.ObjName)
	}
	if ns.OpFamilyMethod != "gist" {
		t.Errorf("OpFamilyMethod = %q, want %q", ns.OpFamilyMethod, "gist")
	}
}

// TestParseCreateOperatorClassStillWorks guards against the new "family"
// branch shadowing the pre-existing CREATE OPERATOR CLASS parse path.
// DU-002 slice 408.
func TestParseCreateOperatorClassStillWorks(t *testing.T) {
	stmts, err := Parse("CREATE OPERATOR CLASS my_opclass FOR TYPE int4 USING hash AS FUNCTION 2 my_hash_func(int4)")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	oc, ok := stmts[0].(*CreateOpClassStmt)
	if !ok {
		t.Fatalf("Expected *CreateOpClassStmt, got %T", stmts[0])
	}
	if oc.Name != "my_opclass" {
		t.Errorf("Name = %q, want %q", oc.Name, "my_opclass")
	}
}

// TestParseCreateOperatorClassFullShape verifies the DU-002 (M0119-0004)
// extension captures the schema-qualified name, USING method, an explicit
// FAMILY clause, and a STORAGE entry — the shape needed to populate a real
// pg_opclass row (upstream's own `op_class_empty` 002_pg_dump.pl fixture).
func TestParseCreateOperatorClassFullShape(t *testing.T) {
	stmts, err := Parse("CREATE OPERATOR CLASS dump_test.op_class_empty FOR TYPE bigint USING btree FAMILY dump_test.op_family AS STORAGE bigint")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	oc, ok := stmts[0].(*CreateOpClassStmt)
	if !ok {
		t.Fatalf("Expected *CreateOpClassStmt, got %T", stmts[0])
	}
	if oc.Schema != "dump_test" || oc.Name != "op_class_empty" {
		t.Errorf("Schema/Name = %q/%q, want dump_test/op_class_empty", oc.Schema, oc.Name)
	}
	if oc.ForType != "bigint" {
		t.Errorf("ForType = %q, want bigint", oc.ForType)
	}
	if oc.Method != "btree" {
		t.Errorf("Method = %q, want btree", oc.Method)
	}
	if oc.FamilySchema != "dump_test" || oc.FamilyName != "op_family" {
		t.Errorf("FamilySchema/FamilyName = %q/%q, want dump_test/op_family", oc.FamilySchema, oc.FamilyName)
	}
	if oc.StorageType != "bigint" {
		t.Errorf("StorageType = %q, want bigint", oc.StorageType)
	}
	if oc.IsDefault {
		t.Errorf("IsDefault = true, want false (no DEFAULT keyword)")
	}
}

// TestParseCreateOperatorClassDefaultKeyword verifies the DEFAULT keyword
// and an unqualified (no schema, no FAMILY clause) name parse correctly,
// exercising the auto-create-family path's parse-side prerequisites.
func TestParseCreateOperatorClassDefaultKeyword(t *testing.T) {
	stmts, err := Parse("CREATE OPERATOR CLASS op_class_custom DEFAULT FOR TYPE int4 USING btree AS STORAGE int4")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	oc, ok := stmts[0].(*CreateOpClassStmt)
	if !ok {
		t.Fatalf("Expected *CreateOpClassStmt, got %T", stmts[0])
	}
	if !oc.IsDefault {
		t.Errorf("IsDefault = false, want true")
	}
	if oc.Schema != "" || oc.Name != "op_class_custom" {
		t.Errorf("Schema/Name = %q/%q, want \"\"/op_class_custom", oc.Schema, oc.Name)
	}
	if oc.FamilyName != "" {
		t.Errorf("FamilyName = %q, want \"\" (FAMILY clause omitted)", oc.FamilyName)
	}
}

// TestParseCreateOperatorClassForOrderBy verifies an OPERATOR entry's
// opclass_purpose "FOR ORDER BY family_name" clause is captured on the
// member (SortFamilySchema/SortFamilyName) instead of parsed-and-discarded
// — closes the loop #37/#39 ledger rows' "FOR ORDER BY" deferral. DU-002
// (M0119-0004) slice 414.
func TestParseCreateOperatorClassForOrderBy(t *testing.T) {
	stmts, err := Parse(`CREATE OPERATOR CLASS my_opclass FOR TYPE int4 USING btree AS
		OPERATOR 1 ~=~ (int4, int4) FOR ORDER BY dump_test.sort_family,
		OPERATOR 2 = (int4, int4) FOR SEARCH,
		OPERATOR 3 <> (int4, int4)`)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	oc, ok := stmts[0].(*CreateOpClassStmt)
	if !ok {
		t.Fatalf("Expected *CreateOpClassStmt, got %T", stmts[0])
	}
	if len(oc.Members) != 3 {
		t.Fatalf("Members = %d, want 3", len(oc.Members))
	}
	m0 := oc.Members[0]
	if m0.SortFamilySchema != "dump_test" || m0.SortFamilyName != "sort_family" {
		t.Errorf("Members[0] SortFamilySchema/SortFamilyName = %q/%q, want dump_test/sort_family", m0.SortFamilySchema, m0.SortFamilyName)
	}
	if m1 := oc.Members[1]; m1.SortFamilyName != "" {
		t.Errorf("Members[1] (FOR SEARCH) SortFamilyName = %q, want \"\"", m1.SortFamilyName)
	}
	if m2 := oc.Members[2]; m2.SortFamilyName != "" {
		t.Errorf("Members[2] (bare, no opclass_purpose) SortFamilyName = %q, want \"\"", m2.SortFamilyName)
	}
}

// TestParseAlterOperatorFamilyAdd verifies the ADD form (loose OPERATOR/
// FUNCTION members attached directly to a family, opclasscmds.c
// AlterOpFamilyAdd) parses into a real AlterOpFamilyAddStmt — previously
// this whole statement (ADD and DROP alike) was a full no-op consumed to
// ';'. Mirrors the upstream 002_pg_dump.pl `op_family` fixture's own ADD
// list shape (explicit operand types, a FOR ORDER BY clause, and an
// explicit FUNCTION arg-type override). DU-002 (M0119-0004).
func TestParseAlterOperatorFamilyAdd(t *testing.T) {
	stmts, err := Parse(`ALTER OPERATOR FAMILY dump_test.op_family USING btree ADD
		OPERATOR 1 < (int4, int4) FOR ORDER BY dump_test.sort_family,
		OPERATOR 3 = (int4, int4),
		FUNCTION 1 (int4, int4) btint4cmp(int4, int4)`)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	af, ok := stmts[0].(*AlterOpFamilyAddStmt)
	if !ok {
		t.Fatalf("Expected *AlterOpFamilyAddStmt, got %T", stmts[0])
	}
	if af.Schema != "dump_test" || af.Name != "op_family" {
		t.Errorf("Schema/Name = %q/%q, want dump_test/op_family", af.Schema, af.Name)
	}
	if af.Method != "btree" {
		t.Errorf("Method = %q, want btree", af.Method)
	}
	if len(af.Members) != 3 {
		t.Fatalf("Members = %d, want 3", len(af.Members))
	}
	m0 := af.Members[0]
	if m0.IsFunction || m0.Number != 1 || m0.Name != "<" || !m0.HasExplicitArgTypes {
		t.Errorf("Members[0] = %+v, want OPERATOR 1 < with explicit arg types", m0)
	}
	if m0.LeftType != "int4" || m0.RightType != "int4" {
		t.Errorf("Members[0] LeftType/RightType = %q/%q, want int4/int4", m0.LeftType, m0.RightType)
	}
	if m0.SortFamilySchema != "dump_test" || m0.SortFamilyName != "sort_family" {
		t.Errorf("Members[0] SortFamilySchema/SortFamilyName = %q/%q, want dump_test/sort_family", m0.SortFamilySchema, m0.SortFamilyName)
	}
	m1 := af.Members[1]
	if m1.SortFamilyName != "" {
		t.Errorf("Members[1] SortFamilyName = %q, want \"\" (no FOR ORDER BY)", m1.SortFamilyName)
	}
	m2 := af.Members[2]
	if !m2.IsFunction || m2.Number != 1 || m2.Name != "btint4cmp" || m2.LeftType != "int4" || m2.RightType != "int4" {
		t.Errorf("Members[2] = %+v, want FUNCTION 1 (int4,int4) btint4cmp", m2)
	}
}

// TestParseAlterOperatorFamilyAddRequiresArgTypes verifies an OPERATOR entry
// with no explicit (lefttype, righttype) parses successfully (the grammar
// itself allows omitting them — HasExplicitArgTypes just comes back false)
// since PG raises "operator argument types must be specified in ALTER
// OPERATOR FAMILY" at DDL-execution time (opclasscmds.c), not during
// parsing/analysis.
func TestParseAlterOperatorFamilyAddRequiresArgTypes(t *testing.T) {
	stmts, err := Parse(`ALTER OPERATOR FAMILY dump_test.op_family USING btree ADD OPERATOR 1 <`)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	af, ok := stmts[0].(*AlterOpFamilyAddStmt)
	if !ok {
		t.Fatalf("Expected *AlterOpFamilyAddStmt, got %T", stmts[0])
	}
	if len(af.Members) != 1 || af.Members[0].HasExplicitArgTypes {
		t.Errorf("Members = %+v, want one entry with HasExplicitArgTypes=false", af.Members)
	}
}

// TestParseAlterOperatorFamilyDrop verifies the DROP form (opclasscmds.c
// AlterOpFamilyDrop) parses into a real AlterOpFamilyDropStmt — the
// opclass_drop grammar (gram.y) is narrower than the ADD form's
// opclass_item: a mandatory strategy/support number and a mandatory
// parenthesized type list, no operator/function name.
func TestParseAlterOperatorFamilyDrop(t *testing.T) {
	stmts, err := Parse(`ALTER OPERATOR FAMILY dump_test.op_family_loose USING btree DROP
		OPERATOR 1 (int8),
		FUNCTION 1 (int8, int8)`)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	af, ok := stmts[0].(*AlterOpFamilyDropStmt)
	if !ok {
		t.Fatalf("Expected *AlterOpFamilyDropStmt, got %T", stmts[0])
	}
	if af.Schema != "dump_test" || af.Name != "op_family_loose" {
		t.Errorf("Schema/Name = %q/%q, want dump_test/op_family_loose", af.Schema, af.Name)
	}
	if af.Method != "btree" {
		t.Errorf("Method = %q, want btree", af.Method)
	}
	if len(af.Members) != 2 {
		t.Fatalf("Members = %d, want 2", len(af.Members))
	}
	m0 := af.Members[0]
	if m0.IsFunction || m0.Number != 1 || m0.LeftType != "int8" || m0.RightType != "int8" {
		t.Errorf("Members[0] = %+v, want OPERATOR 1 (int8) with righttype defaulted to int8", m0)
	}
	m1 := af.Members[1]
	if !m1.IsFunction || m1.Number != 1 || m1.LeftType != "int8" || m1.RightType != "int8" {
		t.Errorf("Members[1] = %+v, want FUNCTION 1 (int8,int8)", m1)
	}
}

// TestParseAlterOperatorFamilyDropRequiresParens verifies a malformed DROP
// tail (missing the mandatory type-list parens) tolerantly falls back to the
// accepted-and-ignored no-op stub rather than a hard parse error, matching
// every other unrecognized ALTER OPERATOR CLASS|FAMILY tail (RENAME TO,
// OWNER TO, etc. — see TestParseAlterOperatorOwnerToIsNoop).
func TestParseAlterOperatorFamilyDropRequiresParens(t *testing.T) {
	stmts, err := Parse(`ALTER OPERATOR FAMILY dump_test.op_family_loose USING btree DROP OPERATOR 1`)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	af, ok := stmts[0].(*AlterOpFamilyDropStmt)
	if !ok {
		t.Fatalf("Expected *AlterOpFamilyDropStmt, got %T", stmts[0])
	}
	if len(af.Members) != 0 {
		t.Errorf("Members = %+v, want none (missing parens stops the scan)", af.Members)
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
