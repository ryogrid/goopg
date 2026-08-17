package postmaster

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/libpq"
)

// TestExecuteCoercesRegclassArrayParam covers acceptance criterion 1 of
// m0134-0005a: EXECUTE gi('{notnull_tbl1}') against a
// PREPARE gi(regclass[]) statement must coerce the string-literal array
// argument to a real regclass[] datum so `conrelid = ANY($1)` matches by
// OID, not by accidental text equality. Before this change, goopg only
// validated the argument shape and bound the raw KindString datum, so the
// ANY() comparison always evaluated false and the query returned 0 rows.
// Oracle: postgres/src/backend/commands/prepare.c:EvaluateParams.
func TestExecuteCoercesRegclassArrayParam(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()

	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, `
CREATE TABLE notnull_tbl1(a int NOT NULL);
PREPARE gi(regclass[]) AS SELECT conname FROM pg_constraint WHERE conrelid = ANY($1) AND contype='n';
EXECUTE gi('{notnull_tbl1}');
`)
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
		t.Fatalf("DataRow count=%d, want 1 (regclass[] param must coerce to real OIDs); frames=%+v", len(dataRows), frames)
	}
}

// TestExecuteCoercesScalarRegclassParam covers acceptance criterion 2 of
// m0134-0005a's underlying goal — a scalar (non-array) parameter is the
// same generic bug as the regclass[] case above — via `WHERE oid = $1`,
// the exact pgbench-probe shape cited in expr.go's CastExpr regclass arm
// comment (`... WHERE oid = $1::pg_catalog.regclass`). Before this change
// $1 stayed a raw KindString relation name and never matched any oid.
//
// Deviation from the brief's literal acceptance-criterion-2 wording
// (`PREPARE t1(regclass) AS SELECT pg_typeof($1)` reporting "regclass"):
// pg_typeof($1) cannot observe this fix at all. optimizer/planner.go's
// resolveExpr unconditionally folds `pg_typeof(<any-arg>)` into a
// StringConst *at plan time*, via exprType(arg); exprType has no
// `case *ParamRef` (optimizer/plan.go's ParamRef struct carries no Type
// field), so it falls through to the "unknown" default — before any
// Datum, let alone a coerced one, exists. This is a pre-existing,
// generic-across-every-type planner gap (`pg_typeof($1)` reports
// "unknown" for an int/text/whatever param too, not just regclass),
// orthogonal to the postmaster/executor param-coercion path this brief
// scopes. Fixing it needs a ParamRef.Type field threaded from
// prepDef.paramTypes into the planner — out of the two-dispatch-branch
// scope. See report.md for the full trace; reported as a deferral
// candidate, not fixed here.
func TestExecuteCoercesScalarRegclassParam(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()

	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, `
CREATE TABLE notnull_tbl1(a int NOT NULL);
PREPARE t1(regclass) AS SELECT relname FROM pg_class WHERE oid = $1;
EXECUTE t1('notnull_tbl1');
`)
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
		t.Fatalf("DataRow count=%d, want 1 (regclass param must coerce to the real OID so `oid = $1` matches); frames=%+v", len(dataRows), frames)
	}
	row := decodeDataRow(t, dataRows[0])
	if got := string(row[0]); got != "notnull_tbl1" {
		t.Fatalf("relname=%q, want notnull_tbl1", got)
	}
}

// TestExecuteRegclassParamNoSuchRelationErrors covers acceptance criterion
// 3: a parameter that cannot be cast to the declared type must surface the
// cast's own PG error (42P01 relation does not exist), not a silent wrong
// answer.
func TestExecuteRegclassParamNoSuchRelationErrors(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()

	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, `
PREPARE t1(regclass) AS SELECT pg_typeof($1);
EXECUTE t1('no_such_rel');
`)
	frames := readUntilReady(t, conn)

	var errFrame *libpq.Frame
	for i := range frames {
		if frames[i].Type == libpq.MsgErrorResponse {
			errFrame = &frames[i]
			break
		}
	}
	if errFrame == nil {
		t.Fatalf("expected an error response for an unresolvable regclass param; frames=%+v", frames)
	}
	if !strings.Contains(string(errFrame.Payload), "42P01") {
		t.Fatalf("error payload=%q, want SQLSTATE 42P01", string(errFrame.Payload))
	}
}

// TestCreateTableAsExecuteCoercesParam covers acceptance criterion 4: the
// CREATE TABLE ... AS EXECUTE sibling path (dispatch.go's
// *parser.CreateTableStmt/ExecuteSource branch) must apply the same
// coercion as plain EXECUTE. Hard-won Rule #2 — sibling paths must move
// together. Uses the same `WHERE oid = $1` shape as
// TestExecuteCoercesScalarRegclassParam (see its doc comment for why
// pg_typeof($1) cannot be used to observe this).
func TestCreateTableAsExecuteCoercesParam(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()

	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, `
PREPARE sel(regclass) AS SELECT relname FROM pg_class WHERE oid = $1;
CREATE TABLE ctas_out AS EXECUTE sel('items');
SELECT relname FROM ctas_out;
`)
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
		t.Fatalf("DataRow count=%d, want 1 (regclass param must coerce to the real OID so `oid = $1` matches); frames=%+v", len(dataRows), frames)
	}
	row := decodeDataRow(t, dataRows[0])
	if got := string(row[0]); got != "items" {
		t.Fatalf("ctas_out.relname=%q, want items (CTAS-EXECUTE param must be coerced to declared type)", got)
	}
}
