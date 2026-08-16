package postmaster

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestCrossPartitionUpdate_EPQReevaluatesSetAfterConcurrentInPlace pins
// M0100-0005z: when a partition-key UPDATE waits on a concurrent in-place
// (same-partition) UPDATE that changes a non-key column, after the
// conflicting xact commits the EPQ retry must (a) locate the new tuple
// version through the cross-page t_ctid chain (the in-place UPDATE was
// non-HOT in goopg), (b) re-evaluate the WHERE predicate against the
// refetched row, and (c) re-evaluate the SET expressions against the
// refetched row before routing to the destination partition.
//
// Mirrors permutation 1 of partition-key-update-4.spec
// (s1b s2b s2u1 s1u s2c s1c s1s): expected final row is
// (foo2, 2, 'ABC update2 update1') — proving s2's b update was carried
// into the moved row's b value.
func TestCrossPartitionUpdate_EPQReevaluatesSetAfterConcurrentInPlace(t *testing.T) {
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
	for _, q := range []string{
		`DROP TABLE IF EXISTS xpu_foo`,
		`CREATE TABLE xpu_foo (a int, b text) PARTITION BY LIST(a)`,
		`CREATE TABLE xpu_foo1 PARTITION OF xpu_foo FOR VALUES IN (1)`,
		`CREATE TABLE xpu_foo2 PARTITION OF xpu_foo FOR VALUES IN (2)`,
		`INSERT INTO xpu_foo VALUES (1, 'ABC')`,
	} {
		if _, err := setup.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	_ = setup.Close()

	s1, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s1.Close()
	s2, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	if _, err := s1.ExecContext(ctx, `BEGIN ISOLATION LEVEL READ COMMITTED`); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.ExecContext(ctx, `BEGIN ISOLATION LEVEL READ COMMITTED`); err != nil {
		t.Fatal(err)
	}

	// s2 in-place UPDATE: b becomes 'ABC update2', row stays in xpu_foo1.
	// This is a non-HOT update in goopg; the new tuple lands at a new slot
	// (potentially on a new page).
	if _, err := s2.ExecContext(ctx, `UPDATE xpu_foo SET b = b || ' update2' WHERE a = 1`); err != nil {
		t.Fatal(err)
	}

	// s1 cross-partition UPDATE: a += 1 routes the row from xpu_foo1 to
	// xpu_foo2; b appends ' update1'. The original (1, 'ABC') matches the
	// WHERE; the row's xmax is in-flight (s2), so s1 blocks. Run it on a
	// goroutine so we can commit s2 first.
	type s1Result struct {
		err error
	}
	s1Done := make(chan s1Result, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, e := s1.ExecContext(ctx, `UPDATE xpu_foo SET a = a + 1, b = b || ' update1' WHERE b like '%ABC%'`)
		s1Done <- s1Result{err: e}
	}()

	// Confirm s1 is genuinely waiting before s2 commits.
	select {
	case r := <-s1Done:
		t.Fatalf("s1 UPDATE returned before s2 committed: err=%v", r.err)
	case <-time.After(250 * time.Millisecond):
	}

	if _, err := s2.ExecContext(ctx, `COMMIT`); err != nil {
		t.Fatal(err)
	}

	// s1 must now wake, EPQ-refetch (1, 'ABC update2'), re-evaluate WHERE
	// (matches), re-evaluate SET (a=2, b='ABC update2 update1'), route to
	// xpu_foo2, and write the new row there.
	select {
	case r := <-s1Done:
		if r.err != nil {
			t.Fatalf("s1 UPDATE failed after EPQ retry: %v", r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("s1 UPDATE did not complete within 5s of s2 commit")
	}
	wg.Wait()

	if _, err := s1.ExecContext(ctx, `COMMIT`); err != nil {
		t.Fatal(err)
	}

	// Verify final state on a fresh connection (no session caches).
	verify, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer verify.Close()

	var got string
	row := verify.QueryRowContext(ctx, `SELECT tableoid::regclass::text || ' ' || a::text || ' ' || b FROM xpu_foo`)
	if err := row.Scan(&got); err != nil {
		t.Fatalf("final SELECT: %v", err)
	}
	want := "xpu_foo2 2 ABC update2 update1"
	if got != want {
		t.Errorf("final row = %q, want %q (EPQ failed to re-evaluate SET against refetched tuple)", got, want)
	}

	var rowCount int
	if err := verify.QueryRowContext(ctx, `SELECT count(*) FROM xpu_foo`).Scan(&rowCount); err != nil {
		t.Fatal(err)
	}
	if rowCount != 1 {
		t.Errorf("row count = %d, want 1 (EPQ chain follow corrupted tuple visibility)", rowCount)
	}
}
