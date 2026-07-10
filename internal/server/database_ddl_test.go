package server

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/goopg/goopg/internal/activity"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/sqlstate"
	"github.com/goopg/goopg/internal/storage"
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

	handled, _, err := s.tryHandleDatabaseDDL("ALTER DATABASE postgres SET work_mem FROM CURRENT", "postgres", "", resolver)
	if !handled || err != nil {
		t.Fatalf("ALTER DATABASE postgres SET work_mem FROM CURRENT: handled=%v err=%v", handled, err)
	}
	if got := im.DatabaseConfigEntries(catalog.FirstUserOID); len(got) != 1 || got[0] != "work_mem=128MB" {
		t.Errorf("DatabaseConfigEntries(FirstUserOID) = %v, want [work_mem=128MB]", got)
	}

	// An unrecognised GUC name (resolver reports ok=false) errors.
	handled, _, err = s.tryHandleDatabaseDDL("ALTER DATABASE postgres SET no_such_guc FROM CURRENT", "postgres", "", resolver)
	if !handled {
		t.Fatal("ALTER DATABASE postgres SET no_such_guc FROM CURRENT: expected handled=true")
	}
	if err == nil {
		t.Fatal("ALTER DATABASE postgres SET no_such_guc FROM CURRENT: expected an error")
	}

	// A nil resolver (no live session) behaves the same as ok=false.
	handled, _, err = s.tryHandleDatabaseDDL("ALTER DATABASE postgres SET work_mem FROM CURRENT", "postgres", "", nil)
	if !handled || err == nil {
		t.Fatalf("ALTER DATABASE postgres SET work_mem FROM CURRENT (nil resolver): handled=%v err=%v", handled, err)
	}

	// A database other than the connection's own live database is a silent
	// no-op — the resolver must never be consulted (a nil resolver here must
	// NOT error), mirroring applyAlterDatabaseConfig's identical restriction
	// for the literal-value forms.
	handled, _, err = s.tryHandleDatabaseDDL("ALTER DATABASE otherdb SET work_mem FROM CURRENT", "postgres", "", nil)
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
	if _, err := im.CreateDatabase("tpch", catalog.BootstrapSuperuserOID); err != nil {
		t.Fatalf("seed CreateDatabase(tpch): %v", err)
	}

	// CREATE DATABASE on an existing name: PG ERRCODE_DUPLICATE_DATABASE (42P04).
	handled, _, err := s.tryHandleDatabaseDDL("CREATE DATABASE tpch", "postgres", "", nil)
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
	handled, _, err = s.tryHandleDatabaseDDL("DROP DATABASE nosuchdb", "postgres", "", nil)
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
	handled, _, err = s.tryHandleDatabaseDDL("ALTER DATABASE postgres SET no_such_guc FROM CURRENT", "postgres", "",
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

// TestTryHandleDatabaseDDLDropRequiresOwnership pins the M0122-0007 DROP
// DATABASE ownership check mirrored from dbcommands.c dropdb()'s
// object_ownercheck call: CREATE DATABASE records the creating role as
// datdba, and only that role (or a superuser) may DROP it — everyone else
// gets 42501 "must be owner of database %s", matching real PG's message and
// SQLSTATE exactly.
func TestTryHandleDatabaseDDLDropRequiresOwnership(t *testing.T) {
	s := newTestRoleServer()
	im := s.cfg.Catalog.(*catalog.InMemory)

	for _, role := range []string{"alice", "bob"} {
		if handled, err := s.tryHandleRoleDDL("CREATE ROLE "+role+" LOGIN", "postgres", nil); !handled || err != nil {
			t.Fatalf("CREATE ROLE %s: handled=%v err=%v", role, handled, err)
		}
	}
	aliceOID, ok := im.RoleOID("alice")
	if !ok {
		t.Fatal("role alice not registered in catalog")
	}

	handled, _, err := s.tryHandleDatabaseDDL("CREATE DATABASE aliceonly", "postgres", "alice", nil)
	if !handled || err != nil {
		t.Fatalf("CREATE DATABASE aliceonly (as alice): handled=%v err=%v", handled, err)
	}
	if got := im.DatabaseOwner("aliceonly"); got != aliceOID {
		t.Errorf("DatabaseOwner(aliceonly) = %d, want alice's OID %d", got, aliceOID)
	}

	// A non-owner, non-superuser role is rejected.
	handled, _, err = s.tryHandleDatabaseDDL("DROP DATABASE aliceonly", "postgres", "bob", nil)
	if !handled || err == nil {
		t.Fatalf("DROP DATABASE aliceonly (as bob): handled=%v err=%v, want an error", handled, err)
	}
	if got := databaseDDLErrorSQLState(err); got != sqlstate.InsufficientPrivilege {
		t.Errorf("DROP DATABASE aliceonly (as bob): sqlstate = %q, want %q", got, sqlstate.InsufficientPrivilege)
	}
	if want := "must be owner of database aliceonly"; err.Error() != want {
		t.Errorf("DROP DATABASE aliceonly (as bob): message = %q, want %q", err.Error(), want)
	}
	if !im.HasDatabase("aliceonly") {
		t.Error("DROP DATABASE aliceonly (as bob): database was removed despite the rejection")
	}

	// The bootstrap superuser (actingRole "") may drop it despite not owning it.
	handled, _, err = s.tryHandleDatabaseDDL("DROP DATABASE aliceonly", "postgres", "", nil)
	if !handled || err != nil {
		t.Fatalf("DROP DATABASE aliceonly (as superuser): handled=%v err=%v, want success", handled, err)
	}
	if im.HasDatabase("aliceonly") {
		t.Error("DROP DATABASE aliceonly (as superuser): database still registered after a successful drop")
	}

	// The owning role itself may always drop its own database.
	handled, _, err = s.tryHandleDatabaseDDL("CREATE DATABASE aliceonly2", "postgres", "alice", nil)
	if !handled || err != nil {
		t.Fatalf("CREATE DATABASE aliceonly2 (as alice): handled=%v err=%v", handled, err)
	}
	handled, _, err = s.tryHandleDatabaseDDL("DROP DATABASE aliceonly2", "postgres", "alice", nil)
	if !handled || err != nil {
		t.Fatalf("DROP DATABASE aliceonly2 (as owner alice): handled=%v err=%v, want success", handled, err)
	}
	if im.HasDatabase("aliceonly2") {
		t.Error("DROP DATABASE aliceonly2 (as owner alice): database still registered after a successful drop")
	}
}

// TestTryHandleDatabaseDDLCreateCreatesPhysicalDirectory pins M0122-0007
// physical-storage-isolation slice 2: CREATE DATABASE must create
// base/<oid>/PG_VERSION under the server's real data directory (a Pool
// wired to a Manager, unlike newTestRoleServer's catalog-only fixture).
func TestTryHandleDatabaseDDLCreateCreatesPhysicalDirectory(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 4})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()

	s := New(Config{Catalog: catalog.NewInMemory(), Pool: pool})
	im := s.cfg.Catalog.(*catalog.InMemory)

	handled, _, err := s.tryHandleDatabaseDDL("CREATE DATABASE physdirtest", "postgres", "", nil)
	if !handled || err != nil {
		t.Fatalf("CREATE DATABASE physdirtest: handled=%v err=%v", handled, err)
	}
	oid := im.DatabaseOid("physdirtest")
	if oid == 0 {
		t.Fatal("DatabaseOid(physdirtest) = 0, want a real allocated oid")
	}
	versionFile := filepath.Join(dataDir, "base", strconv.FormatUint(uint64(oid), 10), "PG_VERSION")
	if _, err := os.Stat(versionFile); err != nil {
		t.Errorf("base/%d/PG_VERSION missing after CREATE DATABASE: %v", oid, err)
	}
}

