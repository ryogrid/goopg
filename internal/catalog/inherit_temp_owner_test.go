package catalog

import "testing"

// TestAccessibleInheritanceChildren verifies the RELATION_IS_OTHER_TEMP
// exclusion helper that underpins the inherit-temp isolation spec (design
// 0118-0036): an inheritance/partition parent's child list, when expanded for
// a given session, must drop temporary children owned by *other* sessions while
// retaining permanent children, the session's own temp children, and unowned
// (session-less) temp children.
func TestAccessibleInheritanceChildren(t *testing.T) {
	perm := &Table{OID: 1, Name: "perm_child"}
	myTemp := &Table{OID: 2, Name: "temp_s1", Temp: true, TempOwner: "s1"}
	otherTemp := &Table{OID: 3, Name: "temp_s2", Temp: true, TempOwner: "s2"}
	unownedTemp := &Table{OID: 4, Name: "temp_anon", Temp: true, TempOwner: ""}

	t.Run("nil in nil out", func(t *testing.T) {
		if got := AccessibleInheritanceChildren(nil, "s1"); got != nil {
			t.Fatalf("expected nil, got %v", got)
		}
	})

	t.Run("excludes other-session temp", func(t *testing.T) {
		in := []*Table{perm, myTemp, otherTemp, unownedTemp}
		got := AccessibleInheritanceChildren(in, "s1")
		want := []*Table{perm, myTemp, unownedTemp}
		if len(got) != len(want) {
			t.Fatalf("got %d children, want %d: %+v", len(got), len(want), names(got))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("child %d = %s, want %s", i, got[i].Name, want[i].Name)
			}
		}
	})

	t.Run("from the other session keeps its own temp, drops s1 temp", func(t *testing.T) {
		in := []*Table{perm, myTemp, otherTemp, unownedTemp}
		got := AccessibleInheritanceChildren(in, "s2")
		want := []*Table{perm, otherTemp, unownedTemp}
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", names(got), names(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("child %d = %s, want %s", i, got[i].Name, want[i].Name)
			}
		}
	})

	t.Run("no temp children returns the same backing slice", func(t *testing.T) {
		in := []*Table{perm}
		got := AccessibleInheritanceChildren(in, "s1")
		if len(got) != 1 || got[0] != perm {
			t.Fatalf("unexpected result %v", names(got))
		}
	})

	t.Run("session with no identity sees only owned-by-nobody temps", func(t *testing.T) {
		// A querying session with an empty owner token must still exclude
		// children that *are* owned by some other session.
		in := []*Table{perm, otherTemp, unownedTemp}
		got := AccessibleInheritanceChildren(in, "")
		want := []*Table{perm, unownedTemp}
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", names(got), names(want))
		}
	})
}

func names(ts []*Table) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Name
	}
	return out
}
