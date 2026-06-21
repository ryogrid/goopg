package multixact

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// allStatuses lists the six defined MultiXactStatus values in their numeric
// (on-disk) order, so a table indexed by Status value lines up positionally.
var allStatuses = []Status{
	StatusForKeyShare,
	StatusForShare,
	StatusForNoKeyUpdate,
	StatusForUpdate,
	StatusNoKeyUpdate,
	StatusUpdate,
}

func TestStatusValid(t *testing.T) {
	for _, s := range allStatuses {
		if !s.Valid() {
			t.Errorf("Status(%d) %s should be Valid", s, s)
		}
	}
	if Status(6).Valid() {
		t.Errorf("Status(6) should be invalid (MaxStatus=%d)", MaxStatus)
	}
	if Status(0xFF).Valid() {
		t.Errorf("Status(0xFF) should be invalid")
	}
}

func TestStatusIsUpdate(t *testing.T) {
	// ISUPDATE_from_mxstatus: status > MultiXactStatusForUpdate (0x03), i.e.
	// only NoKeyUpdate and Update are updates; the four pure locks are not.
	want := map[Status]bool{
		StatusForKeyShare:    false,
		StatusForShare:       false,
		StatusForNoKeyUpdate: false,
		StatusForUpdate:      false,
		StatusNoKeyUpdate:    true,
		StatusUpdate:         true,
	}
	for s, exp := range want {
		if got := s.IsUpdate(); got != exp {
			t.Errorf("%s.IsUpdate() = %v, want %v", s, got, exp)
		}
	}
}

func TestStatusString(t *testing.T) {
	cases := map[Status]string{
		StatusForKeyShare:    "ForKeyShare",
		StatusForShare:       "ForShare",
		StatusForNoKeyUpdate: "ForNoKeyUpdate",
		StatusForUpdate:      "ForUpdate",
		StatusNoKeyUpdate:    "NoKeyUpdate",
		StatusUpdate:         "Update",
		Status(99):           "InvalidStatus",
	}
	for s, exp := range cases {
		if got := s.String(); got != exp {
			t.Errorf("Status(%d).String() = %q, want %q", s, got, exp)
		}
	}
}

// expectedConflict is the authoritative 6x6 row-lock compatibility relation,
// written here INDEPENDENTLY of the implementation's lock-mode tables so the
// test genuinely validates the port (a test that re-derives the answer from
// the same tables it checks proves nothing). It is the matrix you get by
// mapping each MultiXactStatus to its heavyweight lock mode and consulting
// upstream's LockConflicts[] — the well-known PostgreSQL row-lock compatibility:
//
//   - FOR KEY SHARE (AccessShare) conflicts ONLY with the strongest writes
//     (FOR UPDATE / UPDATE) — it is compatible with a concurrent NO KEY UPDATE,
//     which is exactly what makes an FK key-lock coexist with a non-key parent
//     update (the update-locked-tuple / lock-committed-keyupdate specs).
//   - FOR SHARE (RowShare) additionally conflicts with any update
//     (NO KEY UPDATE / UPDATE and their lock forms).
//   - NO KEY UPDATE (Exclusive) conflicts with everything except FOR KEY SHARE.
//   - FOR UPDATE / UPDATE (AccessExclusive) conflict with everything.
//
// Indexed [held][req] by Status value (0..5).
var expectedConflict = [6][6]bool{
	//              KS     S      NKU    FU     NKUpd  Upd
	StatusForKeyShare:    {false, false, false, true, false, true},
	StatusForShare:       {false, false, true, true, true, true},
	StatusForNoKeyUpdate: {false, true, true, true, true, true},
	StatusForUpdate:      {true, true, true, true, true, true},
	StatusNoKeyUpdate:    {false, true, true, true, true, true},
	StatusUpdate:         {true, true, true, true, true, true},
}

func TestStatusesConflictMatrix(t *testing.T) {
	for _, held := range allStatuses {
		for _, req := range allStatuses {
			want := expectedConflict[held][req]
			if got := StatusesConflict(held, req); got != want {
				t.Errorf("StatusesConflict(%s, %s) = %v, want %v", held, req, got, want)
			}
		}
	}
}

