package postmaster

import (
	"net"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/libpq"
)

// queryUserIdentity issues
// `SELECT current_user, session_user, current_role, user` over the
// simple-query protocol and returns the four rendered values in that
// order. M0134-0009.
func queryUserIdentity(t *testing.T, conn net.Conn) [4]string {
	t.Helper()
	writeQuery(t, conn, "SELECT current_user, session_user, current_role, user")
	frames := readUntilReady(t, conn)
	for _, f := range frames {
		if f.Type == libpq.MsgErrorResponse {
			t.Fatalf("SELECT current_user, session_user, current_role, user: unexpected ErrorResponse: %s", f.Payload)
		}
		if f.Type == libpq.MsgDataRow {
			row := decodeDataRow(t, f.Payload)
			if len(row) != 4 {
				t.Fatalf("SELECT current_user, ...: cell count=%d, want 4", len(row))
			}
			var out [4]string
			for i, cell := range row {
				out[i] = string(cell)
			}
			return out
		}
	}
	t.Fatalf("SELECT current_user, ...: no DataRow in %+v", frames)
	return [4]string{}
}

// queryUserIdentityMultiStatement sends setSQL and the identity SELECT as
// ONE simple-query message with an internal ';' — the shape that routes
// through dispatchSimpleQueryViaExecutor (query.go:127-130) into
// dispatch.go's split SetRole/SetSessionAuthorization closures, instead of
// query.go's single-statement string-matching switch. M0134-0009 round 2
// (R2): a bare simple-query message per SET statement (queryUserIdentity's
// sibling tests) never enters those closures at all.
func queryUserIdentityMultiStatement(t *testing.T, conn net.Conn, setSQL string) [4]string {
	t.Helper()
	writeQuery(t, conn, setSQL+"; SELECT current_user, session_user, current_role, user;")
	frames := readUntilReady(t, conn)
	for _, f := range frames {
		if f.Type == libpq.MsgErrorResponse {
			t.Fatalf("%s; SELECT current_user, ...: unexpected ErrorResponse: %s", setSQL, f.Payload)
		}
		if f.Type == libpq.MsgDataRow {
			row := decodeDataRow(t, f.Payload)
			if len(row) != 4 {
				t.Fatalf("%s; SELECT current_user, ...: cell count=%d, want 4", setSQL, len(row))
			}
			var out [4]string
			for i, cell := range row {
				out[i] = string(cell)
			}
			return out
		}
	}
	t.Fatalf("%s; SELECT current_user, ...: no DataRow in %+v", setSQL, frames)
	return [4]string{}
}

// TestFastPathSessionUserIdentity pins M0134-0009 acceptance criteria 1-4
// through the string-matching FAST PATH in query.go (one SET/RESET
// statement per simple-query message — the shape psql and most drivers
// send). FAIL-pre: current_user/current_role/user/session_user all
// hardcoded "postgres" regardless of SET ROLE / SET SESSION AUTHORIZATION.
func TestFastPathSessionUserIdentity(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	// 1: fresh connection reports the login role (postgres, the dial user).
	if got := queryUserIdentity(t, conn); got != [4]string{"postgres", "postgres", "postgres", "postgres"} {
		t.Fatalf("fresh connection identity = %v, want all postgres", got)
	}

	// 2: SET SESSION AUTHORIZATION alice → all four report alice; RESET →
	// back to the login role.
	writeQuery(t, conn, "SET SESSION AUTHORIZATION alice")
	readUntilReady(t, conn)
	if got := queryUserIdentity(t, conn); got != [4]string{"alice", "alice", "alice", "alice"} {
		t.Fatalf("after SET SESSION AUTHORIZATION alice: identity = %v, want all alice", got)
	}
	writeQuery(t, conn, "RESET SESSION AUTHORIZATION")
	readUntilReady(t, conn)
	if got := queryUserIdentity(t, conn); got != [4]string{"postgres", "postgres", "postgres", "postgres"} {
		t.Fatalf("after RESET SESSION AUTHORIZATION: identity = %v, want all postgres", got)
	}

	// 3: SET ROLE alice → current_user/current_role/user report alice but
	// session_user still reports the login role. RESET ROLE restores it.
	writeQuery(t, conn, "SET ROLE alice")
	readUntilReady(t, conn)
	if got := queryUserIdentity(t, conn); got != [4]string{"alice", "postgres", "alice", "alice"} {
		t.Fatalf("after SET ROLE alice: identity (current_user,session_user,current_role,user) = %v, want [alice postgres alice alice]", got)
	}
	writeQuery(t, conn, "RESET ROLE")
	readUntilReady(t, conn)
	if got := queryUserIdentity(t, conn); got != [4]string{"postgres", "postgres", "postgres", "postgres"} {
		t.Fatalf("after RESET ROLE: identity = %v, want all postgres", got)
	}

	// 4: SET ROLE alice; SET SESSION AUTHORIZATION bob → all four report
	// bob (SET SESSION AUTHORIZATION clears the active role).
	writeQuery(t, conn, "SET ROLE alice")
	readUntilReady(t, conn)
	writeQuery(t, conn, "SET SESSION AUTHORIZATION bob")
	readUntilReady(t, conn)
	if got := queryUserIdentity(t, conn); got != [4]string{"bob", "bob", "bob", "bob"} {
		t.Fatalf("after SET ROLE alice; SET SESSION AUTHORIZATION bob: identity = %v, want all bob", got)
	}
}

