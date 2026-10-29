// In-package walsender tests: the ones that reach unexported symbols
// (parseStartReplicationArgs and friends). The wire-level tests that drive a
// real postmaster server live in walsender_wire_test.go, which must be in the
// EXTERNAL test package to avoid a test import cycle through postmaster.
package replication

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/utils/errcodes"
)

// TestParseStartReplicationArgsPhysicalStillWorks: the
// existing PHYSICAL grammar continues to parse, including
// the slot+timeline shape.
func TestParseStartReplicationArgsPhysicalStillWorks(t *testing.T) {
	args, err := parseStartReplicationArgs(`START_REPLICATION SLOT primary PHYSICAL 1/2 TIMELINE 1`)
	if err != nil {
		t.Fatal(err)
	}
	if args.Mode != "PHYSICAL" {
		t.Errorf("Mode=%q", args.Mode)
	}
	if args.SlotName != "primary" {
		t.Errorf("SlotName=%q", args.SlotName)
	}
	if args.StartLSN != (uint64(1)<<32)|2 {
		t.Errorf("StartLSN=%x", args.StartLSN)
	}
	if args.Timeline != 1 {
		t.Errorf("Timeline=%d", args.Timeline)
	}
}

func TestParseStartReplicationArgsPhysicalKeywordOptional(t *testing.T) {
	args, err := parseStartReplicationArgs(`START_REPLICATION SLOT primary 1/2 TIMELINE 1`)
	if err != nil {
		t.Fatal(err)
	}
	if args.Mode != "PHYSICAL" {
		t.Errorf("Mode=%q", args.Mode)
	}
	if args.SlotName != "primary" {
		t.Errorf("SlotName=%q", args.SlotName)
	}
	if args.StartLSN != (uint64(1)<<32)|2 {
		t.Errorf("StartLSN=%x", args.StartLSN)
	}
	if args.Timeline != 1 {
		t.Errorf("Timeline=%d", args.Timeline)
	}
	args, err = parseStartReplicationArgs(`START_REPLICATION 0/0`)
	if err != nil {
		t.Fatal(err)
	}
	if args.Mode != "PHYSICAL" {
		t.Errorf("Mode=%q", args.Mode)
	}
	if args.StartLSN != 0 {
		t.Errorf("StartLSN=%x", args.StartLSN)
	}
}

// TestHistoricTimelineNotInHistory: a START_REPLICATION TIMELINE below the
// current one that the current timeline's history does not list is
// tliSwitchPoint's elog ERROR (XX000), not StartReplication's 55000.
func TestHistoricTimelineNotInHistory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "00000003.history"), []byte("1\t0/3000098\tno recovery target specified\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: Config{WALDirPath: dir}}
	_, code, err := h.historicTimeline(2, 3, 0)
	if err == nil || err.Error() != "requested timeline 2 is not in this server's history" || code != errcodes.InternalError {
		t.Errorf("historicTimeline(2) = %v (%s), want XX000 \"requested timeline 2 is not in this server's history\"", err, code)
	}
	hist, _, err := h.historicTimeline(1, 3, 0)
	if err != nil || !hist.set || hist.validUpto != 0x3000098 || hist.nextTLI != 3 {
		t.Errorf("historicTimeline(1) = %+v, %v; want ends at 0/3000098, next timeline 3", hist, err)
	}
}
