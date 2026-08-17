package postmaster

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "github.com/lib/pq"
)

// TestSerializableFirstStatementSnapshot verifies that a SERIALIZABLE
// transaction captures its snapshot at the FIRST real statement after BEGIN,
// not at BEGIN time. Mirrors read-write-unique.spec perm 2 (r1 w1 c1 r2 w2 c2):
// s2's r2 must see s1's row committed by c1.
func TestSerializableFirstStatementSnapshot(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()

	colonIdx := strings.LastIndex(addr, ":")
	if colonIdx < 0 {
		t.Fatalf("addr: %s", addr)
	}
	host, port := addr[:colonIdx], addr[colonIdx+1:]
	dsn := "host=" + host + " port=" + port + " user=postgres dbname=postgres sslmode=disable"

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	// Setup
	setup, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer setup.Close()
	if _, err := setup.ExecContext(ctx, "CREATE TABLE sss_test (i INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		setup.ExecContext(ctx, "DROP TABLE IF EXISTS sss_test")
	})

	c1, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c1.Close()
	c2, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()

	// s1 and s2 both begin SERIALIZABLE (snapshot NOT yet captured)
	if _, err := c1.ExecContext(ctx, "BEGIN ISOLATION LEVEL SERIALIZABLE"); err != nil {
		t.Fatal(err)
	}
	if _, err := c2.ExecContext(ctx, "BEGIN ISOLATION LEVEL SERIALIZABLE"); err != nil {
		t.Fatal(err)
	}

	// r1: s1 reads empty table (s1's firstSnapshot captured here)
	r1, err := c1.QueryContext(ctx, "SELECT * FROM sss_test")
	if err != nil {
		t.Fatal(err)
	}
	r1.Close()

	// w1: s1 inserts (42)
	if _, err := c1.ExecContext(ctx, "INSERT INTO sss_test VALUES (42)"); err != nil {
		t.Fatal(err)
	}

	// c1: s1 commits
	if _, err := c1.ExecContext(ctx, "COMMIT"); err != nil {
		t.Fatal(err)
	}

	// r2: s2 SELECT (firstSnapshot captured NOW, after c1 → should see 42)
	rows, err := c2.QueryContext(ctx, "SELECT i FROM sss_test")
	if err != nil {
		t.Fatal(err)
	}
	var count, val int
	for rows.Next() {
		if err := rows.Scan(&val); err != nil {
			t.Fatal(err)
		}
		count++
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	c2.ExecContext(ctx, "ROLLBACK")

	if count != 1 || val != 42 {
		t.Errorf("r2 (SERIALIZABLE after c1): got %d rows, val=%d; want 1 row, val=42 (firstSnapshot must be taken at first-stmt)", count, val)
	}
}

// TestRepeatableReadFirstStatementSnapshot is the RR version of the above.
func TestRepeatableReadFirstStatementSnapshot(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	colonIdx := strings.LastIndex(addr, ":")
	if colonIdx < 0 { t.Fatalf("addr: %s", addr) }
	host, port := addr[:colonIdx], addr[colonIdx+1:]
	dsn := "host=" + host + " port=" + port + " user=postgres dbname=postgres sslmode=disable"
	db, err := sql.Open("postgres", dsn)
	if err != nil { t.Fatal(err) }
	defer db.Close()
	ctx := context.Background()

	setup, _ := db.Conn(ctx)
	defer setup.Close()
	setup.ExecContext(ctx, "CREATE TABLE sss_rr_test (i INTEGER PRIMARY KEY)")
	t.Cleanup(func() { setup.ExecContext(ctx, "DROP TABLE IF EXISTS sss_rr_test") })

	c1, _ := db.Conn(ctx)
	defer c1.Close()
	c2, _ := db.Conn(ctx)
	defer c2.Close()

	c1.ExecContext(ctx, "BEGIN ISOLATION LEVEL REPEATABLE READ")
	c2.ExecContext(ctx, "BEGIN ISOLATION LEVEL REPEATABLE READ")

	r1, _ := c1.QueryContext(ctx, "SELECT * FROM sss_rr_test")
	r1.Close()
	c1.ExecContext(ctx, "INSERT INTO sss_rr_test VALUES (42)")
	c1.ExecContext(ctx, "COMMIT")

	rows, _ := c2.QueryContext(ctx, "SELECT i FROM sss_rr_test")
	var count, val int
	for rows.Next() { rows.Scan(&val); count++ }
	rows.Close()
	c2.ExecContext(ctx, "ROLLBACK")
	if count != 1 || val != 42 {
		t.Errorf("r2 (RR after c1): got %d rows val=%d, want 1 row val=42", count, val)
	}
}
