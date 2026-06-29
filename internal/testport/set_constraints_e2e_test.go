package testport

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// scConn pins a single backend connection. Each ExecContext on it is a separate
// simple-query message (lib/pq uses the simple protocol for argument-less
// statements), so an explicit BEGIN/SET/INSERT/COMMIT issued as individual Execs
// is delivered as distinct messages on one backend — the path psql interactive
// sessions use, where each message gets its own snapshot (unlike a single
// multi-statement batch string, and avoiding lib/pq's BeginTx wire-status
// check). 0119-0004.
func scConn(t *testing.T, c *cluster.Cluster, ctx context.Context) (*sql.DB, *sql.Conn) {
	t.Helper()
	host, port, ok := strings.Cut(c.ListenAddr(), ":")
	if !ok {
		t.Fatalf("bad listen addr %q", c.ListenAddr())
	}
	dsn := fmt.Sprintf("host=%s port=%s user=postgres dbname=postgres sslmode=disable", host, port)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		t.Fatalf("pin conn: %v", err)
	}
	return db, conn
}

// TestPort_SetConstraintsDeferral exercises SET CONSTRAINTS runtime deferral of
// a DEFERRABLE INITIALLY IMMEDIATE foreign key end-to-end. 0119-0004.
func TestPort_SetConstraintsDeferral(t *testing.T) {
	c := newCluster(t, "setconstraints")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c, "CREATE TABLE parent (id integer PRIMARY KEY)"); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE child (id integer PRIMARY KEY, pid integer "+
		"REFERENCES parent(id) DEFERRABLE INITIALLY IMMEDIATE)"); err != nil {
		t.Fatalf("create child: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, conn := scConn(t, c, ctx)
	defer db.Close()
	defer conn.Close()

	ex := func(q string) error {
		_, err := conn.ExecContext(ctx, q)
		return err
	}
	isFKErr := func(err error) bool {
		return err != nil && strings.Contains(strings.ToLower(err.Error()), "foreign key")
	}

	// Control: no SET CONSTRAINTS — INITIALLY IMMEDIATE fails at the INSERT.
	if err := ex("BEGIN"); err != nil {
		t.Fatalf("begin control: %v", err)
	}
	if err := ex("INSERT INTO child VALUES (1, 100)"); !isFKErr(err) {
		t.Fatalf("control insert: expected immediate FK violation, got %v", err)
	}
	_ = ex("ROLLBACK")

	// SET CONSTRAINTS ALL DEFERRED defers the check: child before parent works.
	if err := ex("BEGIN"); err != nil {
		t.Fatalf("begin deferred: %v", err)
	}
	if err := ex("SET CONSTRAINTS ALL DEFERRED"); err != nil {
		t.Fatalf("set constraints all deferred: %v", err)
	}
	if err := ex("INSERT INTO child VALUES (2, 200)"); err != nil {
		t.Fatalf("deferred child insert (parent absent): unexpected error %v", err)
	}
	if err := ex("INSERT INTO parent VALUES (200)"); err != nil {
		t.Fatalf("parent insert: %v", err)
	}
	if err := ex("COMMIT"); err != nil {
		t.Fatalf("deferred ordered commit: unexpected error %v", err)
	}
	rows := runSQL(t, c, "SELECT id, pid FROM child WHERE id = 2")
	if len(rows) != 1 || rows[0][0] != "2" || rows[0][1] != "200" {
		t.Fatalf("deferred row not committed: %v", rows)
	}

	// A still-missing parent at COMMIT raises 23503 at COMMIT, not at the INSERT.
	if err := ex("BEGIN"); err != nil {
		t.Fatalf("begin unsatisfied: %v", err)
	}
	if err := ex("SET CONSTRAINTS ALL DEFERRED"); err != nil {
		t.Fatalf("set deferred: %v", err)
	}
	if err := ex("INSERT INTO child VALUES (3, 999)"); err != nil {
		t.Fatalf("deferred insert should not fail at INSERT: %v", err)
	}
	if err := ex("COMMIT"); !isFKErr(err) {
		t.Fatalf("deferred unsatisfied: expected FK violation at COMMIT, got %v", err)
	}
	_ = ex("ROLLBACK")

	// SET CONSTRAINTS ALL IMMEDIATE forces the queued check to run at the SET.
	if err := ex("BEGIN"); err != nil {
		t.Fatalf("begin immediate: %v", err)
	}
	if err := ex("SET CONSTRAINTS ALL DEFERRED"); err != nil {
		t.Fatalf("set deferred: %v", err)
	}
	if err := ex("INSERT INTO child VALUES (4, 888)"); err != nil {
		t.Fatalf("deferred insert: %v", err)
	}
	if err := ex("SET CONSTRAINTS ALL IMMEDIATE"); !isFKErr(err) {
		t.Fatalf("set immediate: expected FK violation at SET IMMEDIATE, got %v", err)
	}
	_ = ex("ROLLBACK")
}