// TestTryHandleDatabaseDDLDropRemovesPhysicalDirectory pins M0122-0007
// physical-storage-isolation slice 3: DROP DATABASE must remove the
// base/<oid> directory the matching CREATE DATABASE allocated, not leave
// it orphaned forever.
func TestTryHandleDatabaseDDLDropRemovesPhysicalDirectory(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 4})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()

	s := New(Config{Catalog: catalog.NewInMemory(), Pool: pool})
	im := s.cfg.Catalog.(*catalog.InMemory)

	handled, _, err := s.tryHandleDatabaseDDL("CREATE DATABASE dropdirtest", "postgres", "", nil)
	if !handled || err != nil {
		t.Fatalf("CREATE DATABASE dropdirtest: handled=%v err=%v", handled, err)
	}
	oid := im.DatabaseOid("dropdirtest")
	dbDir := filepath.Join(dataDir, "base", strconv.FormatUint(uint64(oid), 10))
	if _, err := os.Stat(dbDir); err != nil {
		t.Fatalf("base/%d missing right after CREATE DATABASE: %v", oid, err)
	}

	handled, _, err = s.tryHandleDatabaseDDL("DROP DATABASE dropdirtest", "postgres", "", nil)
	if !handled || err != nil {
		t.Fatalf("DROP DATABASE dropdirtest: handled=%v err=%v", handled, err)
	}
	if _, err := os.Stat(dbDir); !os.IsNotExist(err) {
		t.Errorf("base/%d still present after DROP DATABASE: err=%v", oid, err)
	}
}

