package xlog

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestSyncRepParseRule(t *testing.T) {
	cases := []struct {
		in       string
		wantMode syncRepRuleMode
		wantN    int
		wantList []string
		wantErr  bool
	}{
		{in: "", wantMode: syncRepRuleOff, wantN: 0, wantList: nil},
		{in: "   ", wantMode: syncRepRuleOff, wantN: 0, wantList: nil},
		{in: "standby1", wantMode: syncRepRuleFirst, wantN: 1, wantList: []string{"standby1"}},
		{in: "a, b, c", wantMode: syncRepRuleFirst, wantN: 1, wantList: []string{"a", "b", "c"}},
		{in: "FIRST 2 (a, b, c)", wantMode: syncRepRuleFirst, wantN: 2, wantList: []string{"a", "b", "c"}},
		{in: "first 1 (a, b)", wantMode: syncRepRuleFirst, wantN: 1, wantList: []string{"a", "b"}},
		{in: "ANY 2 (a, b, c)", wantMode: syncRepRuleAny, wantN: 2, wantList: []string{"a", "b", "c"}},
		{in: "any 1 (only)", wantMode: syncRepRuleAny, wantN: 1, wantList: []string{"only"}},
		{in: `"name with spaces"`, wantMode: syncRepRuleFirst, wantN: 1, wantList: []string{"name with spaces"}},
		{in: "FIRST (a, b)", wantMode: syncRepRuleFirst, wantN: 1, wantList: []string{"a", "b"}}, // default count = 1
		{in: "2 (a, b, c)", wantMode: syncRepRuleFirst, wantN: 2, wantList: []string{"a", "b", "c"}},
		{in: "FIRST 4 (a, b, c)", wantMode: syncRepRuleFirst, wantN: 4, wantList: []string{"a", "b", "c"}}, // PG accepts num_sync > members
		{in: "ANY 0 (a)", wantErr: true},
		{in: "FIRST 1 a, b", wantErr: true}, // missing parens
		{in: `"unterminated`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			rule, err := parseSyncRepRule(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got rule=%+v", rule)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if rule.mode != tc.wantMode {
				t.Errorf("mode = %v, want %v", rule.mode, tc.wantMode)
			}
			if rule.count != tc.wantN {
				t.Errorf("count = %d, want %d", rule.count, tc.wantN)
			}
			if !reflect.DeepEqual(rule.names, tc.wantList) {
				t.Errorf("names = %v, want %v", rule.names, tc.wantList)
			}
		})
	}
}

