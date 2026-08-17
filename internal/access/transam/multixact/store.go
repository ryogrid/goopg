package multixact

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/goopg/goopg/internal/storage"
)

// Liveness supplies transaction liveness to the otherwise-pure member store.
//
// Building a MultiXact from an existing one (Expand) drops members that are no
// longer of interest — exactly the running transactions plus any *committed*
// updater (MultiXactIdExpand, multixact.c:545-570). Whether a transaction is
// running or committed is a runtime concern this leaf package deliberately does
// not own (mirroring MembersConflict's injected isCurrent callback); the wiring
// layer supplies it.
//
// A nil field is treated conservatively, matching MembersConflict's nil
// convention: IsInProgress defaults to "yes, still running" (so no member is
// dropped) and DidCommit defaults to "no". A zero Liveness{} therefore keeps
// every member — the liveness-agnostic answer used in tests and by callers that
// pre-filter their member lists.
type Liveness struct {
	IsInProgress func(storage.TransactionID) bool
	DidCommit    func(storage.TransactionID) bool
}

func (l Liveness) isInProgress(xid storage.TransactionID) bool {
	if l.IsInProgress == nil {
		return true
	}
	return l.IsInProgress(xid)
}

func (l Liveness) didCommit(xid storage.TransactionID) bool {
	if l.DidCommit == nil {
		return false
	}
	return l.DidCommit(xid)
}

// ErrMultipleUpdaters is returned when a member set would contain more than one
// updating member. Mirrors the upstream elog(ERROR) in
// MultiXactIdCreateFromMembers (multixact.c:848): a well-formed MultiXact names
// at most one updater (plus any number of pure lockers).
var ErrMultipleUpdaters = errors.New("multixact: new multixact has more than one updating member")

// Store is the in-memory analog of PostgreSQL's MultiXact SLRU
// (pg_multixact/offsets + pg_multixact/members): it hands out monotonically
// increasing MultiXactIds and resolves each back to its immutable member set.
//
// MultiXactIds are immutable by construction — adding a member never mutates an
// existing id, it creates a new one (Expand). This is load-bearing upstream: it
// avoids a race against code waiting for one MultiXactId to finish
// (MultiXactIdExpand header comment, multixact.c:471-474).
//
// Like upstream's per-backend cache (mXactCacheGetBySet), Store re-uses an
// existing MultiXactId when an identical member *set* is requested again, which
// keeps id consumption low when many tuples are locked by the same transactions.
// Two faithful divergences from the SLRU: the dedup cache is global and
// unbounded (upstream's is per-backend, LRU-capped at 256) — deterministic and
// fine at spec/test scale; and ids are never truncated/vacuumed (the
// MultiXactId wraparound machinery is a separate, deferred concern). Neither
// affects member-set semantics.
//
// Store is safe for concurrent use: the hot path that will eventually wire it in
// (stampLockInner) is concurrent.
type Store struct {
	mu    sync.Mutex
	next  MultiXactId
	byID  map[MultiXactId][]Member
	bySet map[string]MultiXactId
}

// NewStore returns an empty Store that hands out MultiXactIds starting at
// FirstMultiXactId.
func NewStore() *Store { return NewStoreAt(FirstMultiXactId) }

// NewStoreAt returns an empty Store whose first handed-out MultiXactId is next
// (clamped up to FirstMultiXactId, since InvalidMultiXactId must never be
// allocated). Use this to seed the allocator from pg_control's nextMulti when
// wiring the store into a running cluster.
func NewStoreAt(next MultiXactId) *Store {
	if next < FirstMultiXactId {
		next = FirstMultiXactId
	}
	return &Store{
		next:  next,
		byID:  make(map[MultiXactId][]Member),
		bySet: make(map[string]MultiXactId),
	}
}

// Next reports the MultiXactId that will be assigned to the next freshly-created
// set (analogous to pg_control's nextMXact). A re-used set does not advance it.
func (s *Store) Next() MultiXactId {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.next
}

// Members returns a copy of the member set composing multi. ok is false when
// multi is unknown to this store (InvalidMultiXactId, an id never handed out, or
// — in a future truncating store — one already vacuumed away). The returned
// slice is a copy: mutating it does not affect the store, and the stored set
// stays immutable.
func (s *Store) Members(multi MultiXactId) (members []Member, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ms, found := s.byID[multi]
	if !found {
		return nil, false
	}
	out := make([]Member, len(ms))
	copy(out, ms)
	return out, true
}

// Create constructs a MultiXactId representing exactly two members. Mirrors
// MultiXactIdCreate (multixact.c:430): the two members must differ in xid or in
// status (an identical pair is a caller bug). Both xids must be valid.
func (s *Store) Create(m1, m2 Member) (MultiXactId, error) {
	if m1.Xid == m2.Xid && m1.Status == m2.Status {
		return InvalidMultiXactId, fmt.Errorf("multixact: Create needs two distinct members, got %v twice", m1)
	}
	return s.CreateFromMembers([]Member{m1, m2})
}

