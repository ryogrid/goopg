package initdb

// M0131-S18.2 golden gate: the checkPointCopy members goopg now decodes and
// encodes must agree, field for field, with what the REAL pg_controldata
// reports for the same directory — and must still agree after a runtime
// UpdateControlFile round-trip.
//
// Why a golden gate rather than a self-consistency test: goopg's decoder and
// its own initdb writer share the offset table, so they would agree even if
// every offset were wrong by four bytes (the pg_time_t alignment pad before
// checkPointCopy.time makes that a live risk). pg_controldata is the
// independent oracle.

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/control"
	"github.com/goopg/goopg/internal/access/transam/xlog"
)

// pgControldataBin locates the real pg_controldata: PATH first, then the
// in-tree oracle install. Returns "" when neither is present.
func pgControldataBin(t *testing.T) string {
	t.Helper()
	if p, err := exec.LookPath("pg_controldata"); err == nil {
		return p
	}
	// internal/initdb → repo root is two levels up.
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	p := filepath.Join(wd, "..", "..", "postgres", "local_install", "bin", "pg_controldata")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

// runPgControldata returns pg_controldata's "label: value" output as a map
// with labels trimmed. Note upstream emits some labels with no space before
// the value (e.g. "oldestCommitTsXid:0"), so the split is on the first colon.
func runPgControldata(t *testing.T, bin, dataDir string) map[string]string {
	t.Helper()
	out, err := exec.Command(bin, "-D", dataDir).CombinedOutput()
	if err != nil {
		t.Fatalf("pg_controldata -D %s: %v\n%s", dataDir, err, out)
	}
	fields := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		line := sc.Text()
		i := strings.Index(line, ":")
		if i < 0 {
			continue
		}
		fields[strings.TrimSpace(line[:i])] = strings.TrimSpace(line[i+1:])
	}
	if len(fields) == 0 {
		t.Fatalf("pg_controldata produced no parsable output:\n%s", out)
	}
	// A corrupt file still prints, with this warning prepended — never let
	// that pass as a green run.
	if strings.Contains(string(out), "incorrect checksum") {
		t.Fatalf("pg_controldata reports an incorrect pg_control checksum:\n%s", out)
	}
	return fields
}

func mustUint32(t *testing.T, fields map[string]string, label string) uint32 {
	t.Helper()
	raw, ok := fields[label]
	if !ok {
		t.Fatalf("pg_controldata output has no %q line (labels: %v)", label, sortedLabels(fields))
	}
	v, err := strconv.ParseUint(raw, 10, 32)
	if err != nil {
		t.Fatalf("parse %q = %q: %v", label, raw, err)
	}
	return uint32(v)
}

func sortedLabels(fields map[string]string) []string {
	out := make([]string, 0, len(fields))
	for k := range fields {
		out = append(out, k)
	}
	return out
}

// checkPointCopyGolden compares every S18.2 field against pg_controldata.
func checkPointCopyGolden(t *testing.T, bin, dataDir string, cd *control.ControlFileData) {
	t.Helper()
	fields := runPgControldata(t, bin, dataDir)
	for _, c := range []struct {
		label string
		got   uint32
	}{
		{"Latest checkpoint's NextMultiXactId", cd.CheckPointCopyNextMulti},
		{"Latest checkpoint's NextMultiOffset", cd.CheckPointCopyNextMultiOffset},
		{"Latest checkpoint's oldestXID", cd.CheckPointCopyOldestXid},
		{"Latest checkpoint's oldestXID's DB", cd.CheckPointCopyOldestXidDB},
		{"Latest checkpoint's oldestMultiXid", cd.CheckPointCopyOldestMulti},
		{"Latest checkpoint's oldestMulti's DB", cd.CheckPointCopyOldestMultiDB},
		{"Latest checkpoint's oldestCommitTsXid", cd.CheckPointCopyOldestCommitTsXid},
		{"Latest checkpoint's newestCommitTsXid", cd.CheckPointCopyNewestCommitTsXid},
		{"Latest checkpoint's oldestActiveXID", cd.CheckPointCopyOldestActiveXid},
		{"Latest checkpoint's NextOID", cd.CheckPointCopyNextOid},
	} {
		if want := mustUint32(t, fields, c.label); c.got != want {
			t.Errorf("%s: goopg decoded %d, pg_controldata reports %d", c.label, c.got, want)
		}
	}
}

