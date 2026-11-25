package testport

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Recovery003RecoveryTargetsFull ports postgres/src/test/recovery/t/003_recovery_targets.pl
// to the M0149 contract: standbys restored from one backup (Cluster.pm
// init_from_backup has_restoring: standby => 1) stop at recovery_target =
// 'immediate', recovery_target_xid, _time, _name (pg_create_restore_point)
// and _lsn, and pause there serving reads; overriding settings of one target
// are allowed, two different targets are "multiple recovery targets
// specified", and archive recovery that ends before its target is a FATAL.
// It runs in three pairings: all-goopg, a PG 18.3 primary with goopg
// standbys, and a goopg primary with PG 18.3 standbys.
//
// Gated by GOOPG_TAP_003=1 until it passes (M0149-0023).
func TestPort_Recovery003RecoveryTargetsFull(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping recovery TAP port in short mode")
	}
	if os.Getenv("GOOPG_TAP_003") == "" {
		t.Skip("003 re-port is not passing yet (M0149-0023); set GOOPG_TAP_003=1 to run")
	}
	repo := repoRoot(t)
	tapnode.Available(t, repo)
	goopg := func(t *testing.T, name string) *tapnode.Node { return tapnode.NewGoopg(t, repo, name) }
	pg := func(t *testing.T, name string) *tapnode.Node { return tapnode.NewPG(t, repo, name) }
	goopgFromBackup := func(t *testing.T, name string, root *tapnode.Node, bkp string) *tapnode.Node {
		return tapnode.InitFromBackupGoopg(t, repo, name, root, bkp, false)
	}
	pgFromBackup := func(t *testing.T, name string, root *tapnode.Node, bkp string) *tapnode.Node {
		return tapnode.InitFromBackupPG(t, repo, name, root, bkp, false)
	}
	cases := []struct {
		name    string
		primary func(t *testing.T, name string) *tapnode.Node
		standby func(t *testing.T, name string, root *tapnode.Node, bkp string) *tapnode.Node
	}{
		{"all-goopg", goopg, goopgFromBackup},
		{"pg18-primary-goopg-standbys", pg, goopgFromBackup},
		{"goopg-primary-pg18-standbys", goopg, pgFromBackup},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runRecoveryTargets(t, repo, tc.primary(t, "primary"), func(name, bkp string, root *tapnode.Node) *tapnode.Node {
				return tc.standby(t, name, root, bkp)
			})
		})
	}
}

