package vacuum

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestRelationVacuumGate pins the one-VACUUM-per-relation gate (M0145-0008v):
// while one VACUUM holds a relation, the conditional form (autovacuum,
// SKIP_LOCKED) is refused for that relation and granted for another, and the
// release makes it available again.
func TestRelationVacuumGate(t *testing.T) {
	a := storage.RelFileNode{DBOid: 1, RelOid: 9001, Fork: storage.MainFork}
	b := storage.RelFileNode{DBOid: 1, RelOid: 9002, Fork: storage.MainFork}
	release := LockRelationForVacuum(a)
	if _, ok := TryLockRelationForVacuum(a); ok {
		t.Fatal("a held relation granted the conditional gate")
	}
	rb, ok := TryLockRelationForVacuum(b)
	if !ok {
		t.Fatal("an unrelated relation was refused")
	}
	rb()
	release()
	ra, ok := TryLockRelationForVacuum(a)
	if !ok {
		t.Fatal("released relation still refused")
	}
	ra()
}
