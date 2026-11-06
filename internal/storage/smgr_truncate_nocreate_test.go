package storage

import (
	"os"
	"path/filepath"
	"testing"
)

// The M-NIGHTLY "0-byte relfilenode file" class: TRUNCATE routing (the DROP
// IF EXISTS / TRUNCATE executors, VACUUM, nbtree bulkload, smgr redo) calls
// TruncateRelation for relations that may have no storage at all — PG
// partitioned parents carry relfilenode 0 and no file, and a name-resolution
// miss must not materialise one. PG's mdtruncate
// (src/backend/storage/smgr/md.c:1291) walks the relation's already-open
// segment descriptors, so a relation with no open segment is a no-op; it
// NEVER creates a file. These tests pin that parity: truncate of a missing
// relation is a no-op, not an O_CREATE side effect.

func fileState(t *testing.T, path string) (bool, int64) {
	t.Helper()
	st, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, -1
	}
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return true, st.Size()
}

// relFilePath resolves Manager.RelPath (relative to the data dir) against
// the data dir so os.Stat sees an absolute path.
func relFilePath(m *Manager, rel RelFileNode) string {
	return filepath.Join(m.DataDir(), m.RelPath(rel))
}

func TestTruncateRelationMissingNoCreate(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(ManagerConfig{DataDir: dir})
	defer func() { _ = m.Close() }()

	rel := RelFileNode{DBOid: 1, RelOid: 90001}
	path := relFilePath(m, rel)

	if err := m.TruncateRelation(rel); err != nil {
		t.Fatalf("TruncateRelation of missing rel: %v", err)
	}
	if exists, _ := fileState(t, path); exists {
		t.Fatalf("TruncateRelation materialised %s; PG mdtruncate of a relation with no segments is a no-op", path)
	}
}

func TestTruncateRelationToMissingNoCreate(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(ManagerConfig{DataDir: dir})
	defer func() { _ = m.Close() }()

	rel := RelFileNode{DBOid: 1, RelOid: 90002}
	path := relFilePath(m, rel)

	if err := m.TruncateRelationTo(rel, 0); err != nil {
		t.Fatalf("TruncateRelationTo of missing rel: %v", err)
	}
	if exists, _ := fileState(t, path); exists {
		t.Fatalf("TruncateRelationTo materialised %s; PG mdtruncate of a relation with no segments is a no-op", path)
	}
}

func TestTruncateRelationMissingDBDirNoCreate(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(ManagerConfig{DataDir: dir})
	defer func() { _ = m.Close() }()

	// A relation in a database directory that does not exist at all (e.g. a
	// shared-catalog relpath under global/ before initdb created it). The
	// truncate path must not MkdirAll the directory away as a side effect.
	rel := RelFileNode{DBOid: 424242, RelOid: 90003}
	if err := m.TruncateRelation(rel); err != nil {
		t.Fatalf("TruncateRelation with missing db dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "base", "424242")); !os.IsNotExist(err) {
		t.Fatalf("truncate created base/424242 (stat err = %v)", err)
	}
}

func TestTruncateRelationExistingStillTruncates(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(ManagerConfig{DataDir: dir})
	defer func() { _ = m.Close() }()

	rel := RelFileNode{DBOid: 1, RelOid: 90004}
	buf := make([]byte, BlockSize)
	for i := range buf {
		buf[i] = 0xAB
	}
	// Two blocks through the normal extend path (O_CREATE is correct there).
	for i := 0; i < 2; i++ {
		if _, err := m.Extend(rel, buf); err != nil {
			t.Fatalf("Extend: %v", err)
		}
	}
	path := relFilePath(m, rel)
	if exists, size := fileState(t, path); !exists || size != 2*BlockSize {
		t.Fatalf("pre-truncate file exists=%v size=%d, want 2 blocks", exists, size)
	}
	if err := m.TruncateRelation(rel); err != nil {
		t.Fatalf("TruncateRelation: %v", err)
	}
	if exists, size := fileState(t, path); !exists || size != 0 {
		t.Fatalf("post-truncate file exists=%v size=%d, want 0 bytes", exists, size)
	}

	// TruncateRelationTo keeps the same semantics on an existing file.
	for i := 0; i < 3; i++ {
		if _, err := m.Extend(rel, buf); err != nil {
			t.Fatalf("Extend: %v", err)
		}
	}
	if err := m.TruncateRelationTo(rel, 1); err != nil {
		t.Fatalf("TruncateRelationTo: %v", err)
	}
	if exists, size := fileState(t, path); !exists || size != BlockSize {
		t.Fatalf("post-TruncateRelationTo file exists=%v size=%d, want 1 block", exists, size)
	}
}
