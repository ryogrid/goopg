package wal

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestAppendFlushAndReadAll(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 128})
	if err != nil {
		t.Fatal(err)
	}

	start1, end1, err := w.Append([]byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	start2, end2, err := w.Append([]byte("world"))
	if err != nil {
		t.Fatal(err)
	}
	if start1 != 1 {
		t.Fatalf("first record start LSN = %d, want 1", start1)
	}
	if start2 != end1+1 {
		t.Fatalf("second record start LSN = %d, want %d", start2, end1+1)
	}
	if err := w.FlushUpTo(end2); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	recs, err := ReadAll(walDir, 128)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("record count = %d, want 2", len(recs))
	}
	if got := string(recs[0].Payload); got != "hello" {
		t.Fatalf("record[0] = %q, want hello", got)
	}
	if got := string(recs[1].Payload); got != "world" {
		t.Fatalf("record[1] = %q, want world", got)
	}
	if recs[0].StartLSN != start1 || recs[0].EndLSN != end1 {
		t.Fatalf("record[0] LSN range = [%d,%d], want [%d,%d]", recs[0].StartLSN, recs[0].EndLSN, start1, end1)
	}
	if recs[1].StartLSN != start2 || recs[1].EndLSN != end2 {
		t.Fatalf("record[1] LSN range = [%d,%d], want [%d,%d]", recs[1].StartLSN, recs[1].EndLSN, start2, end2)
	}
}

func TestFlushUpToRejectsUnwrittenLSN(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 128})
	if err != nil {
		t.Fatal(err)
	}
	_, end, err := w.Append([]byte("one"))
	if err != nil {
		t.Fatal(err)
	}
	if err := w.FlushUpTo(end + 1); !errors.Is(err, ErrLSNNotWritten) {
		t.Fatalf("FlushUpTo returned %v, want ErrLSNNotWritten", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReopenContinuesLSN(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w1, err := NewWriter(Config{WALDir: walDir, SegmentSize: 128})
	if err != nil {
		t.Fatal(err)
	}
	_, end1, err := w1.Append([]byte("first"))
	if err != nil {
		t.Fatal(err)
	}
	if err := w1.FlushUpTo(end1); err != nil {
		t.Fatal(err)
	}
	if err := w1.Close(); err != nil {
		t.Fatal(err)
	}

	w2, err := NewWriter(Config{WALDir: walDir, SegmentSize: 128})
	if err != nil {
		t.Fatal(err)
	}
	start2, _, err := w2.Append([]byte("second"))
	if err != nil {
		t.Fatal(err)
	}
	if start2 != end1+1 {
		t.Fatalf("reopened writer start LSN = %d, want %d", start2, end1+1)
	}
	if err := w2.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRecordCanSpanSegments(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 32})
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 80)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	_, end, err := w.Append(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.FlushUpTo(end); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	recs, err := ReadAll(walDir, 32)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("record count = %d, want 1", len(recs))
	}
	if len(recs[0].Payload) != len(payload) {
		t.Fatalf("payload len = %d, want %d", len(recs[0].Payload), len(payload))
	}
	for i := range payload {
		if recs[0].Payload[i] != payload[i] {
			t.Fatalf("payload[%d] = %d, want %d", i, recs[0].Payload[i], payload[i])
		}
	}
}