func TestStatusesConflictSymmetric(t *testing.T) {
	// The lock-conflict relation is symmetric: a held lock blocks a request iff
	// the reverse would also block. Verifying symmetry independently guards
	// against an asymmetric LockConflicts[] transcription error.
	for _, a := range allStatuses {
		for _, b := range allStatuses {
			if StatusesConflict(a, b) != StatusesConflict(b, a) {
				t.Errorf("StatusesConflict not symmetric for (%s,%s): %v vs %v",
					a, b, StatusesConflict(a, b), StatusesConflict(b, a))
			}
		}
	}
}

func TestStatusesConflictKeyShareCompatibleWithNoKeyUpdate(t *testing.T) {
	// The load-bearing case the whole MultiXact subsystem exists to model:
	// a FOR KEY SHARE locker and a concurrent NO KEY UPDATE must coexist on
	// one tuple (FK child holding a key-share lock while the parent's non-key
	// columns are updated). This is precisely why goopg's current single-holder
	// xmax can skip-without-waiting today — and why combining the two requires
	// a multi-member representation.
	if StatusesConflict(StatusForKeyShare, StatusNoKeyUpdate) {
		t.Fatal("FOR KEY SHARE must NOT conflict with NO KEY UPDATE")
	}
	if StatusesConflict(StatusForKeyShare, StatusForNoKeyUpdate) {
		t.Fatal("FOR KEY SHARE must NOT conflict with FOR NO KEY UPDATE")
	}
	// But a key-share locker DOES conflict with a key-changing update/delete.
	if !StatusesConflict(StatusForKeyShare, StatusUpdate) {
		t.Fatal("FOR KEY SHARE must conflict with UPDATE (key change / delete)")
	}
}

func TestMembersConflict(t *testing.T) {
	const (
		xidA storage.TransactionID = 100
		xidB storage.TransactionID = 200
		xidC storage.TransactionID = 300
	)

	t.Run("empty member set never conflicts", func(t *testing.T) {
		if MembersConflict(nil, xidA, StatusForUpdate, nil) {
			t.Error("empty members must not conflict")
		}
	})

	t.Run("conflicting locker blocks request", func(t *testing.T) {
		members := []Member{{Xid: xidA, Status: StatusForShare}}
		// FOR SHARE held vs NO KEY UPDATE requested -> conflict.
		if !MembersConflict(members, xidB, StatusNoKeyUpdate, nil) {
			t.Error("FOR SHARE locker must block a NO KEY UPDATE request")
		}
	})

	t.Run("compatible locker does not block", func(t *testing.T) {
		members := []Member{{Xid: xidA, Status: StatusForKeyShare}}
		// FOR KEY SHARE held vs NO KEY UPDATE requested -> no conflict.
		if MembersConflict(members, xidB, StatusNoKeyUpdate, nil) {
			t.Error("FOR KEY SHARE locker must not block a NO KEY UPDATE request")
		}
	})

	t.Run("self is skipped (lock upgrade)", func(t *testing.T) {
		members := []Member{{Xid: xidA, Status: StatusForUpdate}}
		// Same xid requesting again must never conflict with itself.
		if MembersConflict(members, xidA, StatusForUpdate, nil) {
			t.Error("a transaction must not conflict with its own member")
		}
	})

	t.Run("isCurrent filters finished members", func(t *testing.T) {
		members := []Member{{Xid: xidA, Status: StatusForUpdate}}
		// xidA holds the strongest lock but is reported no-longer-running.
		notRunning := func(x storage.TransactionID) bool { return false }
		if MembersConflict(members, xidB, StatusForKeyShare, notRunning) {
			t.Error("a finished member's lock must be ignored")
		}
		// With xidA still running, the same request conflicts.
		running := func(x storage.TransactionID) bool { return true }
		if !MembersConflict(members, xidB, StatusForKeyShare, running) {
			t.Error("a running FOR UPDATE member must block a FOR KEY SHARE request")
		}
	})

	t.Run("conflict found among multiple members", func(t *testing.T) {
		members := []Member{
			{Xid: xidA, Status: StatusForKeyShare}, // compatible
			{Xid: xidB, Status: StatusForShare},    // the blocker
		}
		if !MembersConflict(members, xidC, StatusNoKeyUpdate, nil) {
			t.Error("a NO KEY UPDATE must conflict when any member is incompatible")
		}
	})
}

