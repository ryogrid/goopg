// Read-stream abstraction layered on top of the AIO core. A
// caller hands the stream a "next block" callback, a per-block
// size, and a desired lookahead depth; the stream issues
// prefetch reads via Engine.Submit ahead of the consumer's
// `Next()` calls so that by the time the consumer is ready for
// block N, block N+lookahead has already been queued for I/O.
//
// Mirrors upstream's `read_stream.h` in shape (caller-supplied
// next-block callback + bounded lookahead) without taking a
// dependency on the buffer manager. v0's stream operates on a
// `File` + byte offsets; the bufmgr-aware variant that returns
// `Buffer` handles is a follow-up that lives wherever the
// upcoming heap-scan integration places it.
//
// What this slice delivers:
//
//   - `ReadStream`, `ReadStreamConfig`, `NextBlockFunc`.
//   - `Next()` blocks until the head prefetch lands, returns
//     its bytes (or io.EOF when exhausted), and refills the
//     window.
//   - `Close()` drains in-flight prefetches.
//   - Construction-time validation: BlockSize > 0, Lookahead
//     >= 1, Engine + File + NextBlock all non-nil.
//
// What's deferred:
//
//   - Per-block contiguous-merge ("io_combine_limit"): when the
//     callback returns adjacent offsets, upstream coalesces
//     them into a single bigger I/O. v0 always issues one I/O
//     per block. The hook is here in spirit but not in code.
//   - The `Reset()` semantics upstream uses for restartable
//     scans. Callers that need a fresh stream construct a new
//     one.
//   - Sequential-detection / ramp-up policy. v0 uses the full
//     Lookahead from the first Next.

package aio

import (
	"errors"
	"fmt"
	"io"
)

// EndOfStream is the sentinel NextBlockFunc returns to signal
// the stream is exhausted. Mirrors upstream's
// `InvalidBlockNumber` return; we use a negative offset because
// v0's stream is offset-based, not block-number-based.
const EndOfStream int64 = -1

// NextBlockFunc returns the next byte offset to fetch, or
// EndOfStream once the stream is done. Called lazily — once
// per prefetch slot the stream needs to fill. Must NOT block.
type NextBlockFunc func() int64

// ReadStreamConfig parameterises one stream. Required: Engine,
// File, BlockSize, NextBlock. Lookahead defaults to
// DefaultReadStreamLookahead when zero.
type ReadStreamConfig struct {
	Engine    *Engine
	File      File
	BlockSize int
	NextBlock NextBlockFunc

	// Lookahead is the maximum prefetched-but-not-yet-consumed
	// blocks the stream keeps in flight. Clamped to
	// [1, MaxReadStreamLookahead]. Zero falls back to the
	// default.
	Lookahead int
}

const (
	// DefaultReadStreamLookahead matches upstream's
	// `effective_io_concurrency` default of 1 for the
	// non-maintenance case. Mild prefetch enough to pipeline
	// the typical sequential scan; tunable via the field.
	DefaultReadStreamLookahead = 4
	// MaxReadStreamLookahead caps the per-stream window so a
	// pathologically large config can't allocate unbounded
	// buffer memory. Independent of the engine's global
	// MaxConcurrency cap (which still applies on top).
	MaxReadStreamLookahead = 256
)

// ReadStream issues prefetch reads ahead of the consumer.
// Not goroutine-safe — one goroutine per stream.
type ReadStream struct {
	cfg     ReadStreamConfig
	pending []*pendingRead

	// exhausted is set the first time NextBlock returns
	// EndOfStream so the refill path doesn't keep calling it.
	exhausted bool
	closed    bool
}

type pendingRead struct {
	h      *Handle
	buf    []byte
	offset int64
}

// ErrReadStreamClosed is returned by Next on a closed stream.
var ErrReadStreamClosed = errors.New("aio: read stream closed")

// NewReadStream validates cfg and primes the prefetch window.
func NewReadStream(cfg ReadStreamConfig) (*ReadStream, error) {
	if cfg.Engine == nil {
		return nil, errors.New("aio.ReadStream: Engine is nil")
	}
	if cfg.File == nil {
		return nil, errors.New("aio.ReadStream: File is nil")
	}
	if cfg.NextBlock == nil {
		return nil, errors.New("aio.ReadStream: NextBlock is nil")
	}
	if cfg.BlockSize <= 0 {
		return nil, fmt.Errorf("aio.ReadStream: BlockSize=%d must be positive", cfg.BlockSize)
	}
	if cfg.Lookahead == 0 {
		cfg.Lookahead = DefaultReadStreamLookahead
	}
	if cfg.Lookahead < 1 {
		cfg.Lookahead = 1
	}
	if cfg.Lookahead > MaxReadStreamLookahead {
		cfg.Lookahead = MaxReadStreamLookahead
	}
	s := &ReadStream{
		cfg:     cfg,
		pending: make([]*pendingRead, 0, cfg.Lookahead),
	}
	s.refill()
	return s, nil
}

// refill fills the prefetch window up to Lookahead in-flight
// I/Os. Stops at the first EndOfStream sentinel.
func (s *ReadStream) refill() {
	for len(s.pending) < s.cfg.Lookahead && !s.exhausted {
		off := s.cfg.NextBlock()
		if off == EndOfStream {
			s.exhausted = true
			return
		}
		buf := make([]byte, s.cfg.BlockSize)
		h := s.cfg.Engine.Submit(Op{
			File:      s.cfg.File,
			Buffer:    buf,
			Offset:    off,
			Direction: DirRead,
		})
		s.pending = append(s.pending, &pendingRead{
			h:      h,
			buf:    buf,
			offset: off,
		})
	}
}

// Next blocks until the head prefetch completes and returns
// its bytes (truncated to the actual byte count returned by
// the underlying ReadAt). io.EOF is returned exactly once when
// the stream's NextBlock callback has returned EndOfStream
// AND the in-flight queue has drained. A short-read EOF on
// any individual block is also surfaced as the per-block error
// (matches `io.ReaderAt`'s semantics).
//
// The returned slice aliases the stream's internal buffer for
// this block and is valid until the next Next/Close call.
func (s *ReadStream) Next() ([]byte, error) {
	if s.closed {
		return nil, ErrReadStreamClosed
	}
	if len(s.pending) == 0 {
		return nil, io.EOF
	}
	head := s.pending[0]
	r := head.h.Wait()
	// Drop the head; refill before returning so the caller's
	// next Next gets a warm window.
	s.pending = s.pending[1:]
	s.refill()
	if r.Err != nil {
		return head.buf[:r.N], r.Err
	}
	return head.buf[:r.N], nil
}

// Close drains in-flight prefetches (their work is already
// queued; we wait for it to land rather than abandon the
// buffers, which would leak the engine's in-flight counter).
// Idempotent.
func (s *ReadStream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	for _, p := range s.pending {
		_ = p.h.Wait()
	}
	s.pending = nil
	return nil
}
