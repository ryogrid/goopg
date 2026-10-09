package catalog

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestBuiltinProcFlags pins representative members of both generated sets
// and a non-member of each.
func TestBuiltinProcFlags(t *testing.T) {
	for _, n := range []string{"generate_series", "unnest", "regexp_matches", "JSON_EACH"} {
		if !BuiltinProcReturnsSet(n) {
			t.Errorf("%s: want set-returning", n)
		}
	}
	for _, n := range []string{"substr", "upper", "random", "now"} {
		if BuiltinProcReturnsSet(n) {
			t.Errorf("%s: want not set-returning", n)
		}
	}
	for _, n := range []string{"random", "nextval", "clock_timestamp", "setseed"} {
		if !BuiltinProcIsVolatile(n) {
			t.Errorf("%s: want volatile", n)
		}
	}
	for _, n := range []string{"substr", "now", "upper", "abs"} {
		if BuiltinProcIsVolatile(n) {
			t.Errorf("%s: want not volatile", n)
		}
		if !IsBuiltinProcName(n) {
			t.Errorf("%s: want a built-in", n)
		}
	}
	if IsBuiltinProcName("no_such_function_xyz") {
		t.Error("no_such_function_xyz: want not a built-in")
	}
}

// TestBuiltinProcFlagsGeneratedFileIsCurrent re-runs the generator
// (cmd/gen-pg-proc-data/main.go -flags) and compares its output with the committed
// file, so drift from pg_proc.dat — or a hand edit — fails here. Skipped
// without the postgres/ submodule.
func TestBuiltinProcFlagsGeneratedFileIsCurrent(t *testing.T) {
	_, self, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(self), "..", "..")
	if _, err := os.Stat(filepath.Join(root, "postgres/src/include/catalog/pg_proc.dat")); err != nil {
		t.Skip("postgres/ source tree not present")
	}
	cmd := exec.Command("go", "run", "cmd/gen-pg-proc-data/main.go", "-flags")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("generator failed: %v", err)
	}
	want, err := os.ReadFile(filepath.Join(root, "internal/catalog/builtin_proc_flags_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(want) {
		t.Fatal("builtin_proc_flags_gen.go is stale: go run cmd/gen-pg-proc-data/main.go -flags > internal/catalog/builtin_proc_flags_gen.go")
	}
}
