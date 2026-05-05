package mvcc

import (
	"fmt"

	"github.com/goopg/goopg/internal/storage"
)

// SubTxnId is a monotonic identifier for a subtransaction within a
// single top-level transaction. It resets to 0 at the start of each
// top-level Begin. The identifier is used by the lock manager to track
// which subtransaction holds a lock — on RELEASE the lock is promoted
// to the parent; on ROLLBACK TO the lock is dropped.
type SubTxnId uint32

// SubXactStatus tracks the lifecycle state of a subtransaction.
type SubXactStatus int

const (
	SubXactActive    SubXactStatus = iota // currently open
	SubXactCommitted                      // RELEASE SAVEPOINT — merged into parent
	SubXactAborted                        // ROLLBACK TO or implicit abort
)

func (s SubXactStatus) String() string {
	switch s {
	case SubXactActive:
		return "active"
	case SubXactCommitted:
		return "committed"
	case SubXactAborted:
		return "aborted"
	default:
		return fmt.Sprintf("SubXactStatus(%d)", int(s))
	}
}

// SubTransactionState holds the state of one subtransaction (savepoint
// level) within a top-level transaction. It is stack-allocated by
// SubxactStack.Push and owned by the SubxactStack.
//
// Thread safety: a SubxactStack is owned by a single session goroutine;
// it is not safe for concurrent use.
type SubTransactionState struct {
	// Id is a monotonic counter scoped to the top-level transaction.
	// It resets to 1 for the first savepoint of each transaction.
	// The lock manager uses this to identify the lock owner.
	Id SubTxnId

	// Name is the SAVEPOINT name, or "" for an implicit subtransaction.
	Name string

	// SubXid is the XID assigned to this subtransaction, or 0 when no
	// write has been performed yet (lazy allocation — mirrors upstream's
	// AssignTransactionId behaviour). The SubXid is allocated by the
	// surrounding Manager when the first heap mutation occurs inside the
	// subtransaction.
	SubXid storage.TransactionID

	// Snap is the snapshot captured at SAVEPOINT push time. On
	// ROLLBACK TO this snapshot is restored as the effective snapshot
	// for the fresh re-pushed entry so that reads after the rollback
	// are consistent with the savepoint's visibility.
	Snap *Snapshot

	// Status transitions: active → committed (RELEASE) or aborted
	// (ROLLBACK TO or top-level abort).
	Status SubXactStatus

	// Parent points to the enclosing SubTransactionState, or nil when
	// this entry is a direct child of the top-level transaction.
	Parent *SubTransactionState
}

// SubxactStack manages the ordered list of subtransaction states for
// one top-level transaction. Index 0 of the internal slice is the
// oldest (outermost) savepoint; the top of the stack (innermost) is
// the last element.
//
// All methods are safe to call on a nil *SubxactStack — a nil stack
// behaves as if no savepoints exist, making the zero value of a session
// valid before any SAVEPOINT is issued.
type SubxactStack struct {
	entries   []*SubTransactionState
	nextSubId SubTxnId
}

// Len returns the number of entries currently on the stack (includes
// already-committed or already-aborted entries that haven't been
// physically popped yet; in practice Push/Release/RollbackTo keep the
// slice tidy).
func (s *SubxactStack) Len() int {
	if s == nil {
		return 0
	}
	return len(s.entries)
}

// Top returns the innermost active subtransaction, or nil when no
// active savepoint exists.
func (s *SubxactStack) Top() *SubTransactionState {
	if s == nil {
		return nil
	}
	for i := len(s.entries) - 1; i >= 0; i-- {
		if s.entries[i].Status == SubXactActive {
			return s.entries[i]
		}
	}
	return nil
}

// All returns a copy of all entries, oldest-first.
func (s *SubxactStack) All() []*SubTransactionState {
	if s == nil {
		return nil
	}
	out := make([]*SubTransactionState, len(s.entries))
	copy(out, s.entries)
	return out
}

// Push creates a new savepoint entry with the given name and the
// supplied snapshot, appends it to the stack, and returns it.
// The caller owns the returned pointer and must not mutate Status
// except through the Release/RollbackTo methods.
func (s *SubxactStack) Push(name string, snap Snapshot) *SubTransactionState {
	s.nextSubId++
	var parent *SubTransactionState
	if len(s.entries) > 0 {
		parent = s.entries[len(s.entries)-1]
	}
	snapCopy := snap
	e := &SubTransactionState{
		Id:     s.nextSubId,
		Name:   name,
		Snap:   &snapCopy,
		Status: SubXactActive,
		Parent: parent,
	}
	s.entries = append(s.entries, e)
	return e
}

// Find returns the index and entry for the innermost active savepoint
// with the given name. Returns (-1, nil) when not found.
func (s *SubxactStack) Find(name string) (int, *SubTransactionState) {
	if s == nil {
		return -1, nil
	}
	for i := len(s.entries) - 1; i >= 0; i-- {
		e := s.entries[i]
		if e.Status == SubXactActive && e.Name == name {
			return i, e
		}
	}
	return -1, nil
}

// Release marks the named savepoint and all savepoints above it
// (inner) as committed and removes them from the stack.
//
// Callers MUST transfer the locks acquired by each released entry
// to its parent before calling Release, or call Release and then
// inspect the returned entries to perform the transfer.
//
// Returns the released entries (outermost first) or an error when
// the named savepoint does not exist.
func (s *SubxactStack) Release(name string) ([]*SubTransactionState, error) {
	idx, _ := s.Find(name)
	if idx < 0 {
		return nil, fmt.Errorf("savepoint %q does not exist", name)
	}
	released := make([]*SubTransactionState, len(s.entries)-idx)
	copy(released, s.entries[idx:])
	for _, e := range released {
		e.Status = SubXactCommitted
	}
	s.entries = s.entries[:idx]
	return released, nil
}

// RollbackTo aborts the named savepoint and all above it, then pushes
// a fresh entry with the same name and the provided snapshot (mirroring
// PostgreSQL's "rollback, then push a new subxact" semantics so
// subsequent mutations in the same savepoint name still work).
//
// Callers MUST release the locks held exclusively by each aborted
// entry before calling RollbackTo, or inspect AbortedEntries after.
//
// Returns (abortedEntries, freshEntry, error).
// abortedEntries are ordered outermost-first.
func (s *SubxactStack) RollbackTo(name string, newSnap Snapshot) ([]*SubTransactionState, *SubTransactionState, error) {
	idx, _ := s.Find(name)
	if idx < 0 {
		return nil, nil, fmt.Errorf("savepoint %q does not exist", name)
	}
	aborted := make([]*SubTransactionState, len(s.entries)-idx)
	copy(aborted, s.entries[idx:])
	for _, e := range aborted {
		e.Status = SubXactAborted
	}
	s.entries = s.entries[:idx]
	// Push a fresh entry so the savepoint name remains usable.
	fresh := s.Push(name, newSnap)
	return aborted, fresh, nil
}

// AbortAll marks every entry on the stack as aborted. Called when the
// top-level transaction is rolled back. Returns all entries so callers
// can release their locks.
func (s *SubxactStack) AbortAll() []*SubTransactionState {
	if s == nil || len(s.entries) == 0 {
		return nil
	}
	all := make([]*SubTransactionState, len(s.entries))
	for i, e := range s.entries {
		e.Status = SubXactAborted
		all[i] = e
	}
	s.entries = s.entries[:0]
	return all
}
