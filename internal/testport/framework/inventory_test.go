package framework

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildSuiteInventory(t *testing.T) {
	repo := t.TempDir()
	mustWriteInventory(t, repo, "postgres/src/test/regress/sql/a.sql", "SELECT 1;")
	mustWriteInventory(t, repo, "postgres/src/test/regress/expected/a.out", "1")
	mustWriteInventory(t, repo, "postgres/src/test/isolation/specs/a.spec", "session \"s1\"")
	mustWriteInventory(t, repo, "postgres/src/test/recovery/t/a.pl", "1;")
	mustWriteInventory(t, repo, "postgres/src/test/subscription/t/a.pl", "1;")
	mustWriteInventory(t, repo, "postgres/src/bin/pg_ctl/t/a.pl", "1;")
	mustWriteInventory(t, repo, "postgres/src/test/modules/moda/sql/a.sql", "SELECT 1;")
	mustWriteInventory(t, repo, "postgres/contrib/abc/sql/a.sql", "SELECT 1;")

	rows, err := BuildSuiteInventory(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatalf("inventory empty")
	}
	got := map[string]int{}
	for _, r := range rows {
		got[r.SuiteID] = r.Count
	}
	if got["regress-sql"] != 1 {
		t.Fatalf("regress-sql=%d want 1", got["regress-sql"])
	}
	if got["isolation-specs"] != 1 {
		t.Fatalf("isolation-specs=%d want 1", got["isolation-specs"])
	}
	if got["client-tools-tap"] != 1 {
		t.Fatalf("client-tools-tap=%d want 1", got["client-tools-tap"])
	}
}

func mustWriteInventory(t *testing.T, repo, rel, content string) {
	t.Helper()
	p := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
