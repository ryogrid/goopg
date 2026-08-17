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
	mu       sync.Mutex
	standbys map[string]standbyProgress
	waiters  []*syncRepWaiter
	rule     syncRepRule
}

// NewSyncRep constructs an empty SyncRep with an "off" rule. Callers should
// invoke SetStandbyNames before commit-path waits can do anything other than
// immediate return.
func NewSyncRep() *SyncRep {
	return &SyncRep{
		standbys: map[string]standbyProgress{},
		rule:     syncRepRule{},
	}
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

// UpdateStandbyProgress records the latest reported LSNs for the named
// standby and releases any waiters whose target is now satisfied. Called by
// the walsender goroutine on every Standby Status Update message. An empty
// appName falls back to "" which the rule will never match — upstream
// behaves identically (a standby without application_name cannot participate
// in sync replication).
func (s *SyncRep) UpdateStandbyProgress(appName string, write, flush, apply uint64) {
	s.mu.Lock()
	prev := s.standbys[appName]
	// LSN values reported by walreceivers are monotonic; clamp here so a
	// transient out-of-order message can never roll the registry backwards.
	if write < prev.WriteLSN {
		write = prev.WriteLSN
	}
	if flush < prev.FlushLSN {
		flush = prev.FlushLSN
	}
	if apply < prev.ApplyLSN {
		apply = prev.ApplyLSN
	}
	s.standbys[appName] = standbyProgress{WriteLSN: write, FlushLSN: flush, ApplyLSN: apply}
	s.releaseWaitersLocked()
	s.mu.Unlock()
}

// ForgetStandby drops the named standby from the registry. Called from the
// walsender's deferred Unregister path so a disconnected standby no longer
// counts toward the FIRST/ANY quorum on the next release pass.
func (s *SyncRep) ForgetStandby(appName string) {
	s.mu.Lock()
	delete(s.standbys, appName)
	s.releaseWaitersLocked()
	s.mu.Unlock()
}

// StandbyProgress returns the last reported (write, flush, apply) LSN for
// the named standby. Intended for tests and observability. Returns zeros
// when the standby is not registered.
func (s *SyncRep) StandbyProgress(appName string) (write, flush, apply uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.standbys[appName]
	return p.WriteLSN, p.FlushLSN, p.ApplyLSN
}

// SyncStateFor returns the pg_stat_replication.sync_state string ("sync",
// "potential", or "async") for the standby identified by appName. Called by
// the pg_stat_replication virtual view row-builder.
func (s *SyncRep) SyncStateFor(appName string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rule.empty() {
		return "async"
	}
	return s.rule.syncStateFor(appName)
}

// SyncPriorityFor returns the pg_stat_replication.sync_priority integer for
// the standby identified by appName (0 for async standbys).
func (s *SyncRep) SyncPriorityFor(appName string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rule.empty() {
		return 0
	}
	return s.rule.syncPriorityFor(appName)
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

// ruleSatisfiedLocked reports whether the configured rule sees enough
// standbys at or beyond targetLSN for the given mode. Caller must hold s.mu.
func (s *SyncRep) ruleSatisfiedLocked(targetLSN uint64, mode SyncRepMode) bool {
	if s.rule.empty() {
		return true
	}
	return s.rule.satisfied(s.standbys, targetLSN, mode)
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

// satisfied applies the rule to the current standby map and returns whether
// the target LSN has been acknowledged by enough configured standbys.
func (r syncRepRule) satisfied(standbys map[string]standbyProgress, targetLSN uint64, mode SyncRepMode) bool {
	switch r.mode {
	case syncRepRuleFirst:
		// FIRST n: the first n listed names must all be at >= targetLSN.
		// If fewer than n names exist in the list, the rule cannot be
		// satisfied — but `parseSyncRepRule` rejects count > len(names)
		// during parsing, so reaching here with insufficient names is
		// impossible. Defensive guard keeps the loop simple.
		if r.count > len(r.names) {
			return false
		}
		for _, name := range r.names[:r.count] {
			p, ok := standbys[name]
			if !ok {
				return false
			}
			if p.progressFor(mode) < targetLSN {
				return false
			}
		}
		return true
	case syncRepRuleAny:
		// ANY n: any n of the listed names must be at >= targetLSN.
		// Equivalent: collect each named standby's progress (0 when
		// missing), sort descending, check the n-th-largest >= target.
		if r.count > len(r.names) {
			return false
		}
		vals := make([]uint64, 0, len(r.names))
		for _, name := range r.names {
			p, ok := standbys[name]
			if !ok {
				vals = append(vals, 0)
				continue
			}
			vals = append(vals, p.progressFor(mode))
		}
		sort.Slice(vals, func(i, j int) bool { return vals[i] > vals[j] })
		return vals[r.count-1] >= targetLSN
	}
	return true
}

// syncStateFor returns the pg_stat_replication.sync_state string for the
// given application_name: "sync", "potential", or "async".
//
//   - FIRST n: names[0..n-1] are "sync"; names[n..] are "potential".
//   - ANY n: all listed names are "sync" (any can fulfil the quorum).
//   - Off / empty: all are "async".
func (r syncRepRule) syncStateFor(appName string) string {
	switch r.mode {
	case syncRepRuleFirst:
		for i, name := range r.names {
			if name == appName {
				if i < r.count {
					return "sync"
				}
				return "potential"
			}
		}
	case syncRepRuleAny:
		for _, name := range r.names {
			if name == appName {
				return "sync"
			}
		}
	}
	return "async"
}

// syncPriorityFor returns the pg_stat_replication.sync_priority integer for
// the given application_name (1-indexed position for FIRST, 1 for any listed
// standby in ANY mode, 0 if not in the rule).
func (r syncRepRule) syncPriorityFor(appName string) int {
	switch r.mode {
	case syncRepRuleFirst:
		for i, name := range r.names {
			if name == appName {
				return i + 1
			}
		}
	case syncRepRuleAny:
		for _, name := range r.names {
			if name == appName {
				return 1
			}
		}
	}
	return 0
}
