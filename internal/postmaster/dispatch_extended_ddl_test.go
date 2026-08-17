package postmaster

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/libpq"
	"github.com/goopg/goopg/internal/utils/errcodes"
)

// dialAndCompleteDB is dialAndComplete but connects to a named database
// (dialAndComplete leaves "database" unset, which is fine for most
// extended-protocol tests but not for exercising ALTER DATABASE ... SET,
// whose v0-scope restriction only takes effect against the connection's
// OWN live database).
func dialAndCompleteDB(t *testing.T, addr, dbName string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	writeStartupPacket(t, conn, map[string]string{"user": "postgres", "database": dbName})
	r := libpq.NewFrameReader(conn)
	for {
		f, err := r.ReadFrame()
		if err != nil {
			t.Fatalf("drain startup: %v", err)
		}
		if f.Type == libpq.MsgReadyForQuery {
			return conn
		}
	}
}

// extendedExec drives one Parse/Bind/Execute/Sync round trip for query
// (no bind parameters) and returns the frames up to ReadyForQuery.
func extendedExec(t *testing.T, conn net.Conn, name, query string) []libpq.Frame {
	t.Helper()
	writeFrontendFrame(t, conn, libpq.MsgParse, parsePayload(name, query, nil))
	writeFrontendFrame(t, conn, libpq.MsgBind, bindPayload("", name, nil, nil, nil))
	writeFrontendFrame(t, conn, libpq.MsgExecute, executePayload("", 0))
	writeFrontendFrame(t, conn, libpq.MsgSync, nil)
	return readUntilReady(t, conn)
}

func commandCompleteTag(t *testing.T, frames []libpq.Frame) (tag string, errPayload string) {
	t.Helper()
	for _, f := range frames {
		switch f.Type {
		case libpq.MsgCommandComplete:
			tag = strings.TrimSuffix(string(f.Payload), "\x00")
		case libpq.MsgErrorResponse:
			errPayload = string(f.Payload)
		}
	}
	return tag, errPayload
}

// TestExtendedProtocolDatabaseAndRoleDDL pins the M0119-0004-ACLHEAP fix:
// dispatchSimpleQueryViaExecutor's CREATE/DROP/ALTER DATABASE and
// CREATE/DROP/ALTER ROLE wire-dispatch bypass (the parser has no grammar
// for these statements) previously had no counterpart on the extended
// (Parse/Bind/Execute) protocol, so a client driving these statements as
// prepared statements — the default mode for JDBC/npgsql/psycopg2, unlike
// psql's simple-query default — got a silent 42601 syntax error instead of
// the DDL actually being applied. Exercises CREATE ROLE, ALTER ROLE ...
// SET, DROP ROLE, and ALTER DATABASE ... SET end-to-end over the wire via
// the extended protocol, cross-checking catalog side effects.
func TestExtendedProtocolDatabaseAndRoleDDL(t *testing.T) {
	addr, cat, stop := startCopyExecServer(t)
	defer stop()
	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatalf("catalog is not *catalog.InMemory")
	}
	// startCopyExecServer wires no live database name of its own, so use
	// the always-seeded "postgres" role's own database for the ALTER
	// DATABASE case's v0-scope "own live database" check.
	conn := dialAndCompleteDB(t, addr, "postgres")
	defer conn.Close()

	frames := extendedExec(t, conn, "cr", "CREATE ROLE extddl_role LOGIN")
	tag, errPayload := commandCompleteTag(t, frames)
	if errPayload != "" {
		t.Fatalf("CREATE ROLE: unexpected ErrorResponse: %s", errPayload)
	}
	if tag != "CREATE ROLE" {
		t.Fatalf("CREATE ROLE: CommandComplete tag=%q, want %q", tag, "CREATE ROLE")
	}
	roleOID, ok := im.RoleOID("extddl_role")
	if !ok {
		t.Fatal("extddl_role not registered in catalog after extended-protocol CREATE ROLE")
	}

	frames = extendedExec(t, conn, "ar", "ALTER ROLE extddl_role SET work_mem = '64MB'")
	tag, errPayload = commandCompleteTag(t, frames)
	if errPayload != "" {
		t.Fatalf("ALTER ROLE SET: unexpected ErrorResponse: %s", errPayload)
	}
	if tag != "ALTER ROLE" {
		t.Fatalf("ALTER ROLE SET: CommandComplete tag=%q, want %q", tag, "ALTER ROLE")
	}
	entries := im.RoleConfigEntries(roleOID, 0)
	if len(entries) != 1 || entries[0] != "work_mem=64MB" {
		t.Fatalf("RoleConfigEntries(%d, 0) = %v, want [work_mem=64MB]", roleOID, entries)
	}

	frames = extendedExec(t, conn, "adb", "ALTER DATABASE postgres SET work_mem = '77MB'")
	tag, errPayload = commandCompleteTag(t, frames)
	if errPayload != "" {
		t.Fatalf("ALTER DATABASE SET: unexpected ErrorResponse: %s", errPayload)
	}
	if tag != "ALTER DATABASE" {
		t.Fatalf("ALTER DATABASE SET: CommandComplete tag=%q, want %q", tag, "ALTER DATABASE")
	}
	dbEntries := im.DatabaseConfigEntries(catalog.FirstUserOID)
	if len(dbEntries) != 1 || dbEntries[0] != "work_mem=77MB" {
		t.Fatalf("DatabaseConfigEntries(FirstUserOID) = %v, want [work_mem=77MB]", dbEntries)
	}

	frames = extendedExec(t, conn, "dr", "DROP ROLE extddl_role")
	tag, errPayload = commandCompleteTag(t, frames)
	if errPayload != "" {
		t.Fatalf("DROP ROLE: unexpected ErrorResponse: %s", errPayload)
	}
	if tag != "DROP ROLE" {
		t.Fatalf("DROP ROLE: CommandComplete tag=%q, want %q", tag, "DROP ROLE")
	}
	if _, ok := im.RoleOID("extddl_role"); ok {
		t.Fatal("extddl_role still registered after extended-protocol DROP ROLE")
	}
}

