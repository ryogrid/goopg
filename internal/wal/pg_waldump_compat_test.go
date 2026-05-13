package wal

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

func findPGWaldump(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("PG_WALDUMP"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath("pg_waldump"); err == nil {
		return p
	}
	candidates := []string{
		filepath.Join("..", "..", "postgres", "local_install", "bin", "pg_waldump"),
		filepath.Join("postgres", "local_install", "bin", "pg_waldump"),
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	t.Skip("pg_waldump not installed")
	return ""
}

func firstSegmentName(t *testing.T, walDir string) string {
	t.Helper()
	entries, err := os.ReadDir(walDir)
	if err != nil {
		t.Fatal(err)
	}
	segs := make([]string, 0)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if _, ok := parseSegmentName(e.Name()); ok {
			segs = append(segs, e.Name())
		}
	}
	if len(segs) == 0 {
		t.Fatal("no WAL segment files found")
	}
	sort.Strings(segs)
	return segs[0]
}

// lsnToRecPtr converts goopg's 1-based byte LSN to the upstream
// PostgreSQL 0-based XLogRecPtr format (high32/low32) that
// pg_waldump expects on its `-s` flag.
func lsnToRecPtr(lsn uint64) string {
	if lsn == 0 {
		return "0/0"
	}
	pos := lsn - 1
	return fmt.Sprintf("%X/%X", uint32(pos>>32), uint32(pos))
}

func TestPGWaldumpParsesEmittedWAL(t *testing.T) {
	waldump := findPGWaldump(t)

	dataDir := t.TempDir()
	walDir := filepath.Join(dataDir, "pg_wal")
	w, err := NewWriter(Config{
		WALDir:      walDir,
		SegmentSize: DefaultSegmentSize,
		Preallocate: true,
		PageHeaders: true,
		SystemID:    0xABCDEF0123456789,
		TimelineID:  1,
	})
	if err != nil {
		t.Fatal(err)
	}

	rel := storage.RelFileNode{DBOid: 1, RelOid: 1001, Fork: storage.MainFork}
	tup := storage.NewHeapTuple(7, storage.InvalidTransactionID, []byte("waldump-row"))
	tupBytes, err := tup.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	page := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(page); err != nil {
		t.Fatal(err)
	}
	page[100] = 0xAB
	pagePayload, err := EncodePageImage(rel, 0, page)
	if err != nil {
		t.Fatal(err)
	}

	records := [][]byte{
		EncodeCheckpoint(),
		EncodeHeapInsert(rel, 0, 1, tupBytes),
		EncodeHeapDelete(rel, 0, 1, storage.TransactionID(42), nil),
		EncodeXactCommit(storage.TransactionID(42)),
		pagePayload,
	}
	var firstStart, lastStart, end uint64
	for i, rec := range records {
		start, nextEnd, err := w.Append(rec)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			firstStart = start
		}
		lastStart = start
		end = nextEnd
	}
	if err := w.FlushUpTo(end); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	startSeg := firstSegmentName(t, walDir)
	if len(startSeg) == 24 && startSeg[:8] == "00000000" {
		alias := "000000010000000000000000"
		raw, err := os.ReadFile(filepath.Join(walDir, startSeg))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(walDir, alias), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		startSeg = alias
	}

	// `-e` stops pg_waldump at the start of the last record so it
	// doesn't read past our written data into the preallocated
	// zero-filled tail (which would surface as a spurious
	// "invalid record length 0" error).
	cmd := exec.Command(waldump,
		"-q",
		"-p", walDir,
		"-t", "1",
		"-s", lsnToRecPtr(firstStart),
		"-e", lsnToRecPtr(lastStart),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pg_waldump failed: %v\ncmd=%q\n%s", err, cmd.Args, string(out))
	}
}
