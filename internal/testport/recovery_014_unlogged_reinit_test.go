package testport

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Recovery014UnloggedReinit ports
// postgres/src/test/recovery/t/014_unlogged_reinit.pl (M0149-0003): after a
// crash, unlogged tables and sequences are reinitialised from their init
// forks (ResetUnloggedRelations, reinit.c) — in the default tablespace and in
// a user tablespace — while their other forks are removed.
//
// Variants (M0149 contract C3, single-node): after the crash and the file
// manipulations, a PostgreSQL 18.3 server cold-starts on a copy of the goopg
// directory and must pass the post-restart assertions; then the goopg node
// itself restarts, on the crashed tablespace state restored, and passes them.
func TestPort_Recovery014UnloggedReinit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping recovery TAP port in short mode")
	}
	repo := repoRoot(t)
	tapnode.Available(t, repo)

	node := tapnode.NewGoopg(t, repo, "main")
	node.Start()
	pgdata := node.DataDir()

	fileExists := func(path string) bool {
		st, err := os.Stat(path)
		return err == nil && st.Mode().IsRegular()
	}
	okf := func(t *testing.T, cond bool, name string) {
		t.Helper()
		if !cond {
			t.Errorf("not ok: %s", name)
		}
	}
	is := func(t *testing.T, got, want, name string) {
		t.Helper()
		if got != want {
			t.Errorf("not ok: %s: got %q, want %q", name, got, want)
		}
	}

	// Create an unlogged table and an unlogged sequence to test that forks
	// other than init are not copied.
	node.SafePSQL("postgres", "CREATE UNLOGGED TABLE base_unlogged (id int)")
	node.SafePSQL("postgres", "CREATE UNLOGGED SEQUENCE seq_unlogged")

	baseUnloggedPath := node.SafePSQL("postgres", "select pg_relation_filepath('base_unlogged')")
	seqUnloggedPath := node.SafePSQL("postgres", "select pg_relation_filepath('seq_unlogged')")

	// Test that main and init forks exist.
	okf(t, fileExists(filepath.Join(pgdata, baseUnloggedPath+"_init")), "table init fork exists")
	okf(t, fileExists(filepath.Join(pgdata, baseUnloggedPath)), "table main fork exists")
	okf(t, fileExists(filepath.Join(pgdata, seqUnloggedPath+"_init")), "sequence init fork exists")
	okf(t, fileExists(filepath.Join(pgdata, seqUnloggedPath)), "sequence main fork exists")

	// Test the sequence.
	is(t, node.SafePSQL("postgres", "SELECT nextval('seq_unlogged')"), "1", "sequence nextval")
	is(t, node.SafePSQL("postgres", "SELECT nextval('seq_unlogged')"), "2", "sequence nextval again")

	// Create an unlogged table in a tablespace.
	tablespaceDir := t.TempDir()
	node.SafePSQL("postgres", "CREATE TABLESPACE ts1 LOCATION '"+tablespaceDir+"'")
	node.SafePSQL("postgres", "CREATE UNLOGGED TABLE ts1_unlogged (id int) TABLESPACE ts1")

	ts1UnloggedPath := node.SafePSQL("postgres", "select pg_relation_filepath('ts1_unlogged')")

	// Test that main and init forks exist.
	okf(t, fileExists(filepath.Join(pgdata, ts1UnloggedPath+"_init")), "init fork in tablespace exists")
	okf(t, fileExists(filepath.Join(pgdata, ts1UnloggedPath)), "main fork in tablespace exists")

	// Create more unlogged sequences for testing.
	node.SafePSQL("postgres", "CREATE UNLOGGED SEQUENCE seq_unlogged2")
	// This rewrites the sequence relation in AlterSequence().
	node.SafePSQL("postgres", "ALTER SEQUENCE seq_unlogged2 INCREMENT 2")
	node.SafePSQL("postgres", "SELECT nextval('seq_unlogged2')")

	node.SafePSQL("postgres", "CREATE UNLOGGED TABLE tab_seq_unlogged3 (a int GENERATED ALWAYS AS IDENTITY)")
	// This rewrites the sequence relation in ResetSequence().
	node.SafePSQL("postgres", "TRUNCATE tab_seq_unlogged3 RESTART IDENTITY")
	node.SafePSQL("postgres", "INSERT INTO tab_seq_unlogged3 DEFAULT VALUES")

	// Crash the postmaster.
	node.Stop(tapnode.StopImmediate)

	appendToFile := func(path, s string) {
		t.Helper()
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatalf("append_to_file %s: %v", path, err)
		}
		if _, err := f.WriteString(s); err != nil {
			t.Fatalf("append_to_file %s: %v", path, err)
		}
		if err := f.Close(); err != nil {
			t.Fatalf("append_to_file %s: %v", path, err)
		}
	}
	unlink := func(path string) {
		t.Helper()
		if err := os.Remove(path); err != nil {
			t.Fatalf("BAIL_OUT: could not remove %q: %v", path, err)
		}
	}

	// Write fake forks to test that they are removed during recovery.
	appendToFile(filepath.Join(pgdata, baseUnloggedPath+"_vm"), "TEST_VM")
	appendToFile(filepath.Join(pgdata, baseUnloggedPath+"_fsm"), "TEST_FSM")

	// Remove main fork to test that it is recopied from init.
	unlink(filepath.Join(pgdata, baseUnloggedPath))
	unlink(filepath.Join(pgdata, seqUnloggedPath))

	// the same for the tablespace
	appendToFile(filepath.Join(pgdata, ts1UnloggedPath+"_vm"), "TEST_VM")
	appendToFile(filepath.Join(pgdata, ts1UnloggedPath+"_fsm"), "TEST_FSM")
	unlink(filepath.Join(pgdata, ts1UnloggedPath))

	after := func(t *testing.T, n *tapnode.Node) {
		t.Helper()
		dir := n.DataDir()
		// check unlogged table in base
		okf(t, fileExists(filepath.Join(dir, baseUnloggedPath+"_init")), "table init fork in base still exists")
		okf(t, fileExists(filepath.Join(dir, baseUnloggedPath)), "table main fork in base recreated at startup")
		okf(t, !fileExists(filepath.Join(dir, baseUnloggedPath+"_vm")), "vm fork in base removed at startup")
		okf(t, !fileExists(filepath.Join(dir, baseUnloggedPath+"_fsm")), "fsm fork in base removed at startup")

		// check unlogged sequence
		okf(t, fileExists(filepath.Join(dir, seqUnloggedPath+"_init")), "sequence init fork still exists")
		okf(t, fileExists(filepath.Join(dir, seqUnloggedPath)), "sequence main fork recreated at startup")

		// Test the sequence after restart
		is(t, n.SafePSQL("postgres", "SELECT nextval('seq_unlogged')"), "1", "sequence nextval after restart")
		is(t, n.SafePSQL("postgres", "SELECT nextval('seq_unlogged')"), "2", "sequence nextval after restart again")

		// check unlogged table in tablespace
		okf(t, fileExists(filepath.Join(dir, ts1UnloggedPath+"_init")), "init fork still exists in tablespace")
		okf(t, fileExists(filepath.Join(dir, ts1UnloggedPath)), "main fork in tablespace recreated at startup")
		okf(t, !fileExists(filepath.Join(dir, ts1UnloggedPath+"_vm")), "vm fork in tablespace removed at startup")
		okf(t, !fileExists(filepath.Join(dir, ts1UnloggedPath+"_fsm")), "fsm fork in tablespace removed at startup")

		// Test other sequences
		is(t, n.SafePSQL("postgres", "SELECT nextval('seq_unlogged2')"), "1", "altered sequence nextval after restart")
		is(t, n.SafePSQL("postgres", "SELECT nextval('seq_unlogged2')"), "3", "altered sequence nextval after restart again")

		n.SafePSQL("postgres", "INSERT INTO tab_seq_unlogged3 VALUES (DEFAULT), (DEFAULT)")
		is(t, n.SafePSQL("postgres", "SELECT * FROM tab_seq_unlogged3"), "1\n2", "reset sequence nextval after restart")
	}

	// The PG copy shares the external tablespace (its redo of
	// XLOG_TBLSPC_CREATE points the link at the logged location), so the
	// tablespace's crashed state is restored before goopg recovers it.
	restoreTablespace := tapnode.SnapshotDir(t, tablespaceDir)
	t.Run("pg18-cold-start", func(t *testing.T) {
		pg := tapnode.ColdStartPG(t, node, "pg-on-goopg-copy")
		after(t, pg)
		pg.Stop(tapnode.StopFast)
	})
	restoreTablespace()
	t.Run("all-goopg", func(t *testing.T) {
		node.Start()
		after(t, node)
	})
}
