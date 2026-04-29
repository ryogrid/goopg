package lockmgr

// Deadlock detection (M0012-0002). See
// docs/design/0012-0002-deadlock-detection-algorithm.md.
//
// The detector runs as a periodic check scheduled by Acquire's
// time.AfterFunc when a backend parks longer than deadlockTimeout.
// It walks the (waiter -> holder-it-blocks-on) wait-for graph,
// finds a cycle via iterative DFS, picks the youngest backend in
// the cycle as victim, and signals the victim's Acquire goroutine
// to abort with ErrDeadlockDetected.

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

// runDeadlockCheck is the time.AfterFunc target. It can fire after
// the parked backend has already woken (signal or context cancel)
// and the lockState GC'd; the lock check is a no-op in those cases.
func (lm *LockManager) runDeadlockCheck() {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	lm.checkDeadlockLocked()
}

// checkDeadlockLocked is the detector entry point. Caller holds
// lm.mu. Walks every waiter, builds outbound edges to conflicting
// holders, runs DFS, cancels at most one victim per pass.
//
// Returns true if a victim was cancelled.
func (lm *LockManager) checkDeadlockLocked() bool {
	// Build adjacency: waiter -> set of holders it's blocked on.
	// A waiter blocks on holder h iff h's mask conflicts with the
	// waiter's mode AND h != waiter (no self-edges).
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

	// Pick victim = youngest backend (highest BackendID).
	victim := cycle[0]
	for _, b := range cycle[1:] {
		if b > victim {
			victim = b
		}
	}

	lm.cancelVictimLocked(victim)
	return true
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
