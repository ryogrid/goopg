package optimizer

import (
	"strings"
	"testing"
)

func TestPGHashTupleSpillTraceMatchesCostFallbackAndDecision(t *testing.T) {
	cp := defaultCostParams()
	cp.workMem = 64 << 10
	outer := &Path{Rel: &RelOptInfo{Relids: 1, Width: 48, NCols: 40}, Rows: 110}
	inner := &Path{Rel: &RelOptInfo{Relids: 2, Width: 48, NCols: 40}, Rows: 650}
	capture := func() string {
		lines := captureTrace(t, func() { tracePGHashTupleGeometry(outer, inner, cp) })
		if len(lines) != 1 {
			t.Fatalf("trace lines = %v, want one", lines)
		}
		return lines[0]
	}

	restore := setPGHashTupleSpillCostForTest(false)
	off := capture()
	restore()
	if !strings.Contains(off, "currency=map") || !strings.Contains(off, "pgtuplebytes=80") || !strings.Contains(off, "pgbuckets=1024") {
		t.Fatalf("switch-off trace lacks map fallback or packed geometry: %q", off)
	}

	restore = setPGHashTupleSpillCostForTest(true)
	on := capture()
	restore()
	if !strings.Contains(on, "currency=pg") || !strings.Contains(on, "pgouterpages=1") || !strings.Contains(on, "pginnerpages=6") {
		t.Fatalf("valid switch-on trace lacks actual PG decision/pages: %q", on)
	}

	outer.Rel.Width = 0
	restore = setPGHashTupleSpillCostForTest(true)
	invalid := capture()
	restore()
	if !strings.Contains(invalid, "currency=map") || !strings.Contains(invalid, "pgouterpages=-") {
		t.Fatalf("invalid outer width must trace cost fallback: %q", invalid)
	}
}
