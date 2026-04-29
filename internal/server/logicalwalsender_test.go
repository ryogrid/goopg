package server

import (
	"bytes"
	"testing"

	"github.com/goopg/goopg/internal/protocol"
)

// TestParseStartReplicationArgsLogical pins the publisher-side
// parser for the LOGICAL form: a real subscriber emits something
// like
//
//	START_REPLICATION SLOT logical1 LOGICAL 0/0
//	    ("proto_version" '1', "publication_names" 'p1,p2')
//
// and the parser must recognise the mode keyword, slot name, LSN,
// and the option block.
func TestParseStartReplicationArgsLogical(t *testing.T) {
	args, err := parseStartReplicationArgs(
		`START_REPLICATION SLOT logical1 LOGICAL 0/CAFE ("proto_version" '1', "publication_names" 'p1,p2')`,
	)
	if err != nil {
		t.Fatal(err)
	}
	if args.Mode != "LOGICAL" {
		t.Errorf("Mode=%q want LOGICAL", args.Mode)
	}
	if args.SlotName != "logical1" {
		t.Errorf("SlotName=%q want logical1", args.SlotName)
	}
	if args.StartLSN != 0xCAFE {
		t.Errorf("StartLSN=%x want 0xCAFE", args.StartLSN)
	}
	if args.Options["proto_version"] != "1" {
		t.Errorf("proto_version=%q want 1", args.Options["proto_version"])
	}
	if args.Options["publication_names"] != "p1,p2" {
		t.Errorf("publication_names=%q want p1,p2", args.Options["publication_names"])
	}
}

// TestParseStartReplicationArgsLogicalRequiresSlot pins the
// upstream rule that LOGICAL mode demands a SLOT clause.
func TestParseStartReplicationArgsLogicalRequiresSlot(t *testing.T) {
	if _, err := parseStartReplicationArgs(`START_REPLICATION LOGICAL 0/0`); err == nil {
		t.Errorf("LOGICAL without SLOT was accepted")
	}
}

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

// TestWalsenderPgoutputAdapterWrapsAsCopyData pins the wire-
// format invariant: every Write call from a PgOutput plugin
// becomes one `'w'` CopyData frame on the wire, with monotonic
// startLSN/endLSN. The subscriber's LogicalReceiver expects
// exactly this shape.
func TestWalsenderPgoutputAdapterWrapsAsCopyData(t *testing.T) {
	var buf bytes.Buffer
	fw := protocol.NewFrameWriter(&buf)

	a := &walsenderPgoutputAdapter{w: fw, nextLSN: 100}
	if _, err := a.Write([]byte("first-message")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Write([]byte("second")); err != nil {
		t.Fatal(err)
	}

	// Read the framed bytes back.
	fr := protocol.NewFrameReader(&buf)
	f1, err := fr.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if f1.Type != protocol.MsgCopyData {
		t.Errorf("frame[0].Type=%q want CopyData", f1.Type)
	}
	parsed, kind, err := protocol.DecodeReplicationMessage(f1.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if kind != protocol.ReplMsgWALData {
		t.Errorf("frame[0] inner kind=%q want w", kind)
	}
	m1 := parsed.(*protocol.WALDataMessage)
	if string(m1.WALBytes) != "first-message" {
		t.Errorf("frame[0] body=%q", m1.WALBytes)
	}
	if m1.StartLSN != 100 {
		t.Errorf("frame[0] StartLSN=%d want 100", m1.StartLSN)
	}
	if m1.EndLSN != 100+uint64(len("first-message"))-1 {
		t.Errorf("frame[0] EndLSN=%d", m1.EndLSN)
	}

	f2, err := fr.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	parsed2, _, _ := protocol.DecodeReplicationMessage(f2.Payload)
	m2 := parsed2.(*protocol.WALDataMessage)
	if m2.StartLSN != m1.EndLSN+1 {
		t.Errorf("frame[1] StartLSN=%d want %d (monotonic)", m2.StartLSN, m1.EndLSN+1)
	}
	if string(m2.WALBytes) != "second" {
		t.Errorf("frame[1] body=%q", m2.WALBytes)
	}
}
