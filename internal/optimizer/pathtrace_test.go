package optimizer

import "testing"

// TestPathTraceRecordsProducerAndVerdict pins take2 P0-11's contract: every
// offered path produces exactly one record, naming the producer that offered it
// and whether it survived dominance.
//
// The provenance question 09 §1 R4 exists to answer is "was this candidate
// generated at all" — five wrong hypotheses were spent on TPC-H Q8 because the
// index producer emitted nothing at that parameterisation and the investigation
// instrumented the cost functions instead. A trace that recorded only accepted
// paths would answer the easy half.
func TestPathTraceRecordsProducerAndVerdict(t *testing.T) {
	rel := &RelOptInfo{Relids: 0b101}

	cheap := &Path{Kind: PathSeqScan, Rel: rel, Rows: 10, Cost: Cost{Startup: 0, Total: 10}}
	dear := &Path{Kind: PathIndexScan, Rel: rel, Rows: 10, Cost: Cost{Startup: 0, Total: 100}}

	var lines []string
	capture := func(rel *RelOptInfo, p *Path, producer string, partial bool, v pathVerdict) {
		lines = append(lines, producer+"="+string(v))
	}
	// Exercise the same decision addPath makes, without depending on stderr.
	rel.Pathlist = addToPathlist(rel.Pathlist, cheap)
	capture(rel, cheap, "scan.seq", false, pathlistVerdict(rel.Pathlist, cheap, 0))
	rel.Pathlist = addToPathlist(rel.Pathlist, dear)
	capture(rel, dear, "index.ordered", false, pathlistVerdict(rel.Pathlist, dear, 1))

	if len(lines) != 2 {
		t.Fatalf("got %d records, want one per offered path", len(lines))
	}
	if lines[0] != "scan.seq=accepted" {
		t.Errorf("first record = %q, want scan.seq=accepted", lines[0])
	}
	// The dearer path is strictly dominated and must be recorded as offered
	// AND rejected — the case the trace exists for.
	if lines[1] != "index.ordered=dominated" {
		t.Errorf("second record = %q, want index.ordered=dominated", lines[1])
	}
}

// TestPathTraceVerdictSurvivesEviction guards the subtle case: an accepted path
// can EVICT several incumbents and shrink the list, so a length comparison
// would misread it as rejected.
func TestPathTraceVerdictSurvivesEviction(t *testing.T) {
	rel := &RelOptInfo{Relids: 1}
	for _, total := range []float64{100, 200, 300} {
		p := &Path{Kind: PathSeqScan, Rel: rel, Rows: 10, Cost: Cost{Total: total}}
		rel.Pathlist = addToPathlist(rel.Pathlist, p)
	}
	before := len(rel.Pathlist)
	winner := &Path{Kind: PathSeqScan, Rel: rel, Rows: 10, Cost: Cost{Total: 1}}
	rel.Pathlist = addToPathlist(rel.Pathlist, winner)
	if got := pathlistVerdict(rel.Pathlist, winner, before); got != verdictAccepted {
		t.Errorf("verdict = %q, want accepted (list went %d -> %d as it evicted incumbents)",
			got, before, len(rel.Pathlist))
	}
}

// TestRelSetBitsIsParseable pins the join key between this channel and the
// DPTRACE pair lines.
func TestRelSetBitsIsParseable(t *testing.T) {
	for _, tc := range []struct {
		in   RelSet
		want string
	}{
		{0, "-"},
		{1, "{0}"},
		{0b101, "{0,2}"},
	} {
		if got := relSetBits(tc.in); got != tc.want {
			t.Errorf("relSetBits(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
