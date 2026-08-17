package lmgr

import "slices"

// Deadlock detection (M0012-0002, soft-edge resolution M0118-0004). See
// docs/design/0012-0002-deadlock-detection-algorithm.md and
// docs/design/0118-0006-soft-deadlock-wait-queue-reordering.md.
//
// The detector runs as a periodic check scheduled by Acquire's
// time.AfterFunc when a backend parks longer than deadlockTimeout.
//
// There are two cycle classes, mirroring upstream's deadlock.c:
//
//   - HARD edges: a waiter blocks on a backend that already HOLDS a
//     conflicting lock. A cycle made entirely of hard edges is
//     unbreakable without aborting a victim.
//   - SOFT edges: a waiter is parked behind another WAITER (earlier in
//     the same lock's FIFO queue) whose pending request conflicts. Such
//     an edge can be eliminated by reordering the wait queue — moving the
//     blocked waiter ahead of the one it conflicts with — provided the
//     reordering stays self-consistent. A cycle containing a soft edge can
//     therefore be resolved WITHOUT rolling anyone back.
//
// The firing-backend path (runDeadlockCheckFor, prefer != 0) runs the
// full soft-aware search: it tries reversing soft edges (TopoSort over the
// affected wait queues) until it finds a deadlock-free ordering, applies
// it, and wakes the newly grantable waiters. Only a genuinely hard cycle
// rolls back the firing backend (mirroring PostgreSQL, which aborts the
// proc whose deadlock_timeout expired and ran DeadLockCheck).
//
// The synchronous CheckDeadlocksNow path (prefer == 0, used by unit tests)
// keeps the original hard-edge-only youngest-victim behaviour.

// CheckDeadlocksNow runs one synchronous deadlock-check pass.
// Tests use this to drive the detector deterministically without
// waiting for the timer; production calls it indirectly through
// runDeadlockCheck.
//
// Reports whether a victim was selected and cancelled — useful for
// test assertions.
func (lm *LockManager) CheckDeadlocksNow() bool {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	return lm.checkDeadlockLocked()
}

// runDeadlockCheckFor is the time.AfterFunc target scheduled by a
// parked backend's Acquire. It can fire after the parked backend has
// already woken (signal or context cancel) and the lockState GC'd; the
// check is a no-op in those cases. `b` is the backend whose timer fired:
// it is the start point of the wait-for search and, for a hard cycle, the
// rollback victim — mirroring PostgreSQL where the process that runs
// DeadLockCheck (its deadlock_timeout having expired) is the one rolled
// back. For a soft cycle the queues are rearranged and nobody dies.
func (lm *LockManager) runDeadlockCheckFor(b BackendID) {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	lm.checkDeadlockLockedFor(b)
}

// checkDeadlockLocked is the detector entry point used by
// CheckDeadlocksNow (no preferred victim → youngest-in-cycle,
// hard-edge-only). Caller holds lm.mu.
//
// Returns true if a victim was cancelled.
func (lm *LockManager) checkDeadlockLocked() bool {
	return lm.checkDeadlockLockedFor(0)
}

// checkDeadlockLockedFor runs the appropriate deadlock check. When
// `prefer` is zero (the synchronous test path) it runs the legacy
// hard-edge-only search and cancels the youngest backend in any cycle.
// When `prefer` is non-zero (the timer-driven path) it runs the
// soft-aware search starting from `prefer`: a resolvable (soft) cycle is
// fixed by rearranging wait queues; an unresolvable (hard) cycle rolls
// back `prefer`. Caller holds lm.mu.
//
// Returns true if a victim was cancelled.
func (lm *LockManager) checkDeadlockLockedFor(prefer BackendID) bool {
	if prefer == 0 {
		return lm.checkDeadlockHardOnlyLocked()
	}
	// The firing backend must still be parked; the timer can race a wake.
	if _, _, ok := lm.waiterInfo(prefer); !ok {
		return false
	}
	resolved, solution := lm.deadLockCheck(prefer)
	if !resolved {
		// Hard deadlock: roll back the firing backend.
		lm.cancelVictimLocked(prefer)
		return true
	}
	if len(solution) == 0 {
		// No cycle involving `prefer`; nothing to do.
		return false
	}
	// Soft deadlock: rearrange the affected wait queues and wake any
	// waiters the new ordering makes grantable. Nobody is rolled back.
	orders, ok := lm.expandConstraints(solution)
	if !ok {
		// deadLockCheck already validated this configuration, so this
		// should be unreachable; degrade safely to a rollback.
		lm.cancelVictimLocked(prefer)
		return true
	}
	lm.applyWaitOrders(orders)
	return false
}

