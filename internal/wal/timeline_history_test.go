package wal_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/wal"
)

// TestTimelineHistoryRoundTrip verifies WriteHistory + ReadHistory
// agree on a 3-timeline chain. The format is line-per-entry with
// tab-separated fields; the ReadHistory contract is that entries
// come back in the same order they were written.
func TestTimelineHistoryRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := []wal.TimelineHistoryEntry{
		{TLI: 1, SwitchLSN: 0x0000000016000000, Reason: "no recovery target specified"},
		{TLI: 2, SwitchLSN: 0x0000000018000000, Reason: "no recovery target specified"},
		{TLI: 3, SwitchLSN: 0x000000001A000000, Reason: "promoted by SIGUSR1"},
	}
	if err := wal.WriteHistory(dir, 4, want); err != nil {
		t.Fatalf("WriteHistory: %v", err)
	}

	got, err := wal.ReadHistory(dir, 4)
	if err != nil {
		t.Fatalf("ReadHistory: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round-trip mismatch:\n got=%#v\nwant=%#v", got, want)
	}
}

// TestTimelineHistoryFileName ensures the on-disk filename uses the
// %08X format upstream's pg_waldump and walreceiver expect.
func TestTimelineHistoryFileName(t *testing.T) {
	cases := []struct {
		tli  uint32
		want string
	}{
		{1, "00000001.history"},
		{2, "00000002.history"},
		{0xABCDEF12, "ABCDEF12.history"},
	}
	for _, c := range cases {
		if got := wal.TimelineHistoryFileName(c.tli); got != c.want {
			t.Errorf("TimelineHistoryFileName(%d) = %q, want %q", c.tli, got, c.want)
		}
	}
}

// TestReadHistoryMissingFile returns (nil, nil) for a TLI whose
// history hasn't been written yet (e.g. TLI=1 on a fresh cluster).
// Upstream walsender treats this as an empty result, not an error.
func TestReadHistoryMissingFile(t *testing.T) {
	dir := t.TempDir()
	got, err := wal.ReadHistory(dir, 1)
	if err != nil {
		t.Fatalf("ReadHistory missing file: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil entries for missing file, got %v", got)
	}
}

// TestWriteHistoryFormat pins the byte format so a future change
// can't silently regress wire compatibility with libpq's walreceiver
// or pg_basebackup. The format is `<TLI>\t<X/X>\t<reason>\n`.
func TestWriteHistoryFormat(t *testing.T) {
	dir := t.TempDir()
	entries := []wal.TimelineHistoryEntry{
		{TLI: 1, SwitchLSN: 0x0000000118000000, Reason: "no recovery target specified"},
	}
	if err := wal.WriteHistory(dir, 2, entries); err != nil {
		t.Fatalf("WriteHistory: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "00000002.history"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	want := "1\t1/18000000\tno recovery target specified\n"
	if string(body) != want {
		t.Fatalf("file format mismatch:\n got=%q\nwant=%q", string(body), want)
	}
}

// TestReadHistoryToleratesCommentAndBlankLines confirms parser
// robustness: PG-format files never contain comments, but the
// timeline.h docs allow `#`-prefixed comments and blank separators.
func TestReadHistoryToleratesCommentAndBlankLines(t *testing.T) {
	dir := t.TempDir()
	body := strings.Join([]string{
		"# this is a comment line — ignored",
		"",
		"1\t0/16000000\tno recovery target specified",
		"# trailing comment after data",
		"2\t0/18000000\tpromoted on demand",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "00000003.history"), []byte(body), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, err := wal.ReadHistory(dir, 3)
	if err != nil {
		t.Fatalf("ReadHistory: %v", err)
	}
	want := []wal.TimelineHistoryEntry{
		{TLI: 1, SwitchLSN: 0x0000000016000000, Reason: "no recovery target specified"},
		{TLI: 2, SwitchLSN: 0x0000000018000000, Reason: "promoted on demand"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parse mismatch:\n got=%#v\nwant=%#v", got, want)
	}
}
