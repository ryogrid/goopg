package framework

import (
	"os"
	"path/filepath"
	"testing"
)

// TestOnDiskInventoryCSVValidates loads and validates the repo's on-disk
// consolidated inventory CSV. It is the mechanical guard against a malformed
// row (unquoted comma, bad status/pass_required vocabulary, an `excluded` row
// marked must-pass, a `port` row whose rationale names no TestPort func, or a
// duplicate id) — the classes that historically broke `gen-oracle-port-status`
// (see fix_plan.md "W-001 unquoted commas" incident). The nightly testport
// stage runs this package, so a broken CSV fails there.
func TestOnDiskInventoryCSVValidates(t *testing.T) {
	root := repoRootFromGoMod(t)
	path := filepath.Join(root, "docs", "test-port", "postgres-oracle-target-inventory.csv")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("inventory csv not present: %v", err)
	}
	rows, err := LoadStatusCSV(path)
	if err != nil {
		t.Fatalf("load on-disk inventory: %v", err)
	}
	if err := ValidateStatusRows(rows); err != nil {
		t.Fatalf("validate on-disk inventory: %v", err)
	}
}

// repoRootFromGoMod walks up from the current working directory to the module
// root (the directory containing go.mod). Go tests run with cwd = the package
// directory, so a fixed relative path would break when invoked from elsewhere.
func repoRootFromGoMod(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod found while walking up from cwd")
		}
		dir = parent
	}
}
