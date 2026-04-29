package catalog

import (
	"errors"
	"reflect"
	"testing"
)

// TestPubSubCreatePublicationByTable: a publication created with
// an explicit table list resolves through Lookup and Publications,
// preserves insertion order, and reports puballtables=false.
func TestPubSubCreatePublicationByTable(t *testing.T) {
	ps := NewPubSub()
	pub, err := ps.CreatePublication("p1", []string{"public.items", "public.events"}, DefaultPublicationOptions())
	if err != nil {
		t.Fatal(err)
	}
	if pub.Name != "p1" || pub.AllTables {
		t.Errorf("created=%+v want {Name:p1, AllTables:false}", pub)
	}
	if !reflect.DeepEqual(pub.Tables, []string{"public.items", "public.events"}) {
		t.Errorf("Tables=%v", pub.Tables)
	}
	if !pub.PublishInsert || !pub.PublishUpdate || !pub.PublishDelete {
		t.Errorf("default publish flags missing: %+v", pub)
	}

	got, ok := ps.LookupPublication("p1")
	if !ok || got.Name != "p1" || got.OID != pub.OID {
		t.Errorf("Lookup=%+v ok=%v want round-trip of %+v", got, ok, pub)
	}

	all := ps.Publications()
	if len(all) != 1 || all[0].Name != "p1" {
		t.Errorf("Publications=%+v want one p1", all)
	}

	// Returned slices must be independent copies — mutating
	// the returned Tables must not bleed into the registry.
	got.Tables[0] = "MUTATED"
	got2, _ := ps.LookupPublication("p1")
	if got2.Tables[0] == "MUTATED" {
		t.Errorf("Lookup leaked aliased Tables slice")
	}
}

// TestPubSubCreatePublicationAllTables: AllTables=true publication
// has an empty Tables slice and reports the right flag.
func TestPubSubCreatePublicationAllTables(t *testing.T) {
	ps := NewPubSub()
	opts := DefaultPublicationOptions()
	opts.AllTables = true
	pub, err := ps.CreatePublication("pall", nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !pub.AllTables {
		t.Errorf("AllTables=false want true")
	}
	if len(pub.Tables) != 0 {
		t.Errorf("Tables=%v want empty (FOR ALL TABLES)", pub.Tables)
	}
}

// TestPubSubDuplicatePublicationName: a second CreatePublication
// with the same name fails with ErrPublicationExists.
func TestPubSubDuplicatePublicationName(t *testing.T) {
	ps := NewPubSub()
	if _, err := ps.CreatePublication("p1", nil, DefaultPublicationOptions()); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.CreatePublication("p1", nil, DefaultPublicationOptions()); !errors.Is(err, ErrPublicationExists) {
		t.Errorf("err=%v want ErrPublicationExists", err)
	}
}

// TestPubSubDropPublication removes the entry; subsequent Lookup
// returns false; a second drop yields ErrPublicationNotFound.
func TestPubSubDropPublication(t *testing.T) {
	ps := NewPubSub()
	if _, err := ps.CreatePublication("p1", []string{"public.items"}, DefaultPublicationOptions()); err != nil {
		t.Fatal(err)
	}
	if err := ps.DropPublication("p1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := ps.LookupPublication("p1"); ok {
		t.Errorf("p1 visible after drop")
	}
	if err := ps.DropPublication("p1"); !errors.Is(err, ErrPublicationNotFound) {
		t.Errorf("second drop err=%v want ErrPublicationNotFound", err)
	}
}

// TestPubSubCreateSubscription pins the round-trip and the
// SlotName default (=name when empty).
func TestPubSubCreateSubscription(t *testing.T) {
	ps := NewPubSub()
	sub, err := ps.CreateSubscription("s1", "host=remote dbname=app", []string{"p1", "p2"}, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if sub.SlotName != "s1" {
		t.Errorf("SlotName=%q want s1 (default to subscription name)", sub.SlotName)
	}
	if sub.Conninfo != "host=remote dbname=app" {
		t.Errorf("Conninfo=%q", sub.Conninfo)
	}
	if !reflect.DeepEqual(sub.Publications, []string{"p1", "p2"}) {
		t.Errorf("Publications=%v", sub.Publications)
	}
	if !sub.Enabled {
		t.Errorf("Enabled=false want true")
	}
}

// TestPubSubDuplicateSubscriptionName mirrors the publication
// uniqueness contract.
func TestPubSubDuplicateSubscriptionName(t *testing.T) {
	ps := NewPubSub()
	if _, err := ps.CreateSubscription("s1", "x", nil, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.CreateSubscription("s1", "x", nil, "", true); !errors.Is(err, ErrSubscriptionExists) {
		t.Errorf("err=%v want ErrSubscriptionExists", err)
	}
}
