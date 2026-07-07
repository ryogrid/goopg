package autovacuum

import (
	"context"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/mvcc"
)

// TestLauncherStartStop verifies the launcher runs and stops cleanly.
func TestLauncherStartStop(t *testing.T) {
	cat := catalog.NewInMemory()
	l := NewLauncher(nil, nil, cat)
	l.NapInterval = 10 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := l.Run(ctx)
	if err != nil && err != context.Canceled && err != context.DeadlineExceeded {
		t.Fatalf("Run: %v", err)
	}
}

// TestNeedsVacuumRespectsAutovacuumEnabledReloption verifies that
// WITH (autovacuum_enabled=false) suppresses both vacuum and analyze,
// mirroring PostgreSQL's relation_needs_vacanalyze (autovacuum.c).
func TestNeedsVacuumRespectsAutovacuumEnabledReloption(t *testing.T) {
	cat := catalog.NewInMemory()
	l := NewLauncher(nil, nil, cat)

	tbl := &catalog.Table{
		Schema: "public",
		Name:   "t",
		Stats:  &catalog.TableStats{RowCount: 100},
	}

	if !l.needsVacuum(tbl) {
		t.Fatalf("needsVacuum: expected true with no reloption set and RowCount>0")
	}
	if !l.needsAnalyze(tbl) {
		t.Fatalf("needsAnalyze: expected true with no reloption set")
	}

	tbl.AutovacuumEnabledSet = true
	tbl.AutovacuumEnabled = false
	if l.needsVacuum(tbl) {
		t.Fatalf("needsVacuum: expected false when autovacuum_enabled=false")
	}
	if l.needsAnalyze(tbl) {
		t.Fatalf("needsAnalyze: expected false when autovacuum_enabled=false")
	}

	tbl.AutovacuumEnabled = true
	if !l.needsVacuum(tbl) {
		t.Fatalf("needsVacuum: expected true when autovacuum_enabled=true")
	}
}

// TestNeedsVacuumAntiWraparoundOverridesDisabledReloption verifies that
// anti-wraparound forcing still fires even when autovacuum_enabled=false,
// matching autovacuum.c's "ignore [the disable] if at risk" comment.
func TestNeedsVacuumAntiWraparoundOverridesDisabledReloption(t *testing.T) {
	cat := catalog.NewInMemory()
	txnMgr := mvcc.NewManager()
	l := NewLauncher(nil, txnMgr, cat)

	txnMgr.SetNextXID(autovacuumFreezeMaxAge + 1_000)

	tbl := &catalog.Table{
		Schema:               "public",
		Name:                 "t",
		Stats:                &catalog.TableStats{RowCount: 100},
		RelFrozenXID:         3,
		AutovacuumEnabledSet: true,
		AutovacuumEnabled:    false,
	}

	if !l.needsVacuum(tbl) {
		t.Fatalf("needsVacuum: expected true (anti-wraparound) even with autovacuum_enabled=false")
	}
}