// checkDeadlockHardOnlyLocked is the legacy synchronous detector: it
// walks every waiter, builds outbound edges to conflicting holders (hard
// edges only), runs DFS, and cancels the youngest backend (highest
// BackendID) in any discovered cycle. Caller holds lm.mu.
//
// Returns true if a victim was cancelled.
func (lm *LockManager) checkDeadlockHardOnlyLocked() bool {
	edges := make(map[BackendID][]BackendID)
	for _, st := range lm.states {
		for _, w := range st.waiters {
			for h, hMask := range st.holders {
				if h == w.Backend {
					continue
				}
				if ConflictsWith(w.Mode, hMask) {
					edges[w.Backend] = append(edges[w.Backend], h)
				}
			}
		}
	}
	if len(edges) == 0 {
		return false
	}
	cycle := findCycle(edges)
	if cycle == nil {
		return false
	}
	victim := cycle[0]
	for _, b := range cycle {
		if b > victim {
			victim = b
		}
	}
	lm.cancelVictimLocked(victim)
	return true
}

// waitEdge is a soft (queue-order) wait-for edge that can potentially be
// eliminated by rearranging `tag`'s wait queue: `waiter` is parked behind
// `blocker` in the queue with a conflicting request. Reversing the edge
// means forcing `waiter` ahead of `blocker` in the queue.
type waitEdge struct {
	waiter  BackendID
	blocker BackendID
	tag     LockTag
}

// waiterInfo returns the tag a backend is currently parked on and the
// mode it requested. A backend blocks in at most one Acquire at a time,
// so it appears as a waiter in at most one lockState. Caller holds lm.mu.
func (lm *LockManager) waiterInfo(b BackendID) (LockTag, Mode, bool) {
	for t, st := range lm.states {
		for _, w := range st.waiters {
			if w.Backend == b {
				return t, w.Mode, true
			}
		}
	}
	return LockTag{}, NoLock, false
}

// waiterMode returns backend `b`'s requested mode in `tag`'s queue.
// Caller holds lm.mu.
func (lm *LockManager) waiterMode(tag LockTag, b BackendID) (Mode, bool) {
	st := lm.states[tag]
	if st == nil {
		return NoLock, false
	}
	for _, w := range st.waiters {
		if w.Backend == b {
			return w.Mode, true
		}
	}
	return NoLock, false
}

// queueOrder returns the order of waiter backends for `tag`. If a
// hypothetical reordering for `tag` is present in `waitOrders` it is
// believed in preference to the true FIFO order (mirroring deadlock.c's
// waitOrders[] override). Caller holds lm.mu.
func (lm *LockManager) queueOrder(tag LockTag, waitOrders map[LockTag][]BackendID) []BackendID {
	if ord, ok := waitOrders[tag]; ok {
		return ord
	}
	st := lm.states[tag]
	if st == nil {
		return nil
	}
	out := make([]BackendID, len(st.waiters))
	for i, w := range st.waiters {
		out[i] = w.Backend
	}
	return out
}

