package testport

import (
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Recovery007SyncRep ports postgres/src/test/recovery/t/007_sync_rep.pl
// (M0149-0014): the primary's pg_stat_replication sync_priority and
// sync_state for up to four standbys under each synchronous_standby_names
// form — the old comma list, '*', num_sync over the list, FIRST and ANY —
// as standbys stop and start. Run all-goopg and with PG 18.3 on either side.
func TestPort_Recovery007SyncRep(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping recovery TAP port in short mode")
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
			runSyncRep(t, tc.primary(t), func(name, bkp string, root *tapnode.Node) *tapnode.Node {
				return tc.standby(t, name, root, bkp)
			})
		})
	}
}

func runSyncRep(t *testing.T, primary *tapnode.Node, standbyFromBackup func(name, bkp string, root *tapnode.Node) *tapnode.Node) {
	// Query checking sync_priority and sync_state of each standby
	const checkSQL = "SELECT application_name, sync_priority, sync_state FROM pg_stat_replication ORDER BY application_name;"

	// Check that sync_state of each standby is expected (waiting till it is).
	// If $setting is given, synchronous_standby_names is set to it and
	// the configuration file is reloaded before the test.
	testSyncState := func(expected, msg string, setting ...string) {
		t.Helper()
		if len(setting) > 0 {
			primary.SafePSQL("postgres", "ALTER SYSTEM SET synchronous_standby_names = '"+setting[0]+"';")
			primary.Reload()
		}
		if !primary.PollQueryUntil("postgres", checkSQL, expected) {
			got, _, _ := primary.PSQLExit("postgres", checkSQL)
			t.Errorf("not ok: %s: got\n%s\nwant\n%s", msg, got, expected)
		}
	}

	// Start a standby and check that it is registered within the WAL sender
	// array of the given primary.  This polls the primary's pg_stat_replication
	// until the standby is confirmed as registered.
	startStandbyAndWait := func(standby *tapnode.Node) {
		t.Helper()
		standby.Start()
		q := "SELECT count(1) = 1 FROM pg_stat_replication WHERE application_name = '" + standby.Name() + "'"
		if !primary.PollQueryUntil("postgres", q, "t") {
			t.Fatalf("standby %q never registered on the primary", standby.Name())
		}
	}

	// Initialize primary node
	primary.Start()
	const backupName = "primary_backup"

	// Take backup
	bkp := primary.Backup(backupName)

	// Create all the standbys.  Their status on the primary is checked to
	// ensure the ordering of each one of them in the WAL sender array of the
	// primary.

	// Create standby1 linking to primary
	standby1 := standbyFromBackup("standby1", bkp, primary)
	startStandbyAndWait(standby1)

	// Create standby2 linking to primary
	standby2 := standbyFromBackup("standby2", bkp, primary)
	startStandbyAndWait(standby2)

	// Create standby3 linking to primary
	standby3 := standbyFromBackup("standby3", bkp, primary)
	startStandbyAndWait(standby3)

	// Check that sync_state is determined correctly when
	// synchronous_standby_names is specified in old syntax.
	testSyncState("standby1|1|sync\nstandby2|2|potential\nstandby3|0|async",
		"old syntax of synchronous_standby_names", "standby1,standby2")

	// Check that all the standbys are considered as either sync or
	// potential when * is specified in synchronous_standby_names.
	// Note that standby1 is chosen as sync standby because
	// it's stored in the head of WalSnd array which manages
	// all the standbys though they have the same priority.
	testSyncState("standby1|1|sync\nstandby2|1|potential\nstandby3|1|potential",
		"asterisk in synchronous_standby_names", "*")

	// Stop and start standbys to rearrange the order of standbys
	// in WalSnd array. Now, if standbys have the same priority,
	// standby2 is selected preferentially and standby3 is next.
	standby1.Stop(tapnode.StopFast)
	standby2.Stop(tapnode.StopFast)
	standby3.Stop(tapnode.StopFast)

	// Make sure that each standby reports back to the primary in the wanted
	// order.
	startStandbyAndWait(standby2)
	startStandbyAndWait(standby3)

	// Specify 2 as the number of sync standbys.
	// Check that two standbys are in 'sync' state.
	testSyncState("standby2|2|sync\nstandby3|3|sync",
		"2 synchronous standbys", "2(standby1,standby2,standby3)")

	// Start standby1
	startStandbyAndWait(standby1)

	// Create standby4 linking to primary
	standby4 := standbyFromBackup("standby4", bkp, primary)
	standby4.Start()

	// Check that standby1 and standby2 whose names appear earlier in
	// synchronous_standby_names are considered as sync. Also check that
	// standby3 appearing later represents potential, and standby4 is
	// in 'async'.
	testSyncState("standby1|1|sync\nstandby2|2|sync\nstandby3|3|potential\nstandby4|0|async",
		"2 sync, 1 potential, and 1 async")

	// Check that sync_state of each standby is determined correctly
	// when num_sync exceeds the number of names of potential sync standbys
	// specified in synchronous_standby_names.
	testSyncState("standby1|0|async\nstandby2|4|sync\nstandby3|3|sync\nstandby4|1|sync",
		"num_sync exceeds the num of potential sync standbys", "6(standby4,standby0,standby3,standby2)")

	// The setting that * comes before another standby name is acceptable
	// but does not make sense in most cases. Check that sync_state is
	// chosen properly even in case of that setting. standby1 is selected
	// as synchronous as it has the highest priority, and is followed by a
	// second standby listed first in the WAL sender array, which is
	// standby2 in this case.
	testSyncState("standby1|1|sync\nstandby2|2|sync\nstandby3|2|potential\nstandby4|2|potential",
		"asterisk before another standby name", "2(standby1,*,standby2)")

	// Check that the setting of '2(*)' chooses standby2 and standby3 that are stored
	// earlier in WalSnd array as sync standbys.
	testSyncState("standby1|1|potential\nstandby2|1|sync\nstandby3|1|sync\nstandby4|1|potential",
		"multiple standbys having the same priority are chosen as sync", "2(*)")

	// Stop Standby3 which is considered in 'sync' state.
	standby3.Stop(tapnode.StopFast)

	// Check that the state of standby1 stored earlier in WalSnd array than
	// standby4 is transited from potential to sync.
	testSyncState("standby1|1|sync\nstandby2|1|sync\nstandby4|1|potential",
		"potential standby found earlier in array is promoted to sync")

	// Check that standby1 and standby2 are chosen as sync standbys
	// based on their priorities.
	testSyncState("standby1|1|sync\nstandby2|2|sync\nstandby4|0|async",
		"priority-based sync replication specified by FIRST keyword", "FIRST 2(standby1, standby2)")

	// Check that all the listed standbys are considered as candidates
	// for sync standbys in a quorum-based sync replication.
	testSyncState("standby1|1|quorum\nstandby2|1|quorum\nstandby4|0|async",
		"2 quorum and 1 async", "ANY 2(standby1, standby2)")

	// Start Standby3 which will be considered in 'quorum' state.
	standby3.Start()

	// Check that the setting of 'ANY 2(*)' chooses all standbys as
	// candidates for quorum sync standbys.
	testSyncState("standby1|1|quorum\nstandby2|1|quorum\nstandby3|1|quorum\nstandby4|1|quorum",
		"all standbys are considered as candidates for quorum sync standbys", "ANY 2(*)")

	for _, n := range []*tapnode.Node{standby1, standby2, standby3, standby4} {
		n.Stop(tapnode.StopFast)
	}
	primary.Stop(tapnode.StopFast)
}