// TestExtendedProtocolFastPathSessionUserIdentity is
// TestFastPathSessionUserIdentity's sibling: the SAME sequence of
// statements, driven through the extended-query protocol
// (Parse/Bind/Execute/Sync). CORRECTION (M0134-0009 round-2 review, R2):
// this was previously named TestDispatchPathSessionUserIdentity and its doc
// comment claimed it reached dispatch.go's split
// SetRole/SetSessionAuthorization closures — that claim was WRONG. A single
// "SET ROLE alice" sent as its own extended-protocol statement is
// intercepted by extended.go's OWN string-matching fast path
// (setRoleFastPath/setSessionAuthorizationFastPath, extended.go:541-573,
// which call query.go's applySetRole/applySetSessionAuthorization
// directly), so it never enters operators_utility_settings.go or
// dispatch_extended.go at all — this test is a THIRD sibling of
// TestFastPathSessionUserIdentity (query.go's fast path), not a dispatch
// path test. A reviewer's coverage profile over this test showed
// dispatch.go:363-401 and dispatch_extended.go:330-371 at zero. See
// TestDispatchPathSessionUserIdentity and
// TestDispatchExtendedExecutorPathSessionUserIdentity below for tests that
// genuinely reach those two closures.
func TestExtendedProtocolFastPathSessionUserIdentity(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	if got := queryUserIdentity(t, conn); got != [4]string{"postgres", "postgres", "postgres", "postgres"} {
		t.Fatalf("fresh connection identity = %v, want all postgres", got)
	}

	runExtendedStatement(t, conn, "SET SESSION AUTHORIZATION alice")
	if got := queryUserIdentity(t, conn); got != [4]string{"alice", "alice", "alice", "alice"} {
		t.Fatalf("after SET SESSION AUTHORIZATION alice (extended): identity = %v, want all alice", got)
	}
	runExtendedStatement(t, conn, "RESET SESSION AUTHORIZATION")
	if got := queryUserIdentity(t, conn); got != [4]string{"postgres", "postgres", "postgres", "postgres"} {
		t.Fatalf("after RESET SESSION AUTHORIZATION (extended): identity = %v, want all postgres", got)
	}

	runExtendedStatement(t, conn, "SET ROLE alice")
	if got := queryUserIdentity(t, conn); got != [4]string{"alice", "postgres", "alice", "alice"} {
		t.Fatalf("after SET ROLE alice (extended): identity = %v, want [alice postgres alice alice]", got)
	}
	runExtendedStatement(t, conn, "RESET ROLE")
	if got := queryUserIdentity(t, conn); got != [4]string{"postgres", "postgres", "postgres", "postgres"} {
		t.Fatalf("after RESET ROLE (extended): identity = %v, want all postgres", got)
	}

	runExtendedStatement(t, conn, "SET ROLE alice")
	runExtendedStatement(t, conn, "SET SESSION AUTHORIZATION bob")
	if got := queryUserIdentity(t, conn); got != [4]string{"bob", "bob", "bob", "bob"} {
		t.Fatalf("after SET ROLE alice; SET SESSION AUTHORIZATION bob (extended): identity = %v, want all bob", got)
	}
}