// TestExtendedProtocolRoleDDLError pins that a role-DDL error surfaced via
// the new extended-protocol bypass carries the same SQLSTATE the
// simple-query path reports, not a generic syntax error — ALTER ROLE on a
// nonexistent role must be 42704 (undefined_object), matching
// roleErrorSQLState.
func TestExtendedProtocolRoleDDLError(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndCompleteDB(t, addr, "postgres")
	defer conn.Close()

	frames := extendedExec(t, conn, "arerr", "ALTER ROLE no_such_role SET work_mem = '1MB'")
	var gotCode string
	for _, f := range frames {
		if f.Type != libpq.MsgErrorResponse {
			continue
		}
		fields := parseErrorFields(t, f.Payload)
		gotCode = fields[libpq.FieldSQLState]
	}
	if gotCode != "42704" {
		t.Fatalf("ALTER ROLE on nonexistent role: SQLSTATE=%q, want 42704 (undefined_object)", gotCode)
	}
}

// TestExtendedProtocolDatabaseDDLNotice pins loop #84's item (1) residual:
// tryHandleDatabaseDDL's notice return (e.g. DROP DATABASE IF EXISTS on a
// nonexistent name) was silently dropped by
// tryHandleDatabaseOrRoleDDLExtended — the DDL applied but the client never
// saw the NOTICE a real PG / the simple-query path both emit. Verifies a
// NoticeResponse frame now precedes CommandComplete over the wire.
func TestExtendedProtocolDatabaseDDLNotice(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndCompleteDB(t, addr, "postgres")
	defer conn.Close()

	frames := extendedExec(t, conn, "dbnotice", `DROP DATABASE IF EXISTS extddl_ghost_db`)
	var gotNotice string
	for _, f := range frames {
		if f.Type != libpq.MsgNoticeResponse {
			continue
		}
		fields := parseErrorFields(t, f.Payload)
		gotNotice = fields[libpq.FieldMessage]
	}
	wantNotice := `database "extddl_ghost_db" does not exist, skipping`
	if gotNotice != wantNotice {
		t.Fatalf("DROP DATABASE IF EXISTS: NoticeResponse message=%q, want %q", gotNotice, wantNotice)
	}
	tag, errPayload := commandCompleteTag(t, frames)
	if errPayload != "" {
		t.Fatalf("DROP DATABASE IF EXISTS: unexpected ErrorResponse: %s", errPayload)
	}
	if tag != "DROP DATABASE" {
		t.Fatalf("DROP DATABASE IF EXISTS: CommandComplete tag=%q, want %q", tag, "DROP DATABASE")
	}
}

