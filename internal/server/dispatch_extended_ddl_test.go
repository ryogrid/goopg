package server

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/protocol"
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
	r := protocol.NewFrameReader(conn)
	for {
		f, err := r.ReadFrame()
		if err != nil {
			t.Fatalf("drain startup: %v", err)
		}
		if f.Type == protocol.MsgReadyForQuery {
			return conn
		}
	}
}

// extendedExec drives one Parse/Bind/Execute/Sync round trip for query
// (no bind parameters) and returns the frames up to ReadyForQuery.
func extendedExec(t *testing.T, conn net.Conn, name, query string) []protocol.Frame {
	t.Helper()
	writeFrontendFrame(t, conn, protocol.MsgParse, parsePayload(name, query, nil))
	writeFrontendFrame(t, conn, protocol.MsgBind, bindPayload("", name, nil, nil, nil))
	writeFrontendFrame(t, conn, protocol.MsgExecute, executePayload("", 0))
	writeFrontendFrame(t, conn, protocol.MsgSync, nil)
	return readUntilReady(t, conn)
}

func commandCompleteTag(t *testing.T, frames []protocol.Frame) (tag string, errPayload string) {
	t.Helper()
	for _, f := range frames {
		switch f.Type {
		case protocol.MsgCommandComplete:
			tag = strings.TrimSuffix(string(f.Payload), "\x00")
		case protocol.MsgErrorResponse:
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
		if f.Type != protocol.MsgErrorResponse {
			continue
		}
		fields := parseErrorFields(t, f.Payload)
		gotCode = fields[protocol.FieldSQLState]
	}
	if gotCode != "42704" {
		t.Fatalf("ALTER ROLE on nonexistent role: SQLSTATE=%q, want 42704 (undefined_object)", gotCode)
	}
}
