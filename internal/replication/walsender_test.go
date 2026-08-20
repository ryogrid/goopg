// In-package walsender tests: the ones that reach unexported symbols
// (parseStartReplicationArgs and friends). The wire-level tests that drive a
// real postmaster server live in walsender_wire_test.go, which must be in the
// EXTERNAL test package to avoid a test import cycle through postmaster.
package replication

import "testing"

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
