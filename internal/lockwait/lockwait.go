// Package lockwait carries a session's `lock_timeout` budget down through the
// low-level lock-wait primitives (the heavyweight lock manager and the
// transaction-XID waiter) via the request context, and defines the sentinel
// error those primitives return when the budget elapses.
//
// PostgreSQL's lock_timeout (GUC, proc.c) aborts any statement that waits
// longer than the configured interval *while attempting to acquire a lock*,
// raising "canceling statement due to lock timeout". It is distinct from
// statement_timeout, which is measured from statement start regardless of
// whether the backend is waiting. Because the two clocks differ, the wait
// primitives must be able to tell which one fired so the dispatcher can emit
// the matching message; lock_timeout therefore travels as its own value
// rather than collapsing into the statement context's deadline.
//
// The budget is carried as a duration (not an absolute deadline) so each
// individual lock wait re-arms its timer from the moment that wait begins,
// mirroring upstream's enable_timeout_after(LOCK_TIMEOUT) at the top of
// ProcSleep.
package lockwait

import (
	"context"
	"errors"
	"time"
)

// ErrLockTimeout is returned by a lock-wait primitive when the session's
// lock_timeout elapses before the lock is granted. Callers map it to
// SQLSTATE 57014 with the message "canceling statement due to lock timeout".
var ErrLockTimeout = errors.New("canceling statement due to lock timeout")

type ctxKey struct{}

// WithTimeout returns a context carrying a lock_timeout budget of d. A
// non-positive d removes any budget (lock_timeout disabled), matching the
// GUC's "0 = off" semantics.
func WithTimeout(ctx context.Context, d time.Duration) context.Context {
	if d <= 0 {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, d)
}

// Timeout reports the lock_timeout budget attached to ctx, if any. The
// returned duration is always positive when ok is true.
func Timeout(ctx context.Context) (d time.Duration, ok bool) {
	if ctx == nil {
		return 0, false
	}
	v, ok := ctx.Value(ctxKey{}).(time.Duration)
	if !ok || v <= 0 {
		return 0, false
	}
	return v, true
}
