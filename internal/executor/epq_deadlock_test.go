package executor

import (
	"sync"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestEPQDeadlockCycleDetected verifies that registerWFGAndCheckCycle
// identifies a 2-node cycle (TX1 waiting for TX2, TX2 waiting for TX1)
// and returns true without leaving a stale WFG entry. M0099-0004.
func TestEPQDeadlockCycleDetected(t *testing.T) {
	const tx1 storage.TransactionID = 101
	const tx2 storage.TransactionID = 102

	// Clean state before test (global map).
	wfgMu.Lock()
	waitForGraph = make(map[storage.TransactionID]storage.TransactionID)
	wfgMu.Unlock()

	// Register tx2 → tx1 first (tx2 is waiting for tx1).
	if registerWFGAndCheckCycle(tx2, tx1) {
		t.Fatal("unexpected cycle on first registration (only 1 edge)")
	}

	// Now register tx1 → tx2. This closes the cycle.
	if !registerWFGAndCheckCycle(tx1, tx2) {
		t.Fatal("expected deadlock cycle to be detected")
	}

	// After cycle detection, tx1 entry must be removed automatically.
	wfgMu.Lock()
	_, tx1Present := waitForGraph[tx1]
	wfgMu.Unlock()
	if tx1Present {
		t.Error("tx1 WFG entry was not cleaned up after cycle detection")
	}

	// Cleanup tx2 entry.
	deregisterWFG(tx2)
}

// TestEPQNoDeadlockNoCycle verifies that a simple non-cyclic wait (TX1 → TX2
// with no reverse edge) does NOT report a deadlock. M0099-0004.
func TestEPQNoDeadlockNoCycle(t *testing.T) {
	const tx1 storage.TransactionID = 201
	const tx2 storage.TransactionID = 202

	wfgMu.Lock()
	waitForGraph = make(map[storage.TransactionID]storage.TransactionID)
	wfgMu.Unlock()

	if registerWFGAndCheckCycle(tx1, tx2) {
		t.Fatal("false deadlock: single-edge graph reported cycle")
	}
	deregisterWFG(tx1)
}

// TestEPQDeadlockThreeNode verifies 3-node cycle detection
// (TX1→TX2→TX3→TX1). M0099-0004.
func TestEPQDeadlockThreeNode(t *testing.T) {
	const tx1 storage.TransactionID = 301
	const tx2 storage.TransactionID = 302
	const tx3 storage.TransactionID = 303

	wfgMu.Lock()
	waitForGraph = make(map[storage.TransactionID]storage.TransactionID)
	wfgMu.Unlock()

	if registerWFGAndCheckCycle(tx2, tx3) {
		t.Fatal("unexpected cycle: only one edge")
	}
	if registerWFGAndCheckCycle(tx3, tx1) {
		t.Fatal("unexpected cycle: two edges, no back-edge to tx2")
	}
	// tx1→tx2 closes the 3-node cycle.
	if !registerWFGAndCheckCycle(tx1, tx2) {
		t.Fatal("expected 3-node cycle to be detected")
	}
	deregisterWFG(tx2)
	deregisterWFG(tx3)
}

// TestEPQWFGConcurrentSafety verifies that concurrent goroutines can call
// registerWFGAndCheckCycle and deregisterWFG without data races. Each
// goroutine uses a unique XID pair so there are no cycles to detect — the
// test only checks that the operations are race-free. M0099-0004.
func TestEPQWFGConcurrentSafety(t *testing.T) {
	wfgMu.Lock()
	waitForGraph = make(map[storage.TransactionID]storage.TransactionID)
	wfgMu.Unlock()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			myXID := storage.TransactionID(500 + i)
			blockXID := storage.TransactionID(600 + i)
			if !registerWFGAndCheckCycle(myXID, blockXID) {
				deregisterWFG(myXID)
			}
		}()
	}
	wg.Wait()
}
