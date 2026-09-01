// Package lockmgr is goopg's PostgreSQL-style SQL lock manager.
//
// Scope and growth path are documented in
// docs/design/0012-0001-lock-manager-architecture.md. v0 covers the
// core data structures, mode compatibility, and Acquire/Release/
// ReleaseAll surface — wait-for-graph deadlock detection
// (M0012-0002) and executor integration (M0012-0003) build on top.
//
// The implementation tracks holders and waiters per relation-level
// LockTag and grants/blocks based on the upstream conflict table
// (postgres/src/backend/storage/lmgr/lock.c LockConflicts[]). The
// API is goroutine-safe under a single coarse mutex; per-tag
// striping is a future optimisation.
package lmgr

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goopg/goopg/internal/storage/lmgr/lockwait"
)

// ErrDeadlockDetected is returned by Acquire when the deadlock
// detector picked the calling backend as the cycle victim. Higher
// layers translate this to SQLSTATE 40P01 (deadlock_detected). See
// docs/design/0012-0002-deadlock-detection-algorithm.md.
var ErrDeadlockDetected = errors.New("lockmgr: deadlock detected")

// DefaultDeadlockTimeout matches upstream's `deadlock_timeout` GUC
// default (1s). The detector fires once a parked Acquire has been
// blocked at least this long.
const DefaultDeadlockTimeout = time.Second

// Mode is one of upstream's eight lock modes. The numeric values
// match upstream's lockdefs.h so log lines and future SQL views can
// share the vocabulary without translation tables.
type Mode int

const (
	// 0 is unused so Mode-1 indexing matches upstream's 1-based
	// numbering in the conflict matrix.
	NoLock Mode = iota
	AccessShareLock
	RowShareLock
	RowExclusiveLock
	ShareUpdateExclusiveLock
	ShareLock
	ShareRowExclusiveLock
	ExclusiveLock
	AccessExclusiveLock

	maxMode = AccessExclusiveLock
)

// Mask is a bitmask over Mode values. Mask[i] (i ∈ 1..8) corresponds
// to mode i. Wider than uint8 so the conflict-tab loop is straight
// `mask & (1 << mode)`.
type Mask uint16

// bit returns the bitmask for one mode. Mode 0 (NoLock) maps to 0
// so it never satisfies a conflict check.
func bit(m Mode) Mask {
	if m <= NoLock || m > maxMode {
		return 0
	}
	return 1 << uint(m)
}

// modeNames mirrors upstream's lock_mode_names array, slot 0 unused.
var modeNames = [...]string{
	"INVALID",
	"AccessShareLock",
	"RowShareLock",
	"RowExclusiveLock",
	"ShareUpdateExclusiveLock",
	"ShareLock",
	"ShareRowExclusiveLock",
	"ExclusiveLock",
	"AccessExclusiveLock",
}

func (m Mode) String() string {
	if m < 0 || int(m) >= len(modeNames) {
		return fmt.Sprintf("Mode(%d)", int(m))
	}
	return modeNames[m]
}

// ParseMode maps an upstream lock-mode name (the canonical internal
// spelling, e.g. "AccessExclusiveLock") back to its Mode. Returns
// (NoLock, false) for an unrecognised name. The SQL parser emits
// exactly these names for `LOCK TABLE ... IN <mode> MODE`, so the
// executor can round-trip the requested mode into the lock manager
// without a second translation table.
func ParseMode(name string) (Mode, bool) {
	for i := 1; i < len(modeNames); i++ {
		if modeNames[i] == name {
			return Mode(i), true
		}
	}
	return NoLock, false
}

