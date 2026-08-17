package executor

// parallel_agg_split.go — P9 of docs/design/parallel-query/, chapter 06.
//
// Placing the partial/finalize split that P5 built the combine rules for.
//
// Why this phase exists, in one measurement: before it, TPC-H Q1 pushed ~5.9 M
// rows through the Gather so that ONE leader-side aggregate could reduce them
// to four groups. Q1 ran in 21.3 s serial, 7.8 s with two workers — and then
// 7.15 s with four and 7.35 s with EIGHT. Solving T(n) = S + P/n across those
// points puts roughly 6.1 s of the 7.1 s floor in the serial tail. Adding
// workers could not touch it, because the tail was the leader doing all the
// aggregating and all the row transfer.
//
// With the split, each worker aggregates its own share and publishes four
// group states. Four rows per worker cross the boundary instead of 5.9 M.
//
// ## How the states travel
//
// They do not travel through the Gather at all.
//
// The obvious design — put transition states in the rows — needs either a
// pointer-bearing Datum kind (which cuts against the pointer-free-Datum work)
// or a side-channel threaded through rowBatch, TupleSlot and the Gather, so
// that a node whose entire job is "move rows" would have to learn about
// aggregation. Instead the Partial node publishes into a shared accumulator
// keyed by its own plan node, exactly as P8's shared hash tables are published,
// and the Finalize node reads it after draining the Gather to EOF.
//
// The Gather therefore needs no knowledge of aggregation whatsoever, and the
// schema is unchanged at every level.
//
// ## Why draining to EOF is the synchronisation
//
// A Partial node does ALL of its work in Open: it drains its child, builds the
// groups, and merges them into the accumulator before it returns. Its rows are
// then produced (there are none). So when the Gather reports EOF, every worker
// has returned from its Open, which means every merge has already happened.
// No barrier is needed beyond the one the Gather already has.

import (
	"sync"

	"github.com/goopg/goopg/internal/optimizer"
)

// aggPartialGroup is one group's accumulated state, combined across workers.
type aggPartialGroup struct {
	groupValues Row
	passthrough Row
	states      []aggRuntime
}

// aggPartialAccum collects per-group states from every worker running a
// Partial aggregate.
//
// Combining happens on INSERT, under the mutex, rather than being deferred to
// the Finalize node. The contention is negligible — one lock acquisition per
// (worker, group), and Q1 has four groups — while the alternative would hold
// every worker's full group map alive until the end.
type aggPartialAccum struct {
	mu     sync.Mutex
	groups map[string]*aggPartialGroup
	order  []string
}

func newAggPartialAccum() *aggPartialAccum {
	return &aggPartialAccum{groups: map[string]*aggPartialGroup{}}
}

// merge folds one worker's group into the accumulator.
//
// states is consumed by reference: the caller must not touch it afterwards.
// That is safe because a Partial node merges its groups exactly once, at the
// end of its own Open, and never reads them again.
func (a *aggPartialAccum) merge(key string, groupValues, passthrough Row, states []aggRuntime, aggs []optimizer.AggregateCall) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	dst, ok := a.groups[key]
	if !ok {
		// First worker to see this group owns it. groupValues and passthrough
		// are functionally determined by the key, so whichever worker arrives
		// first is as correct as any other.
		a.groups[key] = &aggPartialGroup{
			groupValues: groupValues,
			passthrough: passthrough,
			states:      states,
		}
		a.order = append(a.order, key)
		return nil
	}
	for i := range aggs {
		if i >= len(dst.states) || i >= len(states) {
			break
		}
		if err := combineAggRuntime(aggs[i].Name, &dst.states[i], &states[i]); err != nil {
			return err
		}
	}
	return nil
}

// lookupAggPartialAccum returns the accumulator a Partial node should publish
// into, or nil when this execution is not under a Finalize node.
func lookupAggPartialAccum(ctx *Context, p *optimizer.Aggregate) *aggPartialAccum {
	if ctx == nil || ctx.PartialAggStates == nil || p == nil {
		return nil
	}
	return ctx.PartialAggStates[p]
}
