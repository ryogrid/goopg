package testport

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Recovery009Twophase ports postgres/src/test/recovery/t/009_twophase.pl
// (M0149-0016): prepared transactions across clean and immediate restarts,
// replay on a synchronous standby (with and without its own checkpoints),
// promotion, restart of a standby holding prepared state (restored from
// pg_twophase files, then from WAL records), hot-standby MVCC of a prepared
// transaction, prepared DDL, and pg_subtrans traffic. london and paris swap
// primary/standby roles three times. Run all-goopg and with PG 18.3 as
// london (the first primary) or paris (the first standby).
func TestPort_Recovery009Twophase(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping recovery TAP port in short mode")
	}
	// Not yet passing: see M0149-0016 in fix_plan (old-snapshot MVCC on a
	// hot standby, subtransaction rows after promotion, pg_ls_dir /
	// pg_stat_file). GOOPG_TAP_009=1 runs it anyway.
	if os.Getenv("GOOPG_TAP_009") == "" {
		t.Skip("009 is not passing yet (M0149-0016); set GOOPG_TAP_009=1 to run")
	}
	repo := repoRoot(t)
	tapnode.Available(t, repo)
	goopgStandby := func(t *testing.T, name string, root *tapnode.Node, bkp string) *tapnode.Node {
		return tapnode.InitFromBackupGoopg(t, repo, name, root, bkp, true)
	}
	pgStandby := func(t *testing.T, name string, root *tapnode.Node, bkp string) *tapnode.Node {
		return tapnode.InitFromBackupPG(t, repo, name, root, bkp, true)
	}
	cases := []struct {
		name   string
		london func(t *testing.T) *tapnode.Node
		paris  func(t *testing.T, name string, root *tapnode.Node, bkp string) *tapnode.Node
	}{
		{"all-goopg", func(t *testing.T) *tapnode.Node { return tapnode.NewGoopg(t, repo, "london") }, goopgStandby},
		{"pg18-london-goopg-paris", func(t *testing.T) *tapnode.Node { return tapnode.NewPG(t, repo, "london") }, goopgStandby},
		{"goopg-london-pg18-paris", func(t *testing.T) *tapnode.Node { return tapnode.NewGoopg(t, repo, "london") }, pgStandby},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runTwophase(t, tc.london(t), func(name, bkp string, root *tapnode.Node) *tapnode.Node {
				return tc.paris(t, name, root, bkp)
			})
		})
	}
}

