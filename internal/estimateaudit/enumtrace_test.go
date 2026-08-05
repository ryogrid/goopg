package estimateaudit

// M0127-P5.9-l-ii tests. The subject is ADJUDICATION: given a spine diff's
// bushy partitions and a server log, does the channel separate the two readings
// clause 6 has to choose between — "enumerated and lost on cost" (09 §4 admits)
// from "never enumerated" (09 §4 fails)?
//
// The properties pinned:
//
//  1. the wire format round-trips: the key the planner writes is byte-identical
//     to `SpineJoin.PairKey`, which is what lets a plan-side partition be looked
//     up at all. A drift here would silently turn every candidate into
//     NOT-ENUMERATED — a false clause-6 failure;
//  2. the four negative verdicts are distinguished (declined by the
//     connectivity gate / one side never built / neither, both sides present /
//     no traced problem at all), because each names a different gap;
//  3. controls are derived from goopg's OWN bushy pairings and a failing
//     control reads as a HARNESS fault, not a search fault — the guard that
//     stops an unharvested log from being reported as a planner defect;
//  4. a log with no trace in it says so rather than reporting 0/0 as a pass.

import (
	"strings"
	"testing"
)

// traceLog is a server log with the engine's ordinary output interleaved, since
// that is what an arm run actually produces.
const traceLog = `2026-08-06 12:00:00 LOG:  database system is ready
DPTRACE problem nrels=4 rels=customer,lineitem,orders,supplier
2026-08-06 12:00:01 LOG:  statement: explain analyze select ...
DPTRACE pair phase=1 lev=2 created=1 pair={customer} | {orders} outer={customer} inner={orders}
DPTRACE pair phase=1 lev=2 created=1 pair={lineitem} | {supplier} outer={lineitem} inner={supplier}
DPTRACE pair phase=2 lev=4 created=1 pair={customer+orders} | {lineitem+supplier} outer={customer+orders} inner={lineitem+supplier}
DPTRACE decline phase=1 lev=2 reason=no-join-clause pair={customer} | {supplier}
DPTRACE end top={customer+lineitem+orders+supplier} pairs=3 declined=1 status=ok
DPTRACE problem nrels=2 rels=nation,region
DPTRACE pair phase=1 lev=2 created=1 pair={nation} | {region} outer={nation} inner={region}
DPTRACE end top={nation+region} pairs=1 declined=0 status=ok
`

func parseTestLog(t *testing.T, s string) EnumTrace {
	t.Helper()
	tr := ParseEnumTrace(strings.NewReader(s))
	if tr.Malformed != 0 {
		t.Fatalf("%d malformed lines in a hand-written log", tr.Malformed)
	}
	return tr
}

// TestParseEnumTraceBlocks: the framing, the key reassembly (a pair key
// contains spaces), and the derived `Built` set.
func TestParseEnumTraceBlocks(t *testing.T) {
	tr := parseTestLog(t, traceLog)
	if len(tr.Problems) != 2 {
		t.Fatalf("got %d problems, want 2", len(tr.Problems))
	}
	p := tr.Problems[0]
	if got, want := strings.Join(p.Rels, ","), "customer,lineitem,orders,supplier"; got != want {
		t.Errorf("rels = %q, want %q", got, want)
	}
	if p.Top != "{customer+lineitem+orders+supplier}" || p.Status != "ok" {
		t.Errorf("top=%q status=%q", p.Top, p.Status)
	}
	e, ok := p.Offered["{customer+orders} | {lineitem+supplier}"]
	if !ok {
		t.Fatalf("bushy pair not parsed; offered=%v", p.Offered)
	}
	if e.Phase != 2 || e.Level != 4 || !e.Created {
		t.Errorf("bushy pair parsed as %+v", e)
	}
	if d, ok := p.Declined["{customer} | {supplier}"]; !ok || d.Reason != "no-join-clause" {
		t.Errorf("decline parsed as %+v (ok=%v)", d, ok)
	}
	// Built covers the singletons the header declared, the sides of every
	// offered pair, and their unions.
	for _, want := range []string{"{customer}", "{customer+orders}", "{lineitem+supplier}",
		"{customer+lineitem+orders+supplier}"} {
		if !p.Built[want] {
			t.Errorf("Built is missing %s", want)
		}
	}
}