// TestTryHandleDatabaseDDLCreateNoPoolIsNoop confirms a nil Pool (the
// catalog-only test fixtures every other database_ddl_test.go case uses)
// does not turn createDatabasePhysicalDirectory into an error — matches
// how other DDL operators skip cluster-filesystem effects in that context.
func TestTryHandleDatabaseDDLCreateNoPoolIsNoop(t *testing.T) {
	s := newTestRoleServer()
	handled, _, err := s.tryHandleDatabaseDDL("CREATE DATABASE nopooltest", "postgres", "", nil)
	if !handled || err != nil {
		t.Fatalf("CREATE DATABASE nopooltest (nil Pool): handled=%v err=%v", handled, err)
	}
}

// TestTryHandleDatabaseDDLDropGuards pins the three DROP DATABASE guard
// checks mirrored from PG's dropdb() (dbcommands.c): template protection,
// "can't drop my own database", and the other-backends busy check — all
// three were entirely absent before this change (DropDatabase was a bare
// map delete with no guards at all).
func TestTryHandleDatabaseDDLDropGuards(t *testing.T) {
	s := newTestRoleServer()
	im := s.cfg.Catalog.(*catalog.InMemory)
	if _, err := im.CreateDatabase("tpch", catalog.BootstrapSuperuserOID); err != nil {
		t.Fatalf("seed CreateDatabase(tpch): %v", err)
	}

	// PG: dropdb(), "cannot drop a template database" (42809).
	for _, name := range []string{"template0", "template1"} {
		handled, _, err := s.tryHandleDatabaseDDL("DROP DATABASE "+name, "postgres", "", nil)
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
	handled, _, err := s.tryHandleDatabaseDDL("DROP DATABASE postgres", "postgres", "", nil)
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

	handled, _, err = s.tryHandleDatabaseDDL("DROP DATABASE tpch", "postgres", "", nil)
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
	handled, _, err = s.tryHandleDatabaseDDL("DROP DATABASE tpch", "postgres", "", nil)
	if !handled || err != nil {
		t.Fatalf("DROP DATABASE tpch (idle): handled=%v err=%v, want success", handled, err)
	}
	if im.HasDatabase("tpch") {
		t.Error("DROP DATABASE tpch (idle): database still registered after a successful drop")
	}
}

// TestTryHandleDatabaseDDLDropForceTerminatesOtherBackends pins the WITH
// (FORCE) path (M0122-0007): a busy database that would otherwise fail the
// "is being accessed by other users" guard succeeds instead, because FORCE
// signals the other backend's connection-termination function (the same
// process-wide cancelReg.terminateByPID path pg_terminate_backend(pid)
// already uses) before the busy check runs.
func TestTryHandleDatabaseDDLDropForceTerminatesOtherBackends(t *testing.T) {
	s := newTestRoleServer()
	im := s.cfg.Catalog.(*catalog.InMemory)
	if _, err := im.CreateDatabase("tpch", catalog.BootstrapSuperuserOID); err != nil {
		t.Fatalf("seed CreateDatabase(tpch): %v", err)
	}

	reg := activity.NewRegistry()
	s.cfg.Activity = reg
	reg.Register(&activity.Backend{PID: "999", DatName: "tpch", BackendType: "client_backend"})
	defer reg.Unregister("999")

	// Simulate a live backend: registering a cancelEntry with a terminate
	// function that (mirroring what a real connection's serve-loop teardown
	// does) unregisters the backend from the activity registry once fired.
	terminated := false
	entry := s.cancelReg.register(999, 1)
	entry.setTerminate(func() {
		terminated = true
		reg.Unregister("999")
	})
	defer s.cancelReg.unregister(999)

	// Without FORCE, the plain busy check still rejects — unchanged.
	handled, _, err := s.tryHandleDatabaseDDL("DROP DATABASE tpch", "postgres", "", nil)
	if !handled || err == nil {
		t.Fatalf("DROP DATABASE tpch (busy, no force): handled=%v err=%v, want an error", handled, err)
	}
	if terminated {
		t.Error("DROP DATABASE tpch (no force): terminate function fired without WITH (FORCE)")
	}
	if !im.HasDatabase("tpch") {
		t.Error("DROP DATABASE tpch (busy, no force): database was removed despite the rejection")
	}

	// WITH (FORCE) terminates the other backend and succeeds.
	handled, _, err = s.tryHandleDatabaseDDL("DROP DATABASE tpch WITH (FORCE)", "postgres", "", nil)
	if !handled || err != nil {
		t.Fatalf("DROP DATABASE tpch WITH (FORCE): handled=%v err=%v, want success", handled, err)
	}
	if !terminated {
		t.Error("DROP DATABASE tpch WITH (FORCE): terminate function never fired")
	}
	if im.HasDatabase("tpch") {
		t.Error("DROP DATABASE tpch WITH (FORCE): database still registered after a successful drop")
	}
}

func TestDropDatabaseHasForce(t *testing.T) {
	cases := []struct {
		sql  string
		want bool
	}{
		{"DROP DATABASE foo", false},
		{"DROP DATABASE foo WITH (FORCE)", true},
		{"DROP DATABASE foo WITH ( FORCE )", true},
		{"drop database foo (force)", true},
		{"DROP DATABASE IF EXISTS foo WITH (FORCE);", true},
		{"DROP DATABASE foo WITH (FORCE) ", true},
		{"DROP DATABASE foo TEMPLATE=false", false},
	}
	for _, c := range cases {
		if got := dropDatabaseHasForce(c.sql); got != c.want {
			t.Errorf("dropDatabaseHasForce(%q) = %v, want %v", c.sql, got, c.want)
		}
	}
}

// TestCreateDatabaseTemplateName pins createDatabaseTemplateName's parsing,
// including the "template1" default (mirrors dbcommands.c createdb()'s own
// default) and the false-positive guard: a new database name that happens to
// start with "template" must never be mistaken for the TEMPLATE option
// itself (the option regex is applied only to the substring AFTER the parsed
// database name, see the function's own doc comment).
func TestCreateDatabaseTemplateName(t *testing.T) {
	cases := []struct {
		sql  string
		want string
	}{
		{"CREATE DATABASE foo", "template1"},
		{"CREATE DATABASE foo TEMPLATE bar", "bar"},
		{"CREATE DATABASE foo TEMPLATE=bar", "bar"},
		{"CREATE DATABASE foo TEMPLATE = bar", "bar"},
		{"create database foo template bar", "bar"},
		{`CREATE DATABASE foo TEMPLATE "Bar"`, "Bar"},
		{"CREATE DATABASE foo OWNER me TEMPLATE bar ENCODING 'UTF8'", "bar"},
		{"CREATE DATABASE foo;", "template1"},
		// A database name that merely starts with "template" is not the
		// TEMPLATE option and must not be misparsed as one.
		{"CREATE DATABASE template_scratch", "template1"},
		{"CREATE DATABASE template_scratch TEMPLATE bar", "bar"},
		{"DROP DATABASE foo", "template1"}, // not a CREATE — falls back to the default
	}
	for _, c := range cases {
		if got := createDatabaseTemplateName(c.sql); got != c.want {
			t.Errorf("createDatabaseTemplateName(%q) = %q, want %q", c.sql, got, c.want)
		}
	}
}

// TestTryHandleDatabaseDDLCreateTemplateDoesNotExistErrors pins
// resolveCreateDatabaseTemplate's existence check (mirrors dbcommands.c
// createdb()'s get_db_info ERRCODE_UNDEFINED_DATABASE case): naming a
// nonexistent database as TEMPLATE must surface a typed error, not silently
// create an empty database as classifyDatabaseDDL's pre-existing
// "trailing options are ignored" looseness used to.
func TestTryHandleDatabaseDDLCreateTemplateDoesNotExistErrors(t *testing.T) {
	s := newTestRoleServer()
	im := s.cfg.Catalog.(*catalog.InMemory)

	handled, _, err := s.tryHandleDatabaseDDL("CREATE DATABASE foo TEMPLATE bogus", "postgres", "", nil)
	if !handled || err == nil {
		t.Fatalf("CREATE DATABASE foo TEMPLATE bogus: handled=%v err=%v, want a typed error", handled, err)
	}
	if got := databaseDDLErrorSQLState(err); got != sqlstate.UndefinedDatabase {
		t.Errorf("SQLSTATE = %q, want %q", got, sqlstate.UndefinedDatabase)
	}
	if im.HasDatabase("foo") {
		t.Error("CREATE DATABASE foo TEMPLATE bogus: database was registered despite the error")
	}
}

// TestTryHandleDatabaseDDLCreateEmptyTemplateSucceeds pins the one template
// shape goopg's still-bounded TEMPLATE support fully honors today: a
// template database with zero user relations (tables, views, sequences —
// AllTables) succeeds exactly like a plain CREATE DATABASE, whether the
// template is implicit (no TEMPLATE clause, defaulting to template1),
// explicitly "template1", or a freshly created empty user database.
func TestTryHandleDatabaseDDLCreateEmptyTemplateSucceeds(t *testing.T) {
	s := newTestRoleServer()
	im := s.cfg.Catalog.(*catalog.InMemory)

	if handled, _, err := s.tryHandleDatabaseDDL("CREATE DATABASE emptysrc", "postgres", "", nil); !handled || err != nil {
		t.Fatalf("CREATE DATABASE emptysrc: handled=%v err=%v", handled, err)
	}

	cases := []string{
		"CREATE DATABASE foo1",
		"CREATE DATABASE foo2 TEMPLATE template1",
		"CREATE DATABASE foo3 TEMPLATE emptysrc",
	}
	for _, sql := range cases {
		handled, _, err := s.tryHandleDatabaseDDL(sql, "postgres", "", nil)
		if !handled || err != nil {
			t.Errorf("%s: handled=%v err=%v", sql, handled, err)
		}
	}
	for _, name := range []string{"foo1", "foo2", "foo3"} {
		if !im.HasDatabase(name) {
			t.Errorf("database %q was not registered", name)
		}
	}
}

// TestTryHandleDatabaseDDLCreatePlainTableTemplateCopies pins the
// M0122-0007 4e relation-copy mechanism: a template database whose only user
// relation is a plain, unindexed heap table is now actually COPIED (not
// silently ignored, and not rejected) — a real, semantics-preserving TEMPLATE
// per real PostgreSQL's createdb(), rather than either of the two prior wrong
// behaviors (silently producing an empty database, or the interim
// FeatureNotSupported rejection this loop replaces for the plain-table case).
// This fixture's server has no Pool/TxnMgr wired (newTestRoleServer), so the
// physical file copy and catalog-heap sync are both no-ops — only the
// in-memory catalog.Table clone under the new dbOid is exercised here; the
// full physical-copy + restart-durability path is covered by
// TestCreateDatabaseTemplatePlainTableCopiesDataAndSurvivesRestart
// (table_dbid_restart_test.go).
func TestTryHandleDatabaseDDLCreatePlainTableTemplateCopies(t *testing.T) {
	s := newTestRoleServer()
	im := s.cfg.Catalog.(*catalog.InMemory)

	if handled, _, err := s.tryHandleDatabaseDDL("CREATE DATABASE nonemptysrc", "postgres", "", nil); !handled || err != nil {
		t.Fatalf("CREATE DATABASE nonemptysrc: handled=%v err=%v", handled, err)
	}
	srcOid := im.DatabaseOid("nonemptysrc")
	cols := []catalog.Column{{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 0}}
	if _, err := im.CreateTable(parser.ObjectName{Name: "t"}, cols, srcOid); err != nil {
		t.Fatalf("CreateTable under nonemptysrc: %v", err)
	}

	handled, _, err := s.tryHandleDatabaseDDL("CREATE DATABASE foo TEMPLATE nonemptysrc", "postgres", "", nil)
	if !handled || err != nil {
		t.Fatalf("CREATE DATABASE foo TEMPLATE nonemptysrc: handled=%v err=%v, want success", handled, err)
	}
	if !im.HasDatabase("foo") {
		t.Fatal("CREATE DATABASE foo TEMPLATE nonemptysrc: database was not registered")
	}
	newOid := im.DatabaseOid("foo")
	tables := im.AllTables(newOid)
	if len(tables) != 1 || tables[0].Name != "t" {
		t.Fatalf("AllTables(foo) = %+v, want exactly one table named \"t\"", tables)
	}
	if tables[0].OID == srcOid {
		t.Error("copied table kept the template's own OID instead of getting a fresh one")
	}
}

// TestTryHandleDatabaseDDLCreateTemplateWithIndexErrors pins the still-
// rejected shape: a template containing an INDEX cannot be copied by this
// bounded slice (no index-file cloning implemented yet) even though its
// table alone would otherwise qualify — must fail LOUDLY with a typed
// FeatureNotSupported error, not silently produce an empty or a
// partially-copied database.
func TestTryHandleDatabaseDDLCreateTemplateWithIndexErrors(t *testing.T) {
	s := newTestRoleServer()
	im := s.cfg.Catalog.(*catalog.InMemory)

	if handled, _, err := s.tryHandleDatabaseDDL("CREATE DATABASE idxsrc", "postgres", "", nil); !handled || err != nil {
		t.Fatalf("CREATE DATABASE idxsrc: handled=%v err=%v", handled, err)
	}
	srcOid := im.DatabaseOid("idxsrc")
	cols := []catalog.Column{{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 0}}
	tbl, err := im.CreateTable(parser.ObjectName{Name: "t"}, cols, srcOid)
	if err != nil {
		t.Fatalf("CreateTable under idxsrc: %v", err)
	}
	if _, err := im.CreateIndex(parser.ObjectName{Name: "t_id_idx"}, tbl, []string{"id"}, false, "btree", false, srcOid); err != nil {
		t.Fatalf("CreateIndex under idxsrc: %v", err)
	}

	handled, _, err := s.tryHandleDatabaseDDL("CREATE DATABASE foo TEMPLATE idxsrc", "postgres", "", nil)
	if !handled || err == nil {
		t.Fatalf("CREATE DATABASE foo TEMPLATE idxsrc: handled=%v err=%v, want a typed error", handled, err)
	}
	if got := databaseDDLErrorSQLState(err); got != sqlstate.FeatureNotSupported {
		t.Errorf("SQLSTATE = %q, want %q", got, sqlstate.FeatureNotSupported)
	}
	if im.HasDatabase("foo") {
		t.Error("CREATE DATABASE foo TEMPLATE idxsrc: database was registered despite the error")
	}
}

// TestTryHandleDatabaseDDLCreateTemplateWithSequenceErrors pins the same
// still-rejected shape for a sequence relation.
func TestTryHandleDatabaseDDLCreateTemplateWithSequenceErrors(t *testing.T) {
	s := newTestRoleServer()
	im := s.cfg.Catalog.(*catalog.InMemory)

	if handled, _, err := s.tryHandleDatabaseDDL("CREATE DATABASE seqsrc", "postgres", "", nil); !handled || err != nil {
		t.Fatalf("CREATE DATABASE seqsrc: handled=%v err=%v", handled, err)
	}
	srcOid := im.DatabaseOid("seqsrc")
	seqCols := []catalog.Column{
		{Name: "last_value", Type: catalog.Type{Name: "int8"}, Ordinal: 0},
		{Name: "log_cnt", Type: catalog.Type{Name: "int8"}, Ordinal: 1},
		{Name: "is_called", Type: catalog.Type{Name: "bool"}, Ordinal: 2},
	}
	seq, err := im.CreateTable(parser.ObjectName{Name: "s"}, seqCols, srcOid)
	if err != nil {
		t.Fatalf("CreateTable(sequence) under seqsrc: %v", err)
	}
	seq.IsSequence = true

	handled, _, err := s.tryHandleDatabaseDDL("CREATE DATABASE foo TEMPLATE seqsrc", "postgres", "", nil)
	if !handled || err == nil {
		t.Fatalf("CREATE DATABASE foo TEMPLATE seqsrc: handled=%v err=%v, want a typed error", handled, err)
	}
	if got := databaseDDLErrorSQLState(err); got != sqlstate.FeatureNotSupported {
		t.Errorf("SQLSTATE = %q, want %q", got, sqlstate.FeatureNotSupported)
	}
	if im.HasDatabase("foo") {
		t.Error("CREATE DATABASE foo TEMPLATE seqsrc: database was registered despite the error")
	}
}