func TestSyncRepParseSyncCommitLevel(t *testing.T) {
	cases := map[string]SyncRepMode{
		"off":          SyncRepOff,
		"local":        SyncRepOff,
		"remote_write": SyncRepRemoteWrite,
		"on":           SyncRepRemoteFlush,
		"remote_flush": SyncRepRemoteFlush,
		"remote_apply": SyncRepRemoteApply,
		"":             SyncRepRemoteFlush, // empty falls back to "on" semantics
		"bogus":        SyncRepRemoteFlush, // unknown falls back to "on"
	}
	for in, want := range cases {
		if got := ParseSyncCommitLevel(in); got != want {
			t.Errorf("ParseSyncCommitLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestSyncRepEmptyRuleReturnsImmediately(t *testing.T) {
	s := NewSyncRep()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	// No SetStandbyNames call => empty rule.
	if err := s.WaitForLSN(ctx, 1000, SyncRepRemoteFlush); err != nil {
		t.Fatalf("expected nil for empty rule, got %v", err)
	}
}

func TestSyncRepOffModeReturnsImmediately(t *testing.T) {
	s := NewSyncRep()
	if err := s.SetStandbyNames("standby1"); err != nil {
		t.Fatalf("SetStandbyNames: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := s.WaitForLSN(ctx, 1000, SyncRepOff); err != nil {
		t.Fatalf("expected nil for SyncRepOff, got %v", err)
	}
}

func TestSyncRepFirstWaitsForListedName(t *testing.T) {
	s := NewSyncRep()
	if err := s.SetStandbyNames("standby1"); err != nil {
		t.Fatalf("SetStandbyNames: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- s.WaitForLSN(context.Background(), 100, SyncRepRemoteApply)
	}()

	// Should not be released by an unrelated standby's progress.
	s.UpdateStandbyProgress("other", 200, 200, 200)
	select {
	case <-done:
		t.Fatalf("commit was released by unrelated standby")
	case <-time.After(50 * time.Millisecond):
	}

	// Release: matching standby acknowledges past target.
	s.UpdateStandbyProgress("standby1", 200, 200, 100)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WaitForLSN returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("WaitForLSN did not return after standby ack")
	}
}

func TestSyncRepAnyQuorum(t *testing.T) {
	s := NewSyncRep()
	if err := s.SetStandbyNames("ANY 2 (a, b, c)"); err != nil {
		t.Fatalf("SetStandbyNames: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- s.WaitForLSN(context.Background(), 500, SyncRepRemoteApply)
	}()

	// Only one standby caught up — not enough.
	s.UpdateStandbyProgress("a", 500, 500, 500)
	select {
	case <-done:
		t.Fatalf("commit released with only 1/2 standbys")
	case <-time.After(50 * time.Millisecond):
	}

	// Second standby — quorum met.
	s.UpdateStandbyProgress("b", 500, 500, 500)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WaitForLSN returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("WaitForLSN did not return when quorum was met")
	}
}

func TestSyncRepFirstNQuorumOrderMatters(t *testing.T) {
	s := NewSyncRep()
	if err := s.SetStandbyNames("FIRST 2 (a, b, c)"); err != nil {
		t.Fatalf("SetStandbyNames: %v", err)
	}
	// All three are streaming (valid flush), so the two with the best
	// priority — a and b — are the sync standbys (SyncRepGetCandidateStandbys).
	a, b, c := s.RegisterStandby("a"), s.RegisterStandby("b"), s.RegisterStandby("c")
	a.Update(1, 1, 1)
	b.Update(1, 1, 1)
	c.Update(1, 1, 1)

	done := make(chan error, 1)
	go func() {
		done <- s.WaitForLSN(context.Background(), 100, SyncRepRemoteFlush)
	}()

	// b and c caught up but a didn't: c is only potential, so not released.
	b.Update(100, 100, 100)
	c.Update(100, 100, 100)
	select {
	case <-done:
		t.Fatalf("FIRST 2 released without a's ack")
	case <-time.After(50 * time.Millisecond):
	}

	// a finally catches up — released.
	a.Update(100, 100, 100)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WaitForLSN returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("WaitForLSN did not return after a's ack")
	}
}

// TestSyncRepFirstFallsBackToConnectedStandby: with the first-listed standby
// absent, the next connected one by priority is the sync standby and its
// ack releases the commit (PG: candidates are connected walsenders only).
func TestSyncRepFirstFallsBackToConnectedStandby(t *testing.T) {
	s := NewSyncRep()
	if err := s.SetStandbyNames("FIRST 1 (a, b)"); err != nil {
		t.Fatalf("SetStandbyNames: %v", err)
	}
	b := s.RegisterStandby("b")
	b.Update(1, 1, 1)
	if p, st := s.SlotSyncState(b.Slot()); p != 2 || st != "sync" {
		t.Fatalf("b = %d|%s, want 2|sync", p, st)
	}
	done := make(chan error, 1)
	go func() { done <- s.WaitForLSN(context.Background(), 50, SyncRepRemoteFlush) }()
	b.Update(50, 50, 50)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WaitForLSN: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("not released by b's ack")
	}
}

func TestSyncRepModeDistinguishesWriteFlushApply(t *testing.T) {
	s := NewSyncRep()
	if err := s.SetStandbyNames("standby1"); err != nil {
		t.Fatalf("SetStandbyNames: %v", err)
	}

	// remote_apply waiter: needs apply_lsn ≥ 100.
	done := make(chan error, 1)
	go func() {
		done <- s.WaitForLSN(context.Background(), 100, SyncRepRemoteApply)
	}()

	// Only write/flush reported — apply still behind. Should NOT release.
	s.UpdateStandbyProgress("standby1", 100, 100, 50)
	select {
	case <-done:
		t.Fatalf("remote_apply released while apply_lsn < target")
	case <-time.After(50 * time.Millisecond):
	}

	// Apply catches up — release.
	s.UpdateStandbyProgress("standby1", 100, 100, 100)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WaitForLSN returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("WaitForLSN did not return after apply_lsn caught up")
	}
}

func TestSyncRepImmediateReleaseWhenAlreadyAcked(t *testing.T) {
	s := NewSyncRep()
	if err := s.SetStandbyNames("standby1"); err != nil {
		t.Fatalf("SetStandbyNames: %v", err)
	}
	s.UpdateStandbyProgress("standby1", 200, 200, 200)

	// Commit at LSN 100 with standby already past 200 — should return
	// without ever sleeping.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := s.WaitForLSN(ctx, 100, SyncRepRemoteApply); err != nil {
		t.Fatalf("expected immediate release, got %v", err)
	}
}

