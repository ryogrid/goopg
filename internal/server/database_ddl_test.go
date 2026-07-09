package server

import (
	"errors"
	"testing"

	"github.com/goopg/goopg/internal/activity"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/sqlstate"
)

// TestClassifyDatabaseDDL pins the M0054-0001 string-prefix matcher
// against the shapes HammerDB and `psql` issue.
func TestClassifyDatabaseDDL(t *testing.T) {
	cases := []struct {
		sql      string
		wantKind databaseDDLKind
		wantName string
	}{
		{"CREATE DATABASE tpch", databaseDDLCreate, "tpch"},
		{"create database tpch;", databaseDDLCreate, "tpch"},
		{"  CREATE DATABASE  Foo  ", databaseDDLCreate, "Foo"},
		{`CREATE DATABASE "Mixed Case"`, databaseDDLCreate, "Mixed Case"},
		{"CREATE DATABASE tpch OWNER tpch", databaseDDLCreate, "tpch"},
		{"DROP DATABASE tpch", databaseDDLDrop, "tpch"},
		{"DROP DATABASE IF EXISTS tpch", databaseDDLDrop, "tpch"},
		{"drop database if exists scratch;", databaseDDLDrop, "scratch"},
		// negatives
		{"CREATE TABLE t (a int)", databaseDDLNone, ""},
		{"SELECT 1", databaseDDLNone, ""},
		{"", databaseDDLNone, ""},
	}
	for _, c := range cases {
		gotKind, gotName := classifyDatabaseDDL(c.sql)
		if gotKind != c.wantKind || gotName != c.wantName {
			t.Errorf("classifyDatabaseDDL(%q) = (%d, %q), want (%d, %q)",
				c.sql, gotKind, gotName, c.wantKind, c.wantName)
		}
	}
}

