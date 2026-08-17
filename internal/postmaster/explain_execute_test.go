package postmaster

import (
	"net"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/libpq"
)

// TestExplainExecuteRendersPreparedPlan pins the M0100-0005h fix:
// `EXPLAIN [(opts)] EXECUTE <name>` must look up the prepared
// statement's parsed Query and render its actual plan tree.  Before
// the fix the planner wrapped the unresolved `ExecuteStmt` in a
// `Utility` node and EXPLAIN printed the placeholder string
// "Utility *parser.ExecuteStmt", causing every isolation spec that
// uses `EXPLAIN (COSTS OFF) EXECUTE` to diverge from upstream.
func TestExplainExecuteRendersPreparedPlan(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()

	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, "PREPARE get_items AS SELECT id, label FROM items")
	if errs := collectExplainExecuteErrors(t, conn); len(errs) > 0 {
		t.Fatalf("PREPARE failed: %v", errs)
	}

	writeQuery(t, conn, "EXPLAIN (COSTS OFF) EXECUTE get_items")
	frames := readUntilReady(t, conn)

	var dataRows []string
	for _, f := range frames {
		if f.Type == libpq.MsgErrorResponse {
			t.Fatalf("EXPLAIN EXECUTE failed: %s", string(f.Payload))
		}
		if f.Type == libpq.MsgDataRow {
			dataRows = append(dataRows, string(f.Payload))
		}
	}
	if len(dataRows) == 0 {
		t.Fatalf("EXPLAIN EXECUTE produced no DataRow frames")
	}
	combined := strings.Join(dataRows, "\n")
	if strings.Contains(combined, "Utility") || strings.Contains(combined, "ExecuteStmt") {
		t.Fatalf("EXPLAIN EXECUTE still rendered the unresolved utility placeholder; rows:\n%s", combined)
	}
	if !strings.Contains(combined, "Seq Scan on items") {
		t.Fatalf("EXPLAIN EXECUTE did not render the prepared SELECT's plan; rows:\n%s", combined)
	}
}

// TestExplainExecuteUnknownPreparedReports26000 verifies that
// EXPLAIN EXECUTE on a name that was never prepared surfaces the
// standard 26000 (invalid_sql_statement_name) error rather than
// the generic "could not parse" path.
func TestExplainExecuteUnknownPreparedReports26000(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()

	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, "EXPLAIN EXECUTE never_prepared")
	frames := readUntilReady(t, conn)

	var found bool
	for _, f := range frames {
		if f.Type == libpq.MsgErrorResponse {
			found = true
			if !strings.Contains(string(f.Payload), "26000") {
				t.Fatalf("expected SQLSTATE 26000 in error payload, got: %q", string(f.Payload))
			}
			if !strings.Contains(string(f.Payload), "never_prepared") {
				t.Fatalf("expected error payload to name the missing prepared statement; got %q", string(f.Payload))
			}
		}
	}
	if !found {
		t.Fatalf("expected ErrorResponse for unknown prepared statement; frames=%+v", frames)
	}
}

func collectExplainExecuteErrors(t *testing.T, conn net.Conn) []string {
	t.Helper()
	frames := readUntilReady(t, conn)
	var errs []string
	for _, f := range frames {
		if f.Type == libpq.MsgErrorResponse {
			errs = append(errs, string(f.Payload))
		}
	}
	return errs
}
