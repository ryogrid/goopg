package server

import (
	"net"
	"testing"

	"github.com/goopg/goopg/internal/protocol"
)

// TestExtendedProtocolSetRoleTracksNonSuperuserRole pins the M0119-0004 fix:
// `SET ROLE`/`SET SESSION AUTHORIZATION`/`RESET ROLE`/`RESET SESSION
// AUTHORIZATION` issued through the extended-query protocol (Parse/Bind/
// Execute/Sync) previously either erred out (the fast-path switch in
// executeExtendedQuery mis-treated "ROLE"/"SESSION" as a GUC name for
// sess.Set) or silently no-opped, leaving the reportable is_superuser GUC
// and connTx.NonSuperuserRole stale — unlike the single-statement
// simple-query path (server/query.go's handleQuery), which already tracked
// this. Driving the exact same statements through Parse/Bind/Execute/Sync
// must flip is_superuser off/on identically.
func TestExtendedProtocolSetRoleTracksNonSuperuserRole(t *testing.T) {
	addr, stop := startTestServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	// Bootstrap superuser starts is_superuser = on.
	if got := queryIsSuperuser(t, conn); got != "on" {
		t.Fatalf("initial is_superuser=%q, want %q", got, "on")
	}

	runExtendedStatement(t, conn, "SET ROLE some_nonsuper_role")
	if got := queryIsSuperuser(t, conn); got != "off" {
		t.Fatalf("after SET ROLE (extended): is_superuser=%q, want %q", got, "off")
	}

	runExtendedStatement(t, conn, "RESET ROLE")
	if got := queryIsSuperuser(t, conn); got != "on" {
		t.Fatalf("after RESET ROLE (extended): is_superuser=%q, want %q", got, "on")
	}

	runExtendedStatement(t, conn, "SET SESSION AUTHORIZATION some_nonsuper_role")
	if got := queryIsSuperuser(t, conn); got != "off" {
		t.Fatalf("after SET SESSION AUTHORIZATION (extended): is_superuser=%q, want %q", got, "off")
	}

	runExtendedStatement(t, conn, "RESET SESSION AUTHORIZATION")
	if got := queryIsSuperuser(t, conn); got != "on" {
		t.Fatalf("after RESET SESSION AUTHORIZATION (extended): is_superuser=%q, want %q", got, "on")
	}
}

// runExtendedStatement drives one Parse/Bind/Execute/Sync round trip for a
// parameterless statement and fails the test on any ErrorResponse.
func runExtendedStatement(t *testing.T, conn net.Conn, query string) {
	t.Helper()
	writeFrontendFrame(t, conn, protocol.MsgParse, parsePayload("", query, nil))
	writeFrontendFrame(t, conn, protocol.MsgBind, bindPayload("", "", nil, nil, nil))
	writeFrontendFrame(t, conn, protocol.MsgExecute, executePayload("", 0))
	writeFrontendFrame(t, conn, protocol.MsgSync, nil)

	frames := readUntilReady(t, conn)
	for _, f := range frames {
		if f.Type == protocol.MsgErrorResponse {
			t.Fatalf("%q: unexpected ErrorResponse: %s", query, string(f.Payload))
		}
	}
}

// queryIsSuperuser issues `SHOW is_superuser` over the simple-query protocol
// and returns its rendered value.
func queryIsSuperuser(t *testing.T, conn net.Conn) string {
	t.Helper()
	writeQuery(t, conn, "SHOW is_superuser")
	frames := readUntilReady(t, conn)
	for _, f := range frames {
		if f.Type == protocol.MsgDataRow {
			row := decodeDataRow(t, f.Payload)
			if len(row) != 1 {
				t.Fatalf("SHOW is_superuser: cell count=%d, want 1", len(row))
			}
			return string(row[0])
		}
	}
	t.Fatalf("SHOW is_superuser: no DataRow in %+v", frames)
	return ""
}