// TestSimpleQueryDropDatabaseActuallyDrops pins a real bug this loop found
// while wiring extended-protocol Notice forwarding: DROP DATABASE has real
// parser grammar (DropCompatStmt, a generic no-op DDL absorption added after
// M0054-0001's CREATE/DROP DATABASE catalog-backed bypass), so parser.Parse
// always SUCCEEDED for "DROP DATABASE ..." and the query was silently routed
// to execDropCompat's hardcoded "database" stub (pre-dates real database
// catalog tracking; always reports "does not exist", ignoring catalog
// state entirely) instead of tryHandleDatabaseDDL's real
// catalog.DropDatabase call. DROP DATABASE on a database that genuinely
// exists therefore always 3D000'd. Verifies CREATE DATABASE + DROP DATABASE
// round-trips through the catalog for real over the simple-query protocol.
func TestSimpleQueryDropDatabaseActuallyDrops(t *testing.T) {
	addr, cat, stop := startCopyExecServer(t)
	defer stop()
	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatalf("catalog is not *catalog.InMemory")
	}
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	if err := sendSimpleQuery(conn, []byte("CREATE DATABASE extddl_dropme")); err != nil {
		t.Fatalf("send CREATE DATABASE: %v", err)
	}
	frames := readUntilReady(t, conn)
	tag, errPayload := commandCompleteTag(t, frames)
	if errPayload != "" {
		t.Fatalf("CREATE DATABASE: unexpected ErrorResponse: %s", errPayload)
	}
	if tag != "CREATE DATABASE" {
		t.Fatalf("CREATE DATABASE: CommandComplete tag=%q, want %q", tag, "CREATE DATABASE")
	}
	if !im.HasDatabase("extddl_dropme") {
		t.Fatal("extddl_dropme not registered in catalog after CREATE DATABASE")
	}

	if err := sendSimpleQuery(conn, []byte("DROP DATABASE extddl_dropme")); err != nil {
		t.Fatalf("send DROP DATABASE: %v", err)
	}
	frames = readUntilReady(t, conn)
	tag, errPayload = commandCompleteTag(t, frames)
	if errPayload != "" {
		t.Fatalf("DROP DATABASE on an existing database: unexpected ErrorResponse (this is the bug this test pins): %s", errPayload)
	}
	if tag != "DROP DATABASE" {
		t.Fatalf("DROP DATABASE: CommandComplete tag=%q, want %q", tag, "DROP DATABASE")
	}
	if im.HasDatabase("extddl_dropme") {
		t.Fatal("extddl_dropme still registered in catalog after DROP DATABASE")
	}
}

// TestExtendedProtocolRoleDDLErrorDetail pins loop #84's item (2) residual:
// roleErrorDetailFields' errdetail text (e.g. reservedRoleNameErr's fixed
// pg_-prefix detail) had no extended-protocol counterpart — the
// ErrorResponse carried Code/Message but silently dropped the FieldDetail a
// real PG (and the simple-query path) both send for this exact error.
func TestExtendedProtocolRoleDDLErrorDetail(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndCompleteDB(t, addr, "postgres")
	defer conn.Close()

	frames := extendedExec(t, conn, "crreserved", "CREATE ROLE pg_extddl_reserved LOGIN")
	var gotCode, gotDetail string
	for _, f := range frames {
		if f.Type != libpq.MsgErrorResponse {
			continue
		}
		fields := parseErrorFields(t, f.Payload)
		gotCode = fields[libpq.FieldSQLState]
		gotDetail = fields[libpq.FieldDetail]
	}
	if gotCode != "42939" {
		t.Fatalf("CREATE ROLE pg_*: SQLSTATE=%q, want 42939 (reserved_name)", gotCode)
	}
	if gotDetail == "" {
		t.Fatal("CREATE ROLE pg_*: ErrorResponse has no FieldDetail, want the pg_-prefix errdetail text")
	}
}

