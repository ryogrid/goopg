package planner

// M0127-P5.9-l-ii tests. The subject is PROVENANCE — what the enumerator
// records about the pairs it offered and refused — so every test asserts on the
// trace block, never on a plan or a cost.
//
// The properties pinned, each one a way the channel could lie about clause 6:
//
//  1. the bushy pairing a 4-relation chain only reaches at phase 2 is recorded
//     AS phase 2, with the unordered key the plan side keys on;
//  2. relset names are the FROM item's alias when it has one (Q7's `nation n1`
//     / `nation n2` must not collapse) and the rendering is sorted by NAME, not
//     by relid — the plan side sorts, and a key equal only up to a permutation
//     is not a key;
//  3. the connectivity gate's refusals are recorded with a reason, which is
//     what turns "goopg never chose this partition" into a diagnosis;
//  4. a failed search still emits its block, with the failure in `status`;
//  5. with the gate off the trace is nil and every call site tolerates it —
//     the search must be untouched in production.

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// enableDPTrace turns the gate on for one test. The gate is a process-start
// read in production (joinsearchtrace.go:36) precisely so a plan cannot change
// mid-statement; a test overrides the variable rather than the environment,
// because re-reading the environment is the behaviour being ruled out.
func enableDPTrace(t *testing.T) {
	t.Helper()
	prev := dpTrace
	dpTrace = true
	t.Cleanup(func() { dpTrace = prev })
}

// traceCtx is jslCtx plus a trace whose relid → name map is the given names in
// relid order, built through `newSearchTrace` so the production constructor is
// what the tests exercise.
func traceCtx(t *testing.T, names ...string) *searchCtx {
	t.Helper()
	s := jslCtx(t, len(names))
	bindings := make([]rangeBinding, len(names))
	for i, n := range names {
		bindings[i] = rangeBinding{alias: n}
	}
	s.trace = newSearchTrace(bindings)
	if s.trace == nil {
		t.Fatal("newSearchTrace returned nil with the gate on")
	}
	return s
}

// offeredKeys is the set of pair keys the trace recorded, by phase.
func offeredKeys(t *searchTrace) map[string]int {
	out := map[string]int{}
	for _, p := range t.pairs {
		out[t.pairKey(p.outer, p.inner)] = p.phase
	}
	return out
}

// TestTraceRecordsBushyPairingAtPhase2: the chain a-b-c-d reaches
// {a+b} ⋈ {c+d} only through phase 2, and the record says so. This is the
// positive-control shape 09 §3.11 asks the instrument to demonstrate before any
// negative verdict about Q7/Q8 is admissible.
func TestTraceRecordsBushyPairingAtPhase2(t *testing.T) {
	enableDPTrace(t)
	s := traceCtx(t, "a", "b", "c", "d")
	b := &recordingBuilder{}
	if _, err := s.joinSearch(jslClauses(0b0011, 0b0110, 0b1100), b); err != nil {
		t.Fatalf("joinSearch: %v", err)
	}
	keys := offeredKeys(s.trace)
	if phase, ok := keys["{a+b} | {c+d}"]; !ok {
		t.Fatalf("bushy pairing not recorded; keys=%v", keys)
	} else if phase != tracePhaseBushy {
		t.Errorf("bushy pairing recorded at phase %d, want %d", phase, tracePhaseBushy)
	}
	// …and a left-deep pairing of the same top relset is phase 1, so the two
	// provenances are actually distinguished rather than both defaulting.
	if phase, ok := keys["{a+b+c} | {d}"]; !ok {
		t.Fatalf("left-deep pairing {a+b+c} | {d} not recorded; keys=%v", keys)
	} else if phase != tracePhaseLeftRight {
		t.Errorf("left-deep pairing recorded at phase %d, want %d", phase, tracePhaseLeftRight)
	}
	if s.trace.top != 0b1111 {
		t.Errorf("top relset %#b, want %#b", s.trace.top, 0b1111)
	}
	if s.trace.failed != "" {
		t.Errorf("status %q, want a clean search", s.trace.failed)
	}
}