// TestDispatchPathSessionUserIdentity genuinely reaches dispatch.go's split
// SetRole/SetSessionAuthorization closures (dispatch.go:363-401), unlike
// TestExtendedProtocolFastPathSessionUserIdentity above (M0134-0009 round-2
// review R2). It uses the multi-statement simple-query shape the reviewer
// identified: one SET statement and the identity SELECT in a SINGLE Query
// message, separated by ';' — query.go:127-130 routes any simple query
// containing an internal ';' to dispatchSimpleQueryViaExecutor, the shape
// `psql -c "SET ROLE alice; SELECT current_user;"` and many drivers send.
// FAIL-pre: dispatch.go had ectx.SetRole = ectx.SetSessionAuthorization (the
// same closure — no session_user/current_user split at all), so this test
// would have failed on the SET ROLE/session_user divergence assertions
// exactly like TestFastPathSessionUserIdentity's FAIL-pre.
func TestDispatchPathSessionUserIdentity(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	// 2: SET SESSION AUTHORIZATION alice → all four report alice; RESET →
	// back to the login role.
	if got := queryUserIdentityMultiStatement(t, conn, "SET SESSION AUTHORIZATION alice"); got != [4]string{"alice", "alice", "alice", "alice"} {
		t.Fatalf("after SET SESSION AUTHORIZATION alice (dispatch): identity = %v, want all alice", got)
	}
	if got := queryUserIdentityMultiStatement(t, conn, "RESET SESSION AUTHORIZATION"); got != [4]string{"postgres", "postgres", "postgres", "postgres"} {
		t.Fatalf("after RESET SESSION AUTHORIZATION (dispatch): identity = %v, want all postgres", got)
	}

	// 3: SET ROLE alice → current_user/current_role/user report alice but
	// session_user still reports the login role. RESET ROLE restores it.
	if got := queryUserIdentityMultiStatement(t, conn, "SET ROLE alice"); got != [4]string{"alice", "postgres", "alice", "alice"} {
		t.Fatalf("after SET ROLE alice (dispatch): identity = %v, want [alice postgres alice alice]", got)
	}
	if got := queryUserIdentityMultiStatement(t, conn, "RESET ROLE"); got != [4]string{"postgres", "postgres", "postgres", "postgres"} {
		t.Fatalf("after RESET ROLE (dispatch): identity = %v, want all postgres", got)
	}

	// 4: SET ROLE alice; SET SESSION AUTHORIZATION bob → all four report bob.
	writeQuery(t, conn, "SET ROLE alice; SET SESSION AUTHORIZATION bob; SELECT current_user, session_user, current_role, user;")
	frames := readUntilReady(t, conn)
	var got [4]string
	found := false
	for _, f := range frames {
		if f.Type == libpq.MsgErrorResponse {
			t.Fatalf("SET ROLE alice; SET SESSION AUTHORIZATION bob; SELECT ...: unexpected ErrorResponse: %s", f.Payload)
		}
		if f.Type == libpq.MsgDataRow {
			row := decodeDataRow(t, f.Payload)
			for i, cell := range row {
				got[i] = string(cell)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("SET ROLE alice; SET SESSION AUTHORIZATION bob; SELECT ...: no DataRow in %+v", frames)
	}
	if got != [4]string{"bob", "bob", "bob", "bob"} {
		t.Fatalf("after SET ROLE alice; SET SESSION AUTHORIZATION bob (dispatch): identity = %v, want all bob", got)
	}
}

// TestDispatchExtendedExecutorPathSessionUserIdentity genuinely reaches
// dispatch_extended.go's split SetRole/SetSessionAuthorization closures
// (dispatch_extended.go:330-371) — the extended-protocol analogue of
// TestDispatchPathSessionUserIdentity (M0134-0009 round-2 review R2).
// extended.go's OWN string-matching fast path
// (setRoleFastPath/setSessionAuthorizationFastPath, extended.go:541-573)
// intercepts every well-formed "SET ROLE "/"SET SESSION AUTHORIZATION "
// spelling before it ever reaches the executor, so this test uses a TAB
// between SET and ROLE/SESSION — the lexer treats tab as ordinary
// whitespace (internal/parser/lexer.go skipWhitespaceAndComments) so the
// real parser accepts it identically to a single space, but extended.go's
// naive `strings.HasPrefix(upper, "SET ROLE ")`-style checks (which all
// require exactly one literal space) do not match, so the statement falls
// through every fast-path case to executeExtendedQueryViaExecutor →
// operators_utility_settings.go → ctx.SetRole/ctx.SetSessionAuthorization →
// dispatch_extended.go's closures. This is exactly the kind of
// fast-path/parser divergence the M0134-0009 sibling-path rule exists to
// catch. FAIL-pre: same underlying bug as TestDispatchPathSessionUserIdentity.
func TestDispatchExtendedExecutorPathSessionUserIdentity(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	runExtendedStatement(t, conn, "SET\tSESSION AUTHORIZATION alice")
	if got := queryUserIdentity(t, conn); got != [4]string{"alice", "alice", "alice", "alice"} {
		t.Fatalf("after SET\\tSESSION AUTHORIZATION alice (dispatch_extended): identity = %v, want all alice", got)
	}

	runExtendedStatement(t, conn, "SET\tROLE alice")
	if got := queryUserIdentity(t, conn); got != [4]string{"alice", "alice", "alice", "alice"} {
		t.Fatalf("after SET\\tROLE alice (dispatch_extended): identity = %v, want [alice alice alice alice] (session_user was already alice from the SET SESSION AUTHORIZATION above)", got)
	}

	runExtendedStatement(t, conn, "RESET\tROLE")
	if got := queryUserIdentity(t, conn); got != [4]string{"alice", "alice", "alice", "alice"} {
		t.Fatalf("after RESET\\tROLE (dispatch_extended): identity = %v, want all alice (session auth override, not SET ROLE, is still active)", got)
	}
}

// TestResetRoleNoOpAfterSessionAuthorizationOnly pins the SetRoleIsActive
// gate (M0134-0009): RESET ROLE with no SET ROLE ever having run — only a
// prior SET SESSION AUTHORIZATION — must be a no-op, not clear the session
// authorization's role override (PG parity: miscinit.c GetCurrentRoleId's
// SetRoleIsActive gate). Exercised through the query.go fast path.
func TestResetRoleNoOpAfterSessionAuthorizationOnly(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, "SET SESSION AUTHORIZATION alice")
	readUntilReady(t, conn)
	if got := queryUserIdentity(t, conn); got != [4]string{"alice", "alice", "alice", "alice"} {
		t.Fatalf("after SET SESSION AUTHORIZATION alice: identity = %v, want all alice", got)
	}

	writeQuery(t, conn, "RESET ROLE")
	readUntilReady(t, conn)
	if got := queryUserIdentity(t, conn); got != [4]string{"alice", "alice", "alice", "alice"} {
		t.Fatalf("RESET ROLE with no SET ROLE active must be a no-op: identity = %v, want all alice (unchanged)", got)
	}
}

// TestSelectCurrentRoleParses pins M0134-0009 acceptance criterion 6:
// current_role is a RESERVED_KEYWORD (sqlkeywords) and must parse as a
// bare niladic function call like current_user, not require parentheses.
// FAIL-pre: syntax error (IsNoParenFuncName was missing "current_role").
func TestSelectCurrentRoleParses(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, "SELECT current_role")
	frames := readUntilReady(t, conn)
	for _, f := range frames {
		if f.Type == libpq.MsgErrorResponse {
			t.Fatalf("SELECT current_role: unexpected ErrorResponse: %s", f.Payload)
		}
	}
}