// TestExtendedProtocolCompatNoopSchema pins item (3) of the loop #84 row
// (`0119-0004cv`/`0119-0004cw`): compatNoopCommandTag's no-op DDL absorption
// (dispatch.go, ~line 180 — GRANT/REVOKE/CREATE SCHEMA/COMMENT ON/SECURITY
// LABEL forms the parser doesn't recognise at all) had no extended-protocol
// counterpart, so a client using Parse/Bind/Execute for one of these forms
// got a hard 42601 syntax error where psql's simple-query default silently
// absorbed it. CREATE SCHEMA has no parser grammar whatsoever (parser.Parse
// always fails), making it a clean probe: exercises the catalog+WAL side
// effect (registerCompatNoopSchema) round-trips identically to the
// simple-query path.
func TestExtendedProtocolCompatNoopSchema(t *testing.T) {
	addr, cat, stop := startCopyExecServer(t)
	defer stop()
	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatalf("catalog is not *catalog.InMemory")
	}
	conn := dialAndCompleteDB(t, addr, "postgres")
	defer conn.Close()

	frames := extendedExec(t, conn, "cs", "CREATE SCHEMA extddl_compatnoop_schema")
	tag, errPayload := commandCompleteTag(t, frames)
	if errPayload != "" {
		t.Fatalf("CREATE SCHEMA: unexpected ErrorResponse: %s", errPayload)
	}
	if tag != "CREATE SCHEMA" {
		t.Fatalf("CREATE SCHEMA: CommandComplete tag=%q, want %q", tag, "CREATE SCHEMA")
	}
	if !im.SchemaExists("extddl_compatnoop_schema") {
		t.Fatal("extddl_compatnoop_schema not registered in catalog after extended-protocol CREATE SCHEMA")
	}
}

// TestExtendedProtocolCompatNoopGrantRevokeSecurityLabelUnreachable pins a
// finding from auditing item (3) of the loop #84 row (`0119-0004cv`/
// `0119-0004cw`), which named "GRANT ... ON ALL TABLES IN SCHEMA ...",
// "GRANT ... ON LARGE OBJECT ..." etc. as candidate probes for
// compatNoopCommandTag's GRANT/REVOKE/SECURITY LABEL branches
// (dispatch.go ~1281-1298): unlike CREATE SCHEMA, none of those branches are
// actually reachable through a single well-formed statement. parser.go's
// `case "grant", "revoke"` (~1046) and `case "security"` (~1176) top-level
// dispatch arms consume every token up to the terminating ';'/EOF with no
// required structure and no error return on any path, so parser.Parse
// NEVER fails for a string beginning with GRANT/REVOKE/SECURITY LABEL —
// every such statement parses into a CompatNoopStmt (or a concrete
// TypeACLChange/DatabaseACLChange/ParameterACLChange/AttrACLChange/
// RoleMembership payload) that flows through the ordinary planner/executor
// pipeline identically on both wire protocols, never through
// compatNoopCommandTag/tryCompatNoopExtended at all. This test pins that
// fact directly (rather than writing extended-protocol wire tests that
// could never exercise the target fallback): every case below must produce
// a real (non-fallback) parse.
func TestExtendedProtocolCompatNoopGrantRevokeSecurityLabelUnreachable(t *testing.T) {
	cases := []string{
		"GRANT SELECT ON ALL TABLES IN SCHEMA public TO nosuchrole",
		"GRANT ALL ON LARGE OBJECT 12345 TO nosuchrole",
		"REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM nosuchrole",
		"GRANT",
		"REVOKE",
		"SECURITY LABEL ON TABLE nosuchtable IS 'x'",
	}
	for _, sql := range cases {
		if _, ok := compatNoopCommandTag(sql); !ok {
			t.Fatalf("compatNoopCommandTag(%q) = false; want true (matches its prefix, confirming the branch exists)", sql)
		}
		if _, err := parser.Parse(sql); err != nil {
			t.Fatalf("parser.Parse(%q) = %v; want nil — this statement should never reach compatNoopCommandTag in the live dispatch path", sql, err)
		}
	}
}