func TestSyncRepContextCancellation(t *testing.T) {
	s := NewSyncRep()
	if err := s.SetStandbyNames("standby1"); err != nil {
		t.Fatalf("SetStandbyNames: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- s.WaitForLSN(ctx, 100, SyncRepRemoteFlush)
	}()

	// No standby ack ever arrives — cancel the context.
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("expected ctx.Err(), got nil")
		}
	case <-time.After(time.Second):
		t.Fatalf("WaitForLSN did not return on ctx cancel")
	}
}

func TestSyncRepForgetStandbyReleasesQuorum(t *testing.T) {
	s := NewSyncRep()
	if err := s.SetStandbyNames("ANY 1 (a, b)"); err != nil {
		t.Fatalf("SetStandbyNames: %v", err)
	}
	// b catches up, then disconnects; a never catches up.
	s.UpdateStandbyProgress("b", 100, 100, 100)
	// At this point a wait at LSN 100 should be released immediately.
	if err := s.WaitForLSN(context.Background(), 100, SyncRepRemoteFlush); err != nil {
		t.Fatalf("expected immediate release after b caught up, got %v", err)
	}
	s.ForgetStandby("b")
	// New wait for 200 with a stale at LSN 0 → waits.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := s.WaitForLSN(ctx, 200, SyncRepRemoteFlush); err == nil {
		t.Fatalf("expected ctx deadline after ForgetStandby")
	}
}

func TestSyncRepConcurrentUpdatesAndWaits(t *testing.T) {
	// Stress test: many goroutines call UpdateStandbyProgress while many
	// callers WaitForLSN at varying LSNs. With -race, this catches any
	// lock-protocol bug in releaseWaitersLocked.
	s := NewSyncRep()
	if err := s.SetStandbyNames("standby1"); err != nil {
		t.Fatalf("SetStandbyNames: %v", err)
	}

	const N = 200
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		target := uint64(i)
		go func() {
			defer wg.Done()
			_ = s.WaitForLSN(context.Background(), target, SyncRepRemoteFlush)
		}()
	}
	// Ramp the standby progress up to N — each tick releases a chunk.
	go func() {
		for i := 0; i <= N; i++ {
			s.UpdateStandbyProgress("standby1", uint64(i), uint64(i), uint64(i))
		}
	}()
	wg.Wait()
}

func TestSyncRepStandbyProgressMonotonic(t *testing.T) {
	s := NewSyncRep()
	s.UpdateStandbyProgress("a", 100, 90, 80)
	// A "regression" update from an out-of-order frame must not lower the
	// recorded LSN.
	s.UpdateStandbyProgress("a", 50, 50, 50)
	w, f, ap := s.StandbyProgress("a")
	if w != 100 || f != 90 || ap != 80 {
		t.Fatalf("standby progress regressed: write=%d flush=%d apply=%d", w, f, ap)
	}
}

