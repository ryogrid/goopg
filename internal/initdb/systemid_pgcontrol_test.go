package initdb

// M0131-S2 guards for LoadOrCreateSystemID's pg_control-first resolution.
//
// Before this slice, LoadOrCreateSystemID knew only the goopg-private
// global/system_identifier flat file and invented a fresh random uint64
// whenever it was missing — so opening a directory PostgreSQL's initdb had
// created gave the cluster a SECOND identity, disagreeing with the
// system_identifier sitting in the very same directory's pg_control. That
// value is stamped as xlp_sysid into every long WAL page header, which
// upstream's reader rejects on mismatch (xlogreader.c:1282-1286), so the
// divergence is silent at start-up and only surfaces at a replication attach.
//
// Design: docs/design/0131-0002-system-identifier-pgcontrol-fallback.md

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// stageGlobalDir creates <dir>/global, the parent both copies live in.
func stageGlobalDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "global"), 0o700); err != nil {
		t.Fatalf("mkdir global: %v", err)
	}
	return dir
}

// stagePgControl writes a CRC-valid pg_control carrying sysID, exactly as
// upstream's initdb would leave it.
func stagePgControl(t *testing.T, dir string, sysID uint64) {
	t.Helper()
	buf := buildPgControl(sysID, time.Now(), nil, false)
	if err := os.WriteFile(filepath.Join(dir, pgControlFile), buf, 0o600); err != nil {
		t.Fatalf("write pg_control: %v", err)
	}
}

// readFlatSystemID reads the raw 8 bytes of the flat file.
func readFlatSystemID(t *testing.T, dir string) uint64 {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, systemIdentifierFile))
	if err != nil {
		t.Fatalf("read system_identifier: %v", err)
	}
	if len(data) != 8 {
		t.Fatalf("system_identifier length = %d, want 8", len(data))
	}
	return binary.LittleEndian.Uint64(data)
}

// TestLoadOrCreateSystemID_AdoptsPgControl is the PG-authored-directory
// case: pg_control exists, the goopg-private flat file does not. goopg must
// adopt pg_control's identity rather than invent one. Fails before M0131-S2
// (a random value comes back).
func TestLoadOrCreateSystemID_AdoptsPgControl(t *testing.T) {
	const want = uint64(0x1122334455667788)
	dir := stageGlobalDir(t)
	stagePgControl(t, dir, want)

	got, err := LoadOrCreateSystemID(dir)
	if err != nil {
		t.Fatalf("LoadOrCreateSystemID: %v", err)
	}
	if got != want {
		t.Fatalf("system id = %#x, want pg_control's %#x", got, want)
	}
	// The flat file is written from pg_control so every later start is a
	// single 8-byte read.
	if flat := readFlatSystemID(t, dir); flat != want {
		t.Fatalf("flat file = %#x, want %#x", flat, want)
	}
}

// TestLoadOrCreateSystemID_PgControlWinsOnDisagreement covers the crash /
// hand-edit case: both copies exist and differ. pg_control is authoritative
// (upstream keeps no second copy at all), so the flat file is corrected —
// the same precedence LoadOrCreateTimelineID already applies to the TLI.
func TestLoadOrCreateSystemID_PgControlWinsOnDisagreement(t *testing.T) {
	const ctrlID = uint64(0xAABBCCDD00112233)
	const staleID = uint64(0x0F0F0F0F0F0F0F0F)
	dir := stageGlobalDir(t)
	stagePgControl(t, dir, ctrlID)
	if err := writeSystemIDFile(filepath.Join(dir, systemIdentifierFile), staleID); err != nil {
		t.Fatalf("stage flat file: %v", err)
	}

	got, err := LoadOrCreateSystemID(dir)
	if err != nil {
		t.Fatalf("LoadOrCreateSystemID: %v", err)
	}
	if got != ctrlID {
		t.Fatalf("system id = %#x, want pg_control's %#x", got, ctrlID)
	}
	if flat := readFlatSystemID(t, dir); flat != ctrlID {
		t.Fatalf("flat file not corrected: %#x, want %#x", flat, ctrlID)
	}
}

