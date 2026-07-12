package wal

import "sync"

// walWriteLock is the goopg analog of PostgreSQL's WALWriteLock with
// LWLockAcquireOrWait semantics: it guards all WAL segment write+fdatasync,
// and its acquire has a tri-state result so that a losing committer, once the
// current holder releases, re-checks the durable LSN and usually returns with
// zero I/O (emergent group commit — see docs/design/wal-backend-flush/ 03 §3.1).
//
// Design (03 §3.1): a plain sync.Mutex whose fast-path TryLock is the "acquire",
// plus a swap-on-release generation channel that is closed once per release so
// a parked waiter wakes on the next release without holding the lock.
//
// Missed-wakeup safety. Generations advance only in release(). A waiter
// captures the current gen channel under genMu, then TryLocks; if TryLock
// fails, the mutex is locked or an acquisition is pending, so a future
// release() runs and closes whichever gen is current — and the captured gen
// stays current until some release closes exactly it. Capture and the
// close+swap are both genMu critical sections, so a waiter can never capture a
// channel that has already been swapped out (and will therefore never close).
// A release between capture and the (failed) second TryLock only makes the
// select wake immediately. No lost wakeup; no permanent block.
type walWriteLock struct {
	mu    sync.Mutex    // the exclusive WAL-write lock itself
	genMu sync.Mutex    // guards the generation-channel swap
	gen   chan struct{} // closed once per release ("one flush completed")
}

func newWALWriteLock() *walWriteLock {
	return &walWriteLock{gen: make(chan struct{})}
}

// acquireOrWait attempts to take the lock.
//
//	(true,  nil): the caller now holds the lock and MUST call release().
//	(false, nil): a release happened while the caller waited; the caller holds
//	              NOTHING and must re-check shared state (the durable LSN) and,
//	              if still not covered, loop and call acquireOrWait again.
//	(false, err): closed was signalled (writer shutting down); ErrClosed.
//
// closed is the Writer's done channel; select-ing on it lets a parked waiter
// wake on shutdown instead of blocking forever.
func (l *walWriteLock) acquireOrWait(closed <-chan struct{}) (held bool, err error) {
	if l.mu.TryLock() {
		return true, nil
	}
	// Capture the current generation BEFORE the retry, under genMu, so the
	// captured channel cannot be swapped out between capture and park.
	l.genMu.Lock()
	ch := l.gen
	l.genMu.Unlock()
	if l.mu.TryLock() {
		return true, nil
	}
	select {
	case <-ch:
		return false, nil
	case <-closed:
		return false, ErrClosed
	}
}

// release unlocks the lock and wakes every waiter parked on the current
// generation, then swaps in a fresh generation for the next holders.
func (l *walWriteLock) release() {
	l.mu.Unlock()
	l.genMu.Lock()
	close(l.gen)
	l.gen = make(chan struct{})
	l.genMu.Unlock()
}