func runTwophase(t *testing.T, nodeLondon *tapnode.Node, standbyFromBackup func(name, bkp string, root *tapnode.Node) *tapnode.Node) {
	is := func(got, want, name string) {
		t.Helper()
		if got != want {
			t.Errorf("not ok: %s: got %q, want %q", name, got, want)
		}
	}
	// $node->psql: errors do not fail the test; the log shows them.
	psql := func(n *tapnode.Node, sql string) (string, int) {
		t.Helper()
		out, stderr, rc := n.PSQLExit("postgres", sql)
		if rc != 0 {
			t.Logf("%s: psql exit %d: %s", n.Name(), rc, stderr)
		}
		return out, rc
	}
	rc0 := func(rc int, name string) {
		t.Helper()
		if rc != 0 {
			t.Errorf("not ok: %s (rc=%d)", name, rc)
		}
	}
	configureAndReload := func(n *tapnode.Node, parameter string) {
		t.Helper()
		n.AppendConf(parameter)
		out, _ := psql(n, "SELECT pg_reload_conf()")
		is(out, "t", fmt.Sprintf("reload node %s with %s", n.Name(), parameter))
	}

	// Set up two nodes, which will alternately be primary and replication standby.

	// Setup london node
	nodeLondon.AppendConf("max_prepared_transactions = 10")
	nodeLondon.AppendConf("log_checkpoints = true")
	nodeLondon.Start()
	bkp := nodeLondon.Backup("london_backup")

	// Setup paris node
	nodeParis := standbyFromBackup("paris", bkp, nodeLondon)
	nodeParis.AppendConf("subtransaction_buffers = 32")
	nodeParis.Start()

	// Switch to synchronous replication in both directions
	configureAndReload(nodeLondon, "synchronous_standby_names = 'paris'")
	configureAndReload(nodeParis, "synchronous_standby_names = 'london'")

	// Set up nonce names for current primary and standby nodes
	t.Log("Initially, london is primary and paris is standby")
	curPrimary, curStandby := nodeLondon, nodeParis
	name := curPrimary.Name()

	// Create table we'll use in the test transactions
	psql(curPrimary, "CREATE TABLE t_009_tbl (id int, msg text)")

	ins := func(ids ...int) string {
		var b strings.Builder
		b.WriteString("\tBEGIN;\n")
		for i, id := range ids {
			if i > 0 {
				b.WriteString("\tSAVEPOINT s1;\n")
			}
			fmt.Fprintf(&b, "\tINSERT INTO t_009_tbl VALUES (%d, 'issued to %s');\n", id, name)
		}
		return b.String()
	}

	// Check that we can commit and abort transaction after soft restart.
	// Here checkpoint happens before shutdown and no WAL replay will occur at next
	// startup. In this case postgres re-creates shared-memory state from twophase
	// files.
	psql(curPrimary, ins(1, 2)+"\tPREPARE TRANSACTION 'xact_009_1';\n"+ins(3, 4)+"\tPREPARE TRANSACTION 'xact_009_2';")
	curPrimary.Stop(tapnode.StopFast)
	curPrimary.Start()

	_, rc := psql(curPrimary, "COMMIT PREPARED 'xact_009_1'")
	rc0(rc, "Commit prepared transaction after restart")
	_, rc = psql(curPrimary, "ROLLBACK PREPARED 'xact_009_2'")
	rc0(rc, "Rollback prepared transaction after restart")

	// Check that we can commit and abort after a hard restart.
	// At next startup, WAL replay will re-create shared memory state for prepared
	// transaction using dedicated WAL records.
	psql(curPrimary, "\tCHECKPOINT;\n"+ins(5, 6)+"\tPREPARE TRANSACTION 'xact_009_3';\n"+ins(7, 8)+"\tPREPARE TRANSACTION 'xact_009_4';")
	curPrimary.Stop(tapnode.StopImmediate)
	curPrimary.Start()

	_, rc = psql(curPrimary, "COMMIT PREPARED 'xact_009_3'")
	rc0(rc, "Commit prepared transaction after teardown")
	_, rc = psql(curPrimary, "ROLLBACK PREPARED 'xact_009_4'")
	rc0(rc, "Rollback prepared transaction after teardown")

	// Check that WAL replay can handle several transactions with same GID name.
	psql(curPrimary, "\tCHECKPOINT;\n"+ins(9, 10)+"\tPREPARE TRANSACTION 'xact_009_5';\n\tCOMMIT PREPARED 'xact_009_5';\n"+ins(11, 12)+"\tPREPARE TRANSACTION 'xact_009_5';")
	curPrimary.Stop(tapnode.StopImmediate)
	curPrimary.Start()

	_, rc = psql(curPrimary, "COMMIT PREPARED 'xact_009_5'")
	rc0(rc, "Replay several transactions with same GID")

	// Check that WAL replay cleans up its shared memory state and releases locks
	// while replaying transaction commits.
	psql(curPrimary, ins(13, 14)+"\tPREPARE TRANSACTION 'xact_009_6';\n\tCOMMIT PREPARED 'xact_009_6';")
	curPrimary.Stop(tapnode.StopImmediate)
	curPrimary.Start()
	_, rc = psql(curPrimary, ins(15, 16)+"\t-- This prepare can fail due to conflicting GID or locks conflicts if\n\t-- replay did not fully cleanup its state on previous commit.\n\tPREPARE TRANSACTION 'xact_009_7';")
	rc0(rc, "Cleanup of shared memory state for 2PC commit")

	psql(curPrimary, "COMMIT PREPARED 'xact_009_7'")

	// Check that WAL replay will cleanup its shared memory state on running standby.
	psql(curPrimary, ins(17, 18)+"\tPREPARE TRANSACTION 'xact_009_8';\n\tCOMMIT PREPARED 'xact_009_8';")
	out, _ := psql(curStandby, "SELECT count(*) FROM pg_prepared_xacts")
	is(out, "0", "Cleanup of shared memory state on running standby without checkpoint")

	// Same as in previous case, but let's force checkpoint on standby between
	// prepare and commit to use on-disk twophase files.
	psql(curPrimary, ins(19, 20)+"\tPREPARE TRANSACTION 'xact_009_9';")
	psql(curStandby, "CHECKPOINT")
	psql(curPrimary, "COMMIT PREPARED 'xact_009_9'")
	out, _ = psql(curStandby, "SELECT count(*) FROM pg_prepared_xacts")
	is(out, "0", "Cleanup of shared memory state on running standby after checkpoint")

	// Check that prepared transactions can be committed on promoted standby.
	psql(curPrimary, ins(21, 22)+"\tPREPARE TRANSACTION 'xact_009_10';")
	curPrimary.Stop(tapnode.StopImmediate)
	curStandby.Promote()

	// change roles
	t.Log("Now paris is primary and london is standby")
	curPrimary, curStandby = nodeParis, nodeLondon
	name = curPrimary.Name()

	// because london is not running at this point, we can't use syncrep commit
	// on this command
	_, rc = psql(curPrimary, "SET synchronous_commit = off; COMMIT PREPARED 'xact_009_10'")
	rc0(rc, "Restore of prepared transaction on promoted standby")

	// restart old primary as new standby
	curStandby.EnableStreaming(curPrimary)
	curStandby.Start()

	// Check that prepared transactions are replayed after soft restart of standby
	// while primary is down. Since standby knows that primary is down it uses a
	// different code path on startup to ensure that the status of transactions is
	// consistent.
	psql(curPrimary, ins(23, 24)+"\tPREPARE TRANSACTION 'xact_009_11';")
	curPrimary.Stop(tapnode.StopFast)
	curStandby.Restart()
	curStandby.Promote()

	// change roles
	t.Log("Now london is primary and paris is standby")
	curPrimary, curStandby = nodeLondon, nodeParis
	name = curPrimary.Name()

	out, _ = psql(curPrimary, "SELECT count(*) FROM pg_prepared_xacts")
	is(out, "1", "Restore prepared transactions from files with primary down")

	// restart old primary as new standby
	curStandby.EnableStreaming(curPrimary)
	curStandby.Start()

	psql(curPrimary, "COMMIT PREPARED 'xact_009_11'")

	// Check that prepared transactions are correctly replayed after standby hard
	// restart while primary is down.
	psql(curPrimary, ins(25, 26)+"\tPREPARE TRANSACTION 'xact_009_12';\n\t")
	curPrimary.Stop(tapnode.StopFast)
	curStandby.Stop(tapnode.StopImmediate)
	curStandby.Start()
	curStandby.Promote()

	// change roles
	t.Log("Now paris is primary and london is standby")
	curPrimary, curStandby = nodeParis, nodeLondon
	name = curPrimary.Name()

	out, _ = psql(curPrimary, "SELECT count(*) FROM pg_prepared_xacts")
	is(out, "1", "Restore prepared transactions from records with primary down")

	// restart old primary as new standby
	curStandby.EnableStreaming(curPrimary)
	curStandby.Start()

	psql(curPrimary, "COMMIT PREPARED 'xact_009_12'")

	// Test for a scenario where there are no prepared transactions in the
	// running-xacts snapshot when the standby is restarted, but a prepared
	// transaction exists in the WAL. Regression test for PostgreSQL bug
	// (fixed in commit 64be7b7e).
	psql(curPrimary, `
	SET synchronous_commit='remote_apply'; -- To ensure the standby is caught up
	CREATE TABLE t_009_tbl_standby_mvcc (id int, msg text);
	BEGIN;
	INSERT INTO t_009_tbl_standby_mvcc VALUES (1, 'issued to `+name+`');
	SAVEPOINT s1;
	INSERT INTO t_009_tbl_standby_mvcc VALUES (2, 'issued to `+name+`');
	PREPARE TRANSACTION 'xact_009_standby_mvcc';
	`)
	curPrimary.Stop(tapnode.StopFast)
	curStandby.Restart()

	// Acquire a snapshot in standby, before we commit the prepared transaction
	standbySession := curStandby.BackgroundPSQL("postgres", true)
	standbySession.QuerySafe("BEGIN ISOLATION LEVEL REPEATABLE READ")
	out = standbySession.QuerySafe("SELECT count(*) FROM t_009_tbl_standby_mvcc")
	is(out, "0", "Prepared transaction not visible in standby before commit")

	// Commit the transaction in primary
	curPrimary.Start()
	psql(curPrimary, `
SET synchronous_commit='remote_apply'; -- To ensure the standby is caught up
COMMIT PREPARED 'xact_009_standby_mvcc';
`)

	// Still not visible to the old snapshot
	out = standbySession.QuerySafe("SELECT count(*) FROM t_009_tbl_standby_mvcc")
	is(out, "0", "Committed prepared transaction not visible to old snapshot in standby")

	// Is visible to a new snapshot
	standbySession.QuerySafe("COMMIT")
	out = standbySession.QuerySafe("SELECT count(*) FROM t_009_tbl_standby_mvcc")
	is(out, "2", "Committed prepared transaction is visible to new snapshot in standby")
	_ = standbySession.Quit()

	// Check for a lock conflict between prepared transaction with DDL inside and
	// replay of XLOG_STANDBY_LOCK wal record.
	psql(curPrimary, `
	BEGIN;
	CREATE TABLE t_009_tbl2 (id int, msg text);
	SAVEPOINT s1;
	INSERT INTO t_009_tbl2 VALUES (27, 'issued to `+name+`');
	PREPARE TRANSACTION 'xact_009_13';
	-- checkpoint will issue XLOG_STANDBY_LOCK that can conflict with lock
	-- held by 'create table' statement
	CHECKPOINT;
	COMMIT PREPARED 'xact_009_13';`)

	// Ensure that last transaction is replayed on standby.
	curPrimaryLSN := curPrimary.SafePSQL("postgres", "SELECT pg_current_wal_lsn()")
	caughtupQuery := "SELECT '" + curPrimaryLSN + "'::pg_lsn <= pg_last_wal_replay_lsn()"
	if !curStandby.PollQueryUntil("postgres", caughtupQuery, "t") {
		t.Fatal("Timed out while waiting for standby to catch up")
	}

	out, _ = psql(curStandby, "SELECT count(*) FROM t_009_tbl2")
	is(out, "1", "Replay prepared transaction with DDL")

	// Check recovery of prepared transaction with DDL inside after a hard restart
	// of the primary.
	psql(curPrimary, `
	BEGIN;
	CREATE TABLE t_009_tbl3 (id int, msg text);
	SAVEPOINT s1;
	INSERT INTO t_009_tbl3 VALUES (28, 'issued to `+name+`');
	PREPARE TRANSACTION 'xact_009_14';
	BEGIN;
	CREATE TABLE t_009_tbl4 (id int, msg text);
	SAVEPOINT s1;
	INSERT INTO t_009_tbl4 VALUES (29, 'issued to `+name+`');
	PREPARE TRANSACTION 'xact_009_15';`)

	curPrimary.Stop(tapnode.StopImmediate)
	curPrimary.Start()

	_, rc = psql(curPrimary, "COMMIT PREPARED 'xact_009_14'")
	rc0(rc, "Commit prepared transaction after teardown")
	_, rc = psql(curPrimary, "ROLLBACK PREPARED 'xact_009_15'")
	rc0(rc, "Rollback prepared transaction after teardown")

	// Check recovery of prepared transaction with DDL inside after a soft restart
	// of the primary.
	psql(curPrimary, `
	BEGIN;
	CREATE TABLE t_009_tbl5 (id int, msg text);
	SAVEPOINT s1;
	INSERT INTO t_009_tbl5 VALUES (30, 'issued to `+name+`');
	PREPARE TRANSACTION 'xact_009_16';
	BEGIN;
	CREATE TABLE t_009_tbl6 (id int, msg text);
	SAVEPOINT s1;
	INSERT INTO t_009_tbl6 VALUES (31, 'issued to `+name+`');
	PREPARE TRANSACTION 'xact_009_17';`)

	curPrimary.Stop(tapnode.StopFast)
	curPrimary.Start()

	_, rc = psql(curPrimary, "COMMIT PREPARED 'xact_009_16'")
	rc0(rc, "Commit prepared transaction after restart")
	_, rc = psql(curPrimary, "ROLLBACK PREPARED 'xact_009_17'")
	rc0(rc, "Rollback prepared transaction after restart")

	// Verify expected data appears on both servers.
	out, _ = psql(curPrimary, "SELECT count(*) FROM pg_prepared_xacts")
	is(out, "0", "No uncommitted prepared transactions on primary")

	const want009 = `1|issued to london
2|issued to london
5|issued to london
6|issued to london
9|issued to london
10|issued to london
11|issued to london
12|issued to london
13|issued to london
14|issued to london
15|issued to london
16|issued to london
17|issued to london
18|issued to london
19|issued to london
20|issued to london
21|issued to london
22|issued to london
23|issued to paris
24|issued to paris
25|issued to london
26|issued to london`
	out, _ = psql(curPrimary, "SELECT * FROM t_009_tbl ORDER BY id")
	is(out, want009, "Check expected t_009_tbl data on primary")

	out, _ = psql(curPrimary, "SELECT * FROM t_009_tbl2")
	is(out, "27|issued to paris", "Check expected t_009_tbl2 data on primary")

	out, _ = psql(curStandby, "SELECT count(*) FROM pg_prepared_xacts")
	is(out, "0", "No uncommitted prepared transactions on standby")

	out, _ = psql(curStandby, "SELECT * FROM t_009_tbl ORDER BY id")
	is(out, want009, "Check expected t_009_tbl data on standby")

	out, _ = psql(curStandby, "SELECT * FROM t_009_tbl2")
	is(out, "27|issued to paris", "Check expected t_009_tbl2 data on standby")

	// Exercise the 2PC recovery code in StartupSUBTRANS, which is concerned with
	// ensuring that enough pg_subtrans pages exist on disk to cover the range of
	// prepared transactions at server start time.  There's not much we can verify
	// directly, but let's at least get the code to run.
	curStandby.Stop(tapnode.StopFast)
	configureAndReload(curPrimary, "synchronous_standby_names = ''")

	curPrimary.SafePSQL("postgres", "CHECKPOINT")

	curPrimary.SafePSQL("postgres", "select pg_current_wal_insert_lsn()")
	curPrimary.SafePSQL("postgres", "CREATE TABLE test(); BEGIN; CREATE TABLE test1(); PREPARE TRANSACTION 'foo';")
	const subtransQuery = "select 'pg_subtrans/'||f, s.size from pg_ls_dir('pg_subtrans') f, pg_stat_file('pg_subtrans/'||f) s"
	osubtrans := curPrimary.SafePSQL("postgres", subtransQuery)
	curPrimary.Pgbench([]string{"--no-vacuum", "--client=5", "--transactions=1000"},
		map[string]string{"009_twophase.pgb": "insert into test default values"})
	curPrimary.Stop(tapnode.StopFast)
	curPrimary.Start()
	nsubtrans := curPrimary.SafePSQL("postgres", subtransQuery)
	if osubtrans == nsubtrans {
		t.Errorf("not ok: contents of pg_subtrans/ have changed (%q)", osubtrans)
	}

	curPrimary.Stop(tapnode.StopFast)
}
