package lmgr

// perf-optimize-take3 candidate A: PostgreSQL's fast path for weak relation
// locks.
//
// goopg's autocommit statement path (Context.acquireRelLockMaybeTransient)
// acquires a relation lock and releases it again immediately, per relation AND
// per index, on every statement. At c=50 that churn made the single global
// LockManager mutex 90.8% of all read-path mutex delay and put 19.9% of every
// backend sample in Lock:relation — a wait PostgreSQL reports ZERO of.
//
// Upstream never touches its shared lock table for such a request. Any mode
// below ShareUpdateExclusiveLock on a non-shared relation is
// EligibleForRelationFastPath (postgres/src/backend/storage/lmgr/lock.c:267)
// and is granted out of the backend's own PGPROC slot array by
// FastPathGrantRelationLock (lock.c:2750). The shared table is consulted only
// when a STRONG lock exists on the relation, which upstream detects with a
// counter array indexed by a hash of the tag —
// FastPathStrongRelationLocks->count[] (lock.c:999).
//
// This file ports that counter. The weak modes cannot conflict with each other
// (AccessShare/RowShare/RowExclusive are mutually compatible in upstream's
// conflict table), so if no strong lock is present on the tag's bucket, a weak
// request provably cannot conflict and there is nothing to record — the
// transient acquire/release pair collapses to one atomic load.
//
// Scope note: this is deliberately used ONLY by the transient
// acquire-then-immediately-release path, never for locks a transaction holds
// to end-of-transaction. A transient acquire's entire observable contract is
// "block if something conflicting is held right now"; it records nothing
// afterwards, so skipping it when nothing conflicting exists is
// indistinguishable from having run it a moment earlier. Locks that are RETAINED
// still go through the full table, because their presence has to be visible to
// everyone else.

// fastPathStrongBuckets mirrors FAST_PATH_STRONG_LOCK_HASH_PARTITIONS
// (postgres/src/include/storage/lock.h). A power of two so the modulo is a mask.
const fastPathStrongBuckets = 1024

// strongModeMask is every mode that conflicts with at least one fast-path
// eligible (weak) mode, i.e. ShareUpdateExclusiveLock and above. It is the
// complement of the eligibility test in EligibleForFastPath.
const strongModeMask = Mask(1)<<uint(ShareUpdateExclusiveLock) |
	Mask(1)<<uint(ShareLock) |
	Mask(1)<<uint(ShareRowExclusiveLock) |
	Mask(1)<<uint(ExclusiveLock) |
	Mask(1)<<uint(AccessExclusiveLock)

// EligibleForFastPath is EligibleForRelationFastPath's mode test: every mode
// weaker than ShareUpdateExclusiveLock. goopg's LockTag is already
// per-database, so the MyDatabaseId half of upstream's macro is implicit.
func EligibleForFastPath(m Mode) bool {
	return m > NoLock && m < ShareUpdateExclusiveLock
}

// fastPathBucket hashes a tag into the strong-lock counter array. FNV-1a over
// the four words; the tag is small and fixed-size, so this is a handful of
// multiplies with no allocation.
func fastPathBucket(t LockTag) uint32 {
	const (
		off = 2166136261
		prm = 16777619
	)
	h := uint32(off)
	for _, w := range [4]uint32{t.DB, t.Rel, t.Block, t.Offset} {
		h = (h ^ (w & 0xff)) * prm
		h = (h ^ ((w >> 8) & 0xff)) * prm
		h = (h ^ ((w >> 16) & 0xff)) * prm
		h = (h ^ (w >> 24)) * prm
	}
	return h & (fastPathStrongBuckets - 1)
}

// syncStrongLocked reconciles the strong-lock counter with st.granted after any
// change to it. Caller must hold lm.mu, which is what makes the counter's
// increments and decrements pair correctly; readers use an atomic load.
//
// It must run BEFORE gcLocked drops an empty state from the map, or a tag that
// still counted as strong would leak its bucket increment forever.
func (lm *LockManager) syncStrongLocked(t LockTag, st *lockState) {
	strong := st.granted&strongModeMask != 0
	if strong == st.strongCounted {
		return
	}
	if strong {
		lm.strongCounts[fastPathBucket(t)].Add(1)
	} else {
		lm.strongCounts[fastPathBucket(t)].Add(-1)
	}
	st.strongCounted = strong
}

// NoConflictFastPath reports whether a WEAK lock request on t is provably
// conflict-free right now, so a transient acquire/release pair can be skipped
// entirely. It is never a substitute for acquiring a lock that will be held.
//
// A false negative (returning false when no conflict exists) only costs the
// slow path. A false positive cannot occur for a retained strong lock: the
// counter is raised under lm.mu before the strong lock is granted and lowered
// after it is gone, so any strong lock that is fully granted is visible here.
func (lm *LockManager) NoConflictFastPath(t LockTag, m Mode) bool {
	if !EligibleForFastPath(m) {
		return false
	}
	return lm.strongCounts[fastPathBucket(t)].Load() == 0
}
