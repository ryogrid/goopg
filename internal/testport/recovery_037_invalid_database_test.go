package testport

import (
	"regexp"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Recovery037InvalidDatabase ports
// postgres/src/test/recovery/t/037_invalid_database.pl (M0149-0005): a
// database marked invalid (datconnlimit = -2), as an interrupted DROP
// DATABASE leaves it, refuses connections, ALTER and use as a template, is
// ignored by vac_truncate_clog, and can still be dropped; and a DROP
// DATABASE cancelled while it waits for pg_tablespace leaves the database
// invalid.
func TestPort_Recovery037InvalidDatabase(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping recovery TAP port in short mode")
	}
	repo := repoRoot(t)
	tapnode.Available(t, repo)

	node := tapnode.NewGoopg(t, repo, "node")
	node.AppendConf("autovacuum = off")
	node.AppendConf("max_prepared_transactions=5")
	node.AppendConf("log_min_duration_statement=0")
	node.AppendConf("log_connections=receipt")
	node.AppendConf("log_disconnections=on")
	node.Start()

	is := func(got, want int, name string) {
		t.Helper()
		if got != want {
			t.Errorf("not ok: %s: got %d, want %d", name, got, want)
		}
	}

	// First verify that we can't connect to or ALTER an invalid database.
	// Just mark the database as invalid ourselves, that's more reliable than
	// hoping that the database creation at the right time.
	node.SafePSQL("postgres", `
CREATE DATABASE regression_invalid;
UPDATE pg_database SET datconnlimit = -2 WHERE datname = 'regression_invalid';
`)

	_, stderr, exit := node.PSQLExit("regression_invalid", "")
	is(exit, 2, "can't connect to invalid database - error code")
	if !regexp.MustCompile(`FATAL:\s+cannot connect to invalid database "regression_invalid"`).MatchString(stderr) {
		t.Errorf("not ok: can't connect to invalid database - error message: %q", stderr)
	}

	_, _, exit = node.PSQLExit("postgres", "ALTER DATABASE regression_invalid CONNECTION LIMIT 10")
	is(exit, 2, "can't ALTER invalid database")

	// check invalid database can't be used as a template
	_, _, exit = node.PSQLExit("postgres", "CREATE DATABASE copy_invalid TEMPLATE regression_invalid")
	is(exit, 3, "can't use invalid database as template")

	// Verify that VACUUM ignores an invalid database when computing how much
	// of the clog is needed (vac_truncate_clog()). For that we modify the
	// pg_database row of the invalid database to have an outdated
	// datfrozenxid.
	_, stderr, _ = node.PSQLExit("postgres", `
UPDATE pg_database SET datfrozenxid = '123456' WHERE datname = 'regression_invalid';
DROP TABLE IF EXISTS foo_tbl; CREATE TABLE foo_tbl();
VACUUM FREEZE;`)
	if regexp.MustCompile(`some databases have not been vacuumed in over 2 billion transactions`).MatchString(stderr) {
		t.Errorf("not ok: invalid databases are ignored by vac_truncate_clog: %q", stderr)
	}

	// Check that a DROP DATABASE of an invalid database works.
	_, _, exit = node.PSQLExit("postgres", "DROP DATABASE regression_invalid")
	is(exit, 0, "can DROP invalid database")

	// Ensure database is gone
	_, _, exit = node.PSQLExit("postgres", "DROP DATABASE regression_invalid")
	is(exit, 3, "can't drop already dropped database")

	// Test that interruption of DROP DATABASE is handled properly. To ensure
	// the interruption happens at the appropriate moment, we lock
	// pg_tablespace. DROP DATABASE scans pg_tablespace once it has reached
	// the "irreversible" part of dropping the database, making it a suitable
	// point to wait.
	cancel := node.BackgroundPSQL("postgres", true)
	bgpsql := node.BackgroundPSQL("postgres", false)

	pid := bgpsql.Query("SELECT pg_backend_pid()")

	// create the database, prevent drop database via lock held by a 2PC
	// transaction
	bgpsql.QuerySafe(`
  CREATE DATABASE regression_invalid_interrupt;
  BEGIN;
  LOCK pg_tablespace;
  PREPARE TRANSACTION 'lock_tblspc';`)

	// Try to drop. This will wait due to the still held lock.
	bgpsql.Send("DROP DATABASE regression_invalid_interrupt;\n")

	// Once the DROP DATABASE is waiting for the lock, interrupt it.
	cancel.QuerySafe(`
	DO $$
	BEGIN
		WHILE NOT EXISTS(SELECT * FROM pg_locks WHERE NOT granted AND relation = 'pg_tablespace'::regclass AND mode = 'AccessShareLock') LOOP
			PERFORM pg_sleep(.1);
		END LOOP;
	END$$;
	SELECT pg_cancel_backend(` + pid + `);`)
	t.Log("ok: canceling DROP DATABASE")
	_ = cancel.Quit()

	// wait for cancellation to be processed
	if !bgpsql.WaitStderr(regexp.MustCompile(`canceling statement due to user request`), 3*time.Minute) {
		t.Errorf("not ok: cancel processed: stderr %q", bgpsql.Stderr())
	}
	bgpsql.ClearStderr()

	// verify that connection to the database aren't allowed
	_, _, exit = node.PSQLExit("regression_invalid_interrupt", "")
	is(exit, 2, "can't connect to invalid_interrupt database")

	// To properly drop the database, we need to release the lock previously
	// preventing doing so.
	bgpsql.QuerySafe(`ROLLBACK PREPARED 'lock_tblspc'`)
	bgpsql.QuerySafe(`DROP DATABASE regression_invalid_interrupt`)
	_ = bgpsql.Quit()
}
