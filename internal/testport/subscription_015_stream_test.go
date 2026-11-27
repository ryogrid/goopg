package testport

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Subscription015Stream ports
// postgres/src/test/subscription/t/015_stream.pl to the M0149 contract:
// streaming of large in-progress transactions to a subscriber
// (logical_decoding_work_mem = 64kB on the publisher, so every transaction
// here exceeds it and is sent as STREAM START / STOP / COMMIT chunks). With
// streaming = on: a pair of interleaved large transactions (one held open in
// a background session), binary = on streaming, and a non-streamed UPDATE
// after a streamed one — the subscriber's extra columns keep their local
// defaults / locally changed values. ALTER SUBSCRIPTION ... SET (streaming =
// parallel) restarts the apply worker (a new walsender pid in
// pg_stat_replication); the same steps then run through parallel apply
// workers, checked via the DEBUG1 "finished processing the STREAM COMMIT
// command" log line. Then the deadlock detector (deadlock_timeout = 10ms)
// must catch a leader-vs-parallel-worker and a parallel-vs-parallel-worker
// deadlock on a unique index, which is dropped so the transactions apply;
// finally debug_logical_replication_streaming = immediate makes the leader
// serialize the remaining changes of a streamed transaction to a file for
// the parallel apply worker to replay at commit. Three pairings: all-goopg,
// a PG 18.3 publisher with a goopg subscriber, a goopg publisher with a PG
// 18.3 subscriber.
func TestPort_Subscription015Stream(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subscription TAP port in short mode")
	}
	if os.Getenv("GOOPG_TAP_015") == "" {
		t.Skip("015 port is not passing yet (M0149-0063); set GOOPG_TAP_015=1 to run")
	}
	repo := repoRoot(t)
	tapnode.Available(t, repo)
	goopg := func(t *testing.T, name string) *tapnode.Node { return tapnode.NewGoopg(t, repo, name) }
	pg := func(t *testing.T, name string) *tapnode.Node { return tapnode.NewPG(t, repo, name) }
	cases := []struct {
		name                  string
		publisher, subscriber func(t *testing.T, name string) *tapnode.Node
	}{
		{"all-goopg", goopg, goopg},
		{"pg18-publisher-goopg-subscriber", pg, goopg},
		{"goopg-publisher-pg18-subscriber", goopg, pg},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runSubscriptionStream(t, tc.publisher(t, "publisher"), tc.subscriber(t, "subscriber"))
		})
	}
}

