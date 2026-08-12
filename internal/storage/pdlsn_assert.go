package storage

import (
	"fmt"
	"os"
	"runtime"
	"sync"
)

// M0131-S26, deferred item 1 of docs/design/0131-0033: a mechanised guard for
// the pd_lsn completeness invariant.
//
// The invariant is "a page mutated by a WAL-logged action must end the call
// carrying an LSN at or past the record that describes the mutation". The pool
// cannot check that directly — plain MarkDirty is handed a slot and told
// nothing about whether its caller emitted a record — so the guard checks the
// contrapositive that IS observable at the pool boundary:
//
//	in a runtime whose WAL hooks are wired, no page mutation should reach the
//	pool through *plain* MarkDirty.
//
// Every mutation in such a runtime is either logged (and must use one of the
// stamping variants: MarkDirtyWithLSN*, MarkDirtyChangeRecord,
// MarkDirtyLogicalChange, MarkDirtyForceFPI, MarkDirtyCoveredByRecordLocked) or
// deliberately unlogged (MarkDirtyUnlogged, which names its reason), or a hint
// (MarkDirtyHint*, exempt by contract — hint bits are recomputable and PG does
// not log them either). Plain MarkDirty survives only as the hook-nil fallback
// of helpers that pick a logging variant when their own logXxx hook is wired —
// class A of the audit table — and those fallbacks are unreachable once the
// runtime is fully wired. So a report here is exactly the set of call sites
// that inspection has to classify, and an empty report over a real workload is
// the audit's evidence rather than a claim.
//
// The check is report-only (never panics — the pool is on the hot path of every
// backend) and gated on GOOPG_PDLSN_ASSERT=1, read once at package init so the
// cost when unset is one predictable branch. Each caller PC is reported once:
// the interesting output is the SET of sites, and a hot site would otherwise
// drown it.
var pdlsnAssertEnabled = os.Getenv("GOOPG_PDLSN_ASSERT") == "1"

// PDLSNAssertEnabled reports whether the pd_lsn completeness guard is active.
func PDLSNAssertEnabled() bool { return pdlsnAssertEnabled }

var (
	pdlsnAssertMu    sync.Mutex
	pdlsnAssertSeen  = map[uintptr]bool{}
	pdlsnAssertSink  func(string)
	pdlsnAssertCount int
)

// SetPDLSNAssertSinkForTest redirects (and enables) the guard's reports so a
// test can assert on them instead of scraping stderr. It returns a restore
// function. Not for production use: the guard is stderr-only in a real server,
// where its consumer is a human reading the log after a workload run.
func SetPDLSNAssertSinkForTest(sink func(string)) func() {
	pdlsnAssertMu.Lock()
	prevSink, prevEnabled := pdlsnAssertSink, pdlsnAssertEnabled
	prevSeen := pdlsnAssertSeen
	pdlsnAssertSink = sink
	pdlsnAssertEnabled = sink != nil
	pdlsnAssertSeen = map[uintptr]bool{}
	pdlsnAssertMu.Unlock()
	return func() {
		pdlsnAssertMu.Lock()
		pdlsnAssertSink, pdlsnAssertEnabled, pdlsnAssertSeen = prevSink, prevEnabled, prevSeen
		pdlsnAssertMu.Unlock()
	}
}

// reportUnstampedMarkDirty records one plain-MarkDirty call made against a pool
// with WAL logging wired. skip is the number of frames between this function
// and the call site to attribute (0 = the direct caller of the MarkDirty that
// invoked this).
func (p *Pool) reportUnstampedMarkDirty(s *Slot, skip int) {
	if !pdlsnAssertEnabled || p.logFPI == nil {
		return
	}
	var pcs [1]uintptr
	// +3: runtime.Callers, this function, MarkDirty itself.
	if runtime.Callers(skip+3, pcs[:]) == 0 {
		return
	}
	pc := pcs[0]
	pdlsnAssertMu.Lock()
	first := !pdlsnAssertSeen[pc]
	if first {
		pdlsnAssertSeen[pc] = true
		pdlsnAssertCount++
	}
	sink := pdlsnAssertSink
	pdlsnAssertMu.Unlock()
	if !first {
		return
	}
	site := "unknown"
	if fn := runtime.FuncForPC(pc); fn != nil {
		file, line := fn.FileLine(pc)
		site = fmt.Sprintf("%s (%s:%d)", fn.Name(), file, line)
	}
	msg := fmt.Sprintf("PDLSN-UNSTAMPED rel=%d/%d/%d blk=%d pd_lsn=%d site=%s",
		s.tag.Rel.TblOid, s.tag.Rel.DBOid, s.tag.Rel.RelOid, s.tag.Block,
		pageLSNOrZero(s.page), site)
	if sink != nil {
		sink(msg)
		return
	}
	fmt.Fprintln(os.Stderr, msg)
}

// pageLSNOrZero reads pd_lsn defensively: the guard must never turn a
// diagnostic into a panic on a malformed page.
func pageLSNOrZero(page []byte) LSN {
	h, err := Header(page)
	if err != nil {
		return 0
	}
	return h.LSN()
}
