package postmaster

// M0134-0057: PREPARE TRANSACTION must reject a gid that is already in use by
// ANOTHER live prepared transaction, regardless of which isolation-level path
// prepared it first (SERIALIZABLE same-backend keep-open vs RC/RR
// detach-to-dedicated-slot). Mirrors regress case prepared_xacts.sql's bucket
// 3 (postgres/src/test/regress/sql/prepared_xacts.sql:47-63).
//
// PG oracle: postgres/src/backend/access/transam/twophase.c:MarkAsPreparing —
// checks the shared TwoPhaseState GID hash table unconditionally regardless of
// isolation level, raising ERRCODE_DUPLICATE_OBJECT (42710) before any
// transaction-specific work.

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "github.com/lib/pq"
)

// TestPrepareTransactionDuplicateGidSerializable reproduces the regress
// fragment: a SERIALIZABLE PREPARE TRANSACTION 'g' kept open on its
// originating connection must block a SECOND SERIALIZABLE session from
// preparing the same gid.
func TestPrepareTransactionDuplicateGidSerializable(t *testing.T) {
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

	setup, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer setup.Close()
	if _, err := setup.ExecContext(ctx, "CREATE TABLE pxdup_test (i INTEGER)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		setup.ExecContext(ctx, "DROP TABLE IF EXISTS pxdup_test")
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

	if _, err := c1.ExecContext(ctx, "BEGIN TRANSACTION ISOLATION LEVEL SERIALIZABLE"); err != nil {
		t.Fatal(err)
	}
	if _, err := c1.ExecContext(ctx, "INSERT INTO pxdup_test VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := c1.ExecContext(ctx, "PREPARE TRANSACTION 'regress_foo3'"); err != nil {
		t.Fatalf("first PREPARE TRANSACTION: %v", err)
	}
	t.Cleanup(func() {
		// Best-effort cleanup so the gid doesn't leak into other subtests.
		db.ExecContext(ctx, "COMMIT PREPARED 'regress_foo3'")
	})

	if _, err := c2.ExecContext(ctx, "BEGIN TRANSACTION ISOLATION LEVEL SERIALIZABLE"); err != nil {
		t.Fatal(err)
	}
	if _, err := c2.ExecContext(ctx, "INSERT INTO pxdup_test VALUES (2)"); err != nil {
		t.Fatal(err)
	}
	_, err = c2.ExecContext(ctx, "PREPARE TRANSACTION 'regress_foo3'")
	if err == nil {
		t.Fatalf("second PREPARE TRANSACTION with duplicate gid should have failed")
	}
	if !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("unexpected error: %v", err)
	}
}
