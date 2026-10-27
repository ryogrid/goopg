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

	// M0147-0021 promoted the PG currency, so a valid geometry always traces it.
	on := capture()
	if !strings.Contains(on, "currency=pg") || !strings.Contains(on, "pgouterpages=1") || !strings.Contains(on, "pginnerpages=6") ||
		!strings.Contains(on, "pgtuplebytes=80") || !strings.Contains(on, "pgbuckets=1024") {
		t.Fatalf("valid trace lacks the PG decision, pages or packed geometry: %q", on)
	}

	outer.Rel.Width = 0
	invalid := capture()
	if !strings.Contains(invalid, "currency=map") || !strings.Contains(invalid, "pgouterpages=-") {
		t.Fatalf("invalid outer width must trace cost fallback: %q", invalid)
	}
}
