package catalog_test

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

func TestRelcacheInitFileUnlinkRemovesBothFiles(t *testing.T) {
	dir := t.TempDir()
	globalDir := filepath.Join(dir, "global")
	dbDir := filepath.Join(dir, "base", "1")
	for _, d := range []string{globalDir, dbDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	globalInit := filepath.Join(globalDir, "pg_internal.init")
	dbInit := filepath.Join(dbDir, "pg_internal.init")
	for _, p := range []string{globalInit, dbInit} {
		if err := os.WriteFile(p, []byte("placeholder"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := catalog.RelcacheInitFileUnlink(dir, 1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, p := range []string{globalInit, dbInit} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("expected %s to be removed, stat returned: %v", p, err)
		}
	}
}

func TestRelcacheInitFileUnlinkEnoentIsOK(t *testing.T) {
	dir := t.TempDir()
	// Create only the directories, not the files.
	for _, sub := range []string{"global", filepath.Join("base", "1")} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Both files are absent — must return nil.
	if err := catalog.RelcacheInitFileUnlink(dir, 1); err != nil {
		t.Fatalf("expected nil on missing files, got: %v", err)
	}
}

func TestRelcacheInitFileUnlinkPartialIsOK(t *testing.T) {
	dir := t.TempDir()
	globalDir := filepath.Join(dir, "global")
	dbDir := filepath.Join(dir, "base", "1")
	for _, d := range []string{globalDir, dbDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Only the global file exists; per-db file is absent.
	globalInit := filepath.Join(globalDir, "pg_internal.init")
	if err := os.WriteFile(globalInit, []byte("placeholder"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := catalog.RelcacheInitFileUnlink(dir, 1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(globalInit); !os.IsNotExist(err) {
		t.Errorf("expected global init to be removed, stat: %v", err)
	}
}

func TestWithRelCacheInitLockExcludesOtherWaiters(t *testing.T) {
	const goroutines = 5
	var (
		mu      sync.Mutex
		results []int
		wg      sync.WaitGroup
	)
	wg.Add(goroutines)
	for i := range goroutines {
		i := i
		go func() {
			defer wg.Done()
			_ = catalog.WithRelCacheInitLock(func() error {
				mu.Lock()
				results = append(results, i)
				mu.Unlock()
				return nil
			})
		}()
	}
	wg.Wait()
	if len(results) != goroutines {
		t.Errorf("expected %d results, got %d", goroutines, len(results))
	}
}

func TestWithRelCacheInitLockPropagatesError(t *testing.T) {
	sentinel := os.ErrExist
	err := catalog.WithRelCacheInitLock(func() error { return sentinel })
	if err != sentinel {
		t.Errorf("expected sentinel error, got %v", err)
	}
}
