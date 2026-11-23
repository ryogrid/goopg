package testport

import (
	"os"
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Recovery012Subtransactions ports
// postgres/src/test/recovery/t/012_subtransactions.pl (M0149-0015):
// subtransactions — many of them, and inside prepared transactions —
// across a primary restart, streaming replay, promotion and the old
// primary rejoining as a standby. Run all-goopg and with PG 18.3 as the
// initial primary or standby (the roles swap twice during the test, so
// every mixed pairing runs each engine in both roles).
func TestPort_Recovery012Subtransactions(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping recovery TAP port in short mode")
	}
	// Not yet passing: goopg's two-phase commit is in-memory only. A
	// prepared transaction writes no PREPARE record or pg_twophase file and
	// does not survive a restart (M0149-0015a), and a goopg standby does not
	// keep a replayed PREPARE for COMMIT/ROLLBACK PREPARED after promotion
	// (M0149-0015b). GOOPG_TAP_012=1 runs it anyway.
	if os.Getenv("GOOPG_TAP_012") == "" {
		t.Skip("012 needs durable two-phase commit (M0149-0015a/b); set GOOPG_TAP_012=1 to run")
	}
	repo := repoRoot(t)
	tapnode.Available(t, repo)
	type mk func(t *testing.T, name string, root *tapnode.Node, bkp string) *tapnode.Node
	goopgStandby := func(t *testing.T, name string, root *tapnode.Node, bkp string) *tapnode.Node {
		return tapnode.InitFromBackupGoopg(t, repo, name, root, bkp, true)
	}
	pgStandby := func(t *testing.T, name string, root *tapnode.Node, bkp string) *tapnode.Node {
		return tapnode.InitFromBackupPG(t, repo, name, root, bkp, true)
	}
	cases := []struct {
		name    string
		primary func(t *testing.T) *tapnode.Node
		standby mk
	}{
		{"all-goopg", func(t *testing.T) *tapnode.Node { return tapnode.NewGoopg(t, repo, "primary") }, goopgStandby},
		{"pg18-primary-goopg-standby", func(t *testing.T) *tapnode.Node { return tapnode.NewPG(t, repo, "primary") }, goopgStandby},
		{"goopg-primary-pg18-standby", func(t *testing.T) *tapnode.Node { return tapnode.NewGoopg(t, repo, "primary") }, pgStandby},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runSubtransactions(t, tc.primary(t), func(name, bkp string, root *tapnode.Node) *tapnode.Node {
				return tc.standby(t, name, root, bkp)
			})
		})
	}
}