// conflictTab is the bitmask of modes a given mode conflicts with.
// Index = mode (1..8); slot 0 unused. Taken verbatim from
// postgres/src/backend/storage/lmgr/lock.c LockConflicts[].
var conflictTab = [maxMode + 1]Mask{
	0, // NoLock unused
	// AccessShareLock
	bit(AccessExclusiveLock),
	// RowShareLock
	bit(ExclusiveLock) | bit(AccessExclusiveLock),
	// RowExclusiveLock
	bit(ShareLock) | bit(ShareRowExclusiveLock) |
		bit(ExclusiveLock) | bit(AccessExclusiveLock),
	// ShareUpdateExclusiveLock
	bit(ShareUpdateExclusiveLock) |
		bit(ShareLock) | bit(ShareRowExclusiveLock) |
		bit(ExclusiveLock) | bit(AccessExclusiveLock),
	// ShareLock
	bit(RowExclusiveLock) | bit(ShareUpdateExclusiveLock) |
		bit(ShareRowExclusiveLock) |
		bit(ExclusiveLock) | bit(AccessExclusiveLock),
	// ShareRowExclusiveLock
	bit(RowExclusiveLock) | bit(ShareUpdateExclusiveLock) |
		bit(ShareLock) | bit(ShareRowExclusiveLock) |
		bit(ExclusiveLock) | bit(AccessExclusiveLock),
	// ExclusiveLock
	bit(RowShareLock) |
		bit(RowExclusiveLock) | bit(ShareUpdateExclusiveLock) |
		bit(ShareLock) | bit(ShareRowExclusiveLock) |
		bit(ExclusiveLock) | bit(AccessExclusiveLock),
	// AccessExclusiveLock
	bit(AccessShareLock) | bit(RowShareLock) |
		bit(RowExclusiveLock) | bit(ShareUpdateExclusiveLock) |
		bit(ShareLock) | bit(ShareRowExclusiveLock) |
		bit(ExclusiveLock) | bit(AccessExclusiveLock),
}

// ConflictsWith reports whether holding any mode in `held` would
// block a request for mode `m`. Exposed so the upcoming deadlock
// detector (M0012-0002) can ask the same question off the lock
// table.
func ConflictsWith(m Mode, held Mask) bool {
	if m <= NoLock || m > maxMode {
		return false
	}
	return conflictTab[m]&held != 0
}

// LockTag identifies a relation-level lock target. Forks are
// intentionally collapsed: a relation lock covers heap and indexes
// alike, matching upstream.
type LockTag struct {
	DB  uint32
	Rel uint32
	// Block + Offset extend a relation lock tag down to a specific
	// heap tuple (M0021 tuple-level locking step 2b). Both zero
	// → relation-level lock (the historic default; every existing
	// caller continues to work because Go's struct-literal
	// zero-initialises unset fields). Both non-zero → tuple-level
	// lock; the (DB, Rel) tag and the (DB, Rel, Block, Offset)
	// tag are independent map keys, so a relation-level holder
	// doesn't accidentally block tuple-level acquirers and
	// vice-versa. Mirrors upstream's separation between
	// `LOCKTAG_RELATION` and `LOCKTAG_TUPLE`.
	Block  uint32
	Offset uint32
}

// BackendID identifies a session/transaction holding or waiting for
// locks. The lock manager is agnostic about how callers mint these
// — the executor's transaction shell will own assignment.
type BackendID uint32

// Waiter is one queued lock request. Stored by *Waiter so cancel
// can splice it out of the slice by pointer identity.
//
// `signal` fires when the wake-pass promotes this waiter to a
// holder. `victim` fires when the deadlock detector selects this
// waiter as the cycle victim — distinct from `signal` so the
// Acquire goroutine can return ErrDeadlockDetected instead of
// silently succeeding.
type Waiter struct {
	Backend BackendID
	Mode    Mode
	tag     LockTag
	signal  chan struct{}
	victim  chan struct{}
}

// lockState is the per-tag holder + waiter view. Allocated lazily on
// first Acquire and torn down when both holders and waiters are
// empty after a release.
type lockState struct {
	holders map[BackendID]Mask
	waiters []*Waiter
	granted Mask // OR of all holder masks; cached for fast conflict checks
	// strongCounted records whether this tag currently contributes to
	// lm.strongCounts, so syncStrongLocked can pair its increments exactly.
	strongCounted bool
}

