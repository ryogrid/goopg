package wal

// publishTail monotonically advances the buffer's tail to safeTail.
// Returns the resulting tail value (post-update). No-op if
// safeTail <= b.tail. Does NOT mutate head or base — drain is solely
// responsible for head/base advances via advanceHead.
//
// Foundation 10 for M0107-0007 slice B (Phase D4 — 8-stripe WAL
// insert locks per `docs/design/perf-optimize/07-wal-fsm-insert.md`
// §2). Bytes-side mirror of [[0107-0007o]] MemRing.PublishUpTo: under
// the stripe-concurrent writer model a stripe lands bytes via
// writeReserved ([[0107-0007l]]) without advancing tail; tail catches
// up only after every reservation < safeTail has been fully
// byte-written by its owning stripe, as computed by tailPublisher
// ([[0107-0007n]]) and fed here by the drain goroutine.
//
// Why no head-eviction. Unlike MemRing — which writers overwrite as
// the LSN window advances — walBuffer's head can only advance after
// the drain confirms the bytes have been persisted to a segment file
// via writeAt + advanceHead. Auto-evicting on overflow would lose
// pending writes. The contract therefore requires the caller to keep
// (safeTail - b.head) <= b.cap by draining first; the slice B
// call-site rewrite satisfies this the same way Path A does today
// (overflow-drain before append).
//
// Monotonic by construction. Two drain goroutines (or a drain
// catching up after a previous tailPublisher.publishUpTo) calling
// publishTail with regressing safeTail leave tail unchanged. The
// caller is expected to derive safeTail from tailPublisher.publishUpTo
// (already monotonic), so a regressing value typically reflects a
// fresh buffer (b.tail at startLSN) or a stale snapshot; either way
// silent no-op is the right response — matches MemRing.PublishUpTo.
//
// PG counterpart. Mirrors PG's pattern in xlog.c where the flush
// coordinator advances `XLogCtl->LogwrtResult.Write` after
// `WaitXLogInsertionsToFinish` returns; downstream readers
// (`drainBufferBytes` / `readForDrain` / `readAt`) consult the
// published tail before consuming bytes.
//
// Nil receiver returns 0. Matches the nil-safe convention from
// [[0107-0007l]] walBuffer.writeReserved and [[0107-0007o]]
// MemRing.PublishUpTo, letting a future `Writer` constructor leave
// the buffer unset under `Config.WALBuffers == 0` without per-call
// guards at the publication call site.
//
// Concurrent safety. Like the rest of walBuffer today, this
// primitive does NOT take a lock; the caller is responsible for
// synchronisation. Under the slice B call-site rewrite, tail will
// be upgraded to `atomic.Int64` so a single drain goroutine's
// publishTail and concurrent stripe writers' readers (resident /
// readForDrain / readAt) coexist without a data race. That
// atomicity upgrade is a separate follow-on foundation; this
// primitive lands the API first so the call-site rewrite can wire
// it in alongside the upgrade. Under today's single-goroutine
// usage (state.append holding appendMu), publishTail is trivially
// safe.
//
// Lock-ordering tier (leaf publisher; the publisher never reaches
// back up the chain):
//
//	(drain goroutine, after stripe writers complete:)
//	  tailPublisher.publishUpTo(upperBound, insertionTracker)
//	  walBuffer.publishTail(published)        ← here (publisher side)
//	  walBuffer.advanceHead(published - prior)
//	  MemRing.PublishUpTo(published)
func (b *walBuffer) publishTail(safeTail int64) int64 {
	if b == nil {
		return 0
	}
	if safeTail <= b.tail {
		return b.tail
	}
	b.tail = safeTail
	return b.tail
}