// findLockCycle scans the wait-for graph outward from `start` and reports
// whether a cycle passing through `start` exists. If so it returns the
// soft edges contained in that cycle (empty for a pure hard cycle). It
// honours the hypothetical queue orderings in `waitOrders`. Mirrors
// deadlock.c FindLockCycle / FindLockCycleRecurse (without lock groups,
// which goopg does not have). Caller holds lm.mu.
func (lm *LockManager) findLockCycle(start BackendID, waitOrders map[LockTag][]BackendID) (bool, []waitEdge) {
	visited := make([]BackendID, 0, len(lm.states))
	var soft []waitEdge

	var recurse func(check BackendID, depth int) bool
	recurse = func(check BackendID, depth int) bool {
		for i, v := range visited {
			if v == check {
				// Returning to the start point (index 0) closes a cycle
				// through `start`; any other revisit is a cycle that does
				// not include the start, so we say "no deadlock" for it.
				return i == 0
			}
		}
		visited = append(visited, check)

		tag, mode, waiting := lm.waiterInfo(check)
		if !waiting {
			return false
		}
		st := lm.states[tag]
		if st == nil {
			return false
		}

		// Hard edges first: backends that already hold a conflicting lock.
		// (Done before the soft scan so that a backend that both hard- and
		// soft-blocks us is recorded as a hard edge.)
		for h, hMask := range st.holders {
			if h == check {
				continue
			}
			if ConflictsWith(mode, hMask) {
				if recurse(h, depth+1) {
					return true
				}
			}
		}

		// Soft edges: backends ahead of us in the (hypothetical) wait
		// queue whose pending request conflicts with ours.
		for _, ahead := range lm.queueOrder(tag, waitOrders) {
			if ahead == check {
				// Reached our own position; nothing after us soft-blocks us.
				break
			}
			am, ok := lm.waiterMode(tag, ahead)
			if !ok {
				continue
			}
			if ConflictsWith(mode, bit(am)) {
				if recurse(ahead, depth+1) {
					soft = append(soft, waitEdge{waiter: check, blocker: ahead, tag: tag})
					return true
				}
			}
		}
		return false
	}

	found := recurse(start, 0)
	return found, soft
}

// testConfiguration tests one hypothetical constraint set `cur` for
// validity, mirroring deadlock.c TestConfiguration. It expands the
// constraints into wait-queue orderings and then checks for cycles
// reachable from the constraint endpoints and from `start`.
//
// Returns:
//
//	-1  the configuration has a hard deadlock or is not self-consistent
//	 0  the configuration is deadlock-free
//	>0  one or more soft cycles remain; the returned edges are the soft
//	    edges of one such cycle (candidates to reverse next)
//
// Caller holds lm.mu.
func (lm *LockManager) testConfiguration(start BackendID, cur []waitEdge) (int, []waitEdge) {
	waitOrders, ok := lm.expandConstraints(cur)
	if !ok {
		return -1, nil // contradictory constraints
	}

	var softFound []waitEdge
	// consider returns true if a hard deadlock was found from `b`.
	consider := func(b BackendID) bool {
		found, s := lm.findLockCycle(b, waitOrders)
		if !found {
			return false
		}
		if len(s) == 0 {
			return true // hard deadlock
		}
		softFound = s
		return false
	}

	// Check constraint endpoints first, then `start` last so its soft
	// cycle (if any) is the one we deal with next.
	for _, c := range cur {
		if consider(c.waiter) {
			return -1, nil
		}
		if consider(c.blocker) {
			return -1, nil
		}
	}
	if consider(start) {
		return -1, nil
	}
	return len(softFound), softFound
}

// deadLockCheck searches for a deadlock-free rearrangement of wait queues
// reachable from `start`. Mirrors deadlock.c DeadLockCheckRecurse: it
// tries reversing each available soft edge as an added constraint until a
// deadlock-free configuration is found.
//
// Returns resolved=true with the solution constraint set when a
// deadlock-free ordering exists (an empty solution means there was no
// cycle involving `start` at all). Returns resolved=false when the cycle
// is a hard deadlock with no solution. Caller holds lm.mu.
func (lm *LockManager) deadLockCheck(start BackendID) (bool, []waitEdge) {
	var solution []waitEdge

	// recurse returns true if NO solution exists for `cur` (matching the
	// upstream sense of DeadLockCheckRecurse).
	var recurse func(cur []waitEdge) bool
	recurse = func(cur []waitEdge) bool {
		n, soft := lm.testConfiguration(start, cur)
		if n < 0 {
			return true // hard deadlock — no solution down this branch
		}
		if n == 0 {
			solution = append([]waitEdge(nil), cur...)
			return false // good configuration found
		}
		for _, e := range soft {
			if containsEdge(cur, e) {
				continue
			}
			next := append(append([]waitEdge(nil), cur...), e)
			if !recurse(next) {
				return false
			}
		}
		return true
	}

	if recurse(nil) {
		return false, nil
	}
	return true, solution
}

