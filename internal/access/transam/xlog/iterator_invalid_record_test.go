package xlog

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/storage"
)

// tornContinuationWAL writes three records, the last spanning a page
// boundary, zeroes the page the last one continues onto (043's "broken
// replication"), and returns a writer holding those bytes as a standby's
// archive feeder would append them, plus the pages involved.
func tornContinuationWAL(t *testing.T) (w *Writer, walDir string, bigStart, contPage uint64) {
	t.Helper()
	src := filepath.Join(t.TempDir(), "src")
	sw, err := NewWriter(Config{WALDir: src, PageHeaders: true})
	if err != nil {
		t.Fatal(err)
	}
	for i, n := range []int{100, 300, 20000} {
		payload := EncodeHeapVacuum(storage.RelFileNode{DBOid: 5, RelOid: uint32(16400 + i)}, 0, make([]uint16, n/2))
		if _, _, err := sw.Append(payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := sw.Close(); err != nil {
		t.Fatal(err)
	}
	recs, err := ReadAll(src, DefaultSegmentSize)
	if err != nil || len(recs) < 3 {
		t.Fatalf("ReadAll: %d records, %v", len(recs), err)
	}
	big := recs[len(recs)-1]
	bigStart = big.StartLSN - 1
	contPage = bigStart - bigStart%XLOGBlockSize + XLOGBlockSize
	if big.EndLSN <= contPage {
		t.Fatalf("test record does not span pages (%#x..%#x)", bigStart, big.EndLSN)
	}
	data, err := os.ReadFile(filepath.Join(src, FormatSegmentNameTLI(0, 1)))
	if err != nil {
		t.Fatal(err)
	}
	end := big.EndLSN - big.EndLSN%XLOGBlockSize + XLOGBlockSize
	data = data[:end]
	for i := contPage; i < contPage+XLOGBlockSize; i++ {
		data[i] = 0
	}
	walDir = filepath.Join(t.TempDir(), "pg_wal")
	w, err = NewWriter(Config{WALDir: walDir, PageHeaders: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	if _, e, err := w.AppendRaw(data); err != nil {
		t.Fatal(err)
	} else if err := w.FlushUpTo(e); err != nil {
		t.Fatal(err)
	}
	return w, walDir, bigStart, contPage
}

// TestIteratorInvalidContinuationPageIsEndOfWAL pins ReadRecord's handling
// of a record continuing onto a page whose header is invalid (M0149-0028):
// by default the iterator reports the failure; with
// SetEndOfWALAtInvalidRecord (standby replay) it logs PostgreSQL's
// XLogReaderValidatePageHeader message once and waits for more WAL at the
// record's start instead.
func TestIteratorInvalidContinuationPageIsEndOfWAL(t *testing.T) {
	w, walDir, bigStart, contPage := tornContinuationWAL(t)

	it, err := NewRecordIterator(w, walDir, DefaultSegmentSize, 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var gotErr error
	for i := 0; i < 10 && gotErr == nil; i++ {
		_, gotErr = it.Next(ctx)
	}
	var bad *invalidPageHeaderError
	if !errors.As(gotErr, &bad) || !strings.HasPrefix(bad.Msg, "invalid magic number 0000 in WAL segment ") {
		t.Fatalf("default iterator: err = %v, want the invalid magic number failure", gotErr)
	}
	_ = it.Close()

	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	defer slog.SetDefault(prev)

	it, err = NewRecordIterator(w, walDir, DefaultSegmentSize, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()
	it.SetEndOfWALAtInvalidRecord()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel2()
	for {
		rec, err := it.Next(ctx2)
		if err != nil {
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("standby iterator: err = %v, want it to wait at the invalid record", err)
			}
			break
		}
		if rec.StartLSN-1 >= bigStart {
			t.Fatalf("standby iterator returned the torn record at %#x", rec.StartLSN-1)
		}
	}
	if it.pos != int64(bigStart) {
		t.Fatalf("iterator waits at %#x, want the torn record's start %#x", it.pos, bigStart)
	}
	want := "invalid magic number 0000 in WAL segment " + XLogFileName(1, 0, DefaultSegmentSize)
	if n := strings.Count(logBuf.String(), want); n != 1 {
		t.Fatalf("logged %d times, want once: %s", n, logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "offset "+strconvU(contPage)) {
		t.Fatalf("message lacks the page's segment offset %d: %s", contPage, logBuf.String())
	}
}

func strconvU(v uint64) string {
	var b [20]byte
	i := len(b)
	for {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
		if v == 0 {
			return string(b[i:])
		}
	}
}

// TestWriterRewindToSegmentStart pins Writer.RewindTo, the streaming start
// a standby switching from the archive takes (RequestXLogStreaming rounds
// to the segment): insertion moves back to a segment boundary, a non-
// boundary is refused, and a position at or past the written end is a
// no-op.
func TestWriterRewindToSegmentStart(t *testing.T) {
	w, _, _, _ := tornContinuationWAL(t)
	written := w.WrittenLSN()
	if err := w.RewindTo((written/uint64(DefaultSegmentSize) + 1) * uint64(DefaultSegmentSize)); err != nil || w.WrittenLSN() != written {
		t.Fatalf("rewind past the end: %v, written %#x -> %#x", err, written, w.WrittenLSN())
	}
	if err := w.RewindTo(XLOGBlockSize); err == nil {
		t.Fatal("rewind to a non-segment boundary accepted")
	}
	if err := w.RewindTo(0); err != nil {
		t.Fatal(err)
	}
	if got := w.WrittenLSN(); got != 0 {
		t.Fatalf("written after rewind = %#x, want 0", got)
	}
}