// CreateFromMembers makes a new MultiXactId from the given member set, or
// re-uses an existing id if the identical set was created before (the
// mXactCacheGetBySet behavior). Mirrors MultiXactIdCreateFromMembers
// (multixact.c:811): it validates that at most one member is an updater
// (ErrMultipleUpdaters otherwise) and that every member xid is valid. The input
// slice is not modified (it is copied before sorting).
func (s *Store) CreateFromMembers(members []Member) (MultiXactId, error) {
	if len(members) == 0 {
		return InvalidMultiXactId, errors.New("multixact: cannot create from an empty member set")
	}
	updaters := 0
	for _, m := range members {
		if m.Xid == storage.InvalidTransactionID {
			return InvalidMultiXactId, errors.New("multixact: member has an invalid xid")
		}
		if !m.Status.Valid() {
			return InvalidMultiXactId, fmt.Errorf("multixact: member has an invalid status %d", m.Status)
		}
		if m.Status.IsUpdate() {
			updaters++
		}
	}
	if updaters > 1 {
		return InvalidMultiXactId, ErrMultipleUpdaters
	}

	sorted := sortedMembers(members)
	key := setKey(sorted)

	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.bySet[key]; ok {
		return existing, nil
	}
	id := s.next
	s.next++
	s.byID[id] = sorted
	s.bySet[key] = id
	return id, nil
}

// Expand adds a member to a pre-existing MultiXactId, returning a (usually new)
// MultiXactId. Mirrors MultiXactIdExpand (multixact.c:483):
//
//   - If add is already a member with the same status, multi is returned as-is.
//   - Otherwise a NEW MultiXactId is created (existing ids are never mutated)
//     from the surviving old members plus add. A member survives if it is still
//     in progress, or it is a *committed* updater (an aborted update is dropped;
//     keeping a second updater would violate the single-updater invariant).
//   - If multi has no resolvable members (the obsolete case — every member
//     stopped running between the caller's check and now), a singleton MultiXact
//     of just add is created.
//
// add's xid must be valid; multi must not be InvalidMultiXactId.
func (s *Store) Expand(multi MultiXactId, add Member, live Liveness) (MultiXactId, error) {
	if multi == InvalidMultiXactId {
		return InvalidMultiXactId, errors.New("multixact: Expand of InvalidMultiXactId")
	}
	if add.Xid == storage.InvalidTransactionID {
		return InvalidMultiXactId, errors.New("multixact: Expand with an invalid xid")
	}
	if !add.Status.Valid() {
		return InvalidMultiXactId, fmt.Errorf("multixact: Expand with an invalid status %d", add.Status)
	}

	members, ok := s.Members(multi)
	if !ok || len(members) == 0 {
		// Obsolete: create a singleton (multixact.c:509-527).
		return s.CreateFromMembers([]Member{add})
	}

	// Already a member with the same status: nothing to do (multixact.c:533-543).
	for _, m := range members {
		if m.Xid == add.Xid && m.Status == add.Status {
			return multi, nil
		}
	}

	// Keep members still of interest (multixact.c:561-570).
	survivors := make([]Member, 0, len(members)+1)
	for _, m := range members {
		if live.isInProgress(m.Xid) || (m.Status.IsUpdate() && live.didCommit(m.Xid)) {
			survivors = append(survivors, m)
		}
	}
	survivors = append(survivors, add)
	return s.CreateFromMembers(survivors)
}

// sortedMembers returns a copy of members sorted by (xid, status). Mirrors
// mxactMemberComparator (multixact.c:1652): a plain ascending order (XID
// wraparound order is deliberately NOT used — it violates the triangle
// inequality, breaking qsort). The canonical order makes set comparison a byte
// compare (setKey).
func sortedMembers(members []Member) []Member {
	out := make([]Member, len(members))
	copy(out, members)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Xid != out[j].Xid {
			return out[i].Xid < out[j].Xid
		}
		return out[i].Status < out[j].Status
	})
	return out
}

// setKey encodes an already-sorted member set into a canonical string key so two
// identical sets hash to the same MultiXactId (the mXactCacheGetBySet memcmp on
// the sorted array, multixact.c:1704). Five bytes per member: xid (uint32 LE) +
// status (uint8).
func setKey(sorted []Member) string {
	var b strings.Builder
	b.Grow(len(sorted) * 5)
	var buf [5]byte
	for _, m := range sorted {
		binary.LittleEndian.PutUint32(buf[:4], uint32(m.Xid))
		buf[4] = byte(m.Status)
		b.Write(buf[:])
	}
	return b.String()
}