// TestExtractFirstIdentifier pins the lex helper's behaviour for the
// shapes M0054-0001 actually sees in the wild — bare identifiers and
// double-quoted ones with embedded whitespace.
func TestExtractFirstIdentifier(t *testing.T) {
	cases := map[string]string{
		"tpch":             "tpch",
		"tpch OWNER tpch":  "tpch",
		`"Mixed Case"`:     "Mixed Case",
		`"Mixed" SOMETHING`: "Mixed",
		"tpch;":            "tpch",
		"tpch,more":        "tpch",
		"":                 "",
		"   ":              "",
	}
	for in, want := range cases {
		if got := extractFirstIdentifier(in); got != want {
			t.Errorf("extractFirstIdentifier(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDatabaseDDLCommandTag pins the wire-protocol tag returned for
// each kind. The bare empty string covers the negative case so the
// dispatch path can fall through cleanly.
func TestDatabaseDDLCommandTag(t *testing.T) {
	cases := map[string]string{
		"CREATE DATABASE tpch":              "CREATE DATABASE",
		"DROP DATABASE tpch":                "DROP DATABASE",
		"DROP DATABASE IF EXISTS x":         "DROP DATABASE",
		"ALTER DATABASE postgres SET a = 1": "ALTER DATABASE",
		"ALTER DATABASE postgres RESET a":   "ALTER DATABASE",
		"SELECT 1":                          "",
	}
	for sql, want := range cases {
		if got := databaseDDLCommandTag(sql); got != want {
			t.Errorf("databaseDDLCommandTag(%q) = %q, want %q", sql, got, want)
		}
	}
}

// TestParseAlterDatabaseConfig pins parseAlterDatabaseConfig's classification
// of the SET/RESET/RESET ALL forms goopg's parser cannot recognise (ALTER
// DATABASE requires the literal TABLE keyword — see parseAlter,
// internal/parser/ddl.go), plus its deliberate rejection of every other
// ALTER DATABASE sub-form (which must keep falling through to
// compatNoopCommandTag unchanged). M0119-0004-ACLHEAP (ALTER DATABASE ...
// SET follow-up).
func TestParseAlterDatabaseConfig(t *testing.T) {
	cases := []struct {
		sql  string
		want alterDatabaseConfigOp
		ok   bool
	}{
		{
			sql:  "ALTER DATABASE postgres SET work_mem = '64MB'",
			want: alterDatabaseConfigOp{dbName: "postgres", configName: "work_mem", configValue: "64MB"},
			ok:   true,
		},
		{
			sql:  "alter database postgres set work_mem to '64MB';",
			want: alterDatabaseConfigOp{dbName: "postgres", configName: "work_mem", configValue: "64MB"},
			ok:   true,
		},
		{
			// unquoted / numeric values are stored verbatim (no quotes to strip).
			sql:  "ALTER DATABASE postgres SET statement_timeout = 5000",
			want: alterDatabaseConfigOp{dbName: "postgres", configName: "statement_timeout", configValue: "5000"},
			ok:   true,
		},
		{
			// a GUC_LIST_QUOTE-shaped multi-value SET flattens to a comma
			// join with no per-element quoting (mirrors guc.c
			// flatten_set_variable_args; the display quoting is pg_dump's
			// own client-side job, not goopg's).
			sql:  "ALTER DATABASE postgres SET search_path TO public, pg_catalog",
			want: alterDatabaseConfigOp{dbName: "postgres", configName: "search_path", configValue: "public,pg_catalog"},
			ok:   true,
		},
		{
			// SET ... TO DEFAULT is equivalent to RESET.
			sql:  "ALTER DATABASE postgres SET work_mem TO DEFAULT",
			want: alterDatabaseConfigOp{dbName: "postgres", configName: "work_mem", reset: true},
			ok:   true,
		},
		{
			sql:  `ALTER DATABASE "My DB" SET work_mem = '64MB'`,
			want: alterDatabaseConfigOp{dbName: "My DB", configName: "work_mem", configValue: "64MB"},
			ok:   true,
		},
		{
			sql:  "ALTER DATABASE postgres RESET work_mem",
			want: alterDatabaseConfigOp{dbName: "postgres", configName: "work_mem", reset: true},
			ok:   true,
		},
		{
			sql:  "ALTER DATABASE postgres RESET ALL",
			want: alterDatabaseConfigOp{dbName: "postgres", resetAll: true},
			ok:   true,
		},
		// gram.y set_rest "special syntaxes" — valid inside ALTER DATABASE's
		// SetResetClause exactly like a plain SET (same grammar production).
		{
			sql:  "ALTER DATABASE postgres SET TIME ZONE 'UTC'",
			want: alterDatabaseConfigOp{dbName: "postgres", configName: "timezone", configValue: "UTC"},
			ok:   true,
		},
		{
			sql:  "ALTER DATABASE postgres SET TIME ZONE DEFAULT",
			want: alterDatabaseConfigOp{dbName: "postgres", configName: "timezone", reset: true},
			ok:   true,
		},
		{
			sql:  "ALTER DATABASE postgres SET SCHEMA 'app'",
			want: alterDatabaseConfigOp{dbName: "postgres", configName: "search_path", configValue: "app"},
			ok:   true,
		},
		{
			sql:  "ALTER DATABASE postgres SET NAMES 'utf8'",
			want: alterDatabaseConfigOp{dbName: "postgres", configName: "client_encoding", configValue: "utf8"},
			ok:   true,
		},
		{
			sql:  "ALTER DATABASE postgres SET NAMES",
			want: alterDatabaseConfigOp{dbName: "postgres", configName: "client_encoding", reset: true},
			ok:   true,
		},
		{
			sql:  "ALTER DATABASE postgres SET ROLE 'alice'",
			want: alterDatabaseConfigOp{dbName: "postgres", configName: "role", configValue: "alice"},
			ok:   true,
		},
		{
			sql:  "ALTER DATABASE postgres SET SESSION AUTHORIZATION 'alice'",
			want: alterDatabaseConfigOp{dbName: "postgres", configName: "session_authorization", configValue: "alice"},
			ok:   true,
		},
		{
			sql:  "ALTER DATABASE postgres SET SESSION AUTHORIZATION DEFAULT",
			want: alterDatabaseConfigOp{dbName: "postgres", configName: "session_authorization", reset: true},
			ok:   true,
		},
		{
			sql:  "ALTER DATABASE postgres SET XML OPTION DOCUMENT",
			want: alterDatabaseConfigOp{dbName: "postgres", configName: "xmloption", configValue: "DOCUMENT"},
			ok:   true,
		},
		// "var_name FROM CURRENT" (set_rest_more's VAR_SET_CURRENT) — an
		// arbitrary GUC name, not one of the six special-syntax keywords
		// above; configValue is resolved later at apply time, not here.
		{
			sql:  "ALTER DATABASE postgres SET work_mem FROM CURRENT",
			want: alterDatabaseConfigOp{dbName: "postgres", configName: "work_mem", fromCurrent: true},
			ok:   true,
		},
		{
			sql:  "alter database postgres set search_path from current;",
			want: alterDatabaseConfigOp{dbName: "postgres", configName: "search_path", fromCurrent: true},
			ok:   true,
		},
		// negatives: unmodelled ALTER DATABASE forms must fall through
		// unrecognised so the pre-existing compatNoopCommandTag absorption
		// still handles them.
		{sql: "ALTER DATABASE postgres CONNECTION LIMIT = 5", ok: false},
		{sql: "ALTER DATABASE postgres RENAME TO foo", ok: false},
		{sql: "ALTER DATABASE postgres OWNER TO alice", ok: false},
		{sql: "ALTER DATABASE postgres IS_TEMPLATE true", ok: false},
		{sql: "ALTER TABLE t SET (fillfactor = 50)", ok: false},
		{sql: "SELECT 1", ok: false},
		{sql: "", ok: false},
	}
	for _, c := range cases {
		got, ok := parseAlterDatabaseConfig(c.sql)
		if ok != c.ok {
			t.Errorf("parseAlterDatabaseConfig(%q) ok = %v, want %v (got %+v)", c.sql, ok, c.ok, got)
			continue
		}
		if !ok {
			continue
		}
		if got != c.want {
			t.Errorf("parseAlterDatabaseConfig(%q) = %+v, want %+v", c.sql, got, c.want)
		}
	}
}

// TestTryHandleDatabaseDDLAlterDatabaseConfigFromCurrent covers "SET <name>
// FROM CURRENT" (VAR_SET_CURRENT) for ALTER DATABASE — the role-side sibling
// of TestTryHandleRoleDDLAlterRoleConfigFromCurrent (role_config_test.go).
// The stored value must come from the live session's resolver, not a literal
// in the SQL text (there is none for this form).
func TestTryHandleDatabaseDDLAlterDatabaseConfigFromCurrent(t *testing.T) {
	s := newTestRoleServer()
	im := s.cfg.Catalog.(*catalog.InMemory)

	live := map[string]string{"work_mem": "128MB"}
	resolver := currentGUCResolver(func(name string) (string, bool) {
		v, ok := live[name]
		return v, ok
	})

	handled, _, err := s.tryHandleDatabaseDDL("ALTER DATABASE postgres SET work_mem FROM CURRENT", "postgres", resolver)
	if !handled || err != nil {
		t.Fatalf("ALTER DATABASE postgres SET work_mem FROM CURRENT: handled=%v err=%v", handled, err)
	}
	if got := im.DatabaseConfigEntries(catalog.FirstUserOID); len(got) != 1 || got[0] != "work_mem=128MB" {
		t.Errorf("DatabaseConfigEntries(FirstUserOID) = %v, want [work_mem=128MB]", got)
	}

	// An unrecognised GUC name (resolver reports ok=false) errors.
	handled, _, err = s.tryHandleDatabaseDDL("ALTER DATABASE postgres SET no_such_guc FROM CURRENT", "postgres", resolver)
	if !handled {
		t.Fatal("ALTER DATABASE postgres SET no_such_guc FROM CURRENT: expected handled=true")
	}
	if err == nil {
		t.Fatal("ALTER DATABASE postgres SET no_such_guc FROM CURRENT: expected an error")
	}

	// A nil resolver (no live session) behaves the same as ok=false.
	handled, _, err = s.tryHandleDatabaseDDL("ALTER DATABASE postgres SET work_mem FROM CURRENT", "postgres", nil)
	if !handled || err == nil {
		t.Fatalf("ALTER DATABASE postgres SET work_mem FROM CURRENT (nil resolver): handled=%v err=%v", handled, err)
	}

	// A database other than the connection's own live database is a silent
	// no-op — the resolver must never be consulted (a nil resolver here must
	// NOT error), mirroring applyAlterDatabaseConfig's identical restriction
	// for the literal-value forms.
	handled, _, err = s.tryHandleDatabaseDDL("ALTER DATABASE otherdb SET work_mem FROM CURRENT", "postgres", nil)
	if !handled || err != nil {
		t.Fatalf("ALTER DATABASE otherdb SET work_mem FROM CURRENT: handled=%v err=%v", handled, err)
	}
}

// TestDatabaseDDLErrorSQLState pins the M0119-0004-ACLHEAP loop #79
// deferral fix: tryHandleDatabaseDDL/applyAlterDatabaseConfig errors now
// carry PG's real SQLSTATE (via databaseDDLError) instead of every error
// mapping to the generic sqlstate.SystemError dispatch.go used to hardcode
// unconditionally — mirroring the role-DDL side's roleError/roleErrorSQLState.
func TestDatabaseDDLErrorSQLState(t *testing.T) {
	s := newTestRoleServer()
	im := s.cfg.Catalog.(*catalog.InMemory)
	if err := im.CreateDatabase("tpch"); err != nil {
		t.Fatalf("seed CreateDatabase(tpch): %v", err)
	}

	// CREATE DATABASE on an existing name: PG ERRCODE_DUPLICATE_DATABASE (42P04).
	handled, _, err := s.tryHandleDatabaseDDL("CREATE DATABASE tpch", "postgres", nil)
	if !handled || err == nil {
		t.Fatalf("CREATE DATABASE tpch (duplicate): handled=%v err=%v", handled, err)
	}
	if got := databaseDDLErrorSQLState(err); got != sqlstate.DuplicateDatabase {
		t.Errorf("databaseDDLErrorSQLState(duplicate create) = %q, want %q", got, sqlstate.DuplicateDatabase)
	}
	if want := `database "tpch" already exists`; err.Error() != want {
		t.Errorf("CREATE DATABASE tpch (duplicate) message = %q, want %q", err.Error(), want)
	}

	// DROP DATABASE on a nonexistent name (no IF EXISTS): PG
	// ERRCODE_UNDEFINED_DATABASE (3D000).
	handled, _, err = s.tryHandleDatabaseDDL("DROP DATABASE nosuchdb", "postgres", nil)
	if !handled || err == nil {
		t.Fatalf("DROP DATABASE nosuchdb: handled=%v err=%v", handled, err)
	}
	if got := databaseDDLErrorSQLState(err); got != sqlstate.UndefinedDatabase {
		t.Errorf("databaseDDLErrorSQLState(undefined drop) = %q, want %q", got, sqlstate.UndefinedDatabase)
	}
	if want := `database "nosuchdb" does not exist`; err.Error() != want {
		t.Errorf("DROP DATABASE nosuchdb message = %q, want %q", err.Error(), want)
	}

	// ALTER DATABASE ... SET ... FROM CURRENT on an unresolved GUC name: PG
	// ERRCODE_UNDEFINED_OBJECT (42704), same code SHOW/SET already use.
	handled, _, err = s.tryHandleDatabaseDDL("ALTER DATABASE postgres SET no_such_guc FROM CURRENT", "postgres",
		currentGUCResolver(func(string) (string, bool) { return "", false }))
	if !handled || err == nil {
		t.Fatalf("ALTER DATABASE postgres SET no_such_guc FROM CURRENT: handled=%v err=%v", handled, err)
	}
	if got := databaseDDLErrorSQLState(err); got != sqlstate.UndefinedObject {
		t.Errorf("databaseDDLErrorSQLState(unrecognized GUC) = %q, want %q", got, sqlstate.UndefinedObject)
	}

	// A WAL-append-shaped internal error (not a databaseDDLError) still
	// falls back to the generic sqlstate.SystemError.
	if got := databaseDDLErrorSQLState(errUnwrappedForTest); got != sqlstate.SystemError {
		t.Errorf("databaseDDLErrorSQLState(plain error) = %q, want %q", got, sqlstate.SystemError)
	}
}

var errUnwrappedForTest = errors.New("boom")

// TestTryHandleDatabaseDDLDropGuards pins the three DROP DATABASE guard
// checks mirrored from PG's dropdb() (dbcommands.c): template protection,
// "can't drop my own database", and the other-backends busy check — all
// three were entirely absent before this change (DropDatabase was a bare
// map delete with no guards at all).
func TestTryHandleDatabaseDDLDropGuards(t *testing.T) {
	s := newTestRoleServer()
	im := s.cfg.Catalog.(*catalog.InMemory)
	if err := im.CreateDatabase("tpch"); err != nil {
		t.Fatalf("seed CreateDatabase(tpch): %v", err)
	}

	// PG: dropdb(), "cannot drop a template database" (42809).
	for _, name := range []string{"template0", "template1"} {
		handled, _, err := s.tryHandleDatabaseDDL("DROP DATABASE "+name, "postgres", nil)
		if !handled || err == nil {
			t.Fatalf("DROP DATABASE %s: handled=%v err=%v, want an error", name, handled, err)
		}
		if got := databaseDDLErrorSQLState(err); got != sqlstate.WrongObjectType {
			t.Errorf("DROP DATABASE %s: sqlstate = %q, want %q", name, got, sqlstate.WrongObjectType)
		}
		if want := "cannot drop a template database"; err.Error() != want {
			t.Errorf("DROP DATABASE %s: message = %q, want %q", name, err.Error(), want)
		}
		if !im.HasDatabase(name) {
			t.Errorf("DROP DATABASE %s: database was removed despite the rejection", name)
		}
	}

	// PG: dropdb(), "cannot drop the currently open database" (55006) —
	// the connection's own liveDBName is the target.
	handled, _, err := s.tryHandleDatabaseDDL("DROP DATABASE postgres", "postgres", nil)
	if !handled || err == nil {
		t.Fatalf("DROP DATABASE postgres (self): handled=%v err=%v, want an error", handled, err)
	}
	if got := databaseDDLErrorSQLState(err); got != sqlstate.ObjectInUse {
		t.Errorf("DROP DATABASE postgres (self): sqlstate = %q, want %q", got, sqlstate.ObjectInUse)
	}
	if want := "cannot drop the currently open database"; err.Error() != want {
		t.Errorf("DROP DATABASE postgres (self): message = %q, want %q", err.Error(), want)
	}
	if !im.HasDatabase("postgres") {
		t.Error("DROP DATABASE postgres (self): database was removed despite the rejection")
	}

	// PG: dropdb(), CountOtherDBBackends() -> "is being accessed by other
	// users" (55006). Register a fake backend connected to tpch.
	reg := activity.NewRegistry()
	s.cfg.Activity = reg
	reg.Register(&activity.Backend{PID: "999", DatName: "tpch", BackendType: "client_backend"})
	defer reg.Unregister("999")

	handled, _, err = s.tryHandleDatabaseDDL("DROP DATABASE tpch", "postgres", nil)
	if !handled || err == nil {
		t.Fatalf("DROP DATABASE tpch (busy): handled=%v err=%v, want an error", handled, err)
	}
	if got := databaseDDLErrorSQLState(err); got != sqlstate.ObjectInUse {
		t.Errorf("DROP DATABASE tpch (busy): sqlstate = %q, want %q", got, sqlstate.ObjectInUse)
	}
	if want := `database "tpch" is being accessed by other users`; err.Error() != want {
		t.Errorf("DROP DATABASE tpch (busy): message = %q, want %q", err.Error(), want)
	}
	if !im.HasDatabase("tpch") {
		t.Error("DROP DATABASE tpch (busy): database was removed despite the rejection")
	}

	// Once the other backend disconnects, the drop succeeds.
	reg.Unregister("999")
	handled, _, err = s.tryHandleDatabaseDDL("DROP DATABASE tpch", "postgres", nil)
	if !handled || err != nil {
		t.Fatalf("DROP DATABASE tpch (idle): handled=%v err=%v, want success", handled, err)
	}
	if im.HasDatabase("tpch") {
		t.Error("DROP DATABASE tpch (idle): database still registered after a successful drop")
	}
}