func (s *lockState) recomputeGranted() {
	var g Mask
	for _, m := range s.holders {
		g |= m
	}
	s.granted = g
}

// grantedExcept returns the OR of holder masks excluding backend `b`.
// Self-conflict is not a thing — a backend can hold every mode it
// likes on a tag it already holds. The "except self" mask is what
// gets fed into ConflictsWith for the requesting backend.
func (s *lockState) grantedExcept(b BackendID) Mask {
	// review/260831 ST-14: when b holds nothing on this tag there is nothing
	// to exclude, and `granted` — the cached OR every acquire and release
	// keeps current — is already the answer. That is the common case: a
	// backend asking about a tag it does not yet hold. Only a self-holder pays
	// the walk over the holder map.
	if _, isHolder := s.holders[b]; !isHolder {
		return s.granted
	}
	if len(s.holders) == 1 {
		return 0 // b is the only holder
	}
	var g Mask
	for h, m := range s.holders {
		if h == b {
			continue
		}
		g |= m
	}
	return g
}

// canGrantImmediately reports whether a request for mode `m` by backend
// `b` can be satisfied without blocking. It captures two upstream cases:
//
//  1. No conflict with other backends' holdings and no waiter queued —
//     the plain immediate grant in LockAcquireExtended (lock.c).
//  2. `b` already holds a lock on the tag and would be inserted ahead of
//     a waiter whose requested mode conflicts with what `b` holds, and
//     the new request conflicts neither with the requests ahead of that
//     insertion point nor with any other backend's current holdings —
//     the "special case" early grant in JoinWaitQueue (proc.c). This is
//     what lets a backend that already holds, say, AccessExclusive take
//     a weaker self-compatible mode immediately even while a conflicting
//     EXCLUSIVE waiter is parked ahead of it (the lock-nowait spec).
//
// Caller must hold lm.mu and must already have handled the "already hold
// m exactly" no-op. The deadlock sub-case from JoinWaitQueue (the ahead
// waiter holds a lock that conflicts with `m`) is subsumed by the
// grantedExcept conflict check: that holder's mode is in `others`, so we
// simply decline and let the normal wait path / deadlock detector run.
func (s *lockState) canGrantImmediately(b BackendID, m Mode) bool {
	others := s.grantedExcept(b)
	if len(s.waiters) == 0 {
		return !ConflictsWith(m, others)
	}
	// Early-grant special case only applies when b already holds a lock
	// on this tag (upstream: `myHeldLocks != 0`).
	myHeld := s.holders[b]
	if myHeld == 0 {
		return false
	}
	var aheadRequests Mask
	for _, w := range s.waiters {
		if w.Backend == b {
			continue
		}
		// Must this waiter wait for me? (its requested mode conflicts
		// with a lock I already hold). If so, I'd be inserted ahead of
		// it; grant now iff I conflict with neither the requests ahead
		// of the insertion point nor any other backend's holdings.
		if ConflictsWith(w.Mode, myHeld) {
			return !ConflictsWith(m, aheadRequests) && !ConflictsWith(m, others)
		}
		aheadRequests |= bit(w.Mode)
	}
	return false
}

// hasSimpleDeadlock reports whether enqueueing a request for mode `m` by
// backend `b` (which does not already hold `m`) would constitute an
// immediate "simple" deadlock — the special case upstream's JoinWaitQueue
// (proc.c) resolves synchronously instead of waiting for deadlock_timeout.
//
// The cycle: `b` already holds a lock here; an earlier waiter `w` is parked
// whose requested mode conflicts with what `b` holds (so `w` must wait for
// `b`); and `b`'s requested mode `m` conflicts with what `w` already holds
// (so `b` must wait for `w`). Neither can complete its upgrade — a deadlock
// that PostgreSQL detects the instant the second upgrader joins the queue
// (the deadlock-simple isolation spec). The requester `b` is the victim.
//
// Mirrors the JoinWaitQueue scan exactly: only the first waiter that "must
// wait for me" decides the outcome (deadlock / early-grant / ordinary wait),
// so we return at that waiter. Caller must hold lm.mu.
func (s *lockState) hasSimpleDeadlock(b BackendID, m Mode) bool {
	myHeld := s.holders[b]
	if myHeld == 0 || len(s.waiters) == 0 {
		return false
	}
	for _, w := range s.waiters {
		if w.Backend == b {
			continue
		}
		// Must this waiter wait for me? (its requested mode conflicts with a
		// lock I already hold). The first such waiter is my insertion point;
		// it is a deadlock iff I must also wait for it — i.e. my requested
		// mode conflicts with a lock it already holds.
		if ConflictsWith(w.Mode, myHeld) {
			return ConflictsWith(m, s.holders[w.Backend])
		}
	}
	return false
}