// TestPgControlCheckPointCopyMatchesPgControldata inits a cluster, checks
// goopg's decode against the oracle, then seeds distinctive values, runs a
// no-op UpdateControlFile, and checks again.
//
// The two halves catch different breakages. The oracle comparison catches a
// wrong OFFSET (goopg and pg_controldata disagree about the same bytes). It
// does NOT catch a missing encode line — read-modify-write silently preserves
// a field nobody writes — so the final settability assertion carries that
// case: with the oldestMulti encode line removed, the seeded 55 never lands.
// A missing DECODE line is worse and is caught by both: the field decodes as
// 0 and encode then writes that 0 over the real value.
func TestPgControlCheckPointCopyMatchesPgControldata(t *testing.T) {
	bin := pgControldataBin(t)
	if bin == "" {
		t.Skip("pg_controldata not found on PATH or in postgres/local_install/bin")
	}
	dir := filepath.Join(t.TempDir(), "cluster")
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	cd, err := control.ReadControlFile(dir)
	if err != nil {
		t.Fatalf("ReadControlFile: %v", err)
	}
	t.Run("after_initdb", func(t *testing.T) { checkPointCopyGolden(t, bin, dir, cd) })

	// Byte-for-byte: a no-op UpdateControlFile must not disturb ANY of the
	// 8192 bytes. goopg's fn does not touch `time` (unlike upstream, which
	// restamps it), so the CRC recomputes to the same value. This is the
	// broadest offset guard available — it covers fields nobody has modelled
	// in ControlFileData yet, and it fails if any encode line writes to the
	// wrong offset.
	ctlPath := filepath.Join(dir, "global", "pg_control")
	before, err := os.ReadFile(ctlPath)
	if err != nil {
		t.Fatalf("read pg_control: %v", err)
	}
	if err := control.UpdateControlFile(dir, func(cd *control.ControlFileData) {}); err != nil {
		t.Fatalf("UpdateControlFile (identity): %v", err)
	}
	after, err := os.ReadFile(ctlPath)
	if err != nil {
		t.Fatalf("re-read pg_control: %v", err)
	}
	if len(before) != len(after) {
		t.Fatalf("pg_control size changed: %d → %d", len(before), len(after))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("identity UpdateControlFile changed byte %d: %#x → %#x", i, before[i], after[i])
		}
	}

	// Seed non-default values so the round-trip assertion has something to
	// lose. initdb leaves several of these at 0/1, where a dropped encode
	// line would be invisible.
	if err := control.UpdateControlFile(dir, func(cd *control.ControlFileData) {
		cd.CheckPointCopyNextMulti = 4242
		cd.CheckPointCopyNextMultiOffset = 909091
		cd.CheckPointCopyOldestXid = 777
		cd.CheckPointCopyOldestXidDB = 16401
		cd.CheckPointCopyOldestMulti = 55
		cd.CheckPointCopyOldestMultiDB = 16402
		cd.CheckPointCopyOldestCommitTsXid = 601
		cd.CheckPointCopyNewestCommitTsXid = 602
		cd.CheckPointCopyOldestActiveXid = 603
	}); err != nil {
		t.Fatalf("UpdateControlFile (seed): %v", err)
	}

	// A no-op update: this is the read-modify-write cycle every runtime
	// writer (checkpointer, promotion, XLOG_PARAMETER_CHANGE) performs.
	if err := control.UpdateControlFile(dir, func(cd *control.ControlFileData) {}); err != nil {
		t.Fatalf("UpdateControlFile (no-op): %v", err)
	}

	cd2, err := control.ReadControlFile(dir)
	if err != nil {
		t.Fatalf("ReadControlFile after round-trip: %v", err)
	}
	t.Run("after_update", func(t *testing.T) { checkPointCopyGolden(t, bin, dir, cd2) })
	if cd2.CheckPointCopyOldestMulti != 55 {
		t.Errorf("oldestMulti = %d after round-trip, want 55", cd2.CheckPointCopyOldestMulti)
	}
}

