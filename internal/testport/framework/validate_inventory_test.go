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

// TestValidateMismatchCounts pins the optional mismatch_lines/expected_lines
// rule: blank or non-negative integers, both-or-neither, regress-sql only,
// mismatch <= expected.
func TestValidateMismatchCounts(t *testing.T) {
	base := StatusRow{
		ID: "r1", SuiteID: "regress-sql", Kind: "sql",
		ItemPath: "postgres/src/test/regress/sql/x.sql",
		Status:   "failed", PassRequired: "no",
	}
	cases := []struct {
		name     string
		suite    string
		mismatch string
		expected string
		ok       bool
	}{
		{"both blank", "regress-sql", "", "", true},
		{"valid pair", "regress-sql", "3", "120", true},
		{"zero mismatch", "regress-sql", "0", "120", true},
		{"equal", "regress-sql", "7", "7", true},
		{"mismatch only", "regress-sql", "3", "", false},
		{"expected only", "regress-sql", "", "120", false},
		{"negative", "regress-sql", "-1", "120", false},
		{"non-integer", "regress-sql", "3.5", "120", false},
		{"mismatch exceeds", "regress-sql", "121", "120", false},
		{"non-regress row", "isolation", "3", "120", false},
	}
	for _, tc := range cases {
		r := base
		r.SuiteID = tc.suite
		r.MismatchLines = tc.mismatch
		r.ExpectedLines = tc.expected
		err := ValidateStatusRows([]StatusRow{r})
		if tc.ok && err != nil {
			t.Errorf("%s: unexpected error: %v", tc.name, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s: expected an error", tc.name)
		}
	}
}

// TestLoadStatusCSVOptionalMismatchColumns checks the optional columns are
// read when present and blank when absent.
func TestLoadStatusCSVOptionalMismatchColumns(t *testing.T) {
	dir := t.TempDir()
	with := filepath.Join(dir, "with.csv")
	if err := os.WriteFile(with, []byte("id,suite_id,kind,item_path,status,pass_required,deferred_to,rationale,mismatch_lines,expected_lines\n"+
		"r1,regress-sql,sql,postgres/src/test/regress/sql/x.sql,failed,no,,why,4,40\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rows, err := LoadStatusCSV(with)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].MismatchLines != "4" || rows[0].ExpectedLines != "40" {
		t.Fatalf("got %q/%q, want 4/40", rows[0].MismatchLines, rows[0].ExpectedLines)
	}
	without := filepath.Join(dir, "without.csv")
	if err := os.WriteFile(without, []byte("id,suite_id,kind,item_path,status,pass_required,deferred_to,rationale\n"+
		"r1,regress-sql,sql,postgres/src/test/regress/sql/x.sql,failed,no,,why\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rows, err = LoadStatusCSV(without)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].MismatchLines != "" || rows[0].ExpectedLines != "" {
		t.Fatalf("got %q/%q, want blank", rows[0].MismatchLines, rows[0].ExpectedLines)
	}
}