// LockManager is the lock table. Use New to construct.
type LockManager struct {
	mu              sync.Mutex
	states          map[LockTag]*lockState
	deadlockTimeout time.Duration

	// strongCounts is the FastPathStrongRelationLocks->count[] analogue; see
	// fastpath.go. Read without lm.mu, mutated only under it.
	strongCounts [fastPathStrongBuckets]atomic.Int32
}

// New returns an empty lock manager with the default deadlock
// timeout (DefaultDeadlockTimeout).
func New() *LockManager {
	return &LockManager{
		states:          make(map[LockTag]*lockState),
		deadlockTimeout: DefaultDeadlockTimeout,
	}
}

// SetDeadlockTimeout tunes how long Acquire waits before
// scheduling a deadlock check. Tests use a small value so cycles
// are detected without a real-time-second pause; production uses
// the default.
func (lm *LockManager) SetDeadlockTimeout(d time.Duration) {
	lm.mu.Lock()
	lm.deadlockTimeout = d
	lm.mu.Unlock()
}

// Acquire attempts to take mode `m` on tag `t` for backend `b`.
// Blocks until granted or `ctx` is cancelled.
//
// If the request conflicts with the current granted-mask-minus-self,
// the caller is enqueued FIFO and parked on a buffered signal chan;
// a future Release wake-pass promotes it to a holder and signals.
// Cancellation splices the waiter out of the queue with no leaked
// state.
//
// Acquire is idempotent over (b, t, m): asking for a mode you
// already hold is a no-op and returns nil immediately. Higher-level
// reference-counting is the caller's job.

// ErrLockNotAvailable is the typed sentinel TryAcquire returns when
// the requested mode would have to block. Callers translate it into
// SQLSTATE 55P03 ("could not obtain lock on row in relation X").
var ErrLockNotAvailable = errors.New("lockmgr: could not obtain lock immediately")

// TryAcquire is the non-blocking variant of Acquire used by SELECT
// FOR UPDATE NOWAIT (M0021-0003 follow-up). On grant it returns
// nil; on contention (or any queued waiter ahead) it returns
// ErrLockNotAvailable instead of waiting. The fast path is
// otherwise byte-identical to Acquire's first branch — same
// idempotency, same FIFO fairness rule (don't grant past queued
// waiters even when there's no current holder conflict). Locks
// granted via TryAcquire are tracked in the same state and
// released identically by Release / ReleaseAll.
func (lm *LockManager) TryAcquire(b BackendID, t LockTag, m Mode) error {
	if m <= NoLock || m > maxMode {
		return fmt.Errorf("lockmgr: invalid mode %d", int(m))
	}
	lm.mu.Lock()
	defer lm.mu.Unlock()
	st := lm.states[t]
	if st == nil {
		st = &lockState{holders: make(map[BackendID]Mask)}
		lm.states[t] = st
	}
	// Already hold this mode? No-op (mirrors Acquire).
	if st.holders[b]&bit(m) != 0 {
		return nil
	}
	if st.canGrantImmediately(b, m) {
		st.holders[b] |= bit(m)
		st.granted |= bit(m)
		lm.syncStrongLocked(t, st)
		return nil
	}
	return ErrLockNotAvailable
}