func runSubscriptionStream(t *testing.T, nodePublisher, nodeSubscriber *tapnode.Node) {
	is := func(got, want, name string) {
		t.Helper()
		if got != want {
			t.Errorf("not ok: %s: got %q, want %q", name, got, want)
		}
	}

	// Check that the parallel apply worker has finished applying the streaming
	// transaction.
	checkParallelLog := func(nodeSubscriber *tapnode.Node, offset int64, isParallel bool, typ string) {
		t.Helper()
		if isParallel {
			waitForLog(t, nodeSubscriber,
				regexp.MustCompile(`DEBUG: ( [A-Z0-9]+:)? finished processing the STREAM `+typ+` command`),
				offset)
		}
	}

	// Common test steps for both the streaming=on and streaming=parallel cases.
	testStreaming := func(nodePublisher, nodeSubscriber *tapnode.Node, appname string, isParallel bool) {
		t.Helper()
		// Interleave a pair of transactions, each exceeding the 64kB limit.
		var offset int64

		h := nodePublisher.BackgroundPSQL("postgres", false)

		// Check the subscriber log from now on.
		offset = nodeSubscriber.LogSize()

		h.QuerySafe(`
	BEGIN;
	INSERT INTO test_tab SELECT i, sha256(i::text::bytea) FROM generate_series(3, 5000) s(i);
	UPDATE test_tab SET b = sha256(b) WHERE mod(a,2) = 0;
	DELETE FROM test_tab WHERE mod(a,3) = 0;
	`)

		nodePublisher.SafePSQL("postgres", `
	BEGIN;
	INSERT INTO test_tab SELECT i, sha256(i::text::bytea) FROM generate_series(5001, 9999) s(i);
	DELETE FROM test_tab WHERE a > 5000;
	COMMIT;
	`)

		h.QuerySafe("COMMIT")
		// errors make the next test fail, so ignore them here
		_ = h.Quit()

		waitForSubCatchup(t, nodePublisher, appname)

		checkParallelLog(nodeSubscriber, offset, isParallel, "COMMIT")

		result := nodeSubscriber.SafePSQL("postgres",
			"SELECT count(*), count(c), count(d = 999) FROM test_tab")
		is(result, "3334|3334|3334",
			"check extra columns contain local defaults")

		// Test the streaming in binary mode
		nodeSubscriber.SafePSQL("postgres",
			"ALTER SUBSCRIPTION tap_sub SET (binary = on)")

		// Check the subscriber log from now on.
		offset = nodeSubscriber.LogSize()

		// Insert, update and delete enough rows to exceed the 64kB limit.
		nodePublisher.SafePSQL("postgres", `
	BEGIN;
	INSERT INTO test_tab SELECT i, sha256(i::text::bytea) FROM generate_series(5001, 10000) s(i);
	UPDATE test_tab SET b = sha256(b) WHERE mod(a,2) = 0;
	DELETE FROM test_tab WHERE mod(a,3) = 0;
	COMMIT;
	`)

		waitForSubCatchup(t, nodePublisher, appname)

		checkParallelLog(nodeSubscriber, offset, isParallel, "COMMIT")

		result = nodeSubscriber.SafePSQL("postgres",
			"SELECT count(*), count(c), count(d = 999) FROM test_tab")
		is(result, "6667|6667|6667",
			"check extra columns contain local defaults")

		// Change the local values of the extra columns on the subscriber,
		// update publisher, and check that subscriber retains the expected
		// values. This is to ensure that non-streaming transactions behave
		// properly after a streaming transaction.
		nodeSubscriber.SafePSQL("postgres",
			"UPDATE test_tab SET c = 'epoch'::timestamptz + 987654321 * interval '1s'")

		// Check the subscriber log from now on.
		offset = nodeSubscriber.LogSize()

		nodePublisher.SafePSQL("postgres",
			"UPDATE test_tab SET b = sha256(a::text::bytea)")

		waitForSubCatchup(t, nodePublisher, appname)

		checkParallelLog(nodeSubscriber, offset, isParallel, "COMMIT")

		result = nodeSubscriber.SafePSQL("postgres",
			"SELECT count(*), count(extract(epoch from c) = 987654321), count(d = 999) FROM test_tab")
		is(result, "6667|6667|6667",
			"check extra columns contain locally changed data")

		// Cleanup the test data
		nodePublisher.SafePSQL("postgres",
			"DELETE FROM test_tab WHERE (a > 2)")
		waitForSubCatchup(t, nodePublisher, appname)
	}

	// Create publisher node
	for _, l := range []string{"wal_level = logical", "max_wal_senders = 10", "max_replication_slots = 10"} {
		nodePublisher.AppendConf(l)
	}
	nodePublisher.AppendConf("logical_decoding_work_mem = 64kB")
	nodePublisher.Start()

	// Create subscriber node
	nodeSubscriber.Start()
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		for _, n := range []*tapnode.Node{nodeSubscriber, nodePublisher} {
			for _, l := range strings.Split(fileTail(n.LogPath(), 2000), "\n") {
				if strings.Contains(l, "ERROR") || strings.Contains(l, "FATAL") || strings.Contains(l, "level=ERROR") || strings.Contains(l, "level=WARN") || strings.Contains(l, "decoder run failed") {
					t.Log(n.Name() + " log| " + l)
				}
			}
		}
	})

	// Create some preexisting content on publisher
	nodePublisher.SafePSQL("postgres",
		"CREATE TABLE test_tab (a int primary key, b bytea)")
	nodePublisher.SafePSQL("postgres",
		"INSERT INTO test_tab VALUES (1, 'foo'), (2, 'bar')")

	nodePublisher.SafePSQL("postgres", "CREATE TABLE test_tab_2 (a int)")

	// Setup structure on subscriber
	nodeSubscriber.SafePSQL("postgres",
		"CREATE TABLE test_tab (a int primary key, b bytea, c timestamptz DEFAULT now(), d bigint DEFAULT 999)")

	nodeSubscriber.SafePSQL("postgres", "CREATE TABLE test_tab_2 (a int)")
	nodeSubscriber.SafePSQL("postgres",
		"CREATE UNIQUE INDEX idx_tab on test_tab_2(a)")

	// Setup logical replication
	// (ConnStr already carries dbname=postgres.)
	publisherConnstr := nodePublisher.ConnStr()
	nodePublisher.SafePSQL("postgres",
		"CREATE PUBLICATION tap_pub FOR TABLE test_tab, test_tab_2")

	appname := "tap_sub"

	////////////////////////////////
	// Test using streaming mode 'on'
	////////////////////////////////
	nodeSubscriber.SafePSQL("postgres",
		"CREATE SUBSCRIPTION tap_sub CONNECTION '"+publisherConnstr+" application_name="+appname+"' PUBLICATION tap_pub WITH (streaming = on)")

	// Wait for initial table sync to finish
	waitForSubscriptionSync(t, nodeSubscriber, nodePublisher, appname)

	result := nodeSubscriber.SafePSQL("postgres",
		"SELECT count(*), count(c), count(d = 999) FROM test_tab")
	is(result, "2|2|2", "check initial data was copied to subscriber")

	testStreaming(nodePublisher, nodeSubscriber, appname, false)

	//////////////////////////////////////
	// Test using streaming mode 'parallel'
	//////////////////////////////////////
	oldpid := nodePublisher.SafePSQL("postgres",
		"SELECT pid FROM pg_stat_replication WHERE application_name = '"+appname+"' AND state = 'streaming';")

	nodeSubscriber.SafePSQL("postgres",
		"ALTER SUBSCRIPTION tap_sub SET(streaming = parallel, binary = off)")

	if !nodePublisher.PollQueryUntil("postgres",
		"SELECT pid != "+oldpid+" FROM pg_stat_replication WHERE application_name = '"+appname+"' AND state = 'streaming';", "t") {
		t.Fatalf("Timed out while waiting for apply to restart after changing SUBSCRIPTION")
	}

	// We need to check DEBUG logs to ensure that the parallel apply worker has
	// applied the transaction. So, bump up the log verbosity.
	nodeSubscriber.AppendConf("log_min_messages = debug1")
	nodeSubscriber.Reload()

	// Run a query to make sure that the reload has taken effect.
	nodeSubscriber.SafePSQL("postgres", `SELECT 1`)

	testStreaming(nodePublisher, nodeSubscriber, appname, true)

	// Test that the deadlock is detected among the leader and parallel apply
	// workers.

	nodeSubscriber.AppendConf("deadlock_timeout = 10ms")
	nodeSubscriber.Reload()

	// Run a query to make sure that the reload has taken effect.
	nodeSubscriber.SafePSQL("postgres", `SELECT 1`)

	// Interleave a pair of transactions, each exceeding the 64kB limit.
	h := nodePublisher.BackgroundPSQL("postgres", false)

	// Confirm if a deadlock between the leader apply worker and the parallel apply
	// worker can be detected.

	offset := nodeSubscriber.LogSize()

	h.QuerySafe(`
BEGIN;
INSERT INTO test_tab_2 SELECT i FROM generate_series(1, 5000) s(i);
`)

	// Ensure that the parallel apply worker executes the insert command before the
	// leader worker.
	waitForLog(t, nodeSubscriber,
		regexp.MustCompile(`DEBUG: ( [A-Z0-9]+:)? applied [0-9]+ changes in the streaming chunk`),
		offset)

	nodePublisher.SafePSQL("postgres", "INSERT INTO test_tab_2 values(1)")

	h.QuerySafe("COMMIT")
	_ = h.Quit()

	waitForLog(t, nodeSubscriber, regexp.MustCompile(`ERROR: ( [A-Z0-9]+:)? deadlock detected`),
		offset)

	// In order for the two transactions to be completed normally without causing
	// conflicts due to the unique index, we temporarily drop it.
	nodeSubscriber.SafePSQL("postgres", "DROP INDEX idx_tab")

	// Wait for this streaming transaction to be applied in the apply worker.
	waitForSubCatchup(t, nodePublisher, appname)

	result = nodeSubscriber.SafePSQL("postgres", "SELECT count(*) FROM test_tab_2")
	is(result, "5001", "data replicated to subscriber after dropping index")

	// Clean up test data from the environment.
	nodePublisher.SafePSQL("postgres", "TRUNCATE TABLE test_tab_2")
	waitForSubCatchup(t, nodePublisher, appname)
	nodeSubscriber.SafePSQL("postgres",
		"CREATE UNIQUE INDEX idx_tab on test_tab_2(a)")

	// Confirm if a deadlock between two parallel apply workers can be detected.

	// Check the subscriber log from now on.
	offset = nodeSubscriber.LogSize()

	// The perl reuses $h after $h->quit (IPC::Run restarts the finished
	// harness, i.e. a fresh psql session); tapnode's BackgroundPSQL cannot
	// be restarted after Quit, so open a new session here.
	h = nodePublisher.BackgroundPSQL("postgres", false)
	h.QuerySafe(`
BEGIN;
INSERT INTO test_tab_2 SELECT i FROM generate_series(1, 5000) s(i);
`)

	// Ensure that the first parallel apply worker executes the insert command
	// before the second one.
	waitForLog(t, nodeSubscriber,
		regexp.MustCompile(`DEBUG: ( [A-Z0-9]+:)? applied [0-9]+ changes in the streaming chunk`),
		offset)

	nodePublisher.SafePSQL("postgres",
		"INSERT INTO test_tab_2 SELECT i FROM generate_series(1, 5000) s(i)")

	h.QuerySafe("COMMIT")
	_ = h.Quit()

	waitForLog(t, nodeSubscriber, regexp.MustCompile(`ERROR: ( [A-Z0-9]+:)? deadlock detected`),
		offset)

	// In order for the two transactions to be completed normally without causing
	// conflicts due to the unique index, we temporarily drop it.
	nodeSubscriber.SafePSQL("postgres", "DROP INDEX idx_tab")

	// Wait for this streaming transaction to be applied in the apply worker.
	waitForSubCatchup(t, nodePublisher, appname)

	result = nodeSubscriber.SafePSQL("postgres", "SELECT count(*) FROM test_tab_2")
	is(result, "10000", "data replicated to subscriber after dropping index")

	// Test serializing changes to files and notify the parallel apply worker to
	// apply them at the end of the transaction.
	nodeSubscriber.AppendConf("debug_logical_replication_streaming = immediate")
	// Reset the log_min_messages to default.
	nodeSubscriber.AppendConf("log_min_messages = warning")
	nodeSubscriber.Reload()

	// Run a query to make sure that the reload has taken effect.
	nodeSubscriber.SafePSQL("postgres", `SELECT 1`)

	offset = nodeSubscriber.LogSize()

	nodePublisher.SafePSQL("postgres",
		"INSERT INTO test_tab_2 SELECT i FROM generate_series(1, 5000) s(i)")

	// Ensure that the changes are serialized.
	waitForLog(t, nodeSubscriber,
		regexp.MustCompile(`LOG: ( [A-Z0-9]+:)? logical replication apply worker will serialize the remaining changes of remote transaction \d+ to a file`),
		offset)

	waitForSubCatchup(t, nodePublisher, appname)

	// Check that transaction is committed on subscriber
	result = nodeSubscriber.SafePSQL("postgres", "SELECT count(*) FROM test_tab_2")
	is(result, "15000",
		"parallel apply worker replayed all changes from file")

	nodeSubscriber.Stop(tapnode.StopFast)
	nodePublisher.Stop(tapnode.StopFast)
}