// TestLoadOrCreateSystemID_FreshCluster: neither copy exists (the state
// Init() calls into, before writePgControl runs fourteen lines later). A
// random non-zero id is generated, persisted, and stable across calls.
func TestLoadOrCreateSystemID_FreshCluster(t *testing.T) {
	dir := stageGlobalDir(t)

	first, err := LoadOrCreateSystemID(dir)
	if err != nil {
		t.Fatalf("LoadOrCreateSystemID: %v", err)
	}
	if first == 0 {
		t.Fatal("generated system id is zero")
	}
	if flat := readFlatSystemID(t, dir); flat != first {
		t.Fatalf("flat file = %#x, want %#x", flat, first)
	}
	again, err := LoadOrCreateSystemID(dir)
	if err != nil {
		t.Fatalf("re-load: %v", err)
	}
	if again != first {
		t.Fatalf("system id not stable: %#x then %#x", first, again)
	}
}

// TestLoadOrCreateSystemID_CorruptPgControlFallsBack asserts the CRC gate in
// control.ReadControlFile is what keeps a damaged control file from
// poisoning the cluster identity: the flat file is used instead, and no
// panic escapes.
func TestLoadOrCreateSystemID_CorruptPgControlFallsBack(t *testing.T) {
	const flatID = uint64(0x7766554433221100)
	dir := stageGlobalDir(t)
	stagePgControl(t, dir, 0xDEADBEEFDEADBEEF)
	// Corrupt a CRC-covered byte without touching the stored CRC.
	path := filepath.Join(dir, pgControlFile)
	buf, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read pg_control: %v", err)
	}
	buf[100] ^= 0xFF
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatalf("rewrite pg_control: %v", err)
	}
	if err := writeSystemIDFile(filepath.Join(dir, systemIdentifierFile), flatID); err != nil {
		t.Fatalf("stage flat file: %v", err)
	}

	got, err := LoadOrCreateSystemID(dir)
	if err != nil {
		t.Fatalf("LoadOrCreateSystemID: %v", err)
	}
	if got != flatID {
		t.Fatalf("system id = %#x, want the flat file's %#x", got, flatID)
	}
}

// TestInitSystemIDMatchesPgControlAndBootstrapWAL is the end-to-end
// invariant the pg_control-first order must not disturb: a directory goopg
// itself created agrees with itself in all three places the identity is
// recorded — the flat file, pg_control (offset 0), and xlp_sysid in the
// bootstrap WAL segment's long page header (bytes 24:32, xlog_page.go:169).
func TestInitSystemIDMatchesPgControlAndBootstrapWAL(t *testing.T) {
	dir := t.TempDir()
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	flat := readFlatSystemID(t, dir)
	if flat == 0 {
		t.Fatal("Init left a zero system identifier")
	}

	ctrl, err := os.ReadFile(filepath.Join(dir, pgControlFile))
	if err != nil {
		t.Fatalf("read pg_control: %v", err)
	}
	if got := binary.LittleEndian.Uint64(ctrl[0:8]); got != flat {
		t.Fatalf("pg_control system_identifier = %#x, flat file = %#x", got, flat)
	}

	seg, err := os.ReadFile(filepath.Join(dir, "pg_wal", "000000010000000000000001"))
	if err != nil {
		t.Fatalf("read bootstrap WAL segment: %v", err)
	}
	if got := binary.LittleEndian.Uint64(seg[24:32]); got != flat {
		t.Fatalf("xlp_sysid = %#x, want %#x", got, flat)
	}

	// Re-resolving on the finished directory now takes the pg_control path
	// and must return the same identity.
	again, err := LoadOrCreateSystemID(dir)
	if err != nil {
		t.Fatalf("re-resolve: %v", err)
	}
	if again != flat {
		t.Fatalf("re-resolved system id = %#x, want %#x", again, flat)
	}
}
