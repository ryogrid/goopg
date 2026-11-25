package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/initdb"
)

// TestLogFPWChangeOnlyOutsideRecovery pins UpdateFullPageWrites' rule for
// XLOG_FPW_CHANGE (M0149-0029b): a server in recovery — a standby, or
// archive recovery under recovery.signal — only flips full_page_writes; it
// must not append WAL of its own, or a standby's walreceiver later treats the
// primary's first bytes of that segment as already present and replays a
// spliced record. wal_level minimal never logs it either.
func TestLogFPWChangeOnlyOutsideRecovery(t *testing.T) {
	dir := t.TempDir()
	if !logFPWChange(false, dir, "replica") {
		t.Error("a primary with wal_level replica must log XLOG_FPW_CHANGE")
	}
	if !logFPWChange(false, dir, "logical") {
		t.Error("wal_level logical must log XLOG_FPW_CHANGE")
	}
	if logFPWChange(false, dir, "minimal") {
		t.Error("wal_level minimal logged XLOG_FPW_CHANGE")
	}
	if logFPWChange(true, dir, "replica") {
		t.Error("a standby logged XLOG_FPW_CHANGE")
	}
	if err := os.WriteFile(filepath.Join(dir, initdb.RecoverySignalFile), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if logFPWChange(false, dir, "replica") {
		t.Error("archive recovery (recovery.signal) logged XLOG_FPW_CHANGE")
	}
}
