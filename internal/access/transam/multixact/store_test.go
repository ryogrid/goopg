package multixact

import (
	"errors"
	"sync"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

func mem(xid storage.TransactionID, st Status) Member { return Member{Xid: xid, Status: st} }

func TestStoreAllocatesFromFirstMultiXactId(t *testing.T) {
	s := NewStore()
	if got := s.Next(); got != FirstMultiXactId {
		t.Fatalf("fresh store Next() = %d, want %d", got, FirstMultiXactId)
	}
	id1, err := s.Create(mem(100, StatusForKeyShare), mem(101, StatusNoKeyUpdate))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id1 != FirstMultiXactId {
		t.Fatalf("first id = %d, want %d", id1, FirstMultiXactId)
	}
	// A distinct set advances the allocator.
	id2, err := s.Create(mem(200, StatusForShare), mem(201, StatusForUpdate))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id2 != FirstMultiXactId+1 {
		t.Fatalf("second id = %d, want %d", id2, FirstMultiXactId+1)
	}
	if got := s.Next(); got != FirstMultiXactId+2 {
		t.Fatalf("Next() after two creates = %d, want %d", got, FirstMultiXactId+2)
	}
}

func TestNewStoreAtClampsBelowFirst(t *testing.T) {
	// InvalidMultiXactId (0) must never be allocated.
	s := NewStoreAt(InvalidMultiXactId)
	if got := s.Next(); got != FirstMultiXactId {
		t.Fatalf("NewStoreAt(0) Next() = %d, want %d", got, FirstMultiXactId)
	}
	s2 := NewStoreAt(500)
	if got := s2.Next(); got != 500 {
		t.Fatalf("NewStoreAt(500) Next() = %d, want 500", got)
	}
}

func TestStoreMembersRoundTripSorted(t *testing.T) {
	s := NewStore()
	// Pass members out of order; Members must come back canonically sorted.
	id, err := s.CreateFromMembers([]Member{
		mem(300, StatusForUpdate),
		mem(100, StatusForKeyShare),
		mem(100, StatusForShare),
	})
	if err != nil {
		t.Fatalf("CreateFromMembers: %v", err)
	}
	got, ok := s.Members(id)
	if !ok {
		t.Fatalf("Members(%d) not found", id)
	}
	want := []Member{
		mem(100, StatusForKeyShare),
		mem(100, StatusForShare),
		mem(300, StatusForUpdate),
	}
	if len(got) != len(want) {
		t.Fatalf("members len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("member[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestStoreMembersUnknownID(t *testing.T) {
	s := NewStore()
	if _, ok := s.Members(InvalidMultiXactId); ok {
		t.Fatalf("Members(Invalid) reported ok")
	}
	if _, ok := s.Members(12345); ok {
		t.Fatalf("Members(never-handed-out) reported ok")
	}
}

func TestStoreMembersReturnsCopy(t *testing.T) {
	s := NewStore()
	id, err := s.Create(mem(100, StatusForKeyShare), mem(101, StatusForShare))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, _ := s.Members(id)
	got[0].Xid = 999 // mutate the returned slice
	again, _ := s.Members(id)
	if again[0].Xid == 999 {
		t.Fatalf("mutating the returned slice corrupted the store")
	}
}

func TestStoreDedupIdenticalSet(t *testing.T) {
	s := NewStore()
	id1, err := s.Create(mem(100, StatusForKeyShare), mem(200, StatusNoKeyUpdate))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Same set, opposite argument order -> same MultiXactId, allocator unmoved.
	id2, err := s.Create(mem(200, StatusNoKeyUpdate), mem(100, StatusForKeyShare))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("identical set got different ids %d != %d", id1, id2)
	}
	if got := s.Next(); got != FirstMultiXactId+1 {
		t.Fatalf("re-used set advanced allocator to %d, want %d", got, FirstMultiXactId+1)
	}
}

func TestStoreDistinctSetsGetDistinctIDs(t *testing.T) {
	s := NewStore()
	id1, _ := s.Create(mem(100, StatusForKeyShare), mem(200, StatusForShare))
	// Differs only in one member's status -> a different set -> a different id.
	id2, _ := s.Create(mem(100, StatusForKeyShare), mem(200, StatusForUpdate))
	if id1 == id2 {
		t.Fatalf("distinct sets shared id %d", id1)
	}
}

func TestCreateRejectsIdenticalPair(t *testing.T) {
	s := NewStore()
	if _, err := s.Create(mem(100, StatusForKeyShare), mem(100, StatusForKeyShare)); err == nil {
		t.Fatalf("Create of an identical member pair should error")
	}
	// Same xid, different status is allowed (lock then update by one xact).
	if _, err := s.Create(mem(100, StatusForKeyShare), mem(100, StatusNoKeyUpdate)); err != nil {
		t.Fatalf("Create(same xid, different status): %v", err)
	}
}

func TestCreateFromMembersRejectsMultipleUpdaters(t *testing.T) {
	s := NewStore()
	_, err := s.CreateFromMembers([]Member{
		mem(100, StatusNoKeyUpdate),
		mem(200, StatusUpdate),
	})
	if !errors.Is(err, ErrMultipleUpdaters) {
		t.Fatalf("two updaters: err = %v, want ErrMultipleUpdaters", err)
	}
	// One updater plus any number of pure lockers is fine.
	if _, err := s.CreateFromMembers([]Member{
		mem(100, StatusForKeyShare),
		mem(200, StatusForShare),
		mem(300, StatusUpdate),
	}); err != nil {
		t.Fatalf("one updater + lockers: %v", err)
	}
}

func TestCreateFromMembersRejectsInvalidInput(t *testing.T) {
	s := NewStore()
	if _, err := s.CreateFromMembers(nil); err == nil {
		t.Fatalf("empty set should error")
	}
	if _, err := s.CreateFromMembers([]Member{mem(storage.InvalidTransactionID, StatusForShare)}); err == nil {
		t.Fatalf("invalid xid should error")
	}
	if _, err := s.CreateFromMembers([]Member{{Xid: 100, Status: Status(0x7f)}}); err == nil {
		t.Fatalf("invalid status should error")
	}
}

func TestCreateFromMembersDoesNotMutateInput(t *testing.T) {
	s := NewStore()
	in := []Member{
		mem(300, StatusForUpdate),
		mem(100, StatusForKeyShare),
	}
	if _, err := s.CreateFromMembers(in); err != nil {
		t.Fatalf("CreateFromMembers: %v", err)
	}
	if in[0] != mem(300, StatusForUpdate) || in[1] != mem(100, StatusForKeyShare) {
		t.Fatalf("CreateFromMembers sorted the caller's slice in place: %v", in)
	}
}

func TestExpandAddsMemberAsNewID(t *testing.T) {
	s := NewStore()
	id1, err := s.Create(mem(100, StatusForKeyShare), mem(200, StatusForKeyShare))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Both original lockers still in progress, plus a new no-key updater.
	live := Liveness{IsInProgress: func(storage.TransactionID) bool { return true }}
	id2, err := s.Expand(id1, mem(300, StatusNoKeyUpdate), live)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if id2 == id1 {
		t.Fatalf("Expand returned the original id; must mint a new one")
	}
	// id1 is immutable — still its original 2 members.
	if m, _ := s.Members(id1); len(m) != 2 {
		t.Fatalf("original id mutated: %v", m)
	}
	got, _ := s.Members(id2)
	want := []Member{
		mem(100, StatusForKeyShare),
		mem(200, StatusForKeyShare),
		mem(300, StatusNoKeyUpdate),
	}
	if len(got) != len(want) {
		t.Fatalf("expanded members = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expanded member[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestExpandAlreadyMemberReturnsSameID(t *testing.T) {
	s := NewStore()
	id1, _ := s.Create(mem(100, StatusForKeyShare), mem(200, StatusForShare))
	id2, err := s.Expand(id1, mem(100, StatusForKeyShare), Liveness{})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if id2 != id1 {
		t.Fatalf("re-adding an existing member changed id %d -> %d", id1, id2)
	}
	if got := s.Next(); got != FirstMultiXactId+1 {
		t.Fatalf("no-op Expand advanced allocator to %d", got)
	}
}

func TestExpandDropsFinishedLockerKeepsCommittedUpdater(t *testing.T) {
	s := NewStore()
	// A committed updater (xid 100) and a finished pure locker (xid 200).
	id1, err := s.CreateFromMembers([]Member{
		mem(100, StatusNoKeyUpdate),
		mem(200, StatusForKeyShare),
	})
	if err != nil {
		t.Fatalf("CreateFromMembers: %v", err)
	}
	live := Liveness{
		IsInProgress: func(xid storage.TransactionID) bool { return false }, // both finished
		DidCommit:    func(xid storage.TransactionID) bool { return xid == 100 },
	}
	// New locker 300 joins. The finished pure locker (200) is dropped; the
	// committed updater (100) survives; 300 is added.
	id2, err := s.Expand(id1, mem(300, StatusForShare), live)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	got, _ := s.Members(id2)
	want := []Member{
		mem(100, StatusNoKeyUpdate),
		mem(300, StatusForShare),
	}
	if len(got) != len(want) {
		t.Fatalf("survivors = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("survivor[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestExpandDropsAbortedUpdater(t *testing.T) {
	s := NewStore()
	id1, _ := s.CreateFromMembers([]Member{
		mem(100, StatusNoKeyUpdate), // will be reported aborted
		mem(200, StatusForKeyShare), // still running
	})
	live := Liveness{
		IsInProgress: func(xid storage.TransactionID) bool { return xid == 200 },
		DidCommit:    func(storage.TransactionID) bool { return false },
	}
	// New key-revoking update 300 joins; aborted updater 100 must be dropped so
	// the resulting multixact has only one updater.
	id2, err := s.Expand(id1, mem(300, StatusUpdate), live)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	got, _ := s.Members(id2)
	want := []Member{
		mem(200, StatusForKeyShare),
		mem(300, StatusUpdate),
	}
	if len(got) != len(want) {
		t.Fatalf("survivors = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("survivor[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestExpandObsoleteCreatesSingleton(t *testing.T) {
	s := NewStore()
	// A valid id never handed out by this store: the obsolete path.
	id, err := s.Expand(9999, mem(100, StatusForUpdate), Liveness{})
	if err != nil {
		t.Fatalf("Expand obsolete: %v", err)
	}
	got, _ := s.Members(id)
	if len(got) != 1 || got[0] != mem(100, StatusForUpdate) {
		t.Fatalf("obsolete expand = %v, want singleton {100,Update}", got)
	}
}

func TestExpandNilLivenessKeepsAllMembers(t *testing.T) {
	s := NewStore()
	id1, _ := s.CreateFromMembers([]Member{
		mem(100, StatusForKeyShare),
		mem(200, StatusForShare),
	})
	// Zero Liveness{}: conservative, keep everyone.
	id2, _ := s.Expand(id1, mem(300, StatusForUpdate), Liveness{})
	got, _ := s.Members(id2)
	if len(got) != 3 {
		t.Fatalf("nil-liveness expand dropped members: %v", got)
	}
}

func TestExpandRejectsInvalidInput(t *testing.T) {
	s := NewStore()
	if _, err := s.Expand(InvalidMultiXactId, mem(100, StatusForShare), Liveness{}); err == nil {
		t.Fatalf("Expand of InvalidMultiXactId should error")
	}
	id, _ := s.Create(mem(100, StatusForKeyShare), mem(200, StatusForShare))
	if _, err := s.Expand(id, mem(storage.InvalidTransactionID, StatusForShare), Liveness{}); err == nil {
		t.Fatalf("Expand with invalid xid should error")
	}
}

func TestStoreConcurrentCreate(t *testing.T) {
	s := NewStore()
	const goroutines = 16
	const perG = 64
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(base int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				x := storage.TransactionID(base*1000 + i)
				if _, err := s.Create(mem(x, StatusForKeyShare), mem(x+100000, StatusForShare)); err != nil {
					t.Errorf("concurrent Create: %v", err)
					return
				}
			}
		}(g + 1)
	}
	wg.Wait()
	// Every (base,i) pair is a distinct set, so the allocator advanced exactly
	// goroutines*perG times with no lost/duplicated ids.
	wantNext := FirstMultiXactId + MultiXactId(goroutines*perG)
	if got := s.Next(); got != wantNext {
		t.Fatalf("after concurrent creates Next() = %d, want %d", got, wantNext)
	}
}
