package postmaster

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/libpq"
	"github.com/goopg/goopg/internal/utils/errcodes"
)

// TestStripSetToOrEquals pins the generic_set separator normalizer used by
// the SET ROLE fast paths (query.go, extended.go), M0134-0155. Upstream,
// ROLE is an unreserved keyword so `SET ROLE` also parses through
// generic_set (gram.y:1656-1693 `var_name TO var_list | var_name '='
// var_list`), and an optional TO/= between the fixed prefix and the role
// name must be stripped — never folded into the role name. FAIL-pre: the
// fast path stored the literal garbage role "TO x" for `SET ROLE TO x`,
// a non-empty never-matching NonSuperuserRole that silently denied every
// CREATEROLE-gated privilege check for the real superuser until the next
// explicit SET/RESET ROLE. (SESSION AUTHORIZATION deliberately does NOT get
// this treatment — see setAuthzGenericSetForm.)
func TestStripSetToOrEquals(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"alice", "alice"},         // bare form (no separator)
		{"  alice  ", "alice"},     // surrounding whitespace only
		{"TO alice", "alice"},      // canonical TO form
		{"to alice", "alice"},      // case-insensitive keyword
		{"= alice", "alice"},       // equals form
		{"=alice", "alice"},        // equals form, no space
		{" =  spaced  ", "spaced"}, // separator + inner + outer whitespace
		{"TO  double  ", "double"}, // TO + extra whitespace after
		{"DEFAULT", "DEFAULT"},     // DEFAULT passes through (caller decides)
		{"to default", "default"},  // lowercase TO prefix stripped like uppercase
		{"", ""},                   // bare statement, no remainder
		{"=", ""},                  // lone separator
		{"to", ""},                 // bare TO keyword, no target
		{"TO", ""},
	}
	for _, c := range cases {
		if got := stripSetToOrEquals(c.in); got != c.want {
			t.Errorf("stripSetToOrEquals(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestParseErrorAbortsTransactionBlock pins the M0134-0155 discovery: a
// PARSE-time error inside an explicit transaction must put the block into
// the 25P02 failed state (PG's postgres.c error handler aborts on ANY
// statement error), on BOTH protocol paths. FAIL-pre: only execution- and
// plan-time errors reached connTx.Fail() (M0132-S5), so
// `BEGIN; <syntax error>; SELECT 1` left the block live and healthy.
func TestParseErrorAbortsTransactionBlock(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, "BEGIN")
	readUntilReady(t, conn)

	// Simple-query path: a syntax error must fail the block.
	writeQuery(t, conn, "TOTAALLY NOT VALID SQL")
	var sawSyntaxErr bool
	for _, f := range readUntilReady(t, conn) {
		if f.Type == libpq.MsgErrorResponse {
			sawSyntaxErr = true
			if !strings.Contains(string(f.Payload), "42601") {
				t.Fatalf("garbage statement: ErrorResponse = %q, want 42601", f.Payload)
			}
		}
	}
	if !sawSyntaxErr {
		t.Fatal("garbage statement: no ErrorResponse, want 42601")
	}

	writeQuery(t, conn, "SELECT 1")
	for _, f := range readUntilReady(t, conn) {
		if f.Type == libpq.MsgErrorResponse {
			if !strings.Contains(string(f.Payload), "25P02") {
				t.Fatalf("SELECT after parse error: ErrorResponse = %q, want 25P02 in_failed_sql_transaction", f.Payload)
			}
		}
		if f.Type == libpq.MsgDataRow {
			t.Fatal("SELECT after parse error inside a transaction succeeded; want 25P02 aborted-block rejection")
		}
	}

	// Upstream bug-#17983 parity: an EMPTY statement is not a command — the
	// aborted-block gate never sees it (it lives inside the per-parsetree
	// loop), so `;` returns EmptyQueryResponse even here.
	writeQuery(t, conn, ";")
	var sawEmpty bool
	for _, f := range readUntilReady(t, conn) {
		if f.Type == libpq.MsgEmptyQueryResponse {
			sawEmpty = true
		}
		if f.Type == libpq.MsgErrorResponse {
			t.Fatalf("empty statement in aborted block: ErrorResponse = %q, want EmptyQueryResponse", f.Payload)
		}
	}
	if !sawEmpty {
		t.Fatal("empty statement in aborted block: no EmptyQueryResponse")
	}

	writeQuery(t, conn, "ROLLBACK")
	readUntilReady(t, conn)

	// Extended path: same semantics. runExtendedStatement fails the test on
	// any ErrorResponse, so drive Parse/Bind/Execute/Sync manually here.
	runExtendedStatement(t, conn, "BEGIN")
	writeFrontendFrame(t, conn, libpq.MsgParse, parsePayload("", "ALSO NOT VALID SQL", nil))
	writeFrontendFrame(t, conn, libpq.MsgBind, bindPayload("", "", nil, nil, nil))
	writeFrontendFrame(t, conn, libpq.MsgExecute, executePayload("", 0))
	writeFrontendFrame(t, conn, libpq.MsgSync, nil)
	var sawExtSyntaxErr bool
	for _, f := range readUntilReady(t, conn) {
		if f.Type == libpq.MsgErrorResponse {
			sawExtSyntaxErr = true
			if !strings.Contains(string(f.Payload), string(errcodes.SyntaxError)) {
				t.Fatalf("extended garbage: ErrorResponse = %q, want %s syntax error", f.Payload, errcodes.SyntaxError)
			}
		}
	}
	if !sawExtSyntaxErr {
		t.Fatal("extended garbage: no ErrorResponse, want 42601 syntax error")
	}

	writeFrontendFrame(t, conn, libpq.MsgParse, parsePayload("", "SELECT 1", nil))
	writeFrontendFrame(t, conn, libpq.MsgBind, bindPayload("", "", nil, nil, nil))
	writeFrontendFrame(t, conn, libpq.MsgExecute, executePayload("", 0))
	writeFrontendFrame(t, conn, libpq.MsgSync, nil)
	var sawAborted bool
	for _, f := range readUntilReady(t, conn) {
		if f.Type == libpq.MsgErrorResponse && strings.Contains(string(f.Payload), "25P02") {
			sawAborted = true
		}
	}
	if !sawAborted {
		t.Fatal("extended SELECT after parse error inside a transaction succeeded; want 25P02 aborted-block rejection")
	}
}

// TestFastPathGenericSetSpellings drives the query.go string-matching fast
// path with the generic_set TO/= spellings of SET ROLE (accepted — ROLE also
// parses through generic_set upstream because it is an unreserved keyword)
// and the SESSION AUTHORIZATION = spelling (rejected with PG's 42601 — no
// generic_set grammar for that statement). M0134-0155. Sibling-path rule:
// parser-level acceptance/rejection (internal/parser/parser_test.go) proves
// nothing about the wire fast paths, which bypass the parser entirely via
// prefix matching.
func TestFastPathGenericSetSpellings(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	if got := queryUserIdentity(t, conn); got != [4]string{"postgres", "postgres", "postgres", "postgres"} {
		t.Fatalf("fresh connection identity = %v, want all postgres", got)
	}

	writeQuery(t, conn, "SET ROLE TO alice")
	readUntilReady(t, conn)
	if got := queryUserIdentity(t, conn); got != [4]string{"alice", "postgres", "alice", "alice"} {
		t.Fatalf("after SET ROLE TO alice: identity = %v, want [alice postgres alice alice]", got)
	}

	writeQuery(t, conn, "RESET ROLE")
	readUntilReady(t, conn)
	if got := queryUserIdentity(t, conn); got != [4]string{"postgres", "postgres", "postgres", "postgres"} {
		t.Fatalf("after RESET ROLE: identity = %v, want all postgres", got)
	}

	// SESSION AUTHORIZATION deliberately has NO generic_set grammar upstream
	// (gram.y:1764/:1774), so `= bob` must surface PG's 42601 via the
	// parser-driven path — not be applied as a role name. M0134-0155.
	writeQuery(t, conn, "SET SESSION AUTHORIZATION = bob")
	var sawErr bool
	for _, f := range readUntilReady(t, conn) {
		if f.Type == libpq.MsgErrorResponse {
			sawErr = true
			payload := string(f.Payload)
			if !strings.Contains(payload, "42601") || !strings.Contains(payload, "syntax error") {
				t.Fatalf("SET SESSION AUTHORIZATION = bob: ErrorResponse = %q, want 42601 syntax error", payload)
			}
		}
	}
	if !sawErr {
		t.Fatal("SET SESSION AUTHORIZATION = bob: no ErrorResponse, want 42601 syntax error")
	}
	// The rejected statement must not have changed identity.
	if got := queryUserIdentity(t, conn); got != [4]string{"postgres", "postgres", "postgres", "postgres"} {
		t.Fatalf("after rejected SET SESSION AUTHORIZATION = bob: identity = %v, want all postgres (unchanged)", got)
	}

	// The bare spelling still applies through the fast path.
	writeQuery(t, conn, "SET SESSION AUTHORIZATION bob")
	readUntilReady(t, conn)
	if got := queryUserIdentity(t, conn); got != [4]string{"bob", "bob", "bob", "bob"} {
		t.Fatalf("after SET SESSION AUTHORIZATION bob: identity = %v, want all bob", got)
	}

	writeQuery(t, conn, "RESET SESSION AUTHORIZATION")
	readUntilReady(t, conn)
	if got := queryUserIdentity(t, conn); got != [4]string{"postgres", "postgres", "postgres", "postgres"} {
		t.Fatalf("after RESET SESSION AUTHORIZATION: identity = %v, want all postgres", got)
	}
}
