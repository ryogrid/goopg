// Synchronous replication wait primitive (M0102-0005).
//
// Mirrors upstream `postgres/src/backend/replication/syncrep.c`. The COMMIT
// path on the primary calls WaitForLSN(commitLSN, mode) after its local WAL
// flush; the walsender goroutine calls UpdateStandbyProgress(appName, write,
// flush, apply) whenever a Standby Status Update arrives. UpdateStandbyProgress
// recomputes the "enough standbys" LSN per the parsed
// synchronous_standby_names rule and releases every waiter at or below it.
//
// Decoupled from `mvcc.Manager` and `server.Server` so it can be exercised in
// isolation: the unit tests in `syncrep_test.go` drive the primitive directly
// with fake standby progress updates and assert the wait/release semantics.
package xlog

import (
	"context"
	"sort"
	"strings"
	"sync"
)

// SyncRepMode mirrors upstream's synchronous_commit enum subset relevant to
// the wait primitive. The level decides which standby-reported LSN
// (write/flush/apply) is consulted for release decisions.
type SyncRepMode int

const (
	// SyncRepOff means commit returns immediately after local flush — the
	// dispatcher does not call WaitForLSN. Included as a sentinel so the
	// commit-path can switch on the same enum it would otherwise consume.
	SyncRepOff SyncRepMode = iota
	// SyncRepRemoteWrite waits until the standby has buffered the WAL bytes
	// (write_lsn ≥ target). Matches upstream SYNCHRONOUS_COMMIT_REMOTE_WRITE.
	SyncRepRemoteWrite
	// SyncRepRemoteFlush waits until the standby has fsynced the WAL bytes
	// (flush_lsn ≥ target). Matches upstream SYNCHRONOUS_COMMIT_REMOTE_FLUSH
	// (the default for synchronous_commit=on).
	SyncRepRemoteFlush
	// SyncRepRemoteApply waits until the standby has replayed the bytes and
	// they are visible to readers (apply_lsn ≥ target). Strongest level;
	// required by the M0102 sync subtest.
	SyncRepRemoteApply
)

// ParseSyncCommitLevel maps a synchronous_commit string value (upstream
// enum: off / local / remote_write / on / remote_flush / remote_apply) into
// a SyncRepMode. Unknown values fall back to SyncRepRemoteFlush, matching
// upstream's "treat invalid as on" guc parse-error fallback.
func ParseSyncCommitLevel(v string) SyncRepMode {
	switch v {
	case "off", "local", "false", "0", "no":
		return SyncRepOff
	case "remote_write":
		return SyncRepRemoteWrite
	case "remote_apply":
		return SyncRepRemoteApply
	case "on", "remote_flush", "true", "1", "yes", "":
		return SyncRepRemoteFlush
	}
	return SyncRepRemoteFlush
}

// standbyProgress records the most recent write/flush/apply LSNs reported by
// one named standby. Standbys are tracked by application_name.
type standbyProgress struct {
	WriteLSN uint64
	FlushLSN uint64
	ApplyLSN uint64
}

// progressFor returns the LSN appropriate for the given mode.
func (p standbyProgress) progressFor(mode SyncRepMode) uint64 {
	switch mode {
	case SyncRepRemoteWrite:
		return p.WriteLSN
	case SyncRepRemoteFlush:
		return p.FlushLSN
	case SyncRepRemoteApply:
		return p.ApplyLSN
	}
	return 0
}

// syncRepWaiter holds a single in-flight WaitForLSN caller. `releaseCh` is
// closed (exactly once) when the caller may return — either because the
// target LSN was reached or because the rule emptied (no eligible standbys
// configured).
type syncRepWaiter struct {
	targetLSN uint64
	mode      SyncRepMode
	releaseCh chan struct{}
	released  bool
}

// SyncRep is the process-wide primitive that coordinates COMMIT-side waits
// with walsender-side standby acknowledgements. Construct one per server.
type SyncRep struct {
	mu sync.Mutex
	// slots mirrors WalSndCtl->walsnds: one entry per connected walsender
	// that can count as a synchronous standby. A disconnect frees its slot
	// and the next walsender takes the lowest free one, so slot order is
	// PG's walsnd_index — the tie-break between standbys of equal priority.
	slots   []syncRepSlot
	waiters []*syncRepWaiter
	rule    syncRepRule
}

