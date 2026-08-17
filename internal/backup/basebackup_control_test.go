package backup_test

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/access/transam/control"
	"github.com/goopg/goopg/internal/libpq"
)

// fakeCheckpointer reports a fixed checkpoint position. BASE_BACKUP only
// reads the two LSNs; CheckpointNow is a no-op because the fixture's WAL
// writer has nothing to flush.
type fakeCheckpointer struct {
	redo   uint64
	record uint64
}

func (f fakeCheckpointer) CheckpointNow() error            { return nil }
func (f fakeCheckpointer) CheckpointRedoLSN() uint64       { return f.redo }
func (f fakeCheckpointer) LastCheckpointRecordLSN() uint64 { return f.record }

// collectBaseBackupTar drives one BASE_BACKUP over the replication protocol
// and returns the concatenated base.tar bytes. It is the framing test's
// copy-stream loop reduced to the payload — the framing itself is asserted by
// TestBaseBackupWireProtocolFraming, so this helper only fails on frames that
// would make the tar unreadable.
func collectBaseBackupTar(t *testing.T, addr string) []byte {
	t.Helper()
	conn, r, w := dialReplication(t, addr)
	defer conn.Close()
	sendQuery(t, w, "BASE_BACKUP (LABEL 'pg-control-test', TARGET 'client')")

	var tarBytes bytes.Buffer
	for _, f := range readUntilReadyForQuery(t, r) {
		if f.Type != libpq.MsgCopyData || len(f.Payload) == 0 {
			continue
		}
		switch f.Payload[0] {
		case 'd':
			tarBytes.Write(f.Payload[1:])
		case 'n', 'p':
			// archive header / progress — not tar payload.
		default:
			t.Fatalf("unexpected CopyData type byte %q", f.Payload[0])
		}
	}
	if tarBytes.Len() == 0 {
		t.Fatal("BASE_BACKUP produced no tar bytes")
	}
	return tarBytes.Bytes()
}

// tarFile returns the contents of `name` inside the tar image, or nil.
func tarFile(t *testing.T, image []byte, name string) []byte {
	t.Helper()
	tr := tar.NewReader(bytes.NewReader(image))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			t.Fatalf("tar.Next: %v", err)
		}
		if hdr.Name != name {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read %s from tar: %v", name, err)
		}
		return data
	}
}

