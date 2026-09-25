package vacuum

import (
	"sync"

	"github.com/goopg/goopg/internal/storage"
)

// relationVacuumGates serialises VACUUMs of one relation, first heap pass
// through second heap pass (M0145-0008v). PG gets this from the
// ShareUpdateExclusiveLock every VACUUM holds for its whole run
// (vacuum_rel, vacuum.c; autovacuum takes it with ConditionalLockRelationOid
// and skips a busy table). goopg's VACUUM only waits for that lock and
// releases it at once, and autovacuum takes none, so without this gate two
// VACUUMs could interleave: A collects dead TIDs, B's second pass frees and
// truncates the same items, an insert reuses one of the offsets, and A's index
// pass then deletes the NEW tuple's index entries.
var relationVacuumGates sync.Map // storage.RelFileNode -> *sync.Mutex

func relationVacuumGate(rel storage.RelFileNode) *sync.Mutex {
	g, _ := relationVacuumGates.LoadOrStore(rel, &sync.Mutex{})
	return g.(*sync.Mutex)
}

// LockRelationForVacuum blocks until no other VACUUM of rel is running and
// returns the release function. Manual VACUUM uses it.
func LockRelationForVacuum(rel storage.RelFileNode) func() {
	g := relationVacuumGate(rel)
	g.Lock()
	return g.Unlock
}

// TryLockRelationForVacuum is the conditional form: autovacuum and
// VACUUM (SKIP_LOCKED) skip a relation another VACUUM is already processing.
func TryLockRelationForVacuum(rel storage.RelFileNode) (func(), bool) {
	g := relationVacuumGate(rel)
	if !g.TryLock() {
		return nil, false
	}
	return g.Unlock, true
}