// syncRepSlot is the part of a WalSnd that syncrep.c reads: the standby's
// application_name and its last reported write/flush/apply positions.
type syncRepSlot struct {
	inUse    bool
	gen      uint64 // bumped on reuse, so a stale handle cannot touch a new owner
	appName  string
	progress standbyProgress
}

// SyncRepStandby is a walsender's handle on its slot.
type SyncRepStandby struct {
	s   *SyncRep
	idx int
	gen uint64
}

// NewSyncRep constructs an empty SyncRep with an "off" rule. Callers should
// invoke SetStandbyNames before commit-path waits can do anything other than
// immediate return.
func NewSyncRep() *SyncRep {
	return &SyncRep{rule: syncRepRule{}}
}

// SetStandbyNames re-parses synchronous_standby_names and atomically swaps
// the active rule. Empty value (or a parse error) disables waiting: every
// subsequent WaitForLSN returns immediately. Returns the parse error so the
// caller can surface it as a GUC reload warning; the rule still falls back
// to "no-op" on error so a malformed value cannot wedge commits.
func (s *SyncRep) SetStandbyNames(value string) error {
	rule, err := parseSyncRepRule(value)
	s.mu.Lock()
	if err != nil {
		s.rule = syncRepRule{}
	} else {
		s.rule = rule
	}
	// Re-evaluate waiters under the new rule — a relaxation (e.g. fewer
	// quorum members) may unblock some.
	s.releaseWaitersLocked()
	s.mu.Unlock()
	return err
}

// NeedsWait reports whether the current rule names any standby. The commit
// path uses this as a cheap pre-check so an empty `synchronous_standby_names`
// adds zero overhead per commit.
func (s *SyncRep) NeedsWait() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.rule.empty()
}

// WaitForLSN blocks until enough configured standbys have acknowledged the
// target LSN at the given mode, or until ctx is cancelled. Returns ctx.Err()
// on cancellation; nil on normal release. When the rule is empty (no standby
// names configured) or mode==SyncRepOff, returns immediately with nil —
// upstream-parity behaviour: configuring `synchronous_commit` without
// `synchronous_standby_names` does not hang commits.
func (s *SyncRep) WaitForLSN(ctx context.Context, lsn uint64, mode SyncRepMode) error {
	if mode == SyncRepOff {
		return nil
	}
	s.mu.Lock()
	if s.rule.empty() {
		s.mu.Unlock()
		return nil
	}
	if s.ruleSatisfiedLocked(lsn, mode) {
		s.mu.Unlock()
		return nil
	}
	w := &syncRepWaiter{
		targetLSN: lsn,
		mode:      mode,
		releaseCh: make(chan struct{}),
	}
	s.waiters = append(s.waiters, w)
	s.mu.Unlock()

	select {
	case <-w.releaseCh:
		return nil
	case <-ctx.Done():
		// Detach the waiter so a late release doesn't try to close the
		// channel twice. If the release already fired we treat the
		// commit as released (no error) — matches upstream's
		// `SyncRepCancelWait` which returns success when the COMMIT
		// already saw enough acks.
		s.mu.Lock()
		s.removeWaiterLocked(w)
		alreadyReleased := w.released
		s.mu.Unlock()
		if alreadyReleased {
			return nil
		}
		return ctx.Err()
	}
}

// RegisterStandby claims the lowest free walsender slot for a standby
// streaming under appName (InitWalSenderSlot). Its sync priority follows
// the current synchronous_standby_names (SyncRepGetStandbyPriority).
func (s *SyncRep) RegisterStandby(appName string) *SyncRepStandby {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.registerLocked(appName)
}