// TestExtendedProtocolCompatNoopCommentOnMalformed pins the one GRANT/
// REVOKE/COMMENT ON/SECURITY LABEL sub-case that genuinely reaches
// compatNoopCommandTag's fallback via a single well-formed statement (see
// TestExtendedProtocolCompatNoopGrantRevokeSecurityLabelUnreachable's
// finding that the other three never do): `COMMENT ON <supported-kind>`
// with the required name/target omitted. parseCommentOnTail
// (internal/parser/parser.go) returns a genuine parse error for a
// truncated clause of a *supported* ObjKind (TABLE/INDEX/COLUMN/
// CONSTRAINT/TRIGGER/...) — unlike an *unsupported* ObjKind, which is
// accepted as a silent CompatNoopStmt no-op by design (M0097-0023) — so
// `COMMENT ON TABLE` with no table name is the one input that actually
// drives compatNoopCommandTag's "comment on " branch and therefore
// tryCompatNoopExtended. Confirms extended-protocol parity with the
// simple-query path's existing (silent-absorption) behavior for this
// malformed input, matching loop #86's actual shared-mechanism goal.
func TestExtendedProtocolCompatNoopCommentOnMalformed(t *testing.T) {
	const malformed = "COMMENT ON TABLE"
	if _, err := parser.Parse(malformed); err == nil {
		t.Fatalf("parser.Parse(%q) succeeded; want a parse error (precondition for reaching compatNoopCommandTag)", malformed)
	}
	if tag, ok := compatNoopCommandTag(malformed); !ok || tag != "COMMENT" {
		t.Fatalf("compatNoopCommandTag(%q) = (%q, %v); want (\"COMMENT\", true)", malformed, tag, ok)
	}

	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndCompleteDB(t, addr, "postgres")
	defer conn.Close()

	frames := extendedExec(t, conn, "cotm", malformed)
	tag, errPayload := commandCompleteTag(t, frames)
	if errPayload != "" {
		t.Fatalf("COMMENT ON TABLE (no name), extended protocol: unexpected ErrorResponse: %s", errPayload)
	}
	if tag != "COMMENT" {
		t.Fatalf("COMMENT ON TABLE (no name), extended protocol: CommandComplete tag=%q, want %q", tag, "COMMENT")
	}

	// Simple-query path: same statement must be absorbed identically.
	conn2 := dialAndCompleteDB(t, addr, "postgres")
	defer conn2.Close()
	if err := sendSimpleQuery(conn2, []byte(malformed)); err != nil {
		t.Fatalf("send COMMENT ON TABLE (no name): %v", err)
	}
	frames = readUntilReady(t, conn2)
	tag, errPayload = commandCompleteTag(t, frames)
	if errPayload != "" {
		t.Fatalf("COMMENT ON TABLE (no name), simple-query: unexpected ErrorResponse: %s", errPayload)
	}
	if tag != "COMMENT" {
		t.Fatalf("COMMENT ON TABLE (no name), simple-query: CommandComplete tag=%q, want %q", tag, "COMMENT")
	}
}

