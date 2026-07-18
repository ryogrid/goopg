package initdb

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/control"
	"github.com/goopg/goopg/internal/storage"
)

// TestBuildPgControlDataChecksumVersion verifies buildPgControl writes
// data_checksum_version (offset 252) as 1 when checksums are requested and
// 0 otherwise — the field pg_controldata reports as "Data page checksum
// version".
func TestBuildPgControlDataChecksumVersion(t *testing.T) {
	for _, tc := range []struct {
		name     string
		enabled  bool
		wantVers uint32
	}{
		{"disabled", false, 0},
		{"enabled", true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf := buildPgControl(0xDEADBEEF, time.Now(), nil, tc.enabled)
			got := binary.LittleEndian.Uint32(buf[252:])
			if got != tc.wantVers {
				t.Fatalf("data_checksum_version = %d, want %d", got, tc.wantVers)
			}
		})
	}
}

// TestInitDataChecksumsBootstrapsVerifiablePages is the end-to-end boot test
// for initdb --data-checksums (M0102-0010). It inits a checksummed cluster and
// asserts the on-disk result satisfies the data_checksum_version=1 promise:
// every relation page carries a valid pd_checksum under the same off/BlockSize
// block-number convention the runtime smgr uses on read. A relation file the
// stamp pass missed — an unbootable-cluster bug — fails this test.
func TestInitDataChecksumsBootstrapsVerifiablePages(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cluster")
	if err := Init(Options{DataDir: dir, DataChecksums: true, NoSync: true}); err != nil {
		t.Fatalf("Init with DataChecksums=true: %v", err)
	}

	// pg_control must report checksum version 1.
	ctrl, err := control.ReadControlFile(dir)
	if err != nil {
		t.Fatalf("ReadControlFile: %v", err)
	}
	if ctrl.DataChecksumVersion != 1 {
		t.Fatalf("data_checksum_version = %d, want 1", ctrl.DataChecksumVersion)
	}

	// Every relation page under base/ and global/ must verify. Block number
	// is the block's offset within its file (off/BlockSize), exactly as the
	// smgr relFile derives it on read.
	relFiles := collectRelationFiles(t, dir)
	if len(relFiles) == 0 {
		t.Fatal("no relation files found under base/ and global/")
	}
	pagesChecked, populatedPages := 0, 0
	for _, path := range relFiles {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %q: %v", path, err)
		}
		for i := 0; i < len(raw)/storage.BlockSize; i++ {
			block := raw[i*storage.BlockSize : (i+1)*storage.BlockSize]
			if !storage.VerifyPage(block, storage.BlockNumber(i)) {
				t.Errorf("page verification failed: block %d of %q", i, path)
			}
			pagesChecked++
			if !storage.IsNew(block) {
				populatedPages++
				if storage.MustHeader(block).Checksum() == 0 {
					t.Errorf("populated block %d of %q has zero pd_checksum (not stamped)", i, path)
				}
			}
		}
	}
	if pagesChecked == 0 || populatedPages == 0 {
		t.Fatalf("checked %d pages (%d populated); expected populated catalog pages", pagesChecked, populatedPages)
	}

	// Read block 0 of the core local catalogs through a checksummed Manager —
	// the production read path. A block-numbering mismatch between the stamp
	// pass and the smgr read-verify would surface here as a *ChecksumError.
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir, ChecksumsEnabled: true})
	defer mgr.Close()
	for _, oid := range []uint32{catalog.TypeRelationId, catalog.RelationRelationId, catalog.AttributeRelationId} {
		buf := make([]byte, storage.BlockSize)
		rel := storage.RelFileNode{DBOid: catalog.DefaultDBOid, RelOid: oid, Fork: storage.MainFork}
		if err := mgr.ReadBlock(rel, 0, buf); err != nil {
			t.Errorf("ReadBlock catalog OID %d through checksummed Manager: %v", oid, err)
		}
	}
}

// collectRelationFiles returns the paths of all relation data files under
// <dir>/base/<db>/ and <dir>/global/, using the same relfilenode-name filter
// (relFileNamePattern) that stampClusterChecksums applies.
func collectRelationFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	dirs := []string{filepath.Join(dir, "global")}
	baseDir := filepath.Join(dir, "base")
	dbDirs, err := os.ReadDir(baseDir)
	if err != nil {
		t.Fatalf("read base dir: %v", err)
	}
	for _, d := range dbDirs {
		if d.IsDir() {
			dirs = append(dirs, filepath.Join(baseDir, d.Name()))
		}
	}
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatalf("read %q: %v", d, err)
		}
		for _, e := range entries {
			if !e.IsDir() && relFileNamePattern.MatchString(e.Name()) {
				out = append(out, filepath.Join(d, e.Name()))
			}
		}
	}
	return out
}