func (s *SyncRep) registerLocked(appName string) *SyncRepStandby {
	idx := -1
	for i := range s.slots {
		if !s.slots[i].inUse {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.slots = append(s.slots, syncRepSlot{})
		idx = len(s.slots) - 1
	}
	sl := &s.slots[idx]
	sl.gen++
	sl.inUse = true
	sl.appName = appName
	sl.progress = standbyProgress{}
	return &SyncRepStandby{s: s, idx: idx, gen: sl.gen}
}

// slotLocked returns h's slot, or nil once h was released.
func (h *SyncRepStandby) slotLocked() *syncRepSlot {
	if h == nil || h.idx >= len(h.s.slots) {
		return nil
	}
	sl := &h.s.slots[h.idx]
	if !sl.inUse || sl.gen != h.gen {
		return nil
	}
	return sl
}

// Update records the standby's reported LSNs (a Standby Status Update) and
// releases any waiters now satisfied (SyncRepReleaseWaiters).
func (h *SyncRepStandby) Update(write, flush, apply uint64) {
	if h == nil {
		return
	}
	h.s.mu.Lock()
	defer h.s.mu.Unlock()
	if sl := h.slotLocked(); sl != nil {
		sl.progress.advance(write, flush, apply)
		h.s.releaseWaitersLocked()
	}
}

// Release frees the slot when the walsender exits (WalSndKill), so the
// standby no longer counts toward synchronous_standby_names.
func (h *SyncRepStandby) Release() {
	if h == nil {
		return
	}
	h.s.mu.Lock()
	defer h.s.mu.Unlock()
	if sl := h.slotLocked(); sl != nil {
		sl.inUse = false
		h.s.releaseWaitersLocked()
	}
}

// Slot is the walsender slot index (walsnd_index); -1 for a nil handle.
func (h *SyncRepStandby) Slot() int {
	if h == nil {
		return -1
	}
	return h.idx
}

// advance moves the positions forward; a reply never rolls them back.
func (p *standbyProgress) advance(write, flush, apply uint64) {
	if write > p.WriteLSN {
		p.WriteLSN = write
	}
	if flush > p.FlushLSN {
		p.FlushLSN = flush
	}
	if apply > p.ApplyLSN {
		p.ApplyLSN = apply
	}
}

// UpdateStandbyProgress is Update for the first slot streaming under
// appName, registering one if none is — for callers without a handle.
func (s *SyncRep) UpdateStandbyProgress(appName string, write, flush, apply uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.slotByNameLocked(appName)
	if idx < 0 {
		idx = s.registerLocked(appName).idx
	}
	s.slots[idx].progress.advance(write, flush, apply)
	s.releaseWaitersLocked()
}

// ForgetStandby releases every slot streaming under appName.
func (s *SyncRep) ForgetStandby(appName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.slots {
		if s.slots[i].inUse && s.slots[i].appName == appName {
			s.slots[i].inUse = false
		}
	}
	s.releaseWaitersLocked()
}

func (s *SyncRep) slotByNameLocked(appName string) int {
	for i := range s.slots {
		if s.slots[i].inUse && s.slots[i].appName == appName {
			return i
		}
	}
	return -1
}

// StandbyProgress returns the last reported (write, flush, apply) LSN for
// the named standby. Intended for tests and observability. Returns zeros
// when the standby is not registered.
func (s *SyncRep) StandbyProgress(appName string) (write, flush, apply uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := s.slotByNameLocked(appName); i >= 0 {
		p := s.slots[i].progress
		return p.WriteLSN, p.FlushLSN, p.ApplyLSN
	}
	return 0, 0, 0
}

// SlotSyncState returns pg_stat_replication's sync_priority and sync_state
// for walsender slot idx, as pg_stat_get_wal_senders computes them: the
// priority is 0 (async) for a standby that has not reported a flush
// position; a standby among SyncRepGetCandidateStandbys is "sync"
// (FIRST) or "quorum" (ANY); any other prioritised one is "potential".
func (s *SyncRep) SlotSyncState(idx int) (priority int, state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if idx < 0 || idx >= len(s.slots) || !s.slots[idx].inUse {
		return 0, "async"
	}
	sl := s.slots[idx]
	priority = s.rule.priorityFor(sl.appName)
	if sl.progress.FlushLSN == 0 {
		priority = 0
	}
	if priority == 0 {
		return 0, "async"
	}
	for _, c := range s.candidatesLocked() {
		if c == idx {
			if s.rule.mode == syncRepRuleAny {
				return priority, "quorum"
			}
			return priority, "sync"
		}
	}
	return priority, "potential"
}

// SyncStateFor is SlotSyncState for the first slot streaming under appName.
func (s *SyncRep) SyncStateFor(appName string) string {
	s.mu.Lock()
	idx := s.slotByNameLocked(appName)
	s.mu.Unlock()
	_, st := s.SlotSyncState(idx)
	return st
}

// SyncPriorityFor is SlotSyncState's priority for the first slot streaming
// under appName.
func (s *SyncRep) SyncPriorityFor(appName string) int {
	s.mu.Lock()
	idx := s.slotByNameLocked(appName)
	s.mu.Unlock()
	p, _ := s.SlotSyncState(idx)
	return p
}

// candidatesLocked is SyncRepGetCandidateStandbys: the slots, in slot
// order, of standbys with a non-zero priority and a valid flush position;
// for FIRST with more of them than num_sync, the num_sync with the best
// (lowest) priority, ties going to the lower slot. Caller holds s.mu.
func (s *SyncRep) candidatesLocked() []int {
	if s.rule.empty() {
		return nil
	}
	var out []int
	for i := range s.slots {
		sl := &s.slots[i]
		if !sl.inUse || sl.progress.FlushLSN == 0 || s.rule.priorityFor(sl.appName) == 0 {
			continue
		}
		out = append(out, i)
	}
	if s.rule.mode == syncRepRuleFirst && len(out) > s.rule.count {
		sort.SliceStable(out, func(a, b int) bool {
			pa, pb := s.rule.priorityFor(s.slots[out[a]].appName), s.rule.priorityFor(s.slots[out[b]].appName)
			if pa != pb {
				return pa < pb
			}
			return out[a] < out[b]
		})
		out = out[:s.rule.count]
	}
	return out
}

// releaseWaitersLocked walks the waiter list and closes the release channel
// for every waiter whose target LSN ≤ the rule's "enough" LSN at the
// waiter's mode. Caller must hold s.mu.
func (s *SyncRep) releaseWaitersLocked() {
	if len(s.waiters) == 0 {
		return
	}
	kept := s.waiters[:0]
	for _, w := range s.waiters {
		if s.ruleSatisfiedLocked(w.targetLSN, w.mode) {
			if !w.released {
				w.released = true
				close(w.releaseCh)
			}
			continue
		}
		kept = append(kept, w)
	}
	// Drop tail references so the GC can reclaim released waiters.
	for i := len(kept); i < len(s.waiters); i++ {
		s.waiters[i] = nil
	}
	s.waiters = kept
}

// ruleSatisfiedLocked is SyncRepGetSyncRecPtr's test: with fewer
// candidates than num_sync nothing is released; FIRST releases up to the
// oldest candidate position (SyncRepGetOldestSyncRecPtr), ANY up to the
// num_sync-th latest (SyncRepGetNthLatestSyncRecPtr). Caller holds s.mu.
func (s *SyncRep) ruleSatisfiedLocked(targetLSN uint64, mode SyncRepMode) bool {
	if s.rule.empty() {
		return true
	}
	cands := s.candidatesLocked()
	if len(cands) < s.rule.count {
		return false
	}
	vals := make([]uint64, len(cands))
	for i, c := range cands {
		vals[i] = s.slots[c].progress.progressFor(mode)
	}
	sort.Slice(vals, func(a, b int) bool { return vals[a] > vals[b] })
	if s.rule.mode == syncRepRuleFirst {
		return vals[len(vals)-1] >= targetLSN
	}
	return vals[s.rule.count-1] >= targetLSN
}

// removeWaiterLocked detaches w from the waiter list. Caller must hold s.mu.
func (s *SyncRep) removeWaiterLocked(w *syncRepWaiter) {
	for i, other := range s.waiters {
		if other == w {
			s.waiters[i] = s.waiters[len(s.waiters)-1]
			s.waiters[len(s.waiters)-1] = nil
			s.waiters = s.waiters[:len(s.waiters)-1]
			return
		}
	}
}

// syncRepRule is the parsed form of synchronous_standby_names. An empty
// rule (mode==syncRepRuleOff) means async — every WaitForLSN returns
// immediately.
type syncRepRule struct {
	mode  syncRepRuleMode
	count int      // n in FIRST n / ANY n
	names []string // ordered list (FIRST cares about order)
}

type syncRepRuleMode int

const (
	syncRepRuleOff   syncRepRuleMode = iota // empty rule
	syncRepRuleFirst                        // FIRST n (...)
	syncRepRuleAny                          // ANY n (...)
)

// empty reports whether the rule is the no-op rule.
func (r syncRepRule) empty() bool {
	return r.mode == syncRepRuleOff || len(r.names) == 0 || r.count == 0
}

// priorityFor is SyncRepGetStandbyPriority: the 1-based position of the
// first list member equal to appName (case-insensitively) or "*"; 0 when
// none matches. Under ANY every match has priority 1.
func (r syncRepRule) priorityFor(appName string) int {
	if r.empty() {
		return 0
	}
	for i, name := range r.names {
		if name == "*" || strings.EqualFold(name, appName) {
			if r.mode == syncRepRuleAny {
				return 1
			}
			return i + 1
		}
	}
	return 0
}
