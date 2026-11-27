package catalog

import "testing"

// TestSubscriptionRelHookJournalsEveryChange pins M0149-0061m: every
// tablesync state change — add, advance, drop, and DROP SUBSCRIPTION — is
// handed to the journal hook (pg_subscription_rel durability), outside the
// registry lock (the hook may read the registry).
func TestSubscriptionRelHookJournalsEveryChange(t *testing.T) {
	ps := NewPubSub()
	if _, err := ps.CreateSubscription("s1", "x", []string{"p"}, "", true); err != nil {
		t.Fatal(err)
	}
	type ev struct {
		rel     uint32
		state   string
		deleted bool
	}
	var got []ev
	ps.SetSubscriptionRelHook(func(sub Subscription, sr SubscriptionRel, deleted bool) {
		if _, ok := ps.LookupSubscription(sub.Name); !ok && !deleted {
			t.Errorf("hook for %q: subscription not visible", sub.Name)
		}
		got = append(got, ev{sr.RelOID, sr.State, deleted})
	})
	if _, err := ps.AddSubscriptionRel("s1", 10); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.AddSubscriptionRel("s1", 11); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.AdvanceSubscriptionRel("s1", 10, SubRelStateDataCopy, 0); err != nil {
		t.Fatal(err)
	}
	if err := ps.DropSubscriptionRel("s1", 11); err != nil {
		t.Fatal(err)
	}
	if err := ps.DropSubscription("s1"); err != nil {
		t.Fatal(err)
	}
	want := []ev{{10, SubRelStateInit, false}, {11, SubRelStateInit, false}, {10, SubRelStateDataCopy, false}, {11, SubRelStateInit, true}, {0, "", true}}
	if len(got) != len(want) {
		t.Fatalf("hook events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d = %v, want %v", i, got[i], want[i])
		}
	}
	// A restored row (startup reload) does not re-journal.
	ps.RestoreSubscriptionRel("s2", SubscriptionRel{RelOID: 5, State: SubRelStateReady})
	if len(got) != len(want) {
		t.Errorf("RestoreSubscriptionRel fired the hook")
	}
}