// dialAndCompleteAs is dialAndComplete but connects as an arbitrary login
// role instead of the always-seeded "postgres" (M0134-0009 round-2 review
// R3). The caller must have already CREATE ROLE'd the name — an unknown
// role is rejected 28000 at connect time (server.go's roleExists gate,
// mirroring PostgreSQL's InitializeSessionUserId FATAL).
func dialAndCompleteAs(t *testing.T, addr, user string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	writeStartupPacket(t, conn, map[string]string{"user": user})
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

// TestFreshConnectionAsNonPostgresRoleReportsLoginIdentity pins M0134-0009
// acceptance criterion 1 for a login role OTHER than "postgres" (round-2
// review R3): every prior test in this file logs in as postgres, so
// SessionUser=="" always fell back to the "postgres" default and the whole
// server.go LoginUser/SessionUser threading (runPostStartupLoop's new
// loginUser parameter) could be deleted with every other test in this file
// still green. FAIL-pre: current_user/session_user/current_role/user all
// reported "postgres" regardless of the actual login role.
func TestFreshConnectionAsNonPostgresRoleReportsLoginIdentity(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()

	admin := dialAndComplete(t, addr)
	writeQuery(t, admin, "CREATE ROLE regress_alice LOGIN")
	readUntilReady(t, admin)
	admin.Close()

	conn := dialAndCompleteAs(t, addr, "regress_alice")
	defer conn.Close()

	// A fresh connection with no SET of any kind must report the login role
	// on all four, not the hardcoded/fallback "postgres".
	if got := queryUserIdentity(t, conn); got != [4]string{"regress_alice", "regress_alice", "regress_alice", "regress_alice"} {
		t.Fatalf("fresh connection as regress_alice: identity = %v, want all regress_alice", got)
	}
}

// TestSetSessionAuthorizationPostgresIsNotDefault pins M0134-0009 round-2
// review R6: `SET SESSION AUTHORIZATION postgres` is an EXPLICIT role
// target, not a synonym for DEFAULT/RESET (which restores the login role).
// For a login OTHER than postgres, conflating the two — as
// operators_utility_settings.go's pre-round-2 collapse of the literal
// "postgres" spelling into the same "" sentinel used for DEFAULT/RESET did
// — makes `SET SESSION AUTHORIZATION postgres` restore the login role
// instead of reporting "postgres", AND makes the executor/dispatch path
// disagree with query.go's fast path (which never collapsed "postgres").
// Exercises BOTH: query.go's fast path (one statement per message) and the
// dispatch path (multi-statement simple query, reaching
// operators_utility_settings.go → dispatch.go's closures) — a green result
// on only one proves nothing (Hard-won Rule #2). FAIL-pre: the fast path
// already reported "postgres" correctly (query.go never collapsed it), but
// the dispatch path reported "regress_alice" (the login) — the two
// siblings disagreed on identical SQL.
func TestSetSessionAuthorizationPostgresIsNotDefault(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()

	admin := dialAndComplete(t, addr)
	writeQuery(t, admin, "CREATE ROLE regress_alice LOGIN")
	readUntilReady(t, admin)
	admin.Close()

	// Fast path.
	fastConn := dialAndCompleteAs(t, addr, "regress_alice")
	defer fastConn.Close()
	writeQuery(t, fastConn, "SET SESSION AUTHORIZATION postgres")
	readUntilReady(t, fastConn)
	if got := queryUserIdentity(t, fastConn); got != [4]string{"postgres", "postgres", "postgres", "postgres"} {
		t.Fatalf("fast path, login regress_alice, after SET SESSION AUTHORIZATION postgres: identity = %v, want all postgres", got)
	}

	// Dispatch path (multi-statement simple query).
	dispatchConn := dialAndCompleteAs(t, addr, "regress_alice")
	defer dispatchConn.Close()
	if got := queryUserIdentityMultiStatement(t, dispatchConn, "SET SESSION AUTHORIZATION postgres"); got != [4]string{"postgres", "postgres", "postgres", "postgres"} {
		t.Fatalf("dispatch path, login regress_alice, after SET SESSION AUTHORIZATION postgres: identity = %v, want all postgres", got)
	}
}

// TestSetRolePostgresIsExplicitNotDefault pins M0134-0009 round-2 review R7
// (same root cause as R6, fixed together): `SET ROLE postgres` is an
// explicit role target — current_user()/current_role/user() must report
// "postgres", not fall back to the login role — while session_user() stays
// the login role (SET ROLE never touches it). Exercises BOTH sibling paths.
// FAIL-pre: query.go's applySetRole conflated "POSTGRES" with the
// NONE/DEFAULT clear-branch (a pre-round-2 bug distinct from R6's), so both
// paths reported the login role instead of "postgres" for current_user.
func TestSetRolePostgresIsExplicitNotDefault(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()

	admin := dialAndComplete(t, addr)
	writeQuery(t, admin, "CREATE ROLE regress_alice LOGIN")
	readUntilReady(t, admin)
	admin.Close()

	// Fast path.
	fastConn := dialAndCompleteAs(t, addr, "regress_alice")
	defer fastConn.Close()
	writeQuery(t, fastConn, "SET ROLE postgres")
	readUntilReady(t, fastConn)
	if got := queryUserIdentity(t, fastConn); got != [4]string{"postgres", "regress_alice", "postgres", "postgres"} {
		t.Fatalf("fast path, login regress_alice, after SET ROLE postgres: identity = %v, want [postgres regress_alice postgres postgres]", got)
	}

	// Dispatch path (multi-statement simple query).
	dispatchConn := dialAndCompleteAs(t, addr, "regress_alice")
	defer dispatchConn.Close()
	if got := queryUserIdentityMultiStatement(t, dispatchConn, "SET ROLE postgres"); got != [4]string{"postgres", "regress_alice", "postgres", "postgres"} {
		t.Fatalf("dispatch path, login regress_alice, after SET ROLE postgres: identity = %v, want [postgres regress_alice postgres postgres]", got)
	}
}

// TestSetLocalSessionAuthorizationRevertsSessionUserAtCommit pins M0134-0009
// round-2 review R1 (BLOCKER): SnapshotLocalRoleIfNeeded/End() previously
// snapshotted and restored only NonSuperuserRole, not the two new fields
// (SessionUser, SetRoleIsActive) added alongside it. Repro from the review
// (login postgres): `BEGIN; SET LOCAL SESSION AUTHORIZATION alice; COMMIT;
// SELECT current_user, session_user;` must report postgres|postgres (PG
// 18.3 parity) — not alice|alice leaking past COMMIT for the rest of the
// connection. FAIL-pre: SessionUser stayed "alice" after COMMIT even though
// NonSuperuserRole correctly reverted.
func TestSetLocalSessionAuthorizationRevertsSessionUserAtCommit(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, "BEGIN")
	readUntilReady(t, conn)
	writeQuery(t, conn, "SET LOCAL SESSION AUTHORIZATION alice")
	readUntilReady(t, conn)
	if got := queryUserIdentity(t, conn); got != [4]string{"alice", "alice", "alice", "alice"} {
		t.Fatalf("in txn after SET LOCAL SESSION AUTHORIZATION alice: identity = %v, want all alice", got)
	}
	writeQuery(t, conn, "COMMIT")
	readUntilReady(t, conn)
	if got := queryUserIdentity(t, conn); got != [4]string{"postgres", "postgres", "postgres", "postgres"} {
		t.Fatalf("after COMMIT: identity = %v, want all postgres (SET LOCAL must not leak past COMMIT)", got)
	}

	// Same repro via ROLLBACK.
	writeQuery(t, conn, "BEGIN")
	readUntilReady(t, conn)
	writeQuery(t, conn, "SET LOCAL SESSION AUTHORIZATION alice")
	readUntilReady(t, conn)
	writeQuery(t, conn, "ROLLBACK")
	readUntilReady(t, conn)
	if got := queryUserIdentity(t, conn); got != [4]string{"postgres", "postgres", "postgres", "postgres"} {
		t.Fatalf("after ROLLBACK: identity = %v, want all postgres (SET LOCAL must not leak past ROLLBACK)", got)
	}
}