func (lm *LockManager) Acquire(ctx context.Context, b BackendID, t LockTag, m Mode) error {
	return lm.acquire(ctx, b, t, m, useConfiguredTimeout)
}

// AcquireWithTimeout is Acquire with a per-call deadlock timeout. LOCK
// TABLE uses it so each session's `deadlock_timeout` GUC governs how
// long that session waits before running the detector — the spec suite
// relies on the session with the shortest timeout discovering (and being
// rolled back for) the cycle. A timeout <= 0 disables the auto-fire timer
// for this acquire. M0118-0004.
func (lm *LockManager) AcquireWithTimeout(ctx context.Context, b BackendID, t LockTag, m Mode, timeout time.Duration) error {
	return lm.acquire(ctx, b, t, m, timeout)
}

// useConfiguredTimeout is the sentinel passed to acquire to make it fall
// back to the manager-wide deadlockTimeout field (read under lm.mu).
const useConfiguredTimeout time.Duration = -1

// UseConfiguredTimeout is the exported form of the useConfiguredTimeout
// sentinel, for callers of AcquireWithTimeout that want the manager-wide
// deadlock_timeout rather than a per-call value (e.g. the executor's tuple
// lock helper on its test-path manager).
const UseConfiguredTimeout = useConfiguredTimeout

func (lm *LockManager) acquire(ctx context.Context, b BackendID, t LockTag, m Mode, timeout time.Duration) error {
	if m <= NoLock || m > maxMode {
		return fmt.Errorf("lockmgr: invalid mode %d", int(m))
	}
	lm.mu.Lock()
	st := lm.states[t]
	if st == nil {
		st = &lockState{holders: make(map[BackendID]Mask)}
		lm.states[t] = st
	}
	// Already hold this mode? No-op, no double-grant in the mask.
	if st.holders[b]&bit(m) != 0 {
		lm.mu.Unlock()
		return nil
	}
	// Grant immediately when there's no conflict and no waiter to fall
	// in behind, or when this backend already holds a lock here and may
	// jump ahead of a conflicting waiter (canGrantImmediately captures
	// both upstream cases). Otherwise queueing behind waiters preserves
	// FIFO fairness so a strong waiter doesn't get starved by a stream
	// of compatible-with-current-holders newcomers.
	if st.canGrantImmediately(b, m) {
		st.holders[b] |= bit(m)
		st.granted |= bit(m)
		lm.syncStrongLocked(t, st)
		lm.mu.Unlock()
		return nil
	}
	// Simple deadlock: `b` already holds a lock here and would queue behind a
	// conflicting waiter that itself conflicts with `b`'s held locks. Upstream
	// (JoinWaitQueue) resolves this synchronously rather than parking and
	// waiting for deadlock_timeout. `b` is the victim; release its other
	// holdings so the surviving upgrader can proceed (mirrors the timeout
	// victim path's ReleaseAll), then report the deadlock.
	if st.hasSimpleDeadlock(b, m) {
		lm.mu.Unlock()
		lm.ReleaseAll(b)
		return ErrDeadlockDetected
	}
	// Conflict: enqueue and block.
	w := &Waiter{
		Backend: b,
		Mode:    m,
		tag:     t,
		signal:  make(chan struct{}, 1),
		victim:  make(chan struct{}, 1),
	}
	st.waiters = append(st.waiters, w)
	if timeout == useConfiguredTimeout {
		timeout = lm.deadlockTimeout
	}
	lm.mu.Unlock()

	// Schedule a deadlock check after the configured timeout.
	// Idempotent and cheap; concurrent fires serialise on lm.mu. The
	// check prefers THIS backend as the victim when it is part of a
	// cycle — mirroring PostgreSQL, where the backend whose
	// deadlock_timeout expires runs DeadLockCheck and is rolled back.
	var timer *time.Timer
	if timeout > 0 {
		timer = time.AfterFunc(timeout, func() { lm.runDeadlockCheckFor(b) })
		defer timer.Stop()
	}

	// Arm the session's lock_timeout, if set. Like upstream's
	// enable_timeout_after(LOCK_TIMEOUT) at the top of ProcSleep, the timer
	// starts when THIS wait begins; expiry aborts the acquire with the
	// dedicated lock-timeout sentinel (distinct from a statement_timeout,
	// which arrives through ctx.Done()). M0118-0009 (timeouts).
	var lockTimeoutCh <-chan time.Time
	if d, ok := lockwait.Timeout(ctx); ok {
		lt := time.NewTimer(d)
		defer lt.Stop()
		lockTimeoutCh = lt.C
	}

	select {
	case <-w.signal:
		// Promoted to holder by Release's wake-pass.
		return nil
	case <-w.victim:
		// Selected by the deadlock detector. The detector has
		// already spliced us out of the queue and dropped any
		// holder bit it might have momentarily set. Release any
		// other holdings this backend has so survivors can make
		// progress without waiting for an explicit ReleaseAll.
		lm.ReleaseAll(b)
		return ErrDeadlockDetected
	case <-lockTimeoutCh:
		// lock_timeout elapsed while parked. Unpark with the same
		// bookkeeping as a cancellation and report the lock-timeout
		// sentinel so the executor emits "canceling statement due to
		// lock timeout".
		lm.unparkWaiter(t, w, b, m)
		return lockwait.ErrLockTimeout
	case <-ctx.Done():
		lm.unparkWaiter(t, w, b, m)
		return ctx.Err()
	}
}