// TestBaseBackupDoesNotMutateLiveControlFile is M0131-S29's gate.
//
// BASE_BACKUP needs a checkpoint-patched pg_control for the *backup's*
// consumer: a PG standby restoring it wants a valid REDO location, a non-zero
// minRecoveryPoint (XLogRecPtrIsInvalid(0) would short-circuit
// CheckRecoveryConsistency) and a backupEndPoint. goopg used to obtain that
// image by rewriting the LIVE cluster's control file and then shipping the
// file it had just rewritten. Two things were wrong with that:
//
//   - Upstream never does it. basebackup.c:352-360 sends XLOG_CONTROL_FILE
//     through a plain sendFile(); the source cluster is read-only to a backup.
//   - It publishes minRecoveryPoint = 1 / minRecoveryPointTLI = <live TLI>
//     into a running primary. Crash recovery is required to leave both invalid
//     (xlog.c:7295-7297, "crash recovery should always recover to the end of
//     WAL"), and on a promoted (TLI >= 2) cluster a crash inside the
//     BASE_BACKUP -> next-checkpoint window makes PG FATAL "requested timeline
//     %u does not contain minimum recovery point"
//     (xlogrecovery.c:878-886) — an unbootable cluster produced by taking a
//     backup.
//
// The test therefore asserts BOTH halves: the live file is byte-identical
// across the backup, and the shipped image still carries the patch (so the
// first half cannot pass by simply not patching anything).
func TestBaseBackupDoesNotMutateLiveControlFile(t *testing.T) {
	// A checkpoint REDO location is what puts BASE_BACKUP on the
	// pg_control-patching path at all; without one the fixture would prove
	// nothing (half 2 below is what catches that).
	addr, dataDir, stop := startBaseBackupTestServerWithCheckpointer(t,
		fakeCheckpointer{redo: 0x2000, record: 0x2100})
	defer stop()

	ctlPath := filepath.Join(dataDir, "global", "pg_control")
	// The fixture's pg_control is an 0xC0 stub with no valid CRC. Rewrite it
	// once, before the backup, into a decodable image with the three fields
	// this test watches explicitly zeroed — so a non-zero reading afterwards
	// can only have come from the backup path.
	if err := control.UpdateControlFile(dataDir, func(cd *control.ControlFileData) {
		cd.State = control.DBStateInProduction
		cd.MinRecoveryPoint = 0
		cd.MinRecoveryPointTLI = 0
		cd.BackupEndPoint = 0
		cd.CheckPoint = 0
		cd.CheckPointCopyRedo = 0
		// LoadOrCreateTimelineID reads the timeline out of pg_control, so the
		// stub's 0xC0C0C0C0 has to become a real bootstrap timeline.
		cd.CheckPointCopyThisTLI = 1
		cd.CheckPointCopyPrevTLI = 1
	}); err != nil {
		t.Fatalf("seed pg_control: %v", err)
	}
	before, err := os.ReadFile(ctlPath)
	if err != nil {
		t.Fatalf("read pg_control before backup: %v", err)
	}

	image := collectBaseBackupTar(t, addr)

	// --- half 1: the live control file is untouched, byte for byte. ---
	after, err := os.ReadFile(ctlPath)
	if err != nil {
		t.Fatalf("read pg_control after backup: %v", err)
	}
	if !bytes.Equal(before, after) {
		live, rerr := control.ReadControlFile(dataDir)
		detail := ""
		if rerr == nil && live != nil {
			detail = fieldSummary(live)
		}
		t.Fatalf("BASE_BACKUP rewrote the live global/pg_control (M0131-S29): %s\n"+
			"upstream's basebackup.c:352-360 only reads it; writing minRecoveryPoint "+
			"into a running primary FATALs PG after a crash on a promoted timeline "+
			"(xlogrecovery.c:878-886)", detail)
	}

	// --- half 2: the SHIPPED image does carry the patch. ---
	shipped := tarFile(t, image, "global/pg_control")
	if shipped == nil {
		t.Fatal("tar has no global/pg_control")
	}
	if bytes.Equal(shipped, before) {
		t.Fatal("shipped pg_control is byte-identical to the live one — the backup " +
			"is no longer patched at all, so half 1 above proves nothing")
	}
	// Re-read it the way a restoring standby would, which also checks the CRC
	// was recomputed over the patched bytes.
	restored := t.TempDir()
	if err := os.MkdirAll(filepath.Join(restored, "global"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(restored, "global", "pg_control"), shipped, 0o600); err != nil {
		t.Fatal(err)
	}
	cd, err := control.ReadControlFile(restored)
	if err != nil {
		t.Fatalf("shipped pg_control does not verify: %v", err)
	}
	if cd.MinRecoveryPoint == 0 {
		t.Error("shipped pg_control has minRecoveryPoint = 0; a restoring standby " +
			"short-circuits CheckRecoveryConsistency on XLogRecPtrIsInvalid()")
	}
	if cd.MinRecoveryPointTLI != 1 {
		t.Errorf("shipped pg_control minRecoveryPointTLI = %d, want 1 (the live timeline)",
			cd.MinRecoveryPointTLI)
	}
	if cd.CheckPointCopyThisTLI != 1 {
		t.Errorf("shipped pg_control checkPointCopy.ThisTimeLineID = %d, want 1",
			cd.CheckPointCopyThisTLI)
	}
}

// fieldSummary renders the fields the S29 gate watches, for failure output.
func fieldSummary(cd *control.ControlFileData) string {
	return fmt.Sprintf("live minRecoveryPoint=%d tli=%d backupEndPoint=%d checkPoint=%d",
		cd.MinRecoveryPoint, cd.MinRecoveryPointTLI, cd.BackupEndPoint, cd.CheckPoint)
}