func TestGetUpdateXid(t *testing.T) {
	const (
		locker  storage.TransactionID = 100
		updater storage.TransactionID = 200
	)

	t.Run("pure locks have no updater", func(t *testing.T) {
		members := []Member{
			{Xid: locker, Status: StatusForKeyShare},
			{Xid: 101, Status: StatusForShare},
		}
		if xid, ok := GetUpdateXid(members); ok {
			t.Errorf("expected no updater, got xid=%d ok=%v", xid, ok)
		}
	})

	t.Run("finds the no-key updater", func(t *testing.T) {
		members := []Member{
			{Xid: locker, Status: StatusForKeyShare},
			{Xid: updater, Status: StatusNoKeyUpdate},
		}
		xid, ok := GetUpdateXid(members)
		if !ok || xid != updater {
			t.Errorf("GetUpdateXid = (%d,%v), want (%d,true)", xid, ok, updater)
		}
	})

	t.Run("finds the key updater", func(t *testing.T) {
		members := []Member{{Xid: updater, Status: StatusUpdate}}
		xid, ok := GetUpdateXid(members)
		if !ok || xid != updater {
			t.Errorf("GetUpdateXid = (%d,%v), want (%d,true)", xid, ok, updater)
		}
	})

	t.Run("empty returns InvalidTransactionID,false", func(t *testing.T) {
		xid, ok := GetUpdateXid(nil)
		if ok || xid != storage.InvalidTransactionID {
			t.Errorf("GetUpdateXid(nil) = (%d,%v), want (Invalid,false)", xid, ok)
		}
	})
}

func TestHasLockers(t *testing.T) {
	const (
		locker  storage.TransactionID = 100
		updater storage.TransactionID = 200
	)

	t.Run("pure update has no lockers", func(t *testing.T) {
		members := []Member{{Xid: updater, Status: StatusUpdate}}
		if HasLockers(members) {
			t.Error("a pure-update member set must report no lockers")
		}
	})

	t.Run("locker present alongside updater", func(t *testing.T) {
		members := []Member{
			{Xid: locker, Status: StatusForKeyShare},
			{Xid: updater, Status: StatusUpdate},
		}
		if !HasLockers(members) {
			t.Error("a member set with a FOR KEY SHARE locker must report lockers")
		}
	})

	t.Run("pure locks report lockers", func(t *testing.T) {
		members := []Member{
			{Xid: locker, Status: StatusForShare},
			{Xid: 101, Status: StatusForUpdate},
		}
		if !HasLockers(members) {
			t.Error("a pure-lock member set must report lockers")
		}
	})

	t.Run("empty reports no lockers", func(t *testing.T) {
		if HasLockers(nil) {
			t.Error("empty member set must report no lockers")
		}
	})
}

// TestConstantsMatchUpstream pins the load-bearing on-disk numeric values to
// their upstream MultiXactStatus constants (multixact.h). These are the member
// status encoding and the index into MultiXactStatusLock, so a drift here is a
// silent on-disk format change.
func TestConstantsMatchUpstream(t *testing.T) {
	cases := []struct {
		s    Status
		want uint8
	}{
		{StatusForKeyShare, 0x00},
		{StatusForShare, 0x01},
		{StatusForNoKeyUpdate, 0x02},
		{StatusForUpdate, 0x03},
		{StatusNoKeyUpdate, 0x04},
		{StatusUpdate, 0x05},
	}
	for _, c := range cases {
		if uint8(c.s) != c.want {
			t.Errorf("%s = 0x%02x, want 0x%02x", c.s, uint8(c.s), c.want)
		}
	}
	if MaxStatus != StatusUpdate {
		t.Errorf("MaxStatus = %d, want StatusUpdate(%d)", MaxStatus, StatusUpdate)
	}
	if InvalidMultiXactId != 0 {
		t.Errorf("InvalidMultiXactId = %d, want 0", InvalidMultiXactId)
	}
	if FirstMultiXactId != 1 {
		t.Errorf("FirstMultiXactId = %d, want 1", FirstMultiXactId)
	}
}
