package xlog

import (
	"bytes"
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newTestWriter(t *testing.T) (*Writer, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "wal")
	w, err := NewWriter(Config{WALDir: dir, SegmentSize: 4096})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w, dir
}

// TestRecordIteratorReadsExistingRecords pins the basic case: append
// three records, iterate from LSN 0, get all three back in order with
// matching payloads and contiguous LSNs.
func TestRecordIteratorReadsExistingRecords(t *testing.T) {
	w, dir := newTestWriter(t)
	want := [][]byte{[]byte("alpha"), []byte("beta"), []byte("gamma")}
	starts := make([]uint64, len(want))
	ends := make([]uint64, len(want))
	for i, p := range want {
		s, e, err := w.Append(p)
		if err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
		starts[i], ends[i] = s, e
	}

	it, err := NewRecordIterator(w, dir, 4096, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for i := range want {
		rec, err := it.Next(ctx)
		if err != nil {
			t.Fatalf("Next %d: %v", i, err)
		}
		if string(rec.Payload) != string(want[i]) {
			t.Errorf("record %d payload = %q, want %q", i, rec.Payload, want[i])
		}
		if rec.StartLSN != starts[i] || rec.EndLSN != ends[i] {
			t.Errorf("record %d LSNs = (%d, %d), want (%d, %d)",
				i, rec.StartLSN, rec.EndLSN, starts[i], ends[i])
		}
	}
}

// TestRecordIteratorBlocksThenWakesOnAppend pins the streaming
// behaviour: a goroutine that has consumed all current records sleeps
// inside Next; a subsequent Append wakes it and Next returns the new
// record.
func TestRecordIteratorBlocksThenWakesOnAppend(t *testing.T) {
	w, dir := newTestWriter(t)
	if _, _, err := w.Append([]byte("first")); err != nil {
		t.Fatal(err)
	}
	it, err := NewRecordIterator(w, dir, 4096, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Consume the existing record so we're at the tail.
	if rec, err := it.Next(ctx); err != nil {
		t.Fatalf("first Next: %v", err)
	} else if string(rec.Payload) != "first" {
		t.Fatalf("first.Payload = %q", rec.Payload)
	}

	got := make(chan Record, 1)
	gotErr := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		rec, err := it.Next(ctx)
		if err != nil {
			gotErr <- err
			return
		}
		got <- rec
	}()

	// Give the goroutine a chance to enter the blocked select.
	time.Sleep(50 * time.Millisecond)
	if _, _, err := w.Append([]byte("second")); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	select {
	case err := <-gotErr:
		t.Fatalf("Next: %v", err)
	case rec := <-got:
		if string(rec.Payload) != "second" {
			t.Errorf("payload = %q, want second", rec.Payload)
		}
	}
}

// TestRecordIteratorContextCancel pins clean cancel semantics: a
// goroutine blocked in Next returns ctx.Err() promptly.
func TestRecordIteratorContextCancel(t *testing.T) {
	w, dir := newTestWriter(t)
	it, err := NewRecordIterator(w, dir, 4096, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := it.Next(ctx)
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("Next returned nil after cancel")
		}
		if err != context.Canceled {
			t.Errorf("Next err = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Next did not return within 1s of cancel")
	}
}

// TestRecordIteratorStartLSNSkipsExisting pins that an iterator
// anchored at a mid-stream record-boundary LSN yields only records
// from that point forward.
func TestRecordIteratorStartLSNSkipsExisting(t *testing.T) {
	w, dir := newTestWriter(t)
	if _, _, err := w.Append([]byte("a")); err != nil {
		t.Fatal(err)
	}
	_, secondEnd, err := w.Append([]byte("b"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.Append([]byte("c")); err != nil {
		t.Fatal(err)
	}
	// Anchor at the LSN of the third record's first byte = secondEnd + 1
	// (note: NewRecordIterator interprets startLSN as a record-boundary,
	// and the next record begins at byte offset = secondEnd).
	it, err := NewRecordIterator(w, dir, 4096, secondEnd+1)
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rec, err := it.Next(ctx)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if string(rec.Payload) != "c" {
		t.Errorf("payload = %q, want c", rec.Payload)
	}
}

// TestRecordIteratorAnchorAtTailBlocks pins the convention used by the
// standby replayer and walreceiver in cmd/goopg: when starting at
// "tail = next record after the last byte already written," the
// caller passes startLSN = WrittenLSN()+1. The iterator must block
// (not error) because pos lands exactly at the offset just past the
// last written byte, with no half-record straddling the boundary.
//
// Regression guard: a previous bug anchored at WrittenLSN() itself
// (pos = LSN of the last byte already in a complete record), which
// caused readOneAt to try to decode a header starting in the middle
// of the last record and fail with "bad xlog total length 0" — the
// standby replayer crashed on every boot.
func TestRecordIteratorAnchorAtTailBlocks(t *testing.T) {
	w, dir := newTestWriter(t)
	// Plant a real record so WrittenLSN() points at the end of a
	// committed record, not at offset 0 (where startLSN==0 has its
	// special-case semantics).
	if _, _, err := w.Append([]byte("anchor")); err != nil {
		t.Fatal(err)
	}
	tail := w.WrittenLSN()

	it, err := NewRecordIterator(w, dir, 4096, tail+1)
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()

	// The iterator should be blocked at the tail. Use a short
	// context to confirm "blocks rather than errors": ctx cancel
	// (not corrupt-record) is the expected exit.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = it.Next(ctx)
	if err != context.DeadlineExceeded {
		t.Fatalf("Next at tail = %v, want context.DeadlineExceeded (block-then-cancel)", err)
	}

	// Now append a new record and confirm a fresh Next picks it up.
	if _, _, err := w.Append([]byte("postanchor")); err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	rec, err := it.Next(ctx2)
	if err != nil {
		t.Fatalf("Next after append: %v", err)
	}
	if string(rec.Payload) != "postanchor" {
		t.Errorf("payload = %q, want postanchor", rec.Payload)
	}
}

func TestRecordIteratorReadsPGRecordAfterPagePadding(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wal")
	w, err := NewWriter(Config{
		WALDir:             dir,
		SegmentSize:        DefaultSegmentSize,
		PageHeaders:        true,
		SystemID:           1,
		TimelineID:         1,
		WALBuffers:         256,
		SenderMemoryBuffer: 32,
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()

	first, _, err := encodeRecordXLog([]byte("first"), 0)
	if err != nil {
		t.Fatalf("encode first: %v", err)
	}
	second, _, err := encodeRecordXLog([]byte("second"), 0)
	if err != nil {
		t.Fatalf("encode second: %v", err)
	}
	stream := append([]byte(nil), buildTestLongPageHeader(t)...)
	stream = append(stream, first...)
	padLen := XLOGBlockSize - len(stream)
	if padLen <= 0 {
		t.Fatalf("unexpected padLen=%d", padLen)
	}
	stream = append(stream, make([]byte, padLen)...)
	stream = append(stream, buildPageHeader(XLOGBlockSize, DefaultSegmentSize, 1, 1, false, 0)...)
	stream = append(stream, second...)
	if _, _, err := w.AppendRaw(stream); err != nil {
		t.Fatalf("AppendRaw: %v", err)
	}

	it, err := NewRecordIterator(w, dir, DefaultSegmentSize, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	rec1, err := it.Next(ctx)
	if err != nil {
		t.Fatalf("first Next: %v", err)
	}
	if string(rec1.Payload) != "first" {
		t.Fatalf("first payload = %q, want first", rec1.Payload)
	}
	if it.pos != int64(rec1.EndLSN) {
		t.Fatalf("iterator pos after first record = %d, want %d", it.pos, rec1.EndLSN)
	}
	written := int64(w.WrittenLSN())
	pad, err := it.zeroPagePaddingAdvance(written)
	if err != nil {
		t.Fatalf("zeroPagePaddingAdvance: %v", err)
	}
	if want := int64(XLOGBlockSize - int(rec1.EndLSN%XLOGBlockSize)); pad != want {
		t.Fatalf("zeroPagePaddingAdvance = %d, want %d", pad, want)
	}
	it.pos += pad
	if it.pos%XLOGBlockSize != 0 {
		t.Fatalf("iterator pos after padding skip = %d, want page boundary", it.pos)
	}
	hsize := int64(pageHeaderSizeAt(it.pos, DefaultSegmentSize))
	it.pos += hsize
	headerBytes, _, err := it.readRecordBytesAt(it.pos, xlogRecordHeaderSize)
	if err != nil {
		t.Fatalf("readRecordBytesAt second header: %v", err)
	}
	if !bytes.Equal(headerBytes, second[:xlogRecordHeaderSize]) {
		t.Fatalf("second header bytes = %x, want %x at pos=%d written=%d", headerBytes, second[:xlogRecordHeaderSize], it.pos, w.WrittenLSN())
	}
	rec2, _, err := it.readOneAt(it.pos)
	if err != nil {
		t.Fatalf("readOneAt second record: %v", err)
	}
	if string(rec2.Payload) != "second" {
		t.Fatalf("second payload = %q, want second", rec2.Payload)
	}
}

func TestRecordIteratorReadsPGRecordAfterDirectThenBufferedRawAppend(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wal")
	w, err := NewWriter(Config{
		WALDir:             dir,
		SegmentSize:        DefaultSegmentSize,
		PageHeaders:        true,
		SystemID:           1,
		TimelineID:         1,
		WALBuffers:         256,
		SenderMemoryBuffer: 32,
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()

	first, _, err := encodeRecordXLog([]byte("first"), 0)
	if err != nil {
		t.Fatalf("encode first: %v", err)
	}
	second, _, err := encodeRecordXLog([]byte("second"), 0)
	if err != nil {
		t.Fatalf("encode second: %v", err)
	}
	firstChunk := append([]byte(nil), buildTestLongPageHeader(t)...)
	firstChunk = append(firstChunk, first...)
	padLen := XLOGBlockSize - len(firstChunk)
	if padLen <= 0 {
		t.Fatalf("unexpected padLen=%d", padLen)
	}
	firstChunk = append(firstChunk, make([]byte, padLen)...)
	firstChunk = append(firstChunk, buildPageHeader(XLOGBlockSize, DefaultSegmentSize, 1, 1, false, 0)...)
	if _, _, err := w.AppendRaw(firstChunk); err != nil {
		t.Fatalf("AppendRaw first chunk: %v", err)
	}
	if _, _, err := w.AppendRaw(second); err != nil {
		t.Fatalf("AppendRaw second chunk: %v", err)
	}

	it, err := NewRecordIterator(w, dir, DefaultSegmentSize, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	rec1, err := it.Next(ctx)
	if err != nil {
		t.Fatalf("first Next: %v", err)
	}
	if string(rec1.Payload) != "first" {
		t.Fatalf("first payload = %q, want first", rec1.Payload)
	}
	if it.pos != int64(rec1.EndLSN) {
		t.Fatalf("iterator pos after first record = %d, want %d", it.pos, rec1.EndLSN)
	}
	written := int64(w.WrittenLSN())
	pad, err := it.zeroPagePaddingAdvance(written)
	if err != nil {
		t.Fatalf("zeroPagePaddingAdvance: %v", err)
	}
	if want := int64(XLOGBlockSize - int(rec1.EndLSN%XLOGBlockSize)); pad != want {
		t.Fatalf("zeroPagePaddingAdvance = %d, want %d", pad, want)
	}
	it.pos += pad
	hsize := int64(pageHeaderSizeAt(it.pos, DefaultSegmentSize))
	it.pos += hsize
	headerBytes, _, err := it.readRecordBytesAt(it.pos, xlogRecordHeaderSize)
	if err != nil {
		t.Fatalf("readRecordBytesAt second header: %v", err)
	}
	if !bytes.Equal(headerBytes, second[:xlogRecordHeaderSize]) {
		t.Fatalf("second header bytes = %x, want %x at pos=%d written=%d drained=%d",
			headerBytes, second[:xlogRecordHeaderSize], it.pos, w.WrittenLSN(), w.DrainedLSN())
	}
	rec2, _, err := it.readOneAt(it.pos)
	if err != nil {
		t.Fatalf("readOneAt second record: %v", err)
	}
	if string(rec2.Payload) != "second" {
		t.Fatalf("second payload = %q, want second", rec2.Payload)
	}
}
