package initdb

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// TestSharedCatalogRejectedRowIsLogged pins M0146-0035's diagnostic: an
// online TPC-H clone came up without database and role `tpch` while their
// global/1262 / 1260 rows were on disk, and the reload dropped them without a
// word. A shared-catalog row with no deleter that the liveness filter rejects
// now logs its catalog, position, xmin and CLOG status, so the next
// occurrence names its cause. The control run (xmin committed) must stay
// silent: ordinary reloads print nothing.
func TestSharedCatalogRejectedRowIsLogged(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatal(err)
	}
	// CREATE DATABASE / ROLE / TABLESPACE are out of this package's reach
	// (the postmaster runs the first two; in-place tablespaces are not
	// supported), so give the first global/1262 row (template1) a normal
	// inserting xid by patching its t_xmin — the first four bytes of the
	// tuple header, at the offset its line pointer names.
	const userXmin = storage.TransactionID(100)
	f := filepath.Join(dir, "global", "1262")
	page, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	lp := binary.LittleEndian.Uint32(page[24:28])
	off := int(lp & 0x7fff)
	binary.LittleEndian.PutUint32(page[off:off+4], uint32(userXmin))
	if err := os.WriteFile(f, page, 0o600); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	rel := storage.RelFileNode{DBOid: 0, RelOid: catalog.PgDatabaseRelationOID, Fork: storage.MainFork}

	scan := func(status transam.TxnStatus) string {
		cdir := t.TempDir()
		clog, err := transam.OpenCLog(filepath.Join(cdir, "pg_xact"))
		if err != nil {
			t.Fatal(err)
		}
		if err := clog.EnablePGSLRUMirror(filepath.Join(cdir, "pg_xact_slru")); err != nil {
			t.Fatal(err)
		}
		if status == transam.TxnStatusAborted {
			err = clog.SetAborted(userXmin)
		} else {
			err = clog.SetCommitted(userXmin)
		}
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		prev := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
		defer slog.SetDefault(prev)
		if _, err := scanCatalogHeapRows(rt.StorageMgr, rel, clog, "pg_database",
			func(storage.HeapTuple, storage.ItemPointer) (any, bool, error) { return struct{}{}, false, nil }); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}

	got := scan(transam.TxnStatusAborted)
	if n := strings.Count(got, "row rejected by xmin status"); n != 1 {
		t.Fatalf("aborted xmin: want 1 rejection warning (template1), got %d:\n%s", n, got)
	}
	for _, want := range []string{"catalog=pg_database", fmt.Sprintf("xmin=%d", userXmin), "clog=aborted"} {
		if !strings.Contains(got, want) {
			t.Errorf("warning lacks %q:\n%s", want, got)
		}
	}
	if got := scan(transam.TxnStatusCommitted); got != "" {
		t.Errorf("committed xmin must reload silently, got:\n%s", got)
	}
}
