package postmaster

import (
	"net"
	"testing"

	"github.com/goopg/goopg/internal/libpq"
	"github.com/goopg/goopg/internal/utils/errcodes"
)

// M0134-0157 (psql_pipeline.sql): the extended protocol's Bind/Describe
// rejection messages are user-visible — upstream `psql_pipeline.out` prints
// them verbatim seven times, and clients match on them. goopg's texts were
// paraphrases, so every such line diverged from the oracle.
//
// The pinned strings come from upstream:
//   - `bind message supplies %d parameters, but prepared statement "%s" requires %d`
//     postgres/src/backend/tcop/postgres.c:1729 (exec_bind_message)
//   - `bind message has %d parameter formats but %d parameters`
//     postgres/src/backend/tcop/postgres.c:1723
//   - `unnamed prepared statement does not exist`
//     postgres/src/backend/tcop/postgres.c:1671 (Bind) and :2669 (Describe 'S')
//
// The unnamed-statement case is the one with real semantic content rather than
// wording: upstream only consults the prepared-statement table for a non-empty
// name, so the empty name reports a message carrying no name at all. Bind and
// Describe are sibling paths over the same lookup and must agree, which is why
// both are asserted here.
func TestExtendedBindMessageTextMatchesUpstream(t *testing.T) {
	addr, stop := startTestServer(t)
	defer stop()

	type step struct {
		name    string
		setup   func(t *testing.T, conn net.Conn)
		wantSQL errcodes.Code
		wantMsg string
	}

	steps := []step{
		{
			name: "named statement, too few parameters",
			setup: func(t *testing.T, conn net.Conn) {
				writeFrontendFrame(t, conn, libpq.MsgParse, parsePayload("p1", "SELECT $1", nil))
				writeFrontendFrame(t, conn, libpq.MsgBind, bindPayload("", "p1", nil, nil, nil))
			},
			wantSQL: errcodes.ProtocolViolation,
			wantMsg: `bind message supplies 0 parameters, but prepared statement "p1" requires 1`,
		},
		{
			// The exact shape upstream psql_pipeline.out records: psql's
			// `\bind` binds through the UNNAMED statement, so the name in the
			// message is the empty string, quoted.
			name: "unnamed statement, too few parameters",
			setup: func(t *testing.T, conn net.Conn) {
				writeFrontendFrame(t, conn, libpq.MsgParse, parsePayload("", "SELECT $1", nil))
				writeFrontendFrame(t, conn, libpq.MsgBind, bindPayload("", "", nil, nil, nil))
			},
			wantSQL: errcodes.ProtocolViolation,
			wantMsg: `bind message supplies 0 parameters, but prepared statement "" requires 1`,
		},
		{
			name: "too many parameters",
			setup: func(t *testing.T, conn net.Conn) {
				writeFrontendFrame(t, conn, libpq.MsgParse, parsePayload("p2", "SELECT 1", nil))
				writeFrontendFrame(t, conn, libpq.MsgBind,
					bindPayload("", "p2", nil, []bindParam{{value: "val1"}}, nil))
			},
			wantSQL: errcodes.ProtocolViolation,
			wantMsg: `bind message supplies 1 parameters, but prepared statement "p2" requires 0`,
		},
		{
			name: "parameter format count mismatch",
			setup: func(t *testing.T, conn net.Conn) {
				writeFrontendFrame(t, conn, libpq.MsgParse, parsePayload("p3", "SELECT $1", nil))
				writeFrontendFrame(t, conn, libpq.MsgBind,
					bindPayload("", "p3", []uint16{0, 0, 0}, []bindParam{{value: "v"}}, nil))
			},
			wantSQL: errcodes.ProtocolViolation,
			wantMsg: `bind message has 3 parameter formats but 1 parameters`,
		},
		{
			name: "bind to a missing named statement",
			setup: func(t *testing.T, conn net.Conn) {
				writeFrontendFrame(t, conn, libpq.MsgBind, bindPayload("", "nope", nil, nil, nil))
			},
			wantSQL: errcodes.InvalidSQLStatementName,
			wantMsg: `prepared statement "nope" does not exist`,
		},
		{
			name: "bind to the missing unnamed statement",
			setup: func(t *testing.T, conn net.Conn) {
				writeFrontendFrame(t, conn, libpq.MsgBind, bindPayload("", "", nil, nil, nil))
			},
			wantSQL: errcodes.InvalidSQLStatementName,
			wantMsg: `unnamed prepared statement does not exist`,
		},
		{
			name: "describe the missing unnamed statement",
			setup: func(t *testing.T, conn net.Conn) {
				writeFrontendFrame(t, conn, libpq.MsgDescribe, describePayload('S', ""))
			},
			wantSQL: errcodes.InvalidSQLStatementName,
			wantMsg: `unnamed prepared statement does not exist`,
		},
		{
			name: "describe a missing named statement",
			setup: func(t *testing.T, conn net.Conn) {
				writeFrontendFrame(t, conn, libpq.MsgDescribe, describePayload('S', "nope"))
			},
			wantSQL: errcodes.InvalidSQLStatementName,
			wantMsg: `prepared statement "nope" does not exist`,
		},
	}

	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			// A fresh connection per subtest: an errored extended-protocol
			// message group skips every later message until Sync, so sharing a
			// connection would silently drop the next subtest's setup frames.
			conn := dialAndComplete(t, addr)
			defer conn.Close()

			st.setup(t, conn)
			writeFrontendFrame(t, conn, libpq.MsgSync, nil)

			var errFrame *libpq.Frame
			for _, f := range readUntilReady(t, conn) {
				if f.Type == libpq.MsgErrorResponse {
					fc := f
					errFrame = &fc
					break
				}
			}
			if errFrame == nil {
				t.Fatalf("no ErrorResponse")
			}
			got := parseErrorFields(t, errFrame.Payload)
			if got[libpq.FieldSQLState] != string(st.wantSQL) {
				t.Errorf("sqlstate=%q want %q", got[libpq.FieldSQLState], st.wantSQL)
			}
			if got[libpq.FieldMessage] != st.wantMsg {
				t.Errorf("message=%q want %q", got[libpq.FieldMessage], st.wantMsg)
			}
		})
	}
}
