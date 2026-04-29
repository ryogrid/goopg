// Synchronous I/O method: every Submit performs the underlying
// syscall on the calling goroutine and returns a Handle whose
// Wait is already complete. Useful as the safe default on every
// platform and as a fast-path for tests / micro-benchmarks that
// want to opt out of the worker pool.
//
// Backpressure: when MaxConcurrency > 0 the method serialises
// Submits via a buffered semaphore so a caller running in
// parallel cannot exceed the global in-flight cap. With
// MaxConcurrency == 0 the cap is disabled.

package aio

import (
	"errors"
	"sync"
)

// errEngineClosed is the result handed to a Submit that loses
// the close-vs-submit race.
var errEngineClosed = errors.New("aio: engine closed")

type methodSync struct {
	engine   *Engine
	slots    chan struct{}
	closed   chan struct{}
	closeOne sync.Once
}

func newMethodSync(e *Engine, maxConcurrency int) *methodSync {
	m := &methodSync{
		engine: e,
		closed: make(chan struct{}),
	}
	if maxConcurrency > 0 {
		m.slots = make(chan struct{}, maxConcurrency)
	}
	return m
}

func (m *methodSync) Name() string { return MethodSync }

func (m *methodSync) Submit(op *Op) *Handle {
	h := &Handle{op: *op, done: make(chan struct{}), engine: m.engine}
	if m.slots != nil {
		select {
		case m.slots <- struct{}{}:
		case <-m.closed:
			h.finish(Result{Err: errEngineClosed})
			return h
		}
		defer func() { <-m.slots }()
	}
	m.engine.inFlight.Add(1)
	r := runOp(op)
	m.engine.completionBookkeeping(r)
	h.finish(r)
	return h
}

func (m *methodSync) Close() error {
	m.closeOne.Do(func() { close(m.closed) })
	return nil
}
