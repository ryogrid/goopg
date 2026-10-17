package postmaster

import (
	"testing"

	"github.com/goopg/goopg/internal/libpq"
)

// TestShowThroughExecutorNamesColumnCanonically pins PG 18.3's SHOW result
// column (GetPGVariableResultDesc, guc_funcs.c): it carries the parameter's
// own spelling, whatever case the statement used. A multi-statement message
// runs SHOW through the executor, which labelled the column with the name
// as typed (`datestyle`), while the single-statement fast path already
// printed `DateStyle`. PG 18.3 prints:
//
//	SET LOCAL DateStyle = 'SQL, DMY'; SHOW datestyle  -> column "DateStyle"
//	SHOW timezone                                     -> column "TimeZone"
func TestShowThroughExecutorNamesColumnCanonically(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, "BEGIN; SET LOCAL DateStyle = 'SQL, DMY'; SHOW datestyle; SHOW timezone; COMMIT")
	frames := readUntilReady(t, conn)
	var got []string
	for _, f := range frames {
		if f.Type == libpq.MsgRowDescription {
			got = append(got, rowDescriptionNames(t, f.Payload)...)
		}
	}
	want := []string{"DateStyle", "TimeZone"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("SHOW columns through the executor = %v, want PG 18.3's %v", got, want)
	}
}
