package postmaster

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/libpq"
)

func TestPrepareExecuteSameBatchReturnsRows(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()

	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, "INSERT INTO items VALUES (1, 'alpha'), (2, 'beta'); PREPARE get_items AS SELECT id, label FROM items ORDER BY id; EXECUTE get_items;")
	frames := readUntilReady(t, conn)

	var (
		dataRows [][]byte
		rfqCount int
	)
	for _, f := range frames {
		switch f.Type {
		case libpq.MsgErrorResponse:
			t.Fatalf("unexpected error: %s", string(f.Payload))
		case libpq.MsgDataRow:
			dataRows = append(dataRows, f.Payload)
		case libpq.MsgReadyForQuery:
			rfqCount++
		}
	}
	if rfqCount != 1 {
		t.Fatalf("ReadyForQuery count=%d, want 1; frames=%+v", rfqCount, frames)
	}
	if len(dataRows) != 2 {
		t.Fatalf("DataRow count=%d, want 2; frames=%+v", len(dataRows), frames)
	}
	first := decodeDataRow(t, dataRows[0])
	second := decodeDataRow(t, dataRows[1])
	if got := string(first[0]); got != "1" {
		t.Fatalf("first row id=%q, want 1", got)
	}
	if got := string(first[1]); got != "alpha" {
		t.Fatalf("first row label=%q, want alpha", got)
	}
	if got := string(second[0]); got != "2" {
		t.Fatalf("second row id=%q, want 2", got)
	}
	if got := string(second[1]); got != "beta" {
		t.Fatalf("second row label=%q, want beta", got)
	}
}

func TestPrepareExecuteParametersUseExecuteArgs(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()

	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, "INSERT INTO items VALUES (1, 'alpha'), (2, 'beta'); PREPARE get_label(int) AS SELECT label FROM items WHERE id = $1; EXECUTE get_label(2);")
	frames := readUntilReady(t, conn)

	var dataRows [][]byte
	for _, f := range frames {
		switch f.Type {
		case libpq.MsgErrorResponse:
			t.Fatalf("unexpected error: %s", string(f.Payload))
		case libpq.MsgDataRow:
			dataRows = append(dataRows, f.Payload)
		}
	}
	if len(dataRows) != 1 {
		t.Fatalf("DataRow count=%d, want 1; frames=%+v", len(dataRows), frames)
	}
	row := decodeDataRow(t, dataRows[0])
	if got := string(row[0]); got != "beta" {
		t.Fatalf("label=%q, want beta", got)
	}
}

// TestPrepareExecuteRejectsResultTypeChange verifies PostgreSQL's cached-plan
// result-type revalidation: PREPARE p AS SELECT * FROM t; ALTER TABLE t ADD
// COLUMN …; EXECUTE p; must raise "cached plan must not change result type"
// rather than silently returning the widened row. Mirrors
// RevalidateCachedQuery (postgres/src/backend/utils/cache/plancache.c:858).
// M0134-0054 bucket 5.
func TestPrepareExecuteRejectsResultTypeChange(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()

	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, "CREATE TABLE rtchg (i int); PREPARE p AS SELECT * FROM rtchg; ALTER TABLE rtchg ADD COLUMN q int; EXECUTE p;")
	frames := readUntilReady(t, conn)

	var errMsg string
	for _, f := range frames {
		if f.Type == libpq.MsgErrorResponse {
			errMsg = string(f.Payload)
			break
		}
	}
	if errMsg == "" {
		t.Fatalf("expected an ErrorResponse for EXECUTE after result-type change; frames=%+v", frames)
	}
	if !strings.Contains(errMsg, "cached plan must not change result type") {
		t.Fatalf("error = %q, want substring %q", errMsg, "cached plan must not change result type")
	}
}
