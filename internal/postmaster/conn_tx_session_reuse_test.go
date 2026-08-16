package postmaster

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestConnTxSessionNilWhenNotExplicit pins a root-0024 follow-up (2026-07-04):
// connTxState.Session() must return nil once an explicit transaction has
// ended, even though it lazily allocated and permanently keeps c.sess (Begin
// reuses it for the connection's next explicit transaction; End only flips
// c.active back to false). Before the fix, Session() returned the stale c.sess
// unconditionally, so dispatch.go's per-message wiring
// (`if sess := connTx.Session(); sess != nil { ectx.Session = sess } else {
// ectx.Session = <message-scoped throwaway> }`) handed every LATER autocommit
// statement on the same connection that reused, long-lived session instead of
// a fresh throwaway one — once any explicit transaction had ever run on the
// connection. A successful standalone autocommit CREATE TABLE's
// RecordDDLCreate entry then sat in that reused session's pendingDDL list
// forever (nothing drains it on success), where a LATER, wholly unrelated
// aborting autocommit batch on the SAME connection incorrectly rolled it back
// too via ProcessRollbackUndos, silently dropping an already-committed table.
func TestConnTxSessionNilWhenNotExplicit(t *testing.T) {
	addr, cat, stop := startCopyExecServer(t)
	defer stop()
	im := cat.(*catalog.InMemory)

	conn := dialAndComplete(t, addr)
	defer conn.Close()

	// 1. A throwaway explicit transaction so connTx.sess is lazily allocated
	// and (per Begin's reuse contract) never reset to nil afterward.
	writeQuery(t, conn, "BEGIN; CREATE TABLE zz_conntx_warm (a int4); COMMIT;")
	readUntilReady(t, conn)

	// 2. A successful, standalone autocommit CREATE TABLE (no batch, no failure).
	writeQuery(t, conn, "CREATE TABLE zz_conntx_survivor (a int4);")
	readUntilReady(t, conn)
	if _, ok := im.LookupTable(parser.ObjectName{Name: "zz_conntx_survivor"}); !ok {
		t.Fatalf("zz_conntx_survivor should exist after successful autocommit CREATE TABLE")
	}

	// 3. A LATER, unrelated autocommit batch on the SAME connection that aborts.
	writeQuery(t, conn, "CREATE TABLE zz_conntx_unrelated (a int4); SELECT * FROM zz_definitely_missing_relation;")
	readUntilReady(t, conn)

	if _, ok := im.LookupTable(parser.ObjectName{Name: "zz_conntx_unrelated"}); ok {
		t.Fatalf("zz_conntx_unrelated should have been rolled back")
	}
	// The earlier, already-committed survivor must NOT be collateral damage.
	if _, ok := im.LookupTable(parser.ObjectName{Name: "zz_conntx_survivor"}); !ok {
		t.Fatalf("zz_conntx_survivor was incorrectly rolled back by an unrelated LATER aborting autocommit batch")
	}
}
