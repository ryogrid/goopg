package testport

// M0104-0008 — multi-session SQL-driven SSI write-skew tests.
//
// These tests exercise the M0104-0007 executor wiring end-to-end via two
// real psql-style sessions against a live goopg cluster: predicate-lock
// acquisition on read paths, rw-conflict edge installation on write
// paths, and the pre-commit dangerous-structure check.  They are the
// pass-required evidence for milestone DoD #4 (Applicable deferred
// isolation tests for SERIALIZABLE/SSI are promoted and passing) that
// replaces the spec-runner promotion path — the IsolationRunner does not
// yet auto-generate permutations from session steps the way upstream
// `isolationtester.c` does, so deferred upstream spec files cannot be
// driven through `TestPort_IsolationSuite`; instead these focused Go
// tests directly enact the canonical SSI write-skew permutations.
//
// Source spec: postgres/src/test/isolation/specs/simple-write-skew.spec.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/lib/pq"
)

// openSSICluster spins up a dedicated goopg cluster for a single SSI test
// case so test parallelism is preserved and one tx's serialization failure
// cannot leak into a sibling case via shared catalog state.
func openSSICluster(t *testing.T, name string) (*sql.DB, func()) {
	t.Helper()
	c := newCluster(t, name)
	mustInitStart(t, c)

	dsn := buildDSN(t, c)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		_ = c.Stop(cluster.ShutdownImmediate)
		t.Fatalf("sql.Open: %v", err)
	}
	// Two long-lived sessions need at least two backends.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)

	cleanup := func() {
		_ = db.Close()
		_ = c.Stop(cluster.ShutdownImmediate)
	}
	return db, cleanup
}

// ssiSetupTable seeds the write-skew dataset (matching the upstream
// simple-write-skew spec).
func ssiSetupTable(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// CREATE on a fresh cluster — drop is unnecessary but harmless.
	for _, stmt := range []string{
		"DROP TABLE IF EXISTS ssi_ws",
		"CREATE TABLE ssi_ws (i int PRIMARY KEY, t text)",
		"INSERT INTO ssi_ws VALUES (5, 'apple'), (7, 'pear'), (11, 'banana')",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}
}

// openSerializableSession opens a fresh backend conn and starts a
// SERIALIZABLE transaction on it.  The returned conn must be closed
// after the tx is finished.
func openSerializableSession(t *testing.T, db *sql.DB, label string) *sql.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("%s: db.Conn: %v", label, err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN ISOLATION LEVEL SERIALIZABLE"); err != nil {
		_ = conn.Close()
		t.Fatalf("%s: BEGIN SERIALIZABLE: %v", label, err)
	}
	return conn
}

// stepExec runs a single SQL on a session and reports the resulting error
// (nil on success).  The query is given a short timeout so a deadlocked
// case can still terminate.
func stepExec(t *testing.T, conn *sql.Conn, sqlText string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := conn.ExecContext(ctx, sqlText)
	return err
}

// expectSerializationFailure asserts err is SQLSTATE 40001 with the
// upstream wording prefix.  Returns true if the assertion holds.
func expectSerializationFailure(t *testing.T, err error, where string) bool {
	t.Helper()
	if err == nil {
		t.Errorf("%s: expected SQLSTATE 40001 serialization failure, got nil", where)
		return false
	}
	var pqErr *pq.Error
	if !errors.As(err, &pqErr) {
		t.Errorf("%s: expected *pq.Error, got %T: %v", where, err, err)
		return false
	}
	if pqErr.Code != "40001" {
		t.Errorf("%s: expected SQLSTATE 40001, got %s: %s", where, pqErr.Code, pqErr.Message)
		return false
	}
	if !strings.Contains(pqErr.Message, "could not serialize access due to read/write dependencies among transactions") {
		t.Errorf("%s: expected upstream wording, got %q", where, pqErr.Message)
		return false
	}
	return true
}

// TestPort_SSI_WriteSkew_NoOverlap_BothCommit verifies the
// non-overlapping permutation (rwx1 c1 rwx2 c2) of simple-write-skew:
// when the second writer starts AFTER the first writer commits, no
// rw-conflict edge can form, so both must succeed.  This is the
// "false-positive guard" half of M0104-0007 — the executor wiring must
// not eagerly abort a serially-equivalent schedule.
func TestPort_SSI_WriteSkew_NoOverlap_BothCommit(t *testing.T) {
	db, cleanup := openSSICluster(t, "ssi_ws_no_overlap")
	defer cleanup()
	ssiSetupTable(t, db)

	s1 := openSerializableSession(t, db, "s1")
	if err := stepExec(t, s1, "UPDATE ssi_ws SET t = 'apple' WHERE t = 'pear'"); err != nil {
		t.Fatalf("rwx1: %v", err)
	}
	if err := stepExec(t, s1, "COMMIT"); err != nil {
		t.Fatalf("c1: %v", err)
	}
	_ = s1.Close()

	s2 := openSerializableSession(t, db, "s2")
	if err := stepExec(t, s2, "UPDATE ssi_ws SET t = 'pear' WHERE t = 'apple'"); err != nil {
		t.Fatalf("rwx2: %v", err)
	}
	if err := stepExec(t, s2, "COMMIT"); err != nil {
		t.Fatalf("c2: %v", err)
	}
	_ = s2.Close()
}

