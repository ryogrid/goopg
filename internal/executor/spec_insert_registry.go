package executor

import (
	"context"
	"strconv"
	"sync"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// specInsertRegistry tracks in-progress speculative insertions and their
// waiters for pg_locks synthetic rows (locktype='spectoken'/'transactionid').
// It mirrors PostgreSQL's SpeculativeInsertionLockAcquire/Release protocol.
// Thread-safe.
type specInsertRegistry struct {
	mu          sync.Mutex
	cond        *sync.Cond
	active      map[storage.TransactionID]*specEntry // XID → entry; from RegisterSpec to DeregisterXID
	specWaiters []specWaiter                         // waiting for a spec token (spectoken ShareLock f)
	xidWaiters  []xidWaiter                          // waiting for XID commit/abort (transactionid ShareLock f)
}

type specEntry struct {
	pid     string
	settled bool // false = holding spec token; true = token released
}

type specWaiter struct {
	pid       string
	ownXID    storage.TransactionID
	targetXID storage.TransactionID
}

type xidWaiter struct {
	pid       string
	ownXID    storage.TransactionID
	targetXID storage.TransactionID
}

var globalSpecReg = func() *specInsertRegistry {
	r := &specInsertRegistry{active: make(map[storage.TransactionID]*specEntry)}
	r.cond = sync.NewCond(&r.mu)
	return r
}()

// DeregisterSpecXID removes xid's entry from the registry at transaction end.
// Called from the MVCC manager's OnTxnEnd hook.
func DeregisterSpecXID(xid storage.TransactionID) {
	globalSpecReg.DeregisterXID(xid)
}

// RegisterSpec records that xid holds an unsettled speculative insertion token.
func (r *specInsertRegistry) RegisterSpec(xid storage.TransactionID, pid string) {
	if xid == storage.InvalidTransactionID {
		return
	}
	r.mu.Lock()
	r.active[xid] = &specEntry{pid: pid, settled: false}
	r.mu.Unlock()
}

// SettleSpec marks xid's spec token as released. No-op if xid is not registered.
func (r *specInsertRegistry) SettleSpec(xid storage.TransactionID) {
	if xid == storage.InvalidTransactionID {
		return
	}
	r.mu.Lock()
	if e := r.active[xid]; e != nil {
		e.settled = true
	}
	r.cond.Broadcast()
	r.mu.Unlock()
}

// DeregisterXID removes xid's entry (called at transaction end via OnTxnEnd).
func (r *specInsertRegistry) DeregisterXID(xid storage.TransactionID) {
	if xid == storage.InvalidTransactionID {
		return
	}
	r.mu.Lock()
	delete(r.active, xid)
	r.cond.Broadcast()
	r.mu.Unlock()
}

// WaitForSpecToken blocks until targetXID's spec token is settled (or ctx is
// cancelled). ownPID and ownXID identify the waiter for pg_locks rows.
// Returns (true, nil) if it actually waited and settled normally,
// (false, nil) if the target was already settled/absent, or (false, ctx.Err())
// on cancellation.
func (r *specInsertRegistry) WaitForSpecToken(ctx context.Context, targetXID storage.TransactionID, ownPID string, ownXID storage.TransactionID) (waited bool, err error) {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			r.cond.Broadcast()
		case <-done:
		}
	}()
	defer close(done)

	r.mu.Lock()
	defer r.mu.Unlock()

	e := r.active[targetXID]
	if e == nil || e.settled {
		return false, nil
	}
	r.specWaiters = append(r.specWaiters, specWaiter{pid: ownPID, ownXID: ownXID, targetXID: targetXID})
	for {
		e = r.active[targetXID]
		if e == nil || e.settled {
			break
		}
		if ctx.Err() != nil {
			break
		}
		r.cond.Wait()
	}
	r.specWaiters = removeSpecWaiter(r.specWaiters, ownXID)
	return true, ctx.Err()
}

// RegisterXIDWait records that ownPID/ownXID is waiting for targetXID to
// commit or abort (produces transactionid ShareLock f in pg_locks).
func (r *specInsertRegistry) RegisterXIDWait(pid string, ownXID, targetXID storage.TransactionID) {
	if ownXID == storage.InvalidTransactionID {
		return
	}
	r.mu.Lock()
	r.xidWaiters = append(r.xidWaiters, xidWaiter{pid: pid, ownXID: ownXID, targetXID: targetXID})
	r.mu.Unlock()
}

