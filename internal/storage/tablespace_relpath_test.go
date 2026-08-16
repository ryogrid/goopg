package storage

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/utils/misc"
)

// TestRelPathTablespace verifies that a non-zero TblOid routes a
// RelFileNode's path through pg_tblspc/<TblOid>/<version dir>/<dbOid>/
// instead of the default base/<dbOid>/ layout, mirroring PostgreSQL's
// relpath() tablespace branch. TblOid==0 (pg_default) must keep resolving
// exactly as before this change. M0122-0007 tablespace physical relocation.
func TestRelPathTablespace(t *testing.T) {
	mgr := NewManager(ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()

	def := RelFileNode{TblOid: 0, DBOid: 5, RelOid: 16407, Fork: MainFork}
	if got, want := mgr.RelPath(def), "base/5/16407"; got != want {
		t.Fatalf("RelPath(default tablespace) = %q, want %q", got, want)
	}

	ts := RelFileNode{TblOid: 40000, DBOid: 5, RelOid: 16407, Fork: MainFork}
	want := "pg_tblspc/40000/" + misc.TablespaceVersionDirectory + "/5/16407"
	if got := mgr.RelPath(ts); got != want {
		t.Fatalf("RelPath(tablespace 40000) = %q, want %q", got, want)
	}
}

// TestManagerOpensTablespaceUnderPgTblspcDir verifies that actually writing a
// block through the Manager for a non-zero-TblOid RelFileNode creates the
// file on disk under pg_tblspc/<oid>/..., not base/<dbOid>/.
func TestManagerOpensTablespaceUnderPgTblspcDir(t *testing.T) {
	dataDir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	rel := RelFileNode{TblOid: 40000, DBOid: 5, RelOid: 16407, Fork: MainFork}
	page := make(Page, BlockSize)
	if err := InitPage(page); err != nil {
		t.Fatalf("InitPage: %v", err)
	}
	if _, err := mgr.Extend(rel, page); err != nil {
		t.Fatalf("Extend: %v", err)
	}

	tsPath := filepath.Join(dataDir, "pg_tblspc", "40000", misc.TablespaceVersionDirectory, "5", "16407")
	if _, err := os.Stat(tsPath); err != nil {
		t.Fatalf("expected %s to exist: %v", tsPath, err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "base", "5", "16407")); err == nil {
		t.Fatal("base/5/16407 should NOT exist — a tablespace-scoped relation must not also land in base/")
	}
}

// TestManagerCloseRelationClosesHandleWithoutDeletingFile verifies
// CloseRelation forgets the cached *relFile handle but leaves the backing
// file on disk (unlike DropRelation, which does both). Used by ALTER
// TABLE/INDEX ... SET TABLESPACE's physical-relocation cleanup step, which
// removes the OLD file itself only once the catalog change is durable.
func TestManagerCloseRelationClosesHandleWithoutDeletingFile(t *testing.T) {
	dataDir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	rel := RelFileNode{DBOid: 5, RelOid: 16407, Fork: MainFork}
	page := make(Page, BlockSize)
	if err := InitPage(page); err != nil {
		t.Fatalf("InitPage: %v", err)
	}
	if _, err := mgr.Extend(rel, page); err != nil {
		t.Fatalf("Extend: %v", err)
	}

	mgr.CloseRelation(rel)

	path := filepath.Join(dataDir, "base", "5", "16407")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file to still exist after CloseRelation: %v", err)
	}
	// A subsequent read must re-open cleanly (handle was forgotten, not
	// corrupted).
	buf := make(Page, BlockSize)
	if err := mgr.ReadBlock(rel, 0, buf); err != nil {
		t.Fatalf("ReadBlock after CloseRelation: %v", err)
	}
}