// unparkWaiter removes waiter w (backend b's pending request for mode m on
// tag t) from the wait queue after the wait was abandoned (ctx cancellation
// or lock_timeout). It is splice-and-repair: if a racing Release promoted us
// to holder between the wakeup and our taking lm.mu, the momentarily-set
// holder bit is dropped and the queue re-pumped so the abandoned grant does
// not strand the next waiter.
func (lm *LockManager) unparkWaiter(t LockTag, w *Waiter, b BackendID, m Mode) {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	st := lm.states[t]
	if st == nil {
		return
	}
	for i, q := range st.waiters {
		if q == w {
			st.waiters = append(st.waiters[:i], st.waiters[i+1:]...)
			break
		}
	}
	if st.holders[b]&bit(m) != 0 {
		st.holders[b] &^= bit(m)
		if st.holders[b] == 0 {
			delete(st.holders, b)
		}
		st.recomputeGranted()
		lm.syncStrongLocked(t, st)
		lm.wakePassLocked(t, st)
	}
	lm.gcLocked(t, st)
}

// Release drops mode `m` from backend `b`'s holdings on tag `t`.
// Triggers the wake-pass which may promote one or more waiters.
//
// Release is a no-op if the backend doesn't hold that mode — keeps
// double-release crashes out of the executor's commit/abort path.
func (lm *LockManager) Release(b BackendID, t LockTag, m Mode) {
	if m <= NoLock || m > maxMode {
		return
	}
	lm.mu.Lock()
	defer lm.mu.Unlock()
	st := lm.states[t]
	if st == nil {
		return
	}
	if st.holders[b]&bit(m) == 0 {
		return
	}
	st.holders[b] &^= bit(m)
	if st.holders[b] == 0 {
		delete(st.holders, b)
	}
	st.recomputeGranted()
	lm.syncStrongLocked(t, st)
	lm.wakePassLocked(t, st)
	lm.gcLocked(t, st)
}

// ReleaseAll drops every mode held by backend `b` on every tag. The
// executor calls this at txn commit / rollback so partial-release
// bugs surface as visible held locks rather than ghost entries.
func (lm *LockManager) ReleaseAll(b BackendID) {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	for t, st := range lm.states {
		if _, ok := st.holders[b]; !ok {
			continue
		}
		delete(st.holders, b)
		st.recomputeGranted()
		lm.syncStrongLocked(t, st)
		lm.wakePassLocked(t, st)
		lm.gcLocked(t, st)
	}
}

