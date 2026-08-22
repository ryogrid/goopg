package postmaster

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"

	"github.com/lib/pq"
)

// Unit-level tests for WHERE CURRENT OF resolution (M0134-0074). These
// exercise resolveCurrentOf directly without a running server: unknown cursor
// name → 34000, cursor before first row / past last row → 24000, and a
// positioned cursor → a concrete `ctid = '(block,off)'` equality.

func TestCurrentOfResolveUnknownCursor(t *testing.T) {
	s := &Server{}
	connTx := &connTxState{}

	_, err := s.resolveCurrentOf(connTx, "c")
	if err == nil {
		t.Fatal("resolveCurrentOf on unknown cursor returned nil error, want 34000")
	}
	ee, ok := err.(*executor.ExecError)
	if !ok {
		t.Fatalf("error type = %T, want *executor.ExecError", err)
	}
	if ee.Code != "34000" {
		t.Fatalf("error code = %q, want 34000", ee.Code)
	}
	if want := `cursor "c" does not exist`; ee.Message != want {
		t.Fatalf("error message = %q, want %q", ee.Message, want)
	}
}

func TestCurrentOfResolveNotPositioned(t *testing.T) {
	s := &Server{}

	t.Run("before_first_row", func(t *testing.T) {
		connTx := &connTxState{}
		connTx.cursorDeclare("c", "SELECT 1")
		cur, _ := connTx.cursorLookup("c")
		cur.Materialized = true
		// Pos == 0: a freshly declared cursor, never fetched.
		cur.Pos = 0

		assertNotPositioned(t, s, connTx)
	})

	t.Run("past_last_row", func(t *testing.T) {
		connTx := &connTxState{}
		connTx.cursorDeclare("c", "SELECT 1")
		cur, _ := connTx.cursorLookup("c")
		cur.Materialized = true
		cur.Rows = []executor.Row{{}}
		cur.TIDs = []storage.ItemPointer{{Block: 0, Offset: 1}}
		cur.Pos = 1
		cur.AtEnd = true // past-end forward fetch left Pos unchanged, AtEnd disambiguates

		assertNotPositioned(t, s, connTx)
	})
}

func assertNotPositioned(t *testing.T, s *Server, connTx *connTxState) {
	t.Helper()
	_, err := s.resolveCurrentOf(connTx, "c")
	if err == nil {
		t.Fatal("resolveCurrentOf on non-positioned cursor returned nil error, want 24000")
	}
	ee, ok := err.(*executor.ExecError)
	if !ok {
		t.Fatalf("error type = %T, want *executor.ExecError", err)
	}
	if ee.Code != "24000" {
		t.Fatalf("error code = %q, want 24000", ee.Code)
	}
	if want := `cursor "c" is not positioned on a row`; ee.Message != want {
		t.Fatalf("error message = %q, want %q", ee.Message, want)
	}
}

func TestCurrentOfResolvePositioned(t *testing.T) {
	s := &Server{}
	connTx := &connTxState{}
	connTx.cursorDeclare("c", "SELECT 1")
	cur, _ := connTx.cursorLookup("c")
	cur.Materialized = true
	cur.Rows = []executor.Row{{}}
	cur.TIDs = []storage.ItemPointer{{Block: 0, Offset: 5}}
	cur.Pos = 1 // positioned on Rows[0] = TIDs[0]

	expr, err := s.resolveCurrentOf(connTx, "c")
	if err != nil {
		t.Fatalf("resolveCurrentOf on positioned cursor: %v", err)
	}
	bin, ok := expr.(*parser.BinaryOp)
	if !ok {
		t.Fatalf("expr type = %T, want *parser.BinaryOp", expr)
	}
	if bin.Op != parser.OpEq {
		t.Fatalf("op = %v, want parser.OpEq", bin.Op)
	}
	cref, ok := bin.Left.(*parser.ColumnRef)
	if !ok || cref.Column != "ctid" {
		t.Fatalf("left = %#v, want parser.ColumnRef{Column: \"ctid\"}", bin.Left)
	}
	sc, ok := bin.Right.(*parser.StringConst)
	if !ok || sc.Value != "(0,5)" {
		t.Fatalf("right = %#v, want parser.StringConst{Value: \"(0,5)\"}", bin.Right)
	}
}

