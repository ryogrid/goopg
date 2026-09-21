package optimizer

// M0145-0007 slice 1 — the lowering-locality invariant.
//
// M0145-0001 §6 lists "`translateToLayout` as separate calls" among the
// mechanisms M0145-0007 retires. The recon measured that it is already
// lowering-local: every call site sits in a `createplan*.go` file. So there is
// nothing to retire — but "nothing to retire" is only true for as long as it
// stays true, and the claim is exactly the kind that rots silently. This test
// turns the measurement into an enforced invariant.
//
// The rule it enforces is the single-lowering contract itself (0001 §5.1):
// output-layout-dependent translation happens INSIDE the lowering walk,
// against the `outputLayout` each child returns. A `translateToLayout` call
// anywhere else is a second establishment of coordinates — the class that
// produced the splice/re-resolution family this task exists to remove.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestTranslateToLayoutIsLoweringLocal(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	var offenders []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		// The lowering walk's own files, plus the file that DEFINES the
		// helper (`createplanjoin.go`) — all `createplan*.go` by convention.
		if strings.HasPrefix(name, "createplan") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			code, _, _ := strings.Cut(line, "//")
			if strings.Contains(code, "translateToLayout(") {
				offenders = append(offenders, name+":"+strconv.Itoa(i+1))
			}
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("translateToLayout called outside the lowering walk at %v.\n"+
			"The single-lowering contract (M0145-0001 §5.1) says output-layout-dependent\n"+
			"translation happens inside createPlanNode's walk, against the outputLayout each\n"+
			"child returns. A call elsewhere establishes coordinates a second time, which is\n"+
			"the defect class M0145-0007 exists to remove — move the translation into the\n"+
			"lowering arm rather than relaxing this test.", offenders)
	}
}