func TestSyncRepSetStandbyNamesReleasesOnRelaxation(t *testing.T) {
	s := NewSyncRep()
	if err := s.SetStandbyNames("FIRST 2 (a, b)"); err != nil {
		t.Fatalf("SetStandbyNames: %v", err)
	}
	s.UpdateStandbyProgress("a", 100, 100, 100)
	// Only a is up — FIRST 2 unmet, so wait blocks.
	done := make(chan error, 1)
	go func() {
		done <- s.WaitForLSN(context.Background(), 100, SyncRepRemoteFlush)
	}()
	select {
	case <-done:
		t.Fatalf("released prematurely")
	case <-time.After(50 * time.Millisecond):
	}
	// Relax the rule to FIRST 1 — waiter should now release.
	if err := s.SetStandbyNames("FIRST 1 (a, b)"); err != nil {
		t.Fatalf("SetStandbyNames reload: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WaitForLSN returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("WaitForLSN did not return after rule relaxation")
	}
}

// TestSyncRepSlotSyncState follows 007_sync_rep.pl's expectations through
// pg_stat_get_wal_senders' rules: priority from the first matching list
// member or "*", candidates in walsender slot order, freed slots reused.
func TestSyncRepSlotSyncState(t *testing.T) {
	s := NewSyncRep()
	reg := func(name string) *SyncRepStandby {
		h := s.RegisterStandby(name)
		h.Update(1, 1, 1)
		return h
	}
	s1, s2, s3 := reg("standby1"), reg("standby2"), reg("standby3")
	check := func(msg string, want map[*SyncRepStandby]string) {
		t.Helper()
		for h, w := range want {
			p, st := s.SlotSyncState(h.Slot())
			if got := fmt.Sprintf("%d|%s", p, st); got != w {
				t.Errorf("%s: slot %d = %s, want %s", msg, h.Slot(), got, w)
			}
		}
	}
	set := func(v string) {
		t.Helper()
		if err := s.SetStandbyNames(v); err != nil {
			t.Fatalf("SetStandbyNames(%q): %v", v, err)
		}
	}
	if p, st := s.SlotSyncState(s1.Slot()); p != 0 || st != "async" {
		t.Errorf("no rule: %d|%s, want 0|async", p, st)
	}
	set("standby1,standby2")
	check("old syntax", map[*SyncRepStandby]string{s1: "1|sync", s2: "2|potential", s3: "0|async"})
	set("*")
	check("asterisk", map[*SyncRepStandby]string{s1: "1|sync", s2: "1|potential", s3: "1|potential"})

	// Re-register in the order 2, 3, 1, 4: freed slots are reused lowest first.
	s1.Release()
	s2.Release()
	s3.Release()
	s2, s3 = reg("standby2"), reg("standby3")
	set("2(standby1,standby2,standby3)")
	check("2 sync", map[*SyncRepStandby]string{s2: "2|sync", s3: "3|sync"})
	s1 = reg("standby1")
	s4 := reg("standby4")
	check("2 sync 1 potential 1 async", map[*SyncRepStandby]string{s1: "1|sync", s2: "2|sync", s3: "3|potential", s4: "0|async"})
	set("6(standby4,standby0,standby3,standby2)")
	check("num_sync over members", map[*SyncRepStandby]string{s1: "0|async", s2: "4|sync", s3: "3|sync", s4: "1|sync"})
	set("2(standby1,*,standby2)")
	check("asterisk before name", map[*SyncRepStandby]string{s1: "1|sync", s2: "2|sync", s3: "2|potential", s4: "2|potential"})
	set("2(*)")
	check("same priority by slot", map[*SyncRepStandby]string{s1: "1|potential", s2: "1|sync", s3: "1|sync", s4: "1|potential"})
	s3.Release()
	check("earlier slot promoted", map[*SyncRepStandby]string{s1: "1|sync", s2: "1|sync", s4: "1|potential"})
	set("ANY 2(standby1, standby2)")
	check("quorum", map[*SyncRepStandby]string{s1: "1|quorum", s2: "1|quorum", s4: "0|async"})

	// A standby that has not reported a flush position is async.
	s5 := s.RegisterStandby("standby1")
	if p, st := s.SlotSyncState(s5.Slot()); p != 0 || st != "async" {
		t.Errorf("no flush yet: %d|%s, want 0|async", p, st)
	}
}