// wakePassLocked walks the waiter queue head-first; promotes each
// waiter whose mode no longer conflicts with the rest of the
// granted mask. Stops at the first head that can't be promoted —
// strict head-of-line FIFO is the simplest starvation-safe wake
// rule for v0.
//
// Caller must hold lm.mu.
func (lm *LockManager) wakePassLocked(_ LockTag, st *lockState) {
	for len(st.waiters) > 0 {
		w := st.waiters[0]
		others := st.grantedExcept(w.Backend)
		if ConflictsWith(w.Mode, others) {
			return
		}
		st.holders[w.Backend] |= bit(w.Mode)
		st.granted |= bit(w.Mode)
		lm.syncStrongLocked(w.tag, st)
		st.waiters = st.waiters[1:]
		// Buffered chan of size 1 so the send is non-blocking even
		// if the waiter goroutine hasn't reached its select yet.
		select {
		case w.signal <- struct{}{}:
		default:
		}
	}
}

// gcLocked deletes empty lockState entries so the table doesn't
// accumulate dust over a long-running cluster's worth of relations.
//
// Caller must hold lm.mu.
func (lm *LockManager) gcLocked(t LockTag, st *lockState) {
	if len(st.holders) == 0 && len(st.waiters) == 0 {
		delete(lm.states, t)
	}
}

// Holders returns the per-backend mode masks currently granted on
// `t`. Snapshot — caller may inspect freely. Used by tests and
// (future) `pg_locks` observability.
func (lm *LockManager) Holders(t LockTag) map[BackendID]Mask {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	st := lm.states[t]
	if st == nil {
		return nil
	}
	out := make(map[BackendID]Mask, len(st.holders))
	for k, v := range st.holders {
		out[k] = v
	}
	return out
}

// Waiters returns a snapshot of the FIFO waiter queue for `t`. Used
// by tests and the upcoming wait-for-graph deadlock detector.
func (lm *LockManager) Waiters(t LockTag) []Waiter {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	st := lm.states[t]
	if st == nil {
		return nil
	}
	out := make([]Waiter, len(st.waiters))
	for i, w := range st.waiters {
		out[i] = Waiter{Backend: w.Backend, Mode: w.Mode}
	}
	return out
}

// LockHolding is one (backend, mode) lock entry on a tag — the
// enumeration unit the pg_locks view consumes. A backend's holder Mask
// is expanded into one LockHolding per set mode bit (Granted=true); each
// queued waiter becomes one LockHolding with Granted=false. Tuple-level
// tags (Block/Offset non-zero) are included; callers that want only
// relation locks filter on Tag.Block==0 && Tag.Offset==0.
type LockHolding struct {
	Tag     LockTag
	Backend BackendID
	Mode    Mode
	Granted bool
}

// AllLocks returns every currently-tracked lock holding and waiter across
// all tags, one entry per (backend, mode). It is the read-side analog of
// upstream's GetLockStatusData() (lock.c) that backs the pg_locks view:
// holders surface as Granted=true (one row per mode bit in the Mask, so a
// backend holding several self-compatible modes on a tag yields several
// rows, matching upstream's one-LOCK-per-mode accounting), waiters as
// Granted=false. Ordering is unspecified (map iteration) — callers sort.
//
// Read-only: takes the manager lock, copies out, holds nothing. The
// returned slice is owned by the caller.
func (lm *LockManager) AllLocks() []LockHolding {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	var out []LockHolding
	for tag, st := range lm.states {
		if st == nil {
			continue
		}
		for b, mask := range st.holders {
			for m := AccessShareLock; m <= maxMode; m++ {
				if mask&bit(m) != 0 {
					out = append(out, LockHolding{Tag: tag, Backend: b, Mode: m, Granted: true})
				}
			}
		}
		for _, w := range st.waiters {
			out = append(out, LockHolding{Tag: tag, Backend: w.Backend, Mode: w.Mode, Granted: false})
		}
	}
	return out
}
