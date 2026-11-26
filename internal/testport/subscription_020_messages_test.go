package testport

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Subscription020Messages ports postgres/src/test/subscription/t/020_messages.pl
// to the M0149 contract: logical decoding messages (pg_logical_emit_message)
// through pgoutput, read on the publisher with
// pg_logical_slot_{peek,get}_binary_changes — a transactional message is
// B M C with the 'messages' option and is absent without it (empty
// transaction optimisation), a non-transactional one is a lone M with
// xid 0, and non-transactional messages from an aborted transaction still
// appear. Three pairings: all-goopg, a PG 18.3 publisher with a goopg
// subscriber, a goopg publisher with a PG 18.3 subscriber.
//
// Ported in M0149-0051.
func TestPort_Subscription020Messages(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subscription TAP port in short mode")
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
			runSubscriptionMessages(t, tc.publisher(t, "publisher"), tc.subscriber(t, "subscriber"))
		})
	}
}

func runSubscriptionMessages(t *testing.T, nodePublisher, nodeSubscriber *tapnode.Node) {
	is := func(got, want, name string) {
		t.Helper()
		if got != want {
			t.Errorf("not ok: %s: got %q, want %q", name, got, want)
		}
	}
	for _, l := range []string{"wal_level = logical", "max_wal_senders = 10", "max_replication_slots = 10", "autovacuum = off"} {
		nodePublisher.AppendConf(l)
	}
	nodePublisher.Start()
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

	nodePublisher.SafePSQL("postgres", "CREATE TABLE tab_test (a int primary key)")
	nodeSubscriber.SafePSQL("postgres", "CREATE TABLE tab_test (a int primary key)")
	publisherConnstr := nodePublisher.ConnStr()
	nodePublisher.SafePSQL("postgres", "CREATE PUBLICATION tap_pub FOR TABLE tab_test")
	nodeSubscriber.SafePSQL("postgres", "CREATE SUBSCRIPTION tap_sub CONNECTION '"+publisherConnstr+"' PUBLICATION tap_pub")
	waitForSubCatchup(t, nodePublisher, "tap_sub")

	// Ensure a transactional logical decoding message shows up on the slot
	nodeSubscriber.SafePSQL("postgres", "ALTER SUBSCRIPTION tap_sub DISABLE")
	if !nodePublisher.PollQueryUntil("postgres",
		"SELECT COUNT(*) FROM pg_catalog.pg_replication_slots WHERE slot_name = 'tap_sub' AND active='f'", "1") {
		t.Fatal("timed out waiting for the slot to become inactive")
	}
	nodePublisher.SafePSQL("postgres", "SELECT pg_logical_emit_message(true, 'pgoutput', 'a transactional message')")

	is(nodePublisher.SafePSQL("postgres", `
		SELECT get_byte(data, 0)
		FROM pg_logical_slot_peek_binary_changes('tap_sub', NULL, NULL,
			'proto_version', '1',
			'publication_names', 'tap_pub',
			'messages', 'true')
`), "66\n77\n67", "messages on slot are B M C with message option")

	is(nodePublisher.SafePSQL("postgres", `
		SELECT get_byte(data, 1), encode(substr(data, 11, 8), 'escape')
		FROM pg_logical_slot_peek_binary_changes('tap_sub', NULL, NULL,
			'proto_version', '1',
			'publication_names', 'tap_pub',
			'messages', 'true')
		OFFSET 1 LIMIT 1
`), "1|pgoutput", "flag transactional is set to 1 and prefix is pgoutput")

	is(nodePublisher.SafePSQL("postgres", `
		SELECT get_byte(data, 0)
		FROM pg_logical_slot_get_binary_changes('tap_sub', NULL, NULL,
			'proto_version', '1',
			'publication_names', 'tap_pub')
`), "", "option messages defaults to false so message (M) is not available on slot")

	nodePublisher.SafePSQL("postgres", "INSERT INTO tab_test VALUES (1)")
	messageLSN := nodePublisher.SafePSQL("postgres",
		"SELECT pg_logical_emit_message(false, 'pgoutput', 'a non-transactional message')")
	nodePublisher.SafePSQL("postgres", "INSERT INTO tab_test VALUES (2)")

	is(nodePublisher.SafePSQL("postgres", `
		SELECT get_byte(data, 0), get_byte(data, 1)
		FROM pg_logical_slot_get_binary_changes('tap_sub', NULL, NULL,
			'proto_version', '1',
			'publication_names', 'tap_pub',
			'messages', 'true')
		WHERE lsn = '`+messageLSN+`' AND xid = 0
`), "77|0", "non-transactional message on slot is M")

	nodePublisher.SafePSQL("postgres", `
		BEGIN;
		SELECT pg_logical_emit_message(false, 'pgoutput',
			'a non-transactional message is available even if the transaction is aborted 1');
		INSERT INTO tab_test VALUES (3);
		SELECT pg_logical_emit_message(true, 'pgoutput',
			'a transactional message is not available if the transaction is aborted');
		SELECT pg_logical_emit_message(false, 'pgoutput',
			'a non-transactional message is available even if the transaction is aborted 2');
		ROLLBACK;
		SELECT pg_switch_wal();
`)

	is(nodePublisher.SafePSQL("postgres", `
		SELECT get_byte(data, 0), get_byte(data, 1)
		FROM pg_logical_slot_peek_binary_changes('tap_sub', NULL, NULL,
			'proto_version', '1',
			'publication_names', 'tap_pub',
			'messages', 'true')
`), "77|0\n77|0", "non-transactional message on slot from aborted transaction is M")

	nodeSubscriber.Stop(tapnode.StopFast)
	nodePublisher.Stop(tapnode.StopFast)
}
