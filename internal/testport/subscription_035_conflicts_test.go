package testport

import (
	"regexp"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Subscription035Conflicts ports
// postgres/src/test/subscription/t/035_conflicts.pl to the M0149 contract:
// the subscriber's apply worker reports conflict=multiple_unique_conflicts
// (conflict.c ReportApplyConflict) — one "Key already exists in unique
// index" detail per violated index, with the key, the existing local row
// and the remote row — for an INSERT, for an UPDATE, and for an INSERT
// routed to a leaf partition. Three pairings: all-goopg, a PG 18.3
// publisher with a goopg subscriber, a goopg publisher with a PG 18.3
// subscriber.
//
// Ported in M0149-0056.
func TestPort_Subscription035Conflicts(t *testing.T) {
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
			runSubscriptionConflicts(t, tc.publisher(t, "publisher"), tc.subscriber(t, "subscriber"))
		})
	}
}

func runSubscriptionConflicts(t *testing.T, nodePublisher, nodeSubscriber *tapnode.Node) {
	for _, l := range []string{"wal_level = logical", "max_wal_senders = 10", "max_replication_slots = 10"} {
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

	// Create tables on publisher
	nodePublisher.SafePSQL("postgres", "CREATE TABLE conf_tab (a int PRIMARY KEY, b int UNIQUE, c int UNIQUE);")
	nodePublisher.SafePSQL("postgres", "CREATE TABLE conf_tab_2 (a int PRIMARY KEY, b int UNIQUE, c int UNIQUE);")

	// Create same table on subscriber
	nodeSubscriber.SafePSQL("postgres", "CREATE TABLE conf_tab (a int PRIMARY key, b int UNIQUE, c int UNIQUE);")
	nodeSubscriber.SafePSQL("postgres", `
	 CREATE TABLE conf_tab_2 (a int PRIMARY KEY, b int, c int, unique(a,b)) PARTITION BY RANGE (a);
	 CREATE TABLE conf_tab_2_p1 PARTITION OF conf_tab_2 FOR VALUES FROM (MINVALUE) TO (100);
`)

	// Setup logical replication
	publisherConnstr := nodePublisher.ConnStr()
	nodePublisher.SafePSQL("postgres", "CREATE PUBLICATION pub_tab FOR TABLE conf_tab, conf_tab_2")

	// Create the subscription
	appname := "sub_tab"
	nodeSubscriber.SafePSQL("postgres", `CREATE SUBSCRIPTION sub_tab
	 CONNECTION '`+publisherConnstr+` application_name=`+appname+`'
	 PUBLICATION pub_tab;`)

	// Wait for initial table sync to finish
	waitForSubscriptionSync(t, nodeSubscriber, nodePublisher, appname)

	// Test multiple_unique_conflicts due to INSERT
	nodePublisher.SafePSQL("postgres", "INSERT INTO conf_tab VALUES (1,1,1);")
	nodeSubscriber.SafePSQL("postgres", "INSERT INTO conf_tab VALUES (2,2,2), (3,3,3), (4,4,4);")
	logOffset := nodeSubscriber.LogSize()
	// Insert a row on the publisher that conflicts with multiple unique
	// constraints
	nodePublisher.SafePSQL("postgres", "INSERT INTO conf_tab VALUES (2,3,4);")
	// Confirm that this causes an error on the subscriber
	waitForLog(t, nodeSubscriber, regexp.MustCompile(`conflict detected on relation "public.conf_tab": conflict=multiple_unique_conflicts.*
.*Key already exists in unique index "conf_tab_pkey".*
.*Key \(a\)=\(2\); existing local row \(2, 2, 2\); remote row \(2, 3, 4\).*
.*Key already exists in unique index "conf_tab_b_key".*
.*Key \(b\)=\(3\); existing local row \(3, 3, 3\); remote row \(2, 3, 4\).*
.*Key already exists in unique index "conf_tab_c_key".*
.*Key \(c\)=\(4\); existing local row \(4, 4, 4\); remote row \(2, 3, 4\).`), logOffset)
	// ok: multiple_unique_conflicts detected during insert

	// Truncate table to get rid of the error
	nodeSubscriber.SafePSQL("postgres", "TRUNCATE conf_tab;")

	// Test multiple_unique_conflicts due to UPDATE
	logOffset = nodeSubscriber.LogSize()
	nodePublisher.SafePSQL("postgres", "INSERT INTO conf_tab VALUES (5,5,5);")
	nodeSubscriber.SafePSQL("postgres", "INSERT INTO conf_tab VALUES (6,6,6), (7,7,7), (8,8,8);")
	// Update a row on the publisher that conflicts with multiple unique
	// constraints
	nodePublisher.SafePSQL("postgres", "UPDATE conf_tab set a=6, b=7, c=8 where a=5;")
	waitForLog(t, nodeSubscriber, regexp.MustCompile(`conflict detected on relation "public.conf_tab": conflict=multiple_unique_conflicts.*
.*Key already exists in unique index "conf_tab_pkey".*
.*Key \(a\)=\(6\); existing local row \(6, 6, 6\); remote row \(6, 7, 8\).*
.*Key already exists in unique index "conf_tab_b_key".*
.*Key \(b\)=\(7\); existing local row \(7, 7, 7\); remote row \(6, 7, 8\).*
.*Key already exists in unique index "conf_tab_c_key".*
.*Key \(c\)=\(8\); existing local row \(8, 8, 8\); remote row \(6, 7, 8\).`), logOffset)
	// ok: multiple_unique_conflicts detected during update

	// Truncate table to get rid of the error
	nodeSubscriber.SafePSQL("postgres", "TRUNCATE conf_tab;")

	// Test multiple_unique_conflicts due to INSERT on a leaf partition
	// Insert data in the subscriber table
	nodeSubscriber.SafePSQL("postgres", "INSERT INTO conf_tab_2 VALUES (55,2,3);")
	// Insert data in the publisher table
	nodePublisher.SafePSQL("postgres", "INSERT INTO conf_tab_2 VALUES (55,2,3);")
	waitForLog(t, nodeSubscriber, regexp.MustCompile(`conflict detected on relation "public.conf_tab_2_p1": conflict=multiple_unique_conflicts.*
.*Key already exists in unique index "conf_tab_2_p1_pkey".*
.*Key \(a\)=\(55\); existing local row \(55, 2, 3\); remote row \(55, 2, 3\).*
.*Key already exists in unique index "conf_tab_2_p1_a_b_key".*
.*Key \(a, b\)=\(55, 2\); existing local row \(55, 2, 3\); remote row \(55, 2, 3\).`), logOffset)
	// ok: multiple_unique_conflicts detected on a leaf partition during insert

	nodeSubscriber.Stop(tapnode.StopFast)
	nodePublisher.Stop(tapnode.StopFast)
}