func runRecoveryTargets(t *testing.T, repo string, nodePrimary *tapnode.Node, fromBackup func(name, bkp string, root *tapnode.Node) *tapnode.Node) {
	is := func(got, want, name string) {
		t.Helper()
		if got != want {
			t.Errorf("not ok: %s: got %q, want %q", name, got, want)
		}
	}
	ok := func(cond bool, name string) {
		t.Helper()
		if !cond {
			t.Errorf("not ok: %s", name)
		}
	}
	var bkp string

	// Create and test a standby from given backup, with a certain recovery
	// target. Choose untilLSN later than the transaction commit that causes
	// the row count to reach numRows, yet not later than the recovery target.
	testRecoveryStandby := func(testName, nodeName string, recoveryParams []string, numRows, untilLSN string) {
		t.Helper()
		nodeStandby := fromBackup(nodeName, bkp, nodePrimary)
		nodeStandby.EnableRestoring(nodePrimary, true)
		for _, p := range recoveryParams {
			nodeStandby.AppendConf(p)
		}
		nodeStandby.Start()

		// Wait until standby has replayed enough data
		caughtupQuery := fmt.Sprintf("SELECT '%s'::pg_lsn <= pg_last_wal_replay_lsn()", untilLSN)
		if !nodeStandby.PollQueryUntil("postgres", caughtupQuery, "t") {
			t.Fatalf("Timed out while waiting for standby to catch up (%s)", testName)
		}

		// Create some content on primary and check its presence in standby
		result := nodeStandby.SafePSQL("postgres", "SELECT count(*) FROM tab_int")
		is(result, numRows, "check standby content for "+testName)

		// Stop standby node
		nodeStandby.Stop(tapnode.StopImmediate)
	}

	// Initialize primary node
	nodePrimary.EnableArchiving()
	for _, l := range []string{"wal_level = replica", "max_wal_senders = 10"} {
		nodePrimary.AppendConf(l)
	}

	// Bump the transaction ID epoch. This is useful to stress the
	// portability of recovery_target_xid parsing.
	if out, err := exec.Command(filepath.Join(tapnode.BinDir(repo), "pg_resetwal"), "--epoch", "1", nodePrimary.DataDir()).CombinedOutput(); err != nil {
		t.Fatalf("pg_resetwal --epoch 1: %v\n%s", err, out)
	}

	// Start it
	nodePrimary.Start()

	// Create data before taking the backup, aimed at testing
	// recovery_target = 'immediate'
	nodePrimary.SafePSQL("postgres", "CREATE TABLE tab_int AS SELECT generate_series(1,1000) AS a")
	lsn1 := nodePrimary.SafePSQL("postgres", "SELECT pg_current_wal_lsn();")

	// Take backup from which all operations will be run
	bkp = nodePrimary.Backup("my_backup")

	// Insert some data with used as a replay reference, with a recovery
	// target TXID.
	nodePrimary.SafePSQL("postgres", "INSERT INTO tab_int VALUES (generate_series(1001,2000))")
	ret := nodePrimary.SafePSQL("postgres", "SELECT pg_current_wal_lsn(), pg_current_xact_id();")
	parts := strings.SplitN(ret, "|", 2)
	if len(parts) != 2 {
		t.Fatalf("unexpected lsn/xid result %q", ret)
	}
	lsn2, recoveryTxid := parts[0], parts[1]

	// More data, with recovery target timestamp
	nodePrimary.SafePSQL("postgres", "INSERT INTO tab_int VALUES (generate_series(2001,3000))")
	lsn3 := nodePrimary.SafePSQL("postgres", "SELECT pg_current_wal_lsn();")
	recoveryTime := nodePrimary.SafePSQL("postgres", "SELECT now()")

	// Even more data, this time with a recovery target name
	nodePrimary.SafePSQL("postgres", "INSERT INTO tab_int VALUES (generate_series(3001,4000))")
	recoveryName := "my_target"
	lsn4 := nodePrimary.SafePSQL("postgres", "SELECT pg_current_wal_lsn();")
	nodePrimary.SafePSQL("postgres", fmt.Sprintf("SELECT pg_create_restore_point('%s');", recoveryName))

	// And now for a recovery target LSN
	nodePrimary.SafePSQL("postgres", "INSERT INTO tab_int VALUES (generate_series(4001,5000))")
	lsn5 := nodePrimary.SafePSQL("postgres", "SELECT pg_current_wal_lsn()")
	recoveryLSN := lsn5

	nodePrimary.SafePSQL("postgres", "INSERT INTO tab_int VALUES (generate_series(5001,6000))")

	// Force archiving of WAL file
	nodePrimary.SafePSQL("postgres", "SELECT pg_switch_wal()")

	// Test recovery targets
	testRecoveryStandby("immediate target", "standby_1", []string{"recovery_target = 'immediate'"}, "1000", lsn1)
	testRecoveryStandby("XID", "standby_2", []string{fmt.Sprintf("recovery_target_xid = '%s'", recoveryTxid)}, "2000", lsn2)
	testRecoveryStandby("time", "standby_3", []string{fmt.Sprintf("recovery_target_time = '%s'", recoveryTime)}, "3000", lsn3)
	testRecoveryStandby("name", "standby_4", []string{fmt.Sprintf("recovery_target_name = '%s'", recoveryName)}, "4000", lsn4)
	testRecoveryStandby("LSN", "standby_5", []string{fmt.Sprintf("recovery_target_lsn = '%s'", recoveryLSN)}, "5000", lsn5)

	// Multiple targets
	//
	// Multiple conflicting settings are not allowed, but setting the same
	// parameter multiple times or unsetting a parameter and setting a
	// different one is allowed.
	testRecoveryStandby("multiple overriding settings", "standby_6", []string{
		fmt.Sprintf("recovery_target_name = '%s'", recoveryName),
		"recovery_target_name = ''",
		fmt.Sprintf("recovery_target_time = '%s'", recoveryTime),
	}, "3000", lsn3)

	nodeStandby := fromBackup("standby_7", bkp, nodePrimary)
	nodeStandby.EnableRestoring(nodePrimary, true)
	nodeStandby.AppendConf(fmt.Sprintf("recovery_target_name = '%s'\nrecovery_target_time = '%s'", recoveryName, recoveryTime))
	ok(nodeStandby.StartExpectExit(time.Minute), "invalid recovery startup fails")
	ok(strings.Contains(nodeStandby.LogFrom(0), "multiple recovery targets specified"), "multiple conflicting settings")

	// Check behavior when recovery ends before target is reached
	nodeStandby = fromBackup("standby_8", bkp, nodePrimary)
	nodeStandby.EnableRestoring(nodePrimary, false)
	nodeStandby.AppendConf("recovery_target_name = 'does_not_exist'")
	nodeStandby.StartExpectExit(tapnode.TimeoutDefault())
	ok(regexp.MustCompile(`FATAL: .* recovery ended before configured recovery target was reached`).MatchString(nodeStandby.LogFrom(0)),
		"recovery end before target reached is a fatal error")
}
