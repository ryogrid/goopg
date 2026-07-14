package storage

import (
	"errors"
	"testing"
)

// makeDataPage returns a BlockSize page initialised like a real heap page
// (so it is not "new") with deterministic payload at the given seed.
func makeDataPage(seed byte) []byte {
	p := make([]byte, BlockSize)
	_ = InitPage(p)
	for i := 0; i < 512; i++ {
		p[SizeOfPageHeaderData+i] = byte(i) ^ seed
	}
	return p
}

// TestChecksumWriteVerifyRoundTrip exercises the full Manager path with
// checksums enabled: Extend then ReadBlock must round-trip a page whose
// pd_checksum was set on write and verified on read, without the caller's
// buffer being mutated.
func TestChecksumWriteVerifyRoundTrip(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir, ChecksumsEnabled: true})
	defer mgr.Close()

	rel := RelFileNode{DBOid: 1, RelOid: 100, Fork: MainFork}
	page := makeDataPage(0x5A)
	// The caller's in-memory page must NOT carry a checksum after write —
	// PageSetChecksumCopy works on a copy.
	before := MustHeader(page).Checksum()

	blk, err := mgr.Extend(rel, page)
	if err != nil {
		t.Fatalf("Extend: %v", err)
	}
	if got := MustHeader(page).Checksum(); got != before {
		t.Fatalf("Extend mutated caller buffer pd_checksum: %#x -> %#x", before, got)
	}

	out := make([]byte, BlockSize)
	if err := mgr.ReadBlock(rel, blk, out); err != nil {
		t.Fatalf("ReadBlock (should verify clean): %v", err)
	}
	// The on-disk page carries a non-zero checksum.
	if MustHeader(out).Checksum() == 0 {
		t.Fatal("on-disk page has zero pd_checksum; expected a stored checksum")
	}
}

// TestChecksumDetectsCorruption corrupts a byte on disk through a
// checksum-less Manager and confirms a checksum-enabled Manager rejects the
// read with a *ChecksumError.
func TestChecksumDetectsCorruption(t *testing.T) {
	dir := t.TempDir()
	rel := RelFileNode{DBOid: 1, RelOid: 101, Fork: MainFork}

	// Write a properly-checksummed page.
	mgrCk := NewManager(ManagerConfig{DataDir: dir, ChecksumsEnabled: true})
	blk, err := mgrCk.Extend(rel, makeDataPage(0x11))
	if err != nil {
		t.Fatalf("Extend: %v", err)
	}
	mgrCk.Close()

	// Corrupt one data byte via a checksum-less Manager (no recompute).
	mgrRaw := NewManager(ManagerConfig{DataDir: dir})
	raw := make([]byte, BlockSize)
	if err := mgrRaw.ReadBlock(rel, blk, raw); err != nil {
		t.Fatalf("raw ReadBlock: %v", err)
	}
	raw[BlockSize-1] ^= 0xFF
	if err := mgrRaw.WriteBlock(rel, blk, raw); err != nil {
		t.Fatalf("raw WriteBlock: %v", err)
	}
	mgrRaw.Close()

	// A checksum-enabled Manager must now reject the read.
	mgrVerify := NewManager(ManagerConfig{DataDir: dir, ChecksumsEnabled: true})
	defer mgrVerify.Close()
	err = mgrVerify.ReadBlock(rel, blk, make([]byte, BlockSize))
	var cerr *ChecksumError
	if !errors.As(err, &cerr) {
		t.Fatalf("expected *ChecksumError, got %v", err)
	}
	if cerr.Block != blk {
		t.Fatalf("ChecksumError.Block = %d, want %d", cerr.Block, blk)
	}
}

// TestChecksumIgnoreFailure verifies IgnoreChecksumFailure downgrades a
// mismatch to a non-fatal event and still fires OnChecksumFailure.
func TestChecksumIgnoreFailure(t *testing.T) {
	dir := t.TempDir()
	rel := RelFileNode{DBOid: 1, RelOid: 102, Fork: MainFork}

	mgrCk := NewManager(ManagerConfig{DataDir: dir, ChecksumsEnabled: true})
	blk, err := mgrCk.Extend(rel, makeDataPage(0x22))
	if err != nil {
		t.Fatalf("Extend: %v", err)
	}
	mgrCk.Close()

	mgrRaw := NewManager(ManagerConfig{DataDir: dir})
	raw := make([]byte, BlockSize)
	_ = mgrRaw.ReadBlock(rel, blk, raw)
	raw[100] ^= 0x01
	_ = mgrRaw.WriteBlock(rel, blk, raw)
	mgrRaw.Close()

	var failed bool
	mgr := NewManager(ManagerConfig{
		DataDir:               dir,
		ChecksumsEnabled:      true,
		IgnoreChecksumFailure: true,
		OnChecksumFailure:     func(RelFileNode, BlockNumber) { failed = true },
	})
	defer mgr.Close()
	if err := mgr.ReadBlock(rel, blk, make([]byte, BlockSize)); err != nil {
		t.Fatalf("with IgnoreChecksumFailure, ReadBlock should not error: %v", err)
	}
	if !failed {
		t.Fatal("OnChecksumFailure was not invoked")
	}
}

