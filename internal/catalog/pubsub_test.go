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

// TestSubscriptionRelStateMachineHappyPath pins the canonical
// upstream tablesync flow: i → d → s → r. Each AdvanceSubscriptionRel
// returns the freshly-updated row with state + LSN bumped.
func TestSubscriptionRelStateMachineHappyPath(t *testing.T) {
	ps := NewPubSub()
	if _, err := ps.CreateSubscription("s1", "x", []string{"p"}, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.AddSubscriptionRel("s1", 16400); err != nil {
		t.Fatal(err)
	}
	got, ok := ps.LookupSubscriptionRel("s1", 16400)
	if !ok || got.State != SubRelStateInit {
		t.Errorf("initial state=%+v want %q", got, SubRelStateInit)
	}

	// i → d
	if _, err := ps.AdvanceSubscriptionRel("s1", 16400, SubRelStateDataCopy, 0); err != nil {
		t.Fatal(err)
	}
	// d → s with an LSN
	if r, err := ps.AdvanceSubscriptionRel("s1", 16400, SubRelStateSyncDone, 0xCAFE); err != nil {
		t.Fatal(err)
	} else if r.State != SubRelStateSyncDone || r.LSN != 0xCAFE {
		t.Errorf("after sync done: %+v", r)
	}
	// s → r
	if r, err := ps.AdvanceSubscriptionRel("s1", 16400, SubRelStateReady, 0xDEAD); err != nil {
		t.Fatal(err)
	} else if r.State != SubRelStateReady || r.LSN != 0xDEAD {
		t.Errorf("after ready: %+v", r)
	}
}

// TestSubscriptionRelStateMachineRejectsBackwards: every
// reversal (e.g. r → s) and every illegal jump (e.g. i → r)
// surfaces ErrInvalidSubRelStateTransition. Mirrors upstream's
// monotonic state-machine guarantee.
func TestSubscriptionRelStateMachineRejectsBackwards(t *testing.T) {
	ps := NewPubSub()
	if _, err := ps.CreateSubscription("s1", "x", nil, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.AddSubscriptionRel("s1", 1); err != nil {
		t.Fatal(err)
	}
	// i → r (illegal jump)
	if _, err := ps.AdvanceSubscriptionRel("s1", 1, SubRelStateReady, 0); !errors.Is(err, ErrInvalidSubRelStateTransition) {
		t.Errorf("i → r err=%v want ErrInvalidSubRelStateTransition", err)
	}
	// Walk forward to r, then try to go back.
	for _, st := range []string{SubRelStateDataCopy, SubRelStateSyncDone, SubRelStateReady} {
		if _, err := ps.AdvanceSubscriptionRel("s1", 1, st, 0); err != nil {
			t.Fatal(err)
		}
	}
	// r → d (reversal)
	if _, err := ps.AdvanceSubscriptionRel("s1", 1, SubRelStateDataCopy, 0); !errors.Is(err, ErrInvalidSubRelStateTransition) {
		t.Errorf("r → d err=%v want ErrInvalidSubRelStateTransition", err)
	}
}

// TestSubscriptionRelInitToSyncDoneShortcut: upstream allows
// `i → s` when the table was already empty at slot start so
// the data-copy phase is skipped. v0 honours that shortcut.
func TestSubscriptionRelInitToSyncDoneShortcut(t *testing.T) {
	ps := NewPubSub()
	if _, err := ps.CreateSubscription("s1", "x", nil, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.AddSubscriptionRel("s1", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.AdvanceSubscriptionRel("s1", 1, SubRelStateSyncDone, 0xBEEF); err != nil {
		t.Errorf("i → s shortcut rejected: %v", err)
	}
}

// TestSubscriptionRelLSNMonotonic: LSN never goes backwards
// even when a caller passes a smaller value.
func TestSubscriptionRelLSNMonotonic(t *testing.T) {
	ps := NewPubSub()
	if _, err := ps.CreateSubscription("s1", "x", nil, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.AddSubscriptionRel("s1", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.AdvanceSubscriptionRel("s1", 1, SubRelStateDataCopy, 0xCAFE); err != nil {
		t.Fatal(err)
	}
	r, err := ps.AdvanceSubscriptionRel("s1", 1, SubRelStateDataCopy, 0x1)
	if err != nil {
		t.Fatal(err)
	}
	if r.LSN != 0xCAFE {
		t.Errorf("LSN=%x want 0xCAFE (rewind rejected)", r.LSN)
	}
}

// TestSubscriptionRelDropSubscriptionClearsRels: dropping a
// subscription drops all its tablesync rows so a re-CREATE
// under the same name doesn't inherit stale state.
func TestSubscriptionRelDropSubscriptionClearsRels(t *testing.T) {
	ps := NewPubSub()
	if _, err := ps.CreateSubscription("s1", "x", nil, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.AddSubscriptionRel("s1", 1); err != nil {
		t.Fatal(err)
	}
	if err := ps.DropSubscription("s1"); err != nil {
		t.Fatal(err)
	}
	if rels := ps.SubscriptionRels("s1"); len(rels) != 0 {
		t.Errorf("after drop, rels=%v want empty", rels)
	}
}

// TestAddSubscriptionRelDuplicate pins the uniqueness contract
// for (subName, relOID).
func TestAddSubscriptionRelDuplicate(t *testing.T) {
	ps := NewPubSub()
	if _, err := ps.CreateSubscription("s1", "x", nil, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.AddSubscriptionRel("s1", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.AddSubscriptionRel("s1", 1); !errors.Is(err, ErrSubscriptionRelExists) {
		t.Errorf("err=%v want ErrSubscriptionRelExists", err)
	}
}