// DeregisterXIDWait removes the XID-wait record for ownXID.
func (r *specInsertRegistry) DeregisterXIDWait(ownXID storage.TransactionID) {
	if ownXID == storage.InvalidTransactionID {
		return
	}
	r.mu.Lock()
	r.xidWaiters = removeXIDWaiter(r.xidWaiters, ownXID)
	r.mu.Unlock()
}

// LockRows returns pg_locks-format rows for all active spec entries and waiters.
// Column order matches catalog.AdvisoryLockRowsFunc:
//
//	locktype[0] database[1] relation[2] page[3] tuple[4] virtualxid[5]
//	transactionid[6] classid[7] objid[8] objsubid[9] virtualtransaction[10]
//	pid[11] mode[12] granted[13] fastpath[14] waitstart[15]
func (r *specInsertRegistry) LockRows() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()

	var rows [][]string

	// ownExclEmitted tracks XIDs for which we have already emitted the
	// "transactionid ExclusiveLock t" self-lock row. In PostgreSQL every
	// transaction that has been assigned an XID holds an ExclusiveLock on its
	// own transactionid; a waiter (spec-token or XID) holds it too, but a pure
	// waiter is not in the active map (it is blocked before its own speculative
	// insert), so we must emit its self-lock from the waiter records. Dedup so a
	// waiter that is also active does not produce a duplicate row.
	ownExclEmitted := make(map[storage.TransactionID]bool)
	emitOwnExcl := func(xid storage.TransactionID, pid string) {
		if xid == storage.InvalidTransactionID || ownExclEmitted[xid] {
			return
		}
		ownExclEmitted[xid] = true
		xidStr := strconv.FormatUint(uint64(xid), 10)
		rows = append(rows, []string{
			"transactionid", "", "", "", "", "", xidStr, "", "", "", "", pid, "ExclusiveLock", "t", "f", "",
		})
	}

	// Active spec entries: own transactionid ExclusiveLock t always; spectoken
	// ExclusiveLock t only while the spec token is still held (unsettled).
	for xid, e := range r.active {
		if !e.settled {
			xidStr := strconv.FormatUint(uint64(xid), 10)
			rows = append(rows, []string{
				"spectoken", "", "", "", "", "", "", "", xidStr, "", "", e.pid, "ExclusiveLock", "t", "f", "",
			})
		}
		emitOwnExcl(xid, e.pid)
	}

	// Spec-token waiters: spectoken ShareLock f for the target XID, plus the
	// waiter's own transactionid ExclusiveLock t (a waiting INSERT already holds
	// an XID but is blocked before its own speculative insert, so it is absent
	// from the active map above).
	for _, w := range r.specWaiters {
		targetXIDStr := strconv.FormatUint(uint64(w.targetXID), 10)
		rows = append(rows, []string{
			"spectoken", "", "", "", "", "", "", "", targetXIDStr, "", "", w.pid, "ShareLock", "f", "f", "",
		})
		emitOwnExcl(w.ownXID, w.pid)
	}

	// XID waiters: transactionid ShareLock f for the target XID, plus the
	// waiter's own transactionid ExclusiveLock t.
	for _, w := range r.xidWaiters {
		targetXIDStr := strconv.FormatUint(uint64(w.targetXID), 10)
		rows = append(rows, []string{
			"transactionid", "", "", "", "", "", targetXIDStr, "", "", "", "", w.pid, "ShareLock", "f", "f", "",
		})
		emitOwnExcl(w.ownXID, w.pid)
	}

	return rows
}

func removeSpecWaiter(sl []specWaiter, ownXID storage.TransactionID) []specWaiter {
	for i, w := range sl {
		if w.ownXID == ownXID {
			return append(sl[:i], sl[i+1:]...)
		}
	}
	return sl
}

func removeXIDWaiter(sl []xidWaiter, ownXID storage.TransactionID) []xidWaiter {
	for i, w := range sl {
		if w.ownXID == ownXID {
			return append(sl[:i], sl[i+1:]...)
		}
	}
	return sl
}

func init() {
	catalog.VirtualSpecLockRowsFunc = globalSpecReg.LockRows
}