// EXPLAIN (ANALYZE ...) wraps the DML in an ExplainStmt — the shape
// tidscan.sql uses for every CURRENT OF statement. resolveCurrentOfInStmt must
// unwrap it and resolve the inner DML's Where before planning (the optimizer
// plans the ExplainStmt's Inner recursively, planner.go:260).
func TestCurrentOfResolveExplainWrapped(t *testing.T) {
	s := &Server{}
	connTx := &connTxState{}
	connTx.cursorDeclare("c", "SELECT 1")
	cur, _ := connTx.cursorLookup("c")
	cur.Materialized = true
	cur.Rows = []executor.Row{{}}
	cur.TIDs = []storage.ItemPointer{{Block: 0, Offset: 5}}
	cur.Pos = 1

	upd := &parser.UpdateStmt{CurrentOf: "c"}
	stmt := parser.Stmt(&parser.ExplainStmt{Inner: upd})
	if err := s.resolveCurrentOfInStmt(connTx, stmt); err != nil {
		t.Fatalf("resolveCurrentOfInStmt(ExplainStmt-wrapped): %v", err)
	}
	if upd.Where == nil {
		t.Fatal("EXPLAIN-wrapped UPDATE CurrentOf was not resolved: Where is nil")
	}
	bin, ok := upd.Where.(*parser.BinaryOp)
	if !ok {
		t.Fatalf("Where type = %T, want *parser.BinaryOp", upd.Where)
	}
	if bin.Op != parser.OpEq {
		t.Fatalf("op = %v, want parser.OpEq", bin.Op)
	}
	if !isCurrentOfDML(stmt) {
		t.Fatal("isCurrentOfDML(ExplainStmt wrapping CURRENT OF UPDATE) = false, want true (plan-cache exclusion)")
	}
}

