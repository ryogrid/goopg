package xlog

import (
	"fmt"
	"testing"
)

// TestPGEndOfWALMessageZeroTail pins the message PG's crash recovery logs at
// an ordinary end of WAL — the zero tail after the last record:
// "invalid record length at <next>: expected at least 24, got 0".
// M0149-0076.
func TestPGEndOfWALMessageZeroTail(t *testing.T) {
	dir := t.TempDir()
	segSize := int64(4 * XLOGBlockSize)
	w, err := NewWriter(Config{WALDir: dir, SegmentSize: segSize})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := framedAssemble(RmgrXLog, xlogXLogNoop, 0, []byte("abcdefgh"), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, end, err := w.Append(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("invalid record length at %X/%X: expected at least 24, got 0", uint32(end>>32), uint32(end))
	if got := PGEndOfWALMessage(dir, segSize); got != want {
		t.Fatalf("PGEndOfWALMessage = %q, want %q", got, want)
	}
}