// TestPort_SSI_WriteSkew_Overlap_SecondCommitterAborts verifies the
// overlapping permutation (rwx1 rwx2 c1 c2): both UPDATEs run inside
// concurrent SERIALIZABLE snapshots, c1 commits successfully, c2 must
// abort with SQLSTATE 40001.  This is the canonical write-skew anomaly
// described in the simple-write-skew spec header.
//
// Edge structure that the executor wiring is expected to install:
//   - s1's seqScan over ssi_ws records SIREAD predicate locks on all 3
//     tuples (via ssiRecordTupleRead in seqScanOp.Next).
//   - s1's UPDATE of the 'pear' row records a tuple write (via
//     ssiRecordTupleWrite in updateOp.Next).
//   - s2's seqScan records SIREAD predicate locks; its read of the
//     'pear' tuple sees s1's xmin and registers an OUT-edge.
//   - s2's UPDATE of the 'apple' rows lands on tuples s1 read,
//     producing an IN-edge.
//   - The R→W and W→R edges form a dangerous structure; the second
//     committer (s2) is flagged doomed and PreCommit_Check aborts it.
func TestPort_SSI_WriteSkew_Overlap_SecondCommitterAborts(t *testing.T) {
	db, cleanup := openSSICluster(t, "ssi_ws_overlap_s2")
	defer cleanup()
	ssiSetupTable(t, db)

	s1 := openSerializableSession(t, db, "s1")
	defer func() { _ = s1.Close() }()
	s2 := openSerializableSession(t, db, "s2")
	defer func() { _ = s2.Close() }()

	if err := stepExec(t, s1, "UPDATE ssi_ws SET t = 'apple' WHERE t = 'pear'"); err != nil {
		t.Fatalf("rwx1: %v", err)
	}
	if err := stepExec(t, s2, "UPDATE ssi_ws SET t = 'pear' WHERE t = 'apple'"); err != nil {
		t.Fatalf("rwx2: %v", err)
	}
	if err := stepExec(t, s1, "COMMIT"); err != nil {
		t.Fatalf("c1: %v", err)
	}
	commitErr := stepExec(t, s2, "COMMIT")
	expectSerializationFailure(t, commitErr, "c2")
}

// TestPort_SSI_WriteSkew_Overlap_FirstCommitterAborts mirrors the
// above but reverses commit order (rwx1 rwx2 c2 c1).  In upstream's
// simple-write-skew expected output the FIRST committer is the one
// flagged doomed when its peer commits first — this exercises the
// "doom by peer-commit" leg of the pre-commit check.
func TestPort_SSI_WriteSkew_Overlap_FirstCommitterAborts(t *testing.T) {
	db, cleanup := openSSICluster(t, "ssi_ws_overlap_s1")
	defer cleanup()
	ssiSetupTable(t, db)

	s1 := openSerializableSession(t, db, "s1")
	defer func() { _ = s1.Close() }()
	s2 := openSerializableSession(t, db, "s2")
	defer func() { _ = s2.Close() }()

	if err := stepExec(t, s1, "UPDATE ssi_ws SET t = 'apple' WHERE t = 'pear'"); err != nil {
		t.Fatalf("rwx1: %v", err)
	}
	if err := stepExec(t, s2, "UPDATE ssi_ws SET t = 'pear' WHERE t = 'apple'"); err != nil {
		t.Fatalf("rwx2: %v", err)
	}
	if err := stepExec(t, s2, "COMMIT"); err != nil {
		t.Fatalf("c2: %v", err)
	}
	commitErr := stepExec(t, s1, "COMMIT")
	expectSerializationFailure(t, commitErr, "c1")
}

// TestPort_SSI_WriteSkew_RC_NoSerializationFailure is the
// REPEATABLE-READ / READ-COMMITTED control: with non-SERIALIZABLE
// isolation the same overlapping permutation must NOT trip a
// 40001 — write-skew is permitted under those levels and the executor
// SSI helpers must short-circuit.  This guards against an accidental
// regression where the helpers fire outside of SERIALIZABLE.
func TestPort_SSI_WriteSkew_RC_NoSerializationFailure(t *testing.T) {
	db, cleanup := openSSICluster(t, "ssi_ws_rc_control")
	defer cleanup()
	ssiSetupTable(t, db)

	begin := func(t *testing.T, conn *sql.Conn, level string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		stmt := fmt.Sprintf("BEGIN ISOLATION LEVEL %s", level)
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("BEGIN %s: %v", level, err)
		}
	}

	for _, level := range []string{"READ COMMITTED", "REPEATABLE READ"} {
		t.Run(strings.ReplaceAll(level, " ", "_"), func(t *testing.T) {
			ssiSetupTable(t, db) // re-seed after each level

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			s1, err := db.Conn(ctx)
			if err != nil {
				t.Fatalf("s1 conn: %v", err)
			}
			defer s1.Close()
			s2, err := db.Conn(ctx)
			if err != nil {
				t.Fatalf("s2 conn: %v", err)
			}
			defer s2.Close()

			begin(t, s1, level)
			begin(t, s2, level)

			if err := stepExec(t, s1, "UPDATE ssi_ws SET t = 'apple' WHERE t = 'pear'"); err != nil {
				t.Fatalf("rwx1: %v", err)
			}
			if err := stepExec(t, s2, "UPDATE ssi_ws SET t = 'pear' WHERE t = 'apple'"); err != nil {
				t.Fatalf("rwx2: %v", err)
			}
			if err := stepExec(t, s1, "COMMIT"); err != nil {
				t.Fatalf("c1: %v", err)
			}
			if err := stepExec(t, s2, "COMMIT"); err != nil {
				t.Errorf("%s c2: expected no serialization failure, got: %v", level, err)
			}
		})
	}
}