// End-to-end: UPDATE/DELETE ... WHERE CURRENT OF updates exactly the cursor's
// current row (not the whole table), and the past-end case raises 24000.
// Mirrors the tidscan regress block (postgres/src/test/regress/sql/tidscan.sql)
// and acceptance criterion 2 of M0134-0074.
func TestCurrentOfUpdateEndToEnd(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()

	dsn := "host=" + addrHost(addr) + " port=" + addrPort(addr) + " user=postgres dbname=postgres sslmode=disable"
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `CREATE TABLE co_test(id integer)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	defer db.ExecContext(ctx, `DROP TABLE IF EXISTS co_test`) //nolint:errcheck
	if _, err := db.ExecContext(ctx, `INSERT INTO co_test VALUES (1), (2), (3)`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		t.Fatalf("BEGIN: %v", err)
	}
	defer conn.ExecContext(ctx, "ROLLBACK") //nolint:errcheck

	if _, err := conn.ExecContext(ctx, "DECLARE c CURSOR FOR SELECT id FROM co_test"); err != nil {
		t.Fatalf("DECLARE: %v", err)
	}
	// Skip to row 2, then row 3.
	for i := 0; i < 2; i++ {
		rows, err := conn.QueryContext(ctx, "FETCH NEXT FROM c")
		if err != nil {
			t.Fatalf("FETCH NEXT %d: %v", i, err)
		}
		rows.Close()
	}

	// UPDATE ... WHERE CURRENT OF c — cursor is on row 2.
	if _, err := conn.ExecContext(ctx, "UPDATE co_test SET id = -id WHERE CURRENT OF c"); err != nil {
		t.Fatalf("UPDATE CURRENT OF (row 2): %v", err)
	}

	// FETCH NEXT → row 3, then UPDATE CURRENT OF → row 3.
	rows, err := conn.QueryContext(ctx, "FETCH NEXT FROM c")
	if err != nil {
		t.Fatalf("FETCH NEXT (row 3): %v", err)
	}
	rows.Close()
	if _, err := conn.ExecContext(ctx, "UPDATE co_test SET id = -id WHERE CURRENT OF c"); err != nil {
		t.Fatalf("UPDATE CURRENT OF (row 3): %v", err)
	}

	// The two CURRENT OF updates must touch exactly rows 2 and 3 — row 1 untouched.
	got := selectInts(t, ctx, conn, "SELECT id FROM co_test")
	sort.Ints(got)
	want := []int{-3, -2, 1}
	if len(got) != len(want) {
		t.Fatalf("SELECT id after CURRENT OF updates = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SELECT id after CURRENT OF updates = %v, want %v", got, want)
		}
	}

	// Move past the last row: FETCH NEXT → nothing (AtEnd=true).
	rows, err = conn.QueryContext(ctx, "FETCH NEXT FROM c")
	if err != nil {
		t.Fatalf("FETCH NEXT (past end): %v", err)
	}
	rows.Close()

	// Past-end CURRENT OF must raise 24000, not update everything.
	if _, err := conn.ExecContext(ctx, "UPDATE co_test SET id = -id WHERE CURRENT OF c"); err == nil {
		t.Fatal("UPDATE CURRENT OF past-end returned nil error, want 24000")
	} else {
		var pqErr *pq.Error
		if errors.As(err, &pqErr) {
			if pqErr.Code != "24000" {
				t.Fatalf("UPDATE CURRENT OF past-end SQLSTATE = %s, want 24000 (msg: %s)", pqErr.Code, pqErr.Message)
			}
			if want := "cursor \"c\" is not positioned on a row"; pqErr.Message != want {
				t.Fatalf("UPDATE CURRENT OF past-end message = %q, want %q", pqErr.Message, want)
			}
		} else {
			t.Fatalf("UPDATE CURRENT OF past-end error = %v, want *pq.Error with 24000", err)
		}
	}
}

// End-to-end mirror of the tidscan.sql CURRENT OF block
// (postgres/src/test/regress/sql/tidscan.sql), which wraps EVERY statement in
// EXPLAIN (ANALYZE ...) — the shape that previously bypassed
// executeOneSimpleStmt's top-level UpdateStmt/DeleteStmt resolution. The
// Update node must report actual rows=1.00 (only the cursor's row), the
// past-end form must raise 24000, and the table must end at 1 / -2 / -3.
func TestCurrentOfUpdateExplainAnalyzeEndToEnd(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()

	dsn := "host=" + addrHost(addr) + " port=" + addrPort(addr) + " user=postgres dbname=postgres sslmode=disable"
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE co_ex(id integer)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	defer db.ExecContext(ctx, `DROP TABLE IF EXISTS co_ex`) //nolint:errcheck
	if _, err := db.ExecContext(ctx, `INSERT INTO co_ex VALUES (1), (2), (3)`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		t.Fatalf("BEGIN: %v", err)
	}
	defer conn.ExecContext(ctx, "ROLLBACK") //nolint:errcheck
	if _, err := conn.ExecContext(ctx, "DECLARE c CURSOR FOR SELECT id FROM co_ex"); err != nil {
		t.Fatalf("DECLARE: %v", err)
	}

	// Skip to row 2 (cursor position matches the tidscan.sql block), then
	// UPDATE CURRENT OF under EXPLAIN (ANALYZE ...) — must touch exactly row 2.
	for i := 0; i < 2; i++ {
		rows, err := conn.QueryContext(ctx, "FETCH NEXT FROM c")
		if err != nil {
			t.Fatalf("FETCH NEXT %d: %v", i, err)
		}
		rows.Close()
	}
	plan := explainPlan(t, ctx, conn,
		"EXPLAIN (ANALYZE, COSTS OFF, SUMMARY OFF, TIMING OFF, BUFFERS OFF) UPDATE co_ex SET id = -id WHERE CURRENT OF c RETURNING *")
	if !strings.Contains(plan, "actual rows=1.00") {
		t.Fatalf("EXPLAIN (ANALYZE) UPDATE CURRENT OF (row 2) plan = %q, want Update actual rows=1.00", plan)
	}

	// FETCH to row 3, UPDATE CURRENT OF — must touch exactly row 3.
	rows, err := conn.QueryContext(ctx, "FETCH NEXT FROM c")
	if err != nil {
		t.Fatalf("FETCH NEXT (row 3): %v", err)
	}
	rows.Close()
	plan = explainPlan(t, ctx, conn,
		"EXPLAIN (ANALYZE, COSTS OFF, SUMMARY OFF, TIMING OFF, BUFFERS OFF) UPDATE co_ex SET id = -id WHERE CURRENT OF c RETURNING *")
	if !strings.Contains(plan, "actual rows=1.00") {
		t.Fatalf("EXPLAIN (ANALYZE) UPDATE CURRENT OF (row 3) plan = %q, want Update actual rows=1.00", plan)
	}

	// Rows 2 and 3 negated; row 1 untouched. Mirrors tidscan.sql, which
	// SELECTs before positioning the cursor past the last row.
	got := selectInts(t, ctx, conn, "SELECT id FROM co_ex")
	sort.Ints(got)
	want := []int{-3, -2, 1}
	if len(got) != len(want) {
		t.Fatalf("SELECT id after CURRENT OF updates = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SELECT id after CURRENT OF updates = %v, want %v", got, want)
		}
	}

	// FETCH past the last row → 0 rows → AtEnd=true.
	rows, err = conn.QueryContext(ctx, "FETCH NEXT FROM c")
	if err != nil {
		t.Fatalf("FETCH NEXT (past end): %v", err)
	}
	rows.Close()

	// Past-end EXPLAIN (ANALYZE) UPDATE must raise 24000, not update everything.
	plan = explainPlanErr(t, ctx, conn,
		"EXPLAIN (ANALYZE, COSTS OFF, SUMMARY OFF, TIMING OFF, BUFFERS OFF) UPDATE co_ex SET id = -id WHERE CURRENT OF c RETURNING *")
	if !strings.Contains(plan, "24000") || !strings.Contains(plan, "cursor \"c\" is not positioned on a row") {
		t.Fatalf("EXPLAIN (ANALYZE) UPDATE CURRENT OF past-end error = %q, want 24000 cursor %q", plan, "c")
	}
}

// explainPlan runs an EXPLAIN (ANALYZE ...) statement and returns the plan text.
func explainPlan(t *testing.T, ctx context.Context, conn *sql.Conn, query string) string {
	t.Helper()
	rows, err := conn.QueryContext(ctx, query)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan plan line: %v", err)
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	return b.String()
}

// explainPlanErr runs an EXPLAIN (ANALYZE ...) statement expected to ERROR and
// returns the error text (SQLSTATE + message).
func explainPlanErr(t *testing.T, ctx context.Context, conn *sql.Conn, query string) string {
	t.Helper()
	rows, err := conn.QueryContext(ctx, query)
	if err == nil {
		rows.Close()
		t.Fatalf("%s: expected error, got none", query)
	}
	var pqErr *pq.Error
	if !errors.As(err, &pqErr) {
		t.Fatalf("%s: error = %v, want *pq.Error", query, err)
	}
	return string(pqErr.Code) + ": " + pqErr.Message
}

func selectInts(t *testing.T, ctx context.Context, conn *sql.Conn, query string) []int {
	t.Helper()
	rows, err := conn.QueryContext(ctx, query)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	return out
}
