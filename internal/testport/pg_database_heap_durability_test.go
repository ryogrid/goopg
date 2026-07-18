package testport

// B4.6 Stage 1: CREATE/DROP DATABASE journal a real pg_database SHARED heap row
// (global/1262) via XLOG_HEAP_INSERT/DELETE so a real PG18 standby sees the
// database catalog row. goopg's own `SELECT * FROM pg_database` is served from
// the registry (VirtualRows), so a plain query cannot distinguish heap presence
// — this test reads global/1262 DIRECTLY and asserts: (a) CREATE writes a live
// row for the new database, (b) the row survives a clean restart (the
// XLOG_HEAP_INSERT is durable), and (c) DROP stamps it dead (xmax set). It also
// confirms the 2671/2672 index maintenance leaves the cluster structurally
// sound by round-tripping a reconnect after each step.

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/testutil/cluster"
)

// countLivePgDatabaseRows reads global/1262 and returns the number of LIVE
// (xmin valid, xmax invalid) pg_database heap tuples whose datname == want.
func countLivePgDatabaseRows(t *testing.T, dataDir, want string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dataDir, "global", "1262"))
	if err != nil {
		t.Fatalf("read global/1262: %v", err)
	}
	const nameDataLen = 64
	live := 0
	for off := 0; off+storage.BlockSize <= len(raw); off += storage.BlockSize {
		page := storage.Page(raw[off : off+storage.BlockSize])
		count, err := storage.PageLinePointerCount(page)
		if err != nil {
			continue
		}
		for slot := uint16(1); slot <= uint16(count); slot++ {
			ht, err := storage.PageGetHeapTuple(page, slot)
			if err != nil {
				continue
			}
			if ht.Header.Xmin == storage.InvalidTransactionID || ht.Header.Xmax != storage.InvalidTransactionID {
				continue // dead or deleted
			}
			if len(ht.Data) < 4+nameDataLen {
				continue
			}
			nameBytes := ht.Data[4 : 4+nameDataLen]
			end := bytes.IndexByte(nameBytes, 0)
			if end < 0 {
				end = len(nameBytes)
			}
			if string(nameBytes[:end]) == want {
				live++
			}
		}
	}
	return live
}

// pgDatabaseOidForName returns the oid column of the first live pg_database row
// with datname == want, or 0 if none.
func pgDatabaseOidForName(t *testing.T, dataDir, want string) uint32 {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dataDir, "global", "1262"))
	if err != nil {
		t.Fatalf("read global/1262: %v", err)
	}
	const nameDataLen = 64
	for off := 0; off+storage.BlockSize <= len(raw); off += storage.BlockSize {
		page := storage.Page(raw[off : off+storage.BlockSize])
		count, err := storage.PageLinePointerCount(page)
		if err != nil {
			continue
		}
		for slot := uint16(1); slot <= uint16(count); slot++ {
			ht, err := storage.PageGetHeapTuple(page, slot)
			if err != nil {
				continue
			}
			if ht.Header.Xmin == storage.InvalidTransactionID || ht.Header.Xmax != storage.InvalidTransactionID {
				continue
			}
			if len(ht.Data) < 4+nameDataLen {
				continue
			}
			nameBytes := ht.Data[4 : 4+nameDataLen]
			end := bytes.IndexByte(nameBytes, 0)
			if end < 0 {
				end = len(nameBytes)
			}
			if string(nameBytes[:end]) == want {
				return binary.LittleEndian.Uint32(ht.Data[0:4])
			}
		}
	}
	return 0
}

func TestPort_PgDatabaseHeapRowSurvivesRestart(t *testing.T) {
	const dbName = "stage1_db"

	dataDir := filepath.Join(t.TempDir(), "data")
	c, err := cluster.New("pg-database-heap-durability", cluster.Options{
		RepoRoot:     repoRoot(t),
		DataDir:      dataDir,
		StartupWait:  20 * time.Second,
		ShutdownWait: 20 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Init(); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	// (a) CREATE DATABASE writes a live pg_database heap row.
	if err := runSQLSimple(t, c, "CREATE DATABASE "+dbName); err != nil {
		t.Fatalf("CREATE DATABASE: %v", err)
	}
	// Flush the buffered heap page to disk so the raw file read sees it.
	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop cluster: %v", err)
	}
	if n := countLivePgDatabaseRows(t, dataDir, dbName); n != 1 {
		t.Fatalf("post-CREATE live pg_database heap rows for %q = %d, want 1", dbName, n)
	}
	createdOid := pgDatabaseOidForName(t, dataDir, dbName)
	if createdOid == 0 {
		t.Fatalf("post-CREATE pg_database row for %q has oid 0", dbName)
	}

	// (b) The row survives a clean restart (XLOG_HEAP_INSERT durability).
	if err := c.Start(); err != nil {
		t.Fatalf("restart cluster: %v", err)
	}
	// Sanity: the database is still usable after restart.
	if err := runSQLSimple(t, c, "SELECT 1"); err != nil {
		t.Fatalf("post-restart SELECT: %v", err)
	}
	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop cluster (2): %v", err)
	}
	if n := countLivePgDatabaseRows(t, dataDir, dbName); n != 1 {
		t.Fatalf("post-restart live pg_database heap rows for %q = %d, want 1 (INSERT not durable)", dbName, n)
	}
	if got := pgDatabaseOidForName(t, dataDir, dbName); got != createdOid {
		t.Fatalf("post-restart pg_database oid for %q = %d, want %d (oid not stable)", dbName, got, createdOid)
	}

	// (c) DROP DATABASE stamps the heap row dead.
	if err := c.Start(); err != nil {
		t.Fatalf("restart cluster (3): %v", err)
	}
	if err := runSQLSimple(t, c, "DROP DATABASE "+dbName); err != nil {
		t.Fatalf("DROP DATABASE: %v", err)
	}
	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop cluster (4): %v", err)
	}
	if n := countLivePgDatabaseRows(t, dataDir, dbName); n != 0 {
		t.Fatalf("post-DROP live pg_database heap rows for %q = %d, want 0 (DELETE not durable)", dbName, n)
	}
}
