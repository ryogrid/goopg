// Worker-pool I/O method: a fixed-size goroutine pool drains a
// bounded submission channel, performing pread/pwrite on the
// caller's behalf. Submission blocks once the channel is full,
// providing the back-pressure the upstream `worker` method
// relies on.
//
// Why a channel and not a slot semaphore: the channel doubles as
// the work queue (each entry is one Op handle) and the
// concurrency bound (channel capacity == max in-flight). One
// allocation per Submit instead of two.

package aio

import (
	"sync"
)

type methodWorker struct {
	engine *Engine

	queue    chan *Handle
	wg       sync.WaitGroup
	closed   chan struct{}
	closeOne sync.Once
}

func newMethodWorker(e *Engine, workers int, maxConcurrency int) *methodWorker {
	if maxConcurrency <= 0 {
		// Default the queue depth to 4×workers — enough to
		// keep the pool busy across submission bursts without
		// allowing unbounded growth.
		maxConcurrency = workers * 4
	}
	m := &methodWorker{
		engine: e,
		queue:  make(chan *Handle, maxConcurrency),
		closed: make(chan struct{}),
	}
	for i := 0; i < workers; i++ {
		m.wg.Add(1)
		go m.run()
	}
	return m
}

func (m *methodWorker) Name() string { return MethodWorker }

func (m *methodWorker) Submit(op *Op) *Handle {
	h := &Handle{op: *op, done: make(chan struct{}), engine: m.engine}
	m.engine.inFlight.Add(1)
	select {
	case m.queue <- h:
		return h
	case <-m.closed:
		// Lose the race against Close — finish synchronously
		// with an engine-closed error so the caller never
		// blocks forever in Wait.
		m.engine.inFlight.Add(-1)
		m.engine.errored.Add(1)
		m.engine.completed.Add(1)
		h.finish(Result{Err: errEngineClosed})
		return h
	}
}

// run is the worker goroutine: drain the queue, run each Op,
// finish the handle. Exits cleanly when the queue is closed.
func (m *methodWorker) run() {
	defer m.wg.Done()
	for h := range m.queue {
		r := runOp(&h.op)
		m.engine.completionBookkeeping(r)
		h.finish(r)
	}
}

// Close stops accepting new work, waits for in-flight Ops to
// drain, and joins the worker goroutines. Idempotent.
func (m *methodWorker) Close() error {
	m.closeOne.Do(func() {
		close(m.closed)
		close(m.queue)
	})
	m.wg.Wait()
	return nil
}