// TestSetLocalRoleRevertsSetRoleIsActiveAtCommit pins M0134-0009 round-2
// review R11 (folded into R1's fix): `SET LOCAL ROLE` leaving a stale
// SetRoleIsActive=true past COMMIT would let a later RESET ROLE incorrectly
// clear NonSuperuserRole (and flip is_superuser true) even when a
// non-LOCAL SET SESSION AUTHORIZATION is still active — the RESET ROLE
// no-op gate (TestResetRoleNoOpAfterSessionAuthorizationOnly) depends on
// SetRoleIsActive being accurate.
func TestSetLocalRoleRevertsSetRoleIsActiveAtCommit(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, "SET SESSION AUTHORIZATION alice")
	readUntilReady(t, conn)

	writeQuery(t, conn, "BEGIN")
	readUntilReady(t, conn)
	writeQuery(t, conn, "SET LOCAL ROLE bob")
	readUntilReady(t, conn)
	if got := queryUserIdentity(t, conn); got != [4]string{"bob", "alice", "bob", "bob"} {
		t.Fatalf("in txn after SET LOCAL ROLE bob: identity = %v, want [bob alice bob bob]", got)
	}
	writeQuery(t, conn, "COMMIT")
	readUntilReady(t, conn)
	if got := queryUserIdentity(t, conn); got != [4]string{"alice", "alice", "alice", "alice"} {
		t.Fatalf("after COMMIT: identity = %v, want all alice (SET LOCAL ROLE must not leak past COMMIT)", got)
	}

	// SetRoleIsActive must be back to false after COMMIT — checked directly,
	// since a stale true here would only surface via is_superuser
	// (identity alone can coincidentally still read "alice" because
	// SessionUser itself is untouched by SET LOCAL ROLE — see the RESET
	// ROLE check below for R11's actual observable failure).
	if got := queryIsSuperuser(t, conn); got != "off" {
		t.Fatalf("after COMMIT: is_superuser=%q, want %q (alice's non-superuser session-authorization override must still be in effect)", got, "off")
	}

	// R11's actual failure mode: a bare RESET ROLE after the SET LOCAL ROLE
	// reverted must remain a no-op — a stale SetRoleIsActive=true here would
	// wrongly let RESET ROLE clear NonSuperuserRole (flipping is_superuser
	// to "on") even though alice's SET SESSION AUTHORIZATION is still
	// active and current_user() happens to still read "alice" via the
	// SessionUser fallback either way.
	writeQuery(t, conn, "RESET ROLE")
	readUntilReady(t, conn)
	if got := queryUserIdentity(t, conn); got != [4]string{"alice", "alice", "alice", "alice"} {
		t.Fatalf("after RESET ROLE post-COMMIT: identity = %v, want all alice (session auth override must survive)", got)
	}
	if got := queryIsSuperuser(t, conn); got != "off" {
		t.Fatalf("after RESET ROLE post-COMMIT: is_superuser=%q, want %q (RESET ROLE with no SET ROLE active must be a no-op)", got, "off")
	}
}

