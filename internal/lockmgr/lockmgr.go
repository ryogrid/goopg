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
package lockmgr

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
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
	var g Mask
	for h, m := range s.holders {
		if h == b {
			continue
		}
		g |= m
	}
	return g
}

// LockManager is the lock table. Use New to construct.
type LockManager struct {
	mu              sync.Mutex
	states          map[LockTag]*lockState
	deadlockTimeout time.Duration
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
	if len(st.waiters) == 0 && !ConflictsWith(m, st.grantedExcept(b)) {
		st.holders[b] |= bit(m)
		st.granted |= bit(m)
		return nil
	}
	return ErrLockNotAvailable
}

func (lm *LockManager) Acquire(ctx context.Context, b BackendID, t LockTag, m Mode) error {
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
	// No conflict and no waiters in front? Grant immediately.
	// If any waiters are queued, falling in behind them preserves
	// FIFO fairness so a strong waiter doesn't get starved by a
	// stream of compatible-with-current-holders newcomers.
	if len(st.waiters) == 0 && !ConflictsWith(m, st.grantedExcept(b)) {
		st.holders[b] |= bit(m)
		st.granted |= bit(m)
		lm.mu.Unlock()
		return nil
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
	timeout := lm.deadlockTimeout
	lm.mu.Unlock()

	// Schedule a deadlock check after the configured timeout.
	// Idempotent and cheap; concurrent fires serialise on lm.mu.
	var timer *time.Timer
	if timeout > 0 {
		timer = time.AfterFunc(timeout, lm.runDeadlockCheck)
		defer timer.Stop()
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
	case <-ctx.Done():
		// Splice ourselves out of the queue if we're still there;
		// a racing wake-pass may have already promoted us.
		lm.mu.Lock()
		st := lm.states[t]
		if st != nil {
			for i, q := range st.waiters {
				if q == w {
					st.waiters = append(st.waiters[:i], st.waiters[i+1:]...)
					break
				}
			}
			// If a Release promoted us between the ctx cancel and
			// our taking lm.mu, the holder bit is set — release it
			// to keep state consistent with the cancelled return.
			if st.holders[b]&bit(m) != 0 {
				st.holders[b] &^= bit(m)
				if st.holders[b] == 0 {
					delete(st.holders, b)
				}
				st.recomputeGranted()
				lm.wakePassLocked(t, st)
			}
			lm.gcLocked(t, st)
		}
		lm.mu.Unlock()
		return ctx.Err()
	}
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