// TestSimpleQueryMultiStatementCompatNoopBatchRejectsLaterSyntaxError pins
// the multi-statement-batch masking bug found while writing the tests above
// (M0119-0004-ACLHEAP loop #87 deferral, closed by splitLeadingCompatNoopDDL/
// isMultiStatementSQL in loop #88): a simple-query batch whose FIRST
// statement matches a compatNoopCommandTag prefix (GRANT, here) followed by
// a LATER statement that is genuinely invalid SQL must report the real
// syntax error for the whole batch and execute nothing — matching real
// PostgreSQL's exec_simple_query/pg_parse_query semantics (the entire
// message is parsed up front; any statement's syntax error rejects the
// whole message, nothing runs). Before the fix, parser.Parse failed for the
// full multi-statement string (Go's recursive-descent parser returns 0
// statements + one error for the whole input, not a partial list) and
// compatNoopCommandTag then matched the raw multi-statement text's leading
// "grant " prefix, silently absorbing the WHOLE batch as a bare
// CommandComplete "GRANT" success — swallowing the real syntax error and
// running neither statement.
func TestSimpleQueryMultiStatementCompatNoopBatchRejectsLaterSyntaxError(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndCompleteDB(t, addr, "postgres")
	defer conn.Close()

	const batch = "GRANT SELECT ON nosuchtable TO nosuchrole; !!! not valid sql at all((("
	// Precondition: the first statement alone parses fine (per
	// TestExtendedProtocolCompatNoopGrantRevokeSecurityLabelUnreachable's
	// finding) but the full batch does not — otherwise this test would not
	// be exercising the masking path at all.
	if _, err := parser.Parse("GRANT SELECT ON nosuchtable TO nosuchrole"); err != nil {
		t.Fatalf("precondition: GRANT alone failed to parse: %v", err)
	}
	if _, err := parser.Parse(batch); err == nil {
		t.Fatalf("precondition: full batch parsed successfully; want a parse error to reach the masking path")
	}

	writeQuery(t, conn, batch)
	frames := readUntilReady(t, conn)

	if len(frames) != 2 {
		t.Fatalf("frames=%d, want 2 (ErrorResponse, ReadyForQuery); got %+v", len(frames), frames)
	}
	if frames[0].Type != libpq.MsgErrorResponse {
		t.Fatalf("frame[0].Type=%c, want E (ErrorResponse) — batch must not be silently absorbed as CommandComplete", frames[0].Type)
	}
	fields := parseErrorFields(t, frames[0].Payload)
	if got := fields[libpq.FieldSQLState]; got != string(errcodes.SyntaxError) {
		t.Errorf("SQLSTATE = %q, want %q (syntax_error)", got, errcodes.SyntaxError)
	}
	if frames[1].Type != libpq.MsgReadyForQuery {
		t.Fatalf("frame[1].Type=%c, want Z (ReadyForQuery)", frames[1].Type)
	}
}

// TestSimpleQueryMultiStatementCompatNoopDDLStillRecurses is the companion
// regression guard for the fix above: a multi-statement batch whose FIRST
// statement is a genuine parser-gap compatNoopCommandTag form (CREATE
// SCHEMA, which the parser has no grammar for at all, unlike GRANT) followed
// by a well-formed second statement must still run BOTH statements — the
// splitLeadingCompatNoopDDL split-first-handle-recurse-rest path (mirroring
// splitLeadingRoleDDL, M0118-0008) must keep working for the case it exists
// for, not just reject everything multi-statement.
func TestSimpleQueryMultiStatementCompatNoopDDLStillRecurses(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndCompleteDB(t, addr, "postgres")
	defer conn.Close()

	const batch = "CREATE SCHEMA noop_batch_recurse_schema; SELECT 1"
	writeQuery(t, conn, batch)
	frames := readUntilReady(t, conn)

	var tags []string
	for _, f := range frames {
		if f.Type == libpq.MsgErrorResponse {
			t.Fatalf("unexpected ErrorResponse in recursed batch: %s", string(f.Payload))
		}
		if f.Type == libpq.MsgCommandComplete {
			tags = append(tags, strings.TrimSuffix(string(f.Payload), "\x00"))
		}
	}
	if len(tags) != 2 || tags[0] != "CREATE SCHEMA" || tags[1] != "SELECT 1" {
		t.Fatalf("CommandComplete tags=%v, want [\"CREATE SCHEMA\" \"SELECT 1\"]", tags)
	}
	if frames[len(frames)-1].Type != libpq.MsgReadyForQuery {
		t.Fatalf("last frame type=%c, want Z (ReadyForQuery)", frames[len(frames)-1].Type)
	}
}