// TestCopyToSelectCurrentUserMatchesPlainSelect pins M0134-0009 round-2
// review R5 (MISSED SIBLING — the named SELECT/COPY twin): dispatchCopyViaExecutor
// (copy.go) hand-builds its own executor.Context and never threaded
// SessionUser/NonSuperuserRole/SetRoleIsActive, so `COPY (SELECT
// current_user) TO STDOUT` reported the connection's login default even
// while a plain `SELECT current_user` on the same connection correctly
// reported the SET SESSION AUTHORIZATION target. FAIL-pre: the CopyData
// payload was "postgres\n", not "alice\n".
func TestCopyToSelectCurrentUserMatchesPlainSelect(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, "SET SESSION AUTHORIZATION alice")
	readUntilReady(t, conn)

	// Plain SELECT: known-good already (TestFastPathSessionUserIdentity).
	if got := queryUserIdentity(t, conn); got[0] != "alice" {
		t.Fatalf("plain SELECT current_user = %q, want %q", got[0], "alice")
	}

	// The named twin: COPY (SELECT current_user) TO STDOUT must agree.
	writeQuery(t, conn, "COPY (SELECT current_user) TO STDOUT")
	frames := readUntilReady(t, conn)
	var payload string
	found := false
	for _, f := range frames {
		if f.Type == libpq.MsgErrorResponse {
			t.Fatalf("COPY (SELECT current_user) TO STDOUT: unexpected ErrorResponse: %s", f.Payload)
		}
		if f.Type == libpq.MsgCopyData {
			payload += string(f.Payload)
			found = true
		}
	}
	if !found {
		t.Fatalf("COPY (SELECT current_user) TO STDOUT: no CopyData in %+v", frames)
	}
	if want := "alice\n"; payload != want {
		t.Fatalf("COPY (SELECT current_user) TO STDOUT payload = %q, want %q (must match plain SELECT current_user)", payload, want)
	}
}