// containsEdge reports whether `e` is already in `edges` (same waiter,
// blocker, and tag) — a guard against re-adding a constraint and looping.
func containsEdge(edges []waitEdge, e waitEdge) bool {
	return slices.Contains(edges, e)
}

// expandConstraints expands a list of soft-edge constraints into a set of
// per-lock wait-queue orderings via topological sort, mirroring
// deadlock.c ExpandConstraints. Returns false if any affected queue
// cannot be ordered to satisfy its constraints. Caller holds lm.mu.
func (lm *LockManager) expandConstraints(constraints []waitEdge) (map[LockTag][]BackendID, bool) {
	orders := make(map[LockTag][]BackendID)
	for _, c := range constraints {
		if _, done := orders[c.tag]; done {
			continue
		}
		order, ok := lm.topoSort(c.tag, constraints)
		if !ok {
			return nil, false
		}
		orders[c.tag] = order
	}
	return orders, true
}

// topoSort produces a reordering of `tag`'s wait queue that satisfies all
// constraints mentioning `tag` (each constraint requires its waiter to
// appear before its blocker), minimising disturbance to the existing FIFO
// order. Mirrors deadlock.c TopoSort (lock-group handling omitted — goopg
// has no lock groups). Returns false if the constraints are contradictory
// (a cycle among the constraints themselves). Caller holds lm.mu.
func (lm *LockManager) topoSort(tag LockTag, constraints []waitEdge) ([]BackendID, bool) {
	st := lm.states[tag]
	if st == nil {
		return nil, true
	}
	n := len(st.waiters)
	procs := make([]BackendID, n)
	idx := make(map[BackendID]int, n)
	for i, w := range st.waiters {
		procs[i] = w.Backend
		idx[w.Backend] = i
	}

	// before[j] = number of constraints requiring procs[j] to come before
	// some other (not-yet-emitted) proc. after[k] = waiter indices that
	// must precede procs[k] (so emitting procs[k] relaxes their counts).
	before := make([]int, n)
	after := make([][]int, n)
	for _, c := range constraints {
		if c.tag != tag {
			continue
		}
		wj, okw := idx[c.waiter]
		bk, okb := idx[c.blocker]
		if !okw || !okb {
			continue // constraint not relevant to this queue
		}
		before[wj]++
		after[bk] = append(after[bk], wj)
	}

	// Emit from the back: repeatedly pick the highest-index proc with no
	// remaining before-constraints (minimises rearrangement), place it at
	// the current tail, and relax the procs constrained to precede it.
	out := make([]BackendID, n)
	emitted := make([]bool, n)
	for i := n - 1; i >= 0; i-- {
		j := -1
		for s := n - 1; s >= 0; s-- {
			if !emitted[s] && before[s] == 0 {
				j = s
				break
			}
		}
		if j < 0 {
			return nil, false // contradictory constraints
		}
		out[i] = procs[j]
		emitted[j] = true
		for _, wj := range after[j] {
			before[wj]--
		}
	}
	return out, true
}