// TestTraceRelsetNameSortsByNameAndKeepsAliases: property (2). The relids are
// deliberately assigned in an order that does NOT match the alphabetical one,
// so a renderer that walked relid order would be caught.
func TestTraceRelsetNameSortsByNameAndKeepsAliases(t *testing.T) {
	enableDPTrace(t)
	tr := newSearchTrace([]rangeBinding{
		{alias: "supplier"},
		{alias: "n1"}, // same table as n2 below — aliases must survive
		{alias: "n2"}, //
		{table: &catalog.Table{Name: "public.orders"}}, // no alias: the catalog name, unqualified
		{}, // a searched sub-problem: neither
	})
	if got, want := tr.relsetName(0b00111), "{n1+n2+supplier}"; got != want {
		t.Errorf("relsetName = %q, want %q", got, want)
	}
	if got, want := tr.relsetName(0b01001), "{orders+supplier}"; got != want {
		t.Errorf("relsetName = %q, want %q", got, want)
	}
	if got, want := tr.relsetName(0b10000), "{?4}"; got != want {
		t.Errorf("nameless FROM item: relsetName = %q, want %q", got, want)
	}
	// The pair key is unordered: the two argument orders must produce one key,
	// because `make_join_rel(x, y)` already handles `(y, x)`.
	a, b := tr.pairKey(0b00110, 0b01001), tr.pairKey(0b01001, 0b00110)
	if a != b {
		t.Errorf("pairKey is order-sensitive: %q vs %q", a, b)
	}
	if want := "{n1+n2} | {orders+supplier}"; a != want {
		t.Errorf("pairKey = %q, want %q", a, want)
	}
}

// TestTraceRecordsConnectivityDeclines: property (3). `a` and `c` are not
// connected, so phase 1 refuses the pair — and the refusal, not merely its
// absence, is what the trace has to show.
func TestTraceRecordsConnectivityDeclines(t *testing.T) {
	enableDPTrace(t)
	s := traceCtx(t, "a", "b", "c")
	b := &recordingBuilder{}
	if _, err := s.joinSearch(jslClauses(0b011, 0b110), b); err != nil {
		t.Fatalf("joinSearch: %v", err)
	}
	var found bool
	for _, d := range s.trace.declined {
		if s.trace.pairKey(d.outer, d.inner) == "{a} | {c}" {
			found = true
			if d.reason != "no-join-clause" {
				t.Errorf("decline reason %q, want no-join-clause", d.reason)
			}
			if d.phase != tracePhaseLeftRight {
				t.Errorf("decline phase %d, want %d", d.phase, tracePhaseLeftRight)
			}
		}
	}
	if !found {
		t.Fatalf("the a/c refusal was not recorded; declined=%d", len(s.trace.declined))
	}
	// The rendered block is the wire format the audit tool parses back; assert
	// the vocabulary here so a rename cannot silently desynchronise the two
	// packages (internal/estimateaudit/enumtrace.go).
	out := s.trace.render()
	for _, want := range []string{
		"DPTRACE problem nrels=3 rels=a,b,c",
		"DPTRACE pair phase=1 lev=2 created=1 pair={a} | {b} outer={a} inner={b}",
		"DPTRACE decline phase=1 lev=2 reason=no-join-clause pair={a} | {c}",
		"DPTRACE end top={a+b+c}",
		"status=ok",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered block missing %q:\n%s", want, out)
		}
	}
}

// TestTraceRecordsFailedSearch: property (4). A search that could not enumerate
// is exactly the case where the record is the evidence, so the block must still
// be complete and must say it failed.
func TestTraceRecordsFailedSearch(t *testing.T) {
	enableDPTrace(t)
	s := traceCtx(t, "a", "b", "c")
	b := &recordingBuilder{fail: 0b011}
	if _, err := s.joinSearch(jslClauses(0b011, 0b110), b); err == nil {
		t.Fatal("joinSearch: want an error from the refusing builder")
	}
	if s.trace.failed == "" {
		t.Fatal("a failed search left status clean")
	}
	if out := s.trace.render(); !strings.Contains(out, "status=test builder refuses") {
		t.Errorf("failure not in the rendered block:\n%s", out)
	}
}

// TestTraceOffIsNil: property (5). With the gate off nothing is allocated and
// the search runs unchanged — the production configuration.
func TestTraceOffIsNil(t *testing.T) {
	prev := dpTrace
	dpTrace = false
	t.Cleanup(func() { dpTrace = prev })

	if tr := newSearchTrace([]rangeBinding{{alias: "a"}}); tr != nil {
		t.Fatalf("gate off but newSearchTrace returned %+v", tr)
	}
	s := jslCtx(t, 3)
	if s.trace != nil {
		t.Fatalf("jslCtx left a trace attached")
	}
	b := &recordingBuilder{}
	if _, err := s.joinSearch(jslClauses(0b011, 0b110), b); err != nil {
		t.Fatalf("joinSearch with the trace off: %v", err)
	}
	// The nil-safe call sites: these are what run in production.
	s.trace.offer(tracePhaseBushy, 0b001, 0b010, true)
	s.trace.decline(tracePhaseBushy, 0b001, 0b010, "no-join-clause")
	s.trace.emit()
}