// TestChecksumDisabledIsByteIdentical confirms a checksum-less Manager
// leaves pd_checksum untouched on write and never verifies on read — the
// default-OFF fast path that keeps existing clusters byte-identical.
func TestChecksumDisabledIsByteIdentical(t *testing.T) {
	dir := t.TempDir()
	rel := RelFileNode{DBOid: 1, RelOid: 103, Fork: MainFork}
	mgr := NewManager(ManagerConfig{DataDir: dir})
	defer mgr.Close()

	page := makeDataPage(0x33)
	blk, err := mgr.Extend(rel, page)
	if err != nil {
		t.Fatalf("Extend: %v", err)
	}
	out := make([]byte, BlockSize)
	if err := mgr.ReadBlock(rel, blk, out); err != nil {
		t.Fatalf("ReadBlock: %v", err)
	}
	// pd_checksum stays whatever the page carried (zero here) — no
	// checksum was computed.
	if MustHeader(out).Checksum() != 0 {
		t.Fatalf("checksum-less write set pd_checksum=%#x, want 0", MustHeader(out).Checksum())
	}
}

// TestChecksumExtendBatch verifies every block written by ExtendBatch gets
// its own block-number-specific checksum and reads back clean.
func TestChecksumExtendBatch(t *testing.T) {
	dir := t.TempDir()
	rel := RelFileNode{DBOid: 1, RelOid: 104, Fork: MainFork}
	mgr := NewManager(ManagerConfig{DataDir: dir, ChecksumsEnabled: true})
	defer mgr.Close()

	const n = 8
	first, err := mgr.ExtendBatch(rel, makeDataPage(0x44), n)
	if err != nil {
		t.Fatalf("ExtendBatch: %v", err)
	}
	for i := 0; i < n; i++ {
		if err := mgr.ReadBlock(rel, first+BlockNumber(i), make([]byte, BlockSize)); err != nil {
			t.Fatalf("ReadBlock blk %d: %v", first+BlockNumber(i), err)
		}
	}
}

// TestChecksumNewPageSkipped confirms an all-zero (new) page is written and
// read back without a checksum being imposed — new pages carry none.
func TestChecksumNewPageSkipped(t *testing.T) {
	dir := t.TempDir()
	rel := RelFileNode{DBOid: 1, RelOid: 105, Fork: MainFork}
	mgr := NewManager(ManagerConfig{DataDir: dir, ChecksumsEnabled: true})
	defer mgr.Close()

	zero := make([]byte, BlockSize) // pd_upper == 0 => IsNew
	blk, err := mgr.Extend(rel, zero)
	if err != nil {
		t.Fatalf("Extend: %v", err)
	}
	out := make([]byte, BlockSize)
	if err := mgr.ReadBlock(rel, blk, out); err != nil {
		t.Fatalf("ReadBlock of new page should verify clean: %v", err)
	}
	if MustHeader(out).Checksum() != 0 {
		t.Fatalf("new page got pd_checksum=%#x, want 0", MustHeader(out).Checksum())
	}
}

// TestChecksumRelFilePrepareWriteVerifyRead exercises relFile's
// PrepareWrite/VerifyRead directly. These implement
// aio.ChecksumFile (structurally — storage does not import
// internal/aio) so the io_uring AIO method's raw-fd submission
// path (which bypasses WriteAt/ReadAt and their inline checksum
// stamping/verification entirely) still gets the same checksum
// semantics as the synchronous path. A regression here would let
// io_method=io_uring persist stale checksums or skip verification
// while every other Manager entry point stayed correct — see
// storage/aio-relfile-mu-bypass in .ralph/fix_plan.md.
func TestChecksumRelFilePrepareWriteVerifyRead(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir, ChecksumsEnabled: true})
	defer mgr.Close()

	rel := RelFileNode{DBOid: 1, RelOid: 106, Fork: MainFork}
	page := makeDataPage(0x7C)
	blk, err := mgr.Extend(rel, page)
	if err != nil {
		t.Fatalf("Extend: %v", err)
	}
	f, err := mgr.relFile(rel)
	if err != nil {
		t.Fatalf("relFile: %v", err)
	}
	off := int64(blk) * BlockSize

	before := MustHeader(page).Checksum()
	stamped := f.PrepareWrite(page, off)
	if got := MustHeader(page).Checksum(); got != before {
		t.Fatalf("PrepareWrite mutated caller buffer: %#x -> %#x", before, got)
	}
	if !VerifyPage(stamped, blk) {
		t.Fatal("PrepareWrite's stamped copy does not verify against VerifyPage")
	}
	if err := f.VerifyRead(stamped, off); err != nil {
		t.Fatalf("VerifyRead rejected a correctly-stamped page: %v", err)
	}

	corrupt := append([]byte(nil), stamped...)
	corrupt[0] ^= 0xFF
	var cerr *ChecksumError
	if err := f.VerifyRead(corrupt, off); !errors.As(err, &cerr) {
		t.Fatalf("VerifyRead on a corrupted page: got %v, want *ChecksumError", err)
	} else if cerr.Block != blk {
		t.Fatalf("ChecksumError.Block = %d, want %d", cerr.Block, blk)
	}

	// Misaligned offset / non-block-sized buffer: both must be
	// passed through untouched, mirroring WriteAt/ReadAt's own
	// off%BlockSize-aligned, full-block-length gate.
	short := make([]byte, BlockSize/2)
	if got := f.PrepareWrite(short, off); &got[0] != &short[0] {
		t.Error("PrepareWrite on a non-block-sized buffer should return it unchanged, got a different slice")
	}
	if err := f.VerifyRead(short, off); err != nil {
		t.Errorf("VerifyRead on a non-block-sized buffer should no-op, got %v", err)
	}
	if err := f.VerifyRead(stamped, off+1); err != nil {
		t.Errorf("VerifyRead on a misaligned offset should no-op, got %v", err)
	}
}