// applyWaitOrders rewrites the affected lock-state wait queues to the
// given orderings (preserving the existing *Waiter pointers so parked
// goroutines still receive their signals) and runs a wake-pass on each so
// any waiter the new order makes grantable is promoted. Caller holds
// lm.mu.
func (lm *LockManager) applyWaitOrders(orders map[LockTag][]BackendID) {
	for tag, order := range orders {
		st := lm.states[tag]
		if st == nil {
			continue
		}
		byID := make(map[BackendID]*Waiter, len(st.waiters))
		for _, w := range st.waiters {
			byID[w.Backend] = w
		}
		reordered := make([]*Waiter, 0, len(st.waiters))
		for _, b := range order {
			if w, ok := byID[b]; ok {
				reordered = append(reordered, w)
				delete(byID, b)
			}
		}
		// Defensive: append any waiters not named in `order` (none expected,
		// since topoSort covers the whole queue) in their original order.
		for _, w := range st.waiters {
			if _, ok := byID[w.Backend]; ok {
				reordered = append(reordered, w)
				delete(byID, w.Backend)
			}
		}
		st.waiters = reordered
		lm.wakePassLocked(tag, st)
	}
}

// findCycle runs iterative DFS over `edges` and returns the
// participants of any cycle it finds. nil if the graph is
// cycle-free.
//
// Three-colour DFS: white = unvisited, grey = on stack, black =
// fully explored. A grey-on-grey edge is a back-edge → cycle. The
// returned slice is the cycle members (order is the back-edge path
// — sufficient for victim selection which just takes max).
func findCycle(edges map[BackendID][]BackendID) []BackendID {
	const (
		white = 0
		grey  = 1
		black = 2
	)
	colour := make(map[BackendID]int)
	parent := make(map[BackendID]BackendID)

	// Snapshot the start nodes so the DFS doesn't visit a backend
	// it's already explored from a different root.
	starts := make([]BackendID, 0, len(edges))
	for b := range edges {
		starts = append(starts, b)
	}

	for _, root := range starts {
		if colour[root] != white {
			continue
		}
		// Iterative DFS using an explicit stack of (node, iter-index).
		type frame struct {
			node BackendID
			i    int
		}
		stack := []frame{{root, 0}}
		colour[root] = grey
		for len(stack) > 0 {
			top := &stack[len(stack)-1]
			adj := edges[top.node]
			if top.i >= len(adj) {
				colour[top.node] = black
				stack = stack[:len(stack)-1]
				continue
			}
			next := adj[top.i]
			top.i++
			switch colour[next] {
			case white:
				colour[next] = grey
				parent[next] = top.node
				stack = append(stack, frame{next, 0})
			case grey:
				// Back-edge: cycle = path from `next` up via
				// `parent` chain back to `top.node`, plus the
				// closing edge.
				return reconstructCycle(parent, next, top.node)
			case black:
				// Already explored, no cycle through it.
			}
		}
	}
	return nil
}

// reconstructCycle walks parent pointers from `end` back to `start`
// (the grey node hit by the back-edge) and returns the cycle
// members. Both endpoints appear once in the slice.
func reconstructCycle(parent map[BackendID]BackendID, start, end BackendID) []BackendID {
	cycle := []BackendID{start}
	for cur := end; cur != start; cur = parent[cur] {
		cycle = append(cycle, cur)
		// Defensive: if parent chain is broken (shouldn't happen
		// since we just walked it), bail out with what we have.
		if _, ok := parent[cur]; !ok {
			break
		}
	}
	return cycle
}

// cancelVictimLocked finds the victim's parked Waiter, splices it
// out of whichever queue it's in, and signals it via the `victim`
// channel so the Acquire goroutine returns ErrDeadlockDetected.
//
// Caller holds lm.mu. The Acquire goroutine then calls ReleaseAll
// (outside the mutex) to drop any other holdings the victim has.
func (lm *LockManager) cancelVictimLocked(victim BackendID) {
	for tag, st := range lm.states {
		for i, w := range st.waiters {
			if w.Backend != victim {
				continue
			}
			// Splice out, signal, run wake-pass for any backends
			// queued behind the victim that may now be grantable.
			st.waiters = append(st.waiters[:i], st.waiters[i+1:]...)
			select {
			case w.victim <- struct{}{}:
			default:
			}
			lm.wakePassLocked(tag, st)
			lm.gcLocked(tag, st)
			return
		}
	}
}
