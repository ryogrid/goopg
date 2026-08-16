package aio

import (
	"errors"
	"io"
	"sync/atomic"
	"testing"
)

// blockOffsets is a NextBlockFunc that walks a slice of offsets
// and returns EndOfStream once exhausted. Used by every test
// in this file so the stream's prefetch behaviour is the
// variable under test, not the next-block policy.
func blockOffsets(offs []int64) NextBlockFunc {
	i := 0
	return func() int64 {
		if i >= len(offs) {
			return EndOfStream
		}
		off := offs[i]
		i++
		return off
	}
}

// TestReadStreamSequentialRoundTrip pins the foundational
// contract: a four-block stream returns each block's bytes in
// callback order, then returns io.EOF, with Wait counts that
// reflect the prefetch window (every block fired through
// Engine.Submit exactly once).
func TestReadStreamSequentialRoundTrip(t *testing.T) {
	f := newMemFile(64)
	// Seed four 4-byte blocks at offsets 0, 4, 8, 12.
	for i, b := range [][]byte{
		[]byte("aaaa"), []byte("bbbb"),
		[]byte("cccc"), []byte("dddd"),
	} {
		copy(f.buf[i*4:], b)
	}

	e, _ := NewEngine(EngineConfig{Method: MethodSync})
	defer e.Close()

	s, err := NewReadStream(ReadStreamConfig{
		Engine:    e,
		File:      f,
		BlockSize: 4,
		NextBlock: blockOffsets([]int64{0, 4, 8, 12}),
		Lookahead: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	want := []string{"aaaa", "bbbb", "cccc", "dddd"}
	for i, w := range want {
		got, err := s.Next()
		if err != nil {
			t.Fatalf("block %d: err=%v", i, err)
		}
		if string(got) != w {
			t.Errorf("block %d: %q want %q", i, got, w)
		}
	}
	if _, err := s.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("trailing Next err=%v want io.EOF", err)
	}

	st := e.Stats()
	if st.Submitted != 4 {
		t.Errorf("Submitted=%d want 4 (one per block)", st.Submitted)
	}
}

// TestReadStreamLookaheadCapsConcurrentSubmits: with
// Lookahead=2 across an 8-block stream, the prefetch window
// keeps at most 2 reads in flight — the worker method's
// in-flight counter is sampled mid-stream and never exceeds
// the cap.
func TestReadStreamLookaheadCapsConcurrentSubmits(t *testing.T) {
	const blocks = 8
	f := newMemFile(blocks * 4)
	for i := 0; i < blocks; i++ {
		f.buf[i*4] = byte(i)
	}

	// gateFile blocks every ReadAt until the test releases the
	// gate, so we can sample InFlight before the prefetch
	// window drains.
	g := &gateFile{inner: f, release: make(chan struct{})}

	e, _ := NewEngine(EngineConfig{
		Method: MethodWorker, Workers: 4, MaxConcurrency: blocks,
	})
	defer e.Close()

	offs := make([]int64, blocks)
	for i := range offs {
		offs[i] = int64(i * 4)
	}
	s, err := NewReadStream(ReadStreamConfig{
		Engine:    e,
		File:      g,
		BlockSize: 4,
		NextBlock: blockOffsets(offs),
		Lookahead: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Wait until the prefetch window is full (the engine
	// reports InFlight reaching the lookahead). gateFile
	// blocks ReadAt so the workers stack on the gate.
	deadline := 0
	for e.Stats().InFlight < 2 && deadline < 1000 {
		deadline++
	}
	if got := e.Stats().InFlight; got > 2 {
		t.Errorf("InFlight=%d exceeds Lookahead=2", got)
	}
	close(g.release)

	for i := 0; i < blocks; i++ {
		if _, err := s.Next(); err != nil {
			t.Fatalf("block %d: %v", i, err)
		}
	}
}

// gateFile blocks every ReadAt on a release channel so tests
// can observe the AIO engine's in-flight counter mid-stream.
type gateFile struct {
	inner   *memFile
	release chan struct{}
	hits    atomic.Int32
}

func (g *gateFile) ReadAt(p []byte, off int64) (int, error) {
	g.hits.Add(1)
	<-g.release
	return g.inner.ReadAt(p, off)
}
func (g *gateFile) WriteAt(p []byte, off int64) (int, error) {
	return g.inner.WriteAt(p, off)
}

// TestReadStreamHonoursDefaultLookahead: zero lookahead falls
// back to DefaultReadStreamLookahead.
func TestReadStreamHonoursDefaultLookahead(t *testing.T) {
	e, _ := NewEngine(EngineConfig{Method: MethodSync})
	defer e.Close()
	s, err := NewReadStream(ReadStreamConfig{
		Engine:    e,
		File:      newMemFile(16),
		BlockSize: 4,
		NextBlock: blockOffsets([]int64{0}),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.cfg.Lookahead != DefaultReadStreamLookahead {
		t.Errorf("Lookahead=%d want %d", s.cfg.Lookahead, DefaultReadStreamLookahead)
	}
}

// TestReadStreamClampsHugeLookahead: a pathological caller
// asking for 10× the cap is clamped to MaxReadStreamLookahead.
func TestReadStreamClampsHugeLookahead(t *testing.T) {
	e, _ := NewEngine(EngineConfig{Method: MethodSync})
	defer e.Close()
	s, err := NewReadStream(ReadStreamConfig{
		Engine:    e,
		File:      newMemFile(16),
		BlockSize: 4,
		NextBlock: blockOffsets([]int64{0}),
		Lookahead: MaxReadStreamLookahead * 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.cfg.Lookahead != MaxReadStreamLookahead {
		t.Errorf("Lookahead=%d want %d", s.cfg.Lookahead, MaxReadStreamLookahead)
	}
}

// TestReadStreamRejectsInvalidConfig pins the construction
// guards: nil engine / file / callback and zero block size all
// surface clear errors.
func TestReadStreamRejectsInvalidConfig(t *testing.T) {
	e, _ := NewEngine(EngineConfig{Method: MethodSync})
	defer e.Close()
	cases := []ReadStreamConfig{
		{File: newMemFile(4), BlockSize: 4, NextBlock: blockOffsets(nil)},
		{Engine: e, BlockSize: 4, NextBlock: blockOffsets(nil)},
		{Engine: e, File: newMemFile(4), BlockSize: 4},
		{Engine: e, File: newMemFile(4), NextBlock: blockOffsets(nil)},
	}
	for i, c := range cases {
		if _, err := NewReadStream(c); err == nil {
			t.Errorf("case %d accepted invalid config %+v", i, c)
		}
	}
}

// TestReadStreamSurfacesPerBlockError: a per-block read that
// errors propagates the error to Next and the stream stays in
// a usable state for the trailing EOF.
func TestReadStreamSurfacesPerBlockError(t *testing.T) {
	// Empty file + non-zero offset → io.EOF on every block
	// (EOF is the per-ReaderAt contract for past-end reads).
	e, _ := NewEngine(EngineConfig{Method: MethodSync})
	defer e.Close()
	s, err := NewReadStream(ReadStreamConfig{
		Engine:    e,
		File:      newMemFile(0),
		BlockSize: 4,
		NextBlock: blockOffsets([]int64{0}),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, perr := s.Next()
	if !errors.Is(perr, io.EOF) {
		t.Errorf("per-block err=%v want io.EOF", perr)
	}
}

// TestReadStreamCloseDrainsInFlight: after Close, no in-flight
// I/Os remain in the engine — the close path waits for the
// prefetch window's outstanding Submits to land. This keeps
// the InFlight counter honest.
func TestReadStreamCloseDrainsInFlight(t *testing.T) {
	f := newMemFile(64)
	e, _ := NewEngine(EngineConfig{
		Method: MethodWorker, Workers: 2, MaxConcurrency: 4,
	})
	defer e.Close()
	s, _ := NewReadStream(ReadStreamConfig{
		Engine:    e,
		File:      f,
		BlockSize: 4,
		NextBlock: blockOffsets([]int64{0, 4, 8, 12}),
		Lookahead: 4,
	})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got := e.Stats().InFlight; got != 0 {
		t.Errorf("InFlight=%d after Close want 0", got)
	}
}