// TestCheckpointerWritesLiveTimelineToPgControl is the pg_control half of the
// M0131-S18.3 guard (the WAL-record half lives in
// internal/wal/checkpoint_fields_pg_test.go). It runs a real Checkpointer
// against a real initdb'd directory with a promoted (TLI 3) writer and
// full_page_writes = off, then reads the result back through BOTH goopg's
// decoder and the real pg_controldata.
//
// Why it matters: ThisTimeLineID/PrevTimeLineID/fullPageWrites were literals
// (1/1/true) in the checkpointer's pg_control update. On a cluster promoted by
// M0130-S8.5's finalizePromotion, the next checkpoint therefore stomped
// pg_control back to timeline 1 while pg_wal held segments named for timeline
// 2, and a real PG booted on it PANICs "could not locate a valid checkpoint
// record". The record and pg_control are written from ONE sample, so this test
// also pins that they cannot drift apart.
func TestCheckpointerWritesLiveTimelineToPgControl(t *testing.T) {
	bin := pgControldataBin(t)
	if bin == "" {
		t.Skip("pg_controldata not found on PATH or in postgres/local_install/bin")
	}
	dir := filepath.Join(t.TempDir(), "cluster")
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	// Precondition: a fresh cluster is on the bootstrap timeline, so a green
	// run cannot be "it was already 3".
	if cd, err := control.ReadControlFile(dir); err != nil {
		t.Fatalf("ReadControlFile: %v", err)
	} else if cd.CheckPointCopyThisTLI != 1 {
		t.Fatalf("fresh cluster ThisTimeLineID = %d, want 1 (test precondition)",
			cd.CheckPointCopyThisTLI)
	}

	// A private WAL dir: this test is about what reaches pg_control, and
	// writing TLI-3 segments into the cluster's own pg_wal would leave a
	// directory no later assertion could interpret.
	w, err := xlog.NewWriter(xlog.Config{
		WALDir:      filepath.Join(t.TempDir(), "pg_wal"),
		SegmentSize: 1 << 20,
		TimelineID:  3,
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()

	cp := xlog.NewCheckpointer(nopFlusher{}, w, xlog.CheckpointerConfig{
		DataDir:             dir,
		SegmentSize:         1 << 20,
		PGCompatCheckpoints: true,
		GUCParams:           xlog.DefaultGUCParameters(),
		NextXIDFn:           func() uint64 { return 42 },
		TimelineIDFn:        w.TimelineID,
		FullPageWritesFn:    func() bool { return false },
		NextMultiXactFn:     func() (uint32, uint32, uint32) { return 9, 17, 1 },
	})
	if err := cp.CheckpointNow(); err != nil {
		t.Fatalf("CheckpointNow: %v", err)
	}

	cd, err := control.ReadControlFile(dir)
	if err != nil {
		t.Fatalf("ReadControlFile after checkpoint: %v", err)
	}
	if cd.CheckPointCopyThisTLI != 3 {
		t.Errorf("checkPointCopy.ThisTimeLineID = %d, want 3 (the live writer timeline)",
			cd.CheckPointCopyThisTLI)
	}
	if cd.CheckPointCopyPrevTLI != 3 {
		t.Errorf("checkPointCopy.PrevTimeLineID = %d, want 3", cd.CheckPointCopyPrevTLI)
	}
	if cd.CheckPointCopyFullPageWrites {
		t.Error("checkPointCopy.fullPageWrites = true, want false (full_page_writes = off)")
	}
	if cd.CheckPointCopyNextMulti != 9 {
		t.Errorf("checkPointCopy.nextMulti = %d, want 9 (the live allocator)",
			cd.CheckPointCopyNextMulti)
	}
	if cd.CheckPointCopyNextMultiOffset != 17 {
		t.Errorf("checkPointCopy.nextMultiOffset = %d, want 17", cd.CheckPointCopyNextMultiOffset)
	}

	// The independent oracle: pg_controldata must read the same bytes the
	// same way, including the two text-valued fields the numeric helper
	// above cannot cover.
	fields := runPgControldata(t, bin, dir)
	for label, want := range map[string]string{
		"Latest checkpoint's TimeLineID":       "3",
		"Latest checkpoint's PrevTimeLineID":   "3",
		"Latest checkpoint's full_page_writes": "off",
		"Latest checkpoint's NextMultiXactId":  "9",
		"Latest checkpoint's NextMultiOffset":  "17",
	} {
		if got := fields[label]; got != want {
			t.Errorf("pg_controldata %q = %q, want %q", label, got, want)
		}
	}
	t.Run("oracle_agreement", func(t *testing.T) { checkPointCopyGolden(t, bin, dir, cd) })
}

// nopFlusher satisfies wal.DirtyPageFlusher for a checkpointer that has no
// buffer pool — this test asserts on pg_control, not on page writeback.
type nopFlusher struct{}

func (nopFlusher) FlushAll() error { return nil }