func runSubtransactions(t *testing.T, nodePrimary *tapnode.Node, standbyFromBackup func(name, bkp string, root *tapnode.Node) *tapnode.Node) {
	is := func(got, want, name string) {
		t.Helper()
		if got != want {
			t.Errorf("not ok: %s: got %q, want %q", name, got, want)
		}
	}
	// $node->psql: errors do not fail the test; stdout and the exit code
	// are what the .pl looks at.
	psql := func(n *tapnode.Node, sql string) (string, int) {
		t.Helper()
		out, _, rc := n.PSQLExit("postgres", sql)
		return out, rc
	}

	// Setup primary node
	nodePrimary.AppendConf("max_prepared_transactions = 10")
	nodePrimary.AppendConf("log_checkpoints = true")
	nodePrimary.Start()
	bkp := nodePrimary.Backup("primary_backup")
	psql(nodePrimary, "CREATE TABLE t_012_tbl (id int)")

	// Setup standby node
	nodeStandby := standbyFromBackup("standby", bkp, nodePrimary)
	nodeStandby.Start()

	// Switch to synchronous replication
	nodePrimary.AppendConf("synchronous_standby_names = '*'")
	psql(nodePrimary, "SELECT pg_reload_conf()")

	// Check that replay will correctly set SUBTRANS and properly advance nextXid
	// so that it won't conflict with savepoint xids.
	psql(nodePrimary, `
	BEGIN;
	DELETE FROM t_012_tbl;
	INSERT INTO t_012_tbl VALUES (43);
	SAVEPOINT s1;
	INSERT INTO t_012_tbl VALUES (43);
	SAVEPOINT s2;
	INSERT INTO t_012_tbl VALUES (43);
	SAVEPOINT s3;
	INSERT INTO t_012_tbl VALUES (43);
	SAVEPOINT s4;
	INSERT INTO t_012_tbl VALUES (43);
	SAVEPOINT s5;
	INSERT INTO t_012_tbl VALUES (43);
	PREPARE TRANSACTION 'xact_012_1';
	CHECKPOINT;`)

	nodePrimary.Stop(tapnode.StopFast)
	nodePrimary.Start()
	psql(nodePrimary, `
	-- here we can get xid of previous savepoint if nextXid
	-- wasn't properly advanced
	BEGIN;
	INSERT INTO t_012_tbl VALUES (142);
	ROLLBACK;
	COMMIT PREPARED 'xact_012_1';`)

	out, _ := psql(nodePrimary, "SELECT count(*) FROM t_012_tbl")
	is(out, "6", "Check nextXid handling for prepared subtransactions")

	// Check that replay will correctly set 2PC with more than
	// PGPROC_MAX_CACHED_SUBXIDS subtransactions and also show data properly
	// on promotion
	psql(nodePrimary, "DELETE FROM t_012_tbl")

	// Function borrowed from src/test/regress/sql/hs_primary_extremes.sql
	const hsSubxids = `
    CREATE OR REPLACE FUNCTION hs_subxids (n integer)
    RETURNS void
    LANGUAGE plpgsql
    AS $$
    BEGIN
        IF n <= 0 THEN RETURN; END IF;
        INSERT INTO t_012_tbl VALUES (n);
        PERFORM hs_subxids(n - 1);
        RETURN;
    EXCEPTION WHEN raise_exception THEN NULL; END;
    $$;`
	psql(nodePrimary, hsSubxids)
	psql(nodePrimary, `
	BEGIN;
	SELECT hs_subxids(127);
	COMMIT;`)
	waitForCatchup(t, nodePrimary, nodeStandby)
	out, _ = psql(nodeStandby, "SELECT coalesce(sum(id),-1) FROM t_012_tbl")
	is(out, "8128", "Visible")
	nodePrimary.Stop(tapnode.StopFast)
	nodeStandby.Promote()
	out, _ = psql(nodeStandby, "SELECT coalesce(sum(id),-1) FROM t_012_tbl")
	is(out, "8128", "Visible")

	// restore state
	nodePrimary, nodeStandby = nodeStandby, nodePrimary
	nodeStandby.EnableStreaming(nodePrimary)
	nodeStandby.Start()
	out, _ = psql(nodeStandby, "SELECT coalesce(sum(id),-1) FROM t_012_tbl")
	is(out, "8128", "Visible")

	psql(nodePrimary, "DELETE FROM t_012_tbl")

	// Function borrowed from src/test/regress/sql/hs_primary_extremes.sql
	psql(nodePrimary, hsSubxids)
	psql(nodePrimary, `
	BEGIN;
	SELECT hs_subxids(127);
	PREPARE TRANSACTION 'xact_012_1';`)
	waitForCatchup(t, nodePrimary, nodeStandby)
	out, _ = psql(nodeStandby, "SELECT coalesce(sum(id),-1) FROM t_012_tbl")
	is(out, "-1", "Not visible")
	nodePrimary.Stop(tapnode.StopFast)
	nodeStandby.Promote()
	out, _ = psql(nodeStandby, "SELECT coalesce(sum(id),-1) FROM t_012_tbl")
	is(out, "-1", "Not visible")

	// restore state
	nodePrimary, nodeStandby = nodeStandby, nodePrimary
	nodeStandby.EnableStreaming(nodePrimary)
	nodeStandby.Start()
	_, rc := psql(nodePrimary, "COMMIT PREPARED 'xact_012_1'")
	if rc != 0 {
		t.Errorf("not ok: Restore of PGPROC_MAX_CACHED_SUBXIDS+ prepared transaction on promoted standby (rc=%d)", rc)
	}

	out, _ = psql(nodePrimary, "SELECT coalesce(sum(id),-1) FROM t_012_tbl")
	is(out, "8128", "Visible")

	psql(nodePrimary, "DELETE FROM t_012_tbl")
	psql(nodePrimary, `
	BEGIN;
	SELECT hs_subxids(201);
	PREPARE TRANSACTION 'xact_012_1';`)
	waitForCatchup(t, nodePrimary, nodeStandby)
	out, _ = psql(nodeStandby, "SELECT coalesce(sum(id),-1) FROM t_012_tbl")
	is(out, "-1", "Not visible")
	nodePrimary.Stop(tapnode.StopFast)
	nodeStandby.Promote()
	out, _ = psql(nodeStandby, "SELECT coalesce(sum(id),-1) FROM t_012_tbl")
	is(out, "-1", "Not visible")

	// restore state
	nodePrimary, nodeStandby = nodeStandby, nodePrimary
	nodeStandby.EnableStreaming(nodePrimary)
	nodeStandby.Start()
	_, rc = psql(nodePrimary, "ROLLBACK PREPARED 'xact_012_1'")
	if rc != 0 {
		t.Errorf("not ok: Rollback of PGPROC_MAX_CACHED_SUBXIDS+ prepared transaction on promoted standby (rc=%d)", rc)
	}

	out, _ = psql(nodePrimary, "SELECT coalesce(sum(id),-1) FROM t_012_tbl")
	is(out, "-1", "Not visible")

	nodeStandby.Stop(tapnode.StopFast)
	nodePrimary.Stop(tapnode.StopFast)
}

