package pgcluster

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSegvBacktraceSourceMatchesToolsCopy pins byte-equality between the
// embedded package copy (segv_backtrace_src.txt) and the canonical
// tools/segv_backtrace/segv_backtrace.c. The two copies must not drift
// — the embedded copy is what actually compiles, and the tools/ copy is
// what humans hand-compile when verifying the shim by itself.
func TestSegvBacktraceSourceMatchesToolsCopy(t *testing.T) {
	repoRoot := findRepoRoot(t)
	canonical, err := os.ReadFile(filepath.Join(repoRoot, "tools", "segv_backtrace", "segv_backtrace.c"))
	if err != nil {
		t.Fatalf("read canonical source: %v", err)
	}
	if string(canonical) != string(segvBacktraceSource) {
		t.Fatalf("embedded segv_backtrace_src.txt drift from tools/segv_backtrace/segv_backtrace.c")
	}
}

// TestSegvBacktraceLDPreloadGateOff confirms the helper is a strict no-op
// when GOOPG_TEST_SEGV_BACKTRACE is not set to "1" — production builds
// must not LD_PRELOAD anything.
func TestSegvBacktraceLDPreloadGateOff(t *testing.T) {
	t.Setenv(segvBacktraceEnvName, "")
	soPath, ok, err := segvBacktraceLDPreload()
	if err != nil {
		t.Fatalf("unexpected error with gate off: %v", err)
	}
	if ok || soPath != "" {
		t.Fatalf("gate off should return ok=false soPath=\"\"; got ok=%v soPath=%q", ok, soPath)
	}
}

// TestEnsureSegvBacktraceSOBuilds compiles the .so via the embedded
// source path, then runs a small SIGSEGV-triggering helper under
// LD_PRELOAD and asserts the resulting stderr carries our
// "[GOOPG_SEGV_BACKTRACE]" marker. This is the full end-to-end shim
// verification — same path pgcluster.Start uses, minus PG.
func TestEnsureSegvBacktraceSOBuilds(t *testing.T) {
	if _, err := exec.LookPath("cc"); err != nil {
		t.Skip("cc not available; skipping shim build smoke test")
	}
	soPath, err := ensureSegvBacktraceSO()
	if err != nil {
		t.Fatalf("ensureSegvBacktraceSO: %v", err)
	}
	if _, err := os.Stat(soPath); err != nil {
		t.Fatalf("compiled .so missing: %v", err)
	}

	dir := t.TempDir()
	csrc := filepath.Join(dir, "deref.c")
	if err := os.WriteFile(csrc, []byte("int main(){int *p=0;*p=1;return 0;}\n"), 0o644); err != nil {
		t.Fatalf("write helper source: %v", err)
	}
	bin := filepath.Join(dir, "deref")
	if out, err := exec.Command("cc", "-O0", "-o", bin, csrc).CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v\n%s", err, out)
	}
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "LD_PRELOAD="+soPath)
	out, _ := cmd.CombinedOutput()
	if !strings.Contains(string(out), "[GOOPG_SEGV_BACKTRACE]") {
		t.Fatalf("shim did not fire — output lacked marker:\n%s", out)
	}
	if !strings.Contains(string(out), "end of backtrace") {
		t.Fatalf("shim did not complete — output lacked footer:\n%s", out)
	}
}

// TestAppendLDPreloadMergesExisting verifies that an existing LD_PRELOAD
// entry is preserved (space-joined) — important because some PG test
// harnesses or wrappers may already have set one.
func TestAppendLDPreloadMergesExisting(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want string
	}{
		{"absent", []string{"PATH=/usr/bin"}, "LD_PRELOAD=/x.so"},
		{"empty-existing", []string{"LD_PRELOAD="}, "LD_PRELOAD=/x.so"},
		{"merge", []string{"LD_PRELOAD=/y.so"}, "LD_PRELOAD=/y.so /x.so"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := appendLDPreload(append([]string(nil), tc.in...), "/x.so")
			found := ""
			for _, kv := range got {
				if strings.HasPrefix(kv, "LD_PRELOAD=") {
					found = kv
				}
			}
			if found != tc.want {
				t.Fatalf("appendLDPreload(%v): got %q want %q", tc.in, found, tc.want)
			}
		})
	}
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	d := wd
	for {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			t.Fatalf("repo root (go.mod) not found from %s", wd)
		}
		d = parent
	}
}
