package parser

import (
	"reflect"
	"testing"
)

// TestParseCreatePublicationForTable pins the M0008 / 0008-0003
// parser surface for `CREATE PUBLICATION p FOR TABLE t1, t2`.
func TestParseCreatePublicationForTable(t *testing.T) {
	stmts, err := Parse("CREATE PUBLICATION p FOR TABLE items, events")
	if err != nil {
		t.Fatal(err)
	}
	if len(stmts) != 1 {
		t.Fatalf("len=%d want 1", len(stmts))
	}
	pub, ok := stmts[0].(*CreatePublicationStmt)
	if !ok {
		t.Fatalf("type=%T want *CreatePublicationStmt", stmts[0])
	}
	if pub.Name != "p" {
		t.Errorf("Name=%q", pub.Name)
	}
	if pub.AllTables {
		t.Errorf("AllTables=true want false")
	}
	if len(pub.Tables) != 2 || pub.Tables[0].Name != "items" || pub.Tables[1].Name != "events" {
		t.Errorf("Tables=%+v", pub.Tables)
	}
}

// TestParseCreatePublicationForAllTables pins FOR ALL TABLES.
func TestParseCreatePublicationForAllTables(t *testing.T) {
	stmts, err := Parse("CREATE PUBLICATION pall FOR ALL TABLES")
	if err != nil {
		t.Fatal(err)
	}
	pub := stmts[0].(*CreatePublicationStmt)
	if !pub.AllTables {
		t.Errorf("AllTables=false want true")
	}
	if len(pub.Tables) != 0 {
		t.Errorf("Tables=%v want empty", pub.Tables)
	}
}

// TestParseCreatePublicationWithPublishOption pins WITH (publish=...).
func TestParseCreatePublicationWithPublishOption(t *testing.T) {
	stmts, err := Parse("CREATE PUBLICATION p FOR TABLE t WITH (publish = 'insert,update')")
	if err != nil {
		t.Fatal(err)
	}
	pub := stmts[0].(*CreatePublicationStmt)
	if pub.With["publish"] != "insert,update" {
		t.Errorf("With[publish]=%q", pub.With["publish"])
	}
}

// TestParseDropPublicationIfExists.
func TestParseDropPublicationIfExists(t *testing.T) {
	stmts, err := Parse("DROP PUBLICATION IF EXISTS p")
	if err != nil {
		t.Fatal(err)
	}
	d := stmts[0].(*DropPublicationStmt)
	if !d.IfExists || d.Name != "p" {
		t.Errorf("got=%+v", d)
	}
}

// TestParseCreateSubscription pins
// `CREATE SUBSCRIPTION s CONNECTION '…' PUBLICATION p1, p2 WITH (...)`.
func TestParseCreateSubscription(t *testing.T) {
	stmts, err := Parse("CREATE SUBSCRIPTION s CONNECTION 'host=remote dbname=app' PUBLICATION p1, p2 WITH (slot_name = mysub, enabled = false)")
	if err != nil {
		t.Fatal(err)
	}
	sub := stmts[0].(*CreateSubscriptionStmt)
	if sub.Name != "s" {
		t.Errorf("Name=%q", sub.Name)
	}
	if sub.Conninfo != "host=remote dbname=app" {
		t.Errorf("Conninfo=%q", sub.Conninfo)
	}
	if !reflect.DeepEqual(sub.Publications, []string{"p1", "p2"}) {
		t.Errorf("Publications=%v", sub.Publications)
	}
	if sub.With["slot_name"] != "mysub" {
		t.Errorf("With[slot_name]=%q", sub.With["slot_name"])
	}
	if sub.With["enabled"] != "false" {
		t.Errorf("With[enabled]=%q", sub.With["enabled"])
	}
}

// TestParseDropSubscription pins `DROP SUBSCRIPTION name` and the
// IF EXISTS variant.
func TestParseDropSubscription(t *testing.T) {
	stmts, err := Parse("DROP SUBSCRIPTION s")
	if err != nil {
		t.Fatal(err)
	}
	d := stmts[0].(*DropSubscriptionStmt)
	if d.IfExists || d.Name != "s" {
		t.Errorf("got=%+v", d)
	}
}
