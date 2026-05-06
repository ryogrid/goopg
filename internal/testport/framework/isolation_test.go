package framework

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type mockIsolationExec struct {
	calls []string
}

func (m *mockIsolationExec) ExecuteStep(_ context.Context, session string, sql string) (string, error) {
	m.calls = append(m.calls, session+":"+strings.TrimSpace(sql))
	if strings.Contains(sql, "defer-me") {
		return "", ErrDeferred
	}
	return "ok", nil
}

func TestParseAndRunIsolationPermutation(t *testing.T) {
	spec := `
session "s1"
session "s2"
step "s1_begin" { BEGIN; }
step "s2_begin" { BEGIN; }
step "s1_probe" {
  SELECT 1;
}
permutation "s1_begin" "s2_begin" "s1_probe"
`
	path := filepath.Join(t.TempDir(), "demo.spec")
	if err := os.WriteFile(path, []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}

	parsed, err := ParseIsolationSpec(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Sessions) != 2 {
		t.Fatalf("sessions=%d want 2", len(parsed.Sessions))
	}
	if len(parsed.Permutations) != 1 {
		t.Fatalf("permutations=%d want 1", len(parsed.Permutations))
	}
	exec := &mockIsolationExec{}
	results, err := RunIsolationPermutation(context.Background(), parsed, 0, exec)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("results=%d want 3", len(results))
	}
	for i := range results {
		if results[i].Status != "port" {
			t.Fatalf("step %d status=%q want port", i, results[i].Status)
		}
	}
	wantCalls := []string{"s1:BEGIN;", "s2:BEGIN;", "s1:SELECT 1;"}
	if !reflect.DeepEqual(exec.calls, wantCalls) {
		t.Fatalf("calls=%v want %v", exec.calls, wantCalls)
	}
}

func TestDiscoverIsolationSpecs(t *testing.T) {
	repo := t.TempDir()
	mustWrite := func(rel string) {
		p := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("session \"s1\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("postgres/src/test/isolation/specs/a.spec")
	mustWrite("postgres/src/test/isolation/specs/b.spec")
	paths, err := DiscoverIsolationSpecs(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Fatalf("paths=%d want 2", len(paths))
	}
}