// TestEnumTraceKeyMatchesSpinePairKey is property (1) — the contract between
// the two packages, asserted where both halves are in scope. `SpineJoin` is the
// plan side; the string on the right is what `searchTrace.pairKey` writes.
func TestEnumTraceKeyMatchesSpinePairKey(t *testing.T) {
	j := SpineJoin{
		Rels:   []string{"customer", "lineitem", "orders", "supplier"},
		Inputs: [][]string{{"lineitem", "supplier"}, {"customer", "orders"}},
		Bushy:  true,
	}
	tr := parseTestLog(t, traceLog)
	if _, ok := tr.Problems[0].Offered[j.PairKey()]; !ok {
		t.Fatalf("SpineJoin.PairKey() = %q does not match any traced pair key %v",
			j.PairKey(), tr.Problems[0].Offered)
	}
}

// TestAdjudicateVerdicts is property (2): every distinct answer the channel can
// give, on one log, so a collapse of two of them into one is visible.
func TestAdjudicateVerdicts(t *testing.T) {
	tr := parseTestLog(t, traceLog)
	checks := []EnumCheck{
		{Query: "Qoffered", Kind: "candidate", Key: "{customer+orders} | {lineitem+supplier}"},
		{Query: "Qdeclined", Kind: "candidate", Key: "{customer} | {supplier}"},
		{Query: "Qunbuilt", Kind: "candidate", Key: "{customer+supplier} | {lineitem+orders}"},
		{Query: "Qmissing", Kind: "candidate", Key: "{customer+orders} | {lineitem}"},
		{Query: "Qforeign", Kind: "candidate", Key: "{part} | {partsupp}"},
	}
	want := []EnumVerdict{EnumOffered, EnumDeclined, EnumUnbuiltSide, EnumMissing, EnumNoProblem}
	got := tr.Adjudicate(checks)
	for i := range checks {
		if got[i].Verdict != want[i] {
			t.Errorf("%s: verdict %s (%s), want %s",
				checks[i].Query, got[i].Verdict, got[i].Detail, want[i])
		}
	}
	if !got[0].Passed() {
		t.Error("an OFFERED candidate must pass clause 6 — the divergence is then cost/stats")
	}
	if got[1].Passed() || got[3].Passed() {
		t.Error("a candidate the search never offered must not pass clause 6")
	}
}

// TestEnumChecksDerivesCandidatesAndControls is property (3)'s first half: the
// control set is goopg's own bushy pairings (09 §3.11's Q20), derived rather
// than hard-coded, and an ambiguous PG-only pairing is not adjudicated at all.
func TestEnumChecksDerivesCandidatesAndControls(t *testing.T) {
	bushyRef := &SpineJoin{Query: "Q7", Bushy: true,
		Inputs: [][]string{{"customer", "lineitem", "n2", "orders"}, {"n1", "supplier"}}}
	bushyBoth := &SpineJoin{Query: "Q20", Bushy: true,
		Inputs: [][]string{{"nation", "supplier"}, {"lineitem", "part", "partsupp"}}}
	rows := []SpineRow{
		{Query: "Q7", Status: SpineRefOnly, Ref: bushyRef},
		{Query: "Q20", Status: SpineMatched, Goopg: bushyBoth, Ref: bushyBoth},
		{Query: "Q1", Status: SpineGoopgOnly, Goopg: &SpineJoin{Query: "Q1"}}, // left-deep: not adjudicated
		{Query: "Q8", Status: SpineRefOnly, Ref: &SpineJoin{Query: "Q8", Bushy: true, Ambiguous: true,
			Inputs: [][]string{{"lineitem", "orders", "part"}, {"customer", "n1", "region"}}}},
	}
	checks := EnumChecks(rows)
	if len(checks) != 2 {
		t.Fatalf("got %d checks, want 2 (one candidate, one control): %+v", len(checks), checks)
	}
	if checks[0].Kind != "candidate" || checks[0].Query != "Q7" {
		t.Errorf("first check = %+v, want Q7 candidate", checks[0])
	}
	if checks[1].Kind != "control" || checks[1].Query != "Q20" {
		t.Errorf("second check = %+v, want Q20 control", checks[1])
	}
	if checks[1].Key != bushyBoth.PairKey() {
		t.Errorf("control key %q, want %q", checks[1].Key, bushyBoth.PairKey())
	}
}

