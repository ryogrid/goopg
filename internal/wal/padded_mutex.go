package wal

import "sync"

// paddedMutex is a 64-byte cache-line-padded mutex. In an array, adjacent
// stripes occupy distinct cache lines so contending writers do not pay
// coherence traffic on a stripe they did not intend to lock. Mirrors the
// executor-side paddedMutex landed in M0107-0007 slice A; duplicated here
// (and not lifted to a shared package) so the wal package stays
// dependency-free of executor types — the two stripe arrays live in
// different lock-ordering tiers (heap extend vs. WAL append) and a shared
// alias would invite accidental cross-tier coupling.
//
// Foundation for M0107-0007 slice B (Phase D4 — 8-stripe WAL insert locks
// per `docs/design/perf-optimize/07-wal-fsm-insert.md` §2). Dead code
// until slice B's call-site rewrite lifts Writer.appendMu into a
// `[appendLockStripes]paddedMutex` array; foundation-first pattern
// matches slice C ([[0107-0007b]] / [[0107-0007c]] / [[0107-0007d]] all
// landed before [[0107-0007e]] / [[0107-0007f]] / [[0107-0007g]]
// consumed them).
type paddedMutex struct {
	mu sync.Mutex
	_  [56]byte // pad sync.Mutex (8 B) to 64 B (one cache line)
}

// appendLockStripes is the number of WAL append-lock stripes. Matches PG's
// NUM_XLOGINSERT_LOCKS = 8 (see `postgres/src/include/access/xlog.h` and
// `xlog.c` ReserveXLogInsertLocation / WALInsertLockAcquire). Eight
// distinct backends can append into the WAL buffer in parallel as long
// as they hash to different stripes; [[0107-0007k]] insertPosTracker
// handles disjoint LSN reservation and rotateMu serialises the (rare)
// segment-boundary crossings.
const appendLockStripes = 8

// appendLockSet is the per-Writer set of 8 stripe mutexes selected by
// `procNum & (appendLockStripes-1)`. The set is intentionally a value
// type with no embedded Writer fields so the call-site rewrite can
// either embed it directly in Writer or hold a pointer to one; both
// shapes preserve the cache-line padding because the array layout is
// fixed regardless of embedding.
type appendLockSet struct {
	locks [appendLockStripes]paddedMutex
}

// stripeForProcNum maps a procNum to a stripe index. PG uses the same
// `MyProcNumber % NUM_XLOGINSERT_LOCKS` formula (xlog.c, WALInsertLock
// allocation). Exposed so call sites can compute the stripe once for
// trace/log decoration without re-deriving the formula.
func stripeForProcNum(procNum int32) int {
	return int(uint32(procNum) & (appendLockStripes - 1))
}

// lockByProcNum acquires the stripe selected by procNum and returns the
// unlock callback. The stripe lock guards the WAL buffer write for one
// record append; LSN reservation happens via [[0107-0007k]]
// insertPosTracker.reserve so two stripes can be writing into disjoint
// ranges of the buffer at the same time.
//
// Per the design (§2 "Flush coordination"), flush is unchanged — it
// operates on the cumulative buffer content, not per-stripe, and the
// group-commit waiter chain merges across all stripes naturally because
// LSNs are globally ordered. So lockByProcNum has no flush-side
// coupling to worry about; it is the simple "serialise byte-writes
// into this stripe's notional buffer slice" primitive.
func (s *appendLockSet) lockByProcNum(procNum int32) (unlock func()) {
	stripe := stripeForProcNum(procNum)
	s.locks[stripe].mu.Lock()
	return s.locks[stripe].mu.Unlock
}