// TestRenderEnumVerdicts is property (3)'s second half and (4): what the
// artifact SAYS, which is the only part a later loop reads.
func TestRenderEnumVerdicts(t *testing.T) {
	tr := parseTestLog(t, traceLog)

	// A control the trace does not know about indicts the harness, and no
	// candidate verdict from that run is admissible.
	broken := tr.Adjudicate([]EnumCheck{
		{Query: "Q20", Kind: "control", Key: "{part} | {partsupp}"},
		{Query: "Q7", Kind: "candidate", Key: "{customer+orders} | {lineitem}"},
	})
	out := RenderEnum(tr, broken)
	if !strings.Contains(out, "HARNESS FAULT") {
		t.Errorf("a failing control did not indict the harness:\n%s", out)
	}
	if !strings.Contains(out, "enum_controls=0/1") {
		t.Errorf("ratchet line wrong:\n%s", out)
	}

	// Every candidate offered ⇒ clause 6 passes.
	good := tr.Adjudicate([]EnumCheck{
		{Query: "Q20", Kind: "control", Key: "{customer+orders} | {lineitem+supplier}"},
		{Query: "Q7", Kind: "candidate", Key: "{customer+orders} | {lineitem+supplier}"},
	})
	out = RenderEnum(tr, good)
	if !strings.Contains(out, "Clause 6 passes") {
		t.Errorf("all-offered did not pass clause 6:\n%s", out)
	}
	if !strings.Contains(out, "enum_controls=1/1 enum_candidates_offered=1/1") {
		t.Errorf("ratchet line wrong:\n%s", out)
	}

	// A candidate that was never enumerated ⇒ clause 6 fails.
	bad := tr.Adjudicate([]EnumCheck{
		{Query: "Q20", Kind: "control", Key: "{customer+orders} | {lineitem+supplier}"},
		{Query: "Q7", Kind: "candidate", Key: "{customer+orders} | {lineitem}"},
	})
	if out = RenderEnum(tr, bad); !strings.Contains(out, "Clause 6 fails") {
		t.Errorf("an unenumerated candidate did not fail clause 6:\n%s", out)
	}

	// An empty log is not a pass.
	empty := ParseEnumTrace(strings.NewReader("2026-08-06 LOG:  nothing to see\n"))
	out = RenderEnum(empty, nil)
	if !strings.Contains(out, "NO TRACE HARVESTED") {
		t.Errorf("an empty log did not report itself:\n%s", out)
	}
}

// TestParseEnumTraceTruncatedBlock: a crash mid-search must keep its evidence,
// flagged as partial rather than dropped or mistaken for a complete search.
func TestParseEnumTraceTruncatedBlock(t *testing.T) {
	tr := ParseEnumTrace(strings.NewReader(
		"DPTRACE problem nrels=2 rels=a,b\n" +
			"DPTRACE pair phase=1 lev=2 created=1 pair={a} | {b} outer={a} inner={b}\n"))
	if len(tr.Problems) != 1 {
		t.Fatalf("got %d problems, want 1", len(tr.Problems))
	}
	if tr.Problems[0].Status != "truncated" {
		t.Errorf("status = %q, want truncated", tr.Problems[0].Status)
	}
	if _, ok := tr.Problems[0].Offered["{a} | {b}"]; !ok {
		t.Error("a truncated block dropped the pairs it did emit")
	}
}
