package optimizer

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
	s.trace.cost(nil)
	s.trace.emit()
}

// TestTraceRecordsCostPerRelset: R53 Step-0's instrument. After a full search
// every built joinrel carries one L-number — the level, the rows setCheapest
// priced, the pathlist length, the winner's kind and total — so a costing
// question ("did {ps,p,s,l} lose on rows or on price?") is answered from the
// trace without re-instrumenting.
func TestTraceRecordsCostPerRelset(t *testing.T) {
	enableDPTrace(t)
	s := traceCtx(t, "a", "b", "c")
	b := &recordingBuilder{}
	if _, err := s.joinSearch(jslClauses(0b011, 0b110), b); err != nil {
		t.Fatalf("joinSearch: %v", err)
	}
	// Three joinrels: {a+b} and {b+c} at level 2, {a+b+c} at level 3.
	if len(s.trace.costs) != 3 {
		t.Fatalf("cost records = %d, want 3 (one per built joinrel)", len(s.trace.costs))
	}
	for _, c := range s.trace.costs {
		if c.level != relLevel(c.rel) {
			t.Errorf("cost record level %d for relset %#b (relLevel %d)", c.level, uint32(c.rel), relLevel(c.rel))
		}
		if c.rows <= 0 || c.paths == 0 {
			t.Errorf("cost record holds no pricing: %+v", c)
		}
	}
	out := s.trace.render()
	for _, want := range []string{
		"DPTRACE cost lev=2 rel={a+b}",
		"DPTRACE cost lev=2 rel={b+c}",
		"DPTRACE cost lev=3 rel={a+b+c}",
		"cheapest=",
		"reqouter=",
		"total=",
		"second=",
		"secondtotal=",
		"costs=3",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered block missing %q:\n%s", want, out)
		}
	}
}

// TestTraceCostSecondAndReqouter: the two fields that scope a pricing slice.
// The winner is CheapestTotal (the path the search USES), never the min-total
// pathlist entry: here an unparameterised hash wins while a cheaper
// parameterised NL stands second, and reqouter says the winner needs no
// bindings while the inner-parameterisation still reads `nli` on the second.
func TestTraceCostSecondAndReqouter(t *testing.T) {
	enableDPTrace(t)
	s := traceCtx(t, "a", "b")
	rel := &RelOptInfo{Relids: 0b011, Rows: 200}
	rel.Pathlist = []*Path{
		{Kind: PathHashJoin, Cost: Cost{Total: 100}},
		{Kind: PathNestLoop, Cost: Cost{Total: 50}, Children: []*Path{{}, {RequiredOuter: 0b001}}},
	}
	rel.CheapestTotal = rel.Pathlist[0]
	s.trace.cost(rel)
	if len(s.trace.costs) != 1 {
		t.Fatalf("cost records = %d, want 1", len(s.trace.costs))
	}
	c := s.trace.costs[0]
	if c.kind != "hash" || c.total != 100 || c.reqouter != 0 {
		t.Errorf("winner = %s reqouter=%#b total=%g, want hash reqouter=0 total=100", c.kind, uint32(c.reqouter), c.total)
	}
	if c.second != "nli" || c.secondTotal != 50 {
		t.Errorf("second = %s total=%g, want nli 50", c.second, c.secondTotal)
	}
	out := s.trace.render()
	if !strings.Contains(out, "cheapest=hash reqouter={} total=100 second=nli secondtotal=50") {
		t.Errorf("rendered cost line wrong:\n%s", out)
	}
}

// TestTracePathKindLabels: the winner vocabulary Step-0 reads off the cost
// lines — join methods by name, parameterised (NLI) nestloops split from plain
// ones (same PathKind, different admission rules), and a nil winner named.
func TestTracePathKindLabels(t *testing.T) {
	nli := &Path{Kind: PathNestLoop, Children: []*Path{{}, {RequiredOuter: 0b001}}}
	for _, tc := range []struct {
		path *Path
		want string
	}{
		{&Path{Kind: PathHashJoin}, "hash"},
		{&Path{Kind: PathMergeJoin}, "merge"},
		{&Path{Kind: PathNestLoop}, "nl"},
		{nli, "nli"},
		{&Path{Kind: PathSeqScan}, "seq"},
		{&Path{Kind: PathGatherMerge}, "gathermerge"},
		{nil, "none"},
	} {
		if got := tracePathKind(tc.path); got != tc.want {
			t.Errorf("tracePathKind(%+v) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// TestTraceCPAdmitJoinAndVeto: R54 Step-0's S1/S2 records. An admitted joinrel
// carries its inputs' flags, the clause count, and failidx=-1 with the "none"
// default; a vetoed one names the first failing clause by %T — the S2
// explanation Step-0 reads, computed through `firstParallelUnsafeClause`, the
// same helper the verdict loop is written on, so the name cannot disagree with
// the flag.
func TestTraceCPAdmitJoinAndVeto(t *testing.T) {
	enableDPTrace(t)
	s := traceCtx(t, "a", "b")
	s.trace.admit(0b011, true, true, true, jslClauses(0b011).all, s.cat)
	veto := []*restrictInfo{{relids: 0b011, ecID: noEquivClass, clause: &OuterColumnRef{}}}
	s.trace.admit(0b011, false, true, true, veto, s.cat)
	if len(s.trace.cpAdmits) != 2 {
		t.Fatalf("cpadmit records = %d, want 2", len(s.trace.cpAdmits))
	}
	a := s.trace.cpAdmits[0]
	if !a.cp || !a.in1 || !a.in2 || a.nclauses != 1 || a.failidx != -1 || a.failkind != "" {
		t.Errorf("admitted record wrong: %+v", a)
	}
	v := s.trace.cpAdmits[1]
	if v.cp || v.failidx != 0 || v.failkind != "*optimizer.OuterColumnRef" {
		t.Errorf("veto record wrong: %+v", v)
	}
	out := s.trace.render()
	for _, want := range []string{
		"DPTRACE cpadmit src=join rel={a+b} cp=1 in1=1 in2=1 nclauses=1 failidx=-1 failkind=none",
		"DPTRACE cpadmit src=join rel={a+b} cp=0 in1=1 in2=1 nclauses=1 failidx=0 failkind=*optimizer.OuterColumnRef",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered block missing %q:\n%s", want, out)
		}
	}
}

// TestTraceCPAdmitNilClause: the verdict loop vetoes a nil restrictInfo
// outright; the record names it "nil-clause" rather than panicking on %T of
// nothing. %T of that string is "string", which is what the line carries.
func TestTraceCPAdmitNilClause(t *testing.T) {
	enableDPTrace(t)
	s := traceCtx(t, "a", "b")
	s.trace.admit(0b011, false, true, true, []*restrictInfo{nil}, s.cat)
	if len(s.trace.cpAdmits) != 1 {
		t.Fatalf("cpadmit records = %d, want 1", len(s.trace.cpAdmits))
	}
	v := s.trace.cpAdmits[0]
	if v.failidx != 0 || v.failkind != "string" {
		t.Errorf("nil-clause record wrong: %+v", v)
	}
	if out := s.trace.render(); !strings.Contains(out, "failidx=0 failkind=string") {
		t.Errorf("rendered block missing nil-clause naming:\n%s", out)
	}
}

// TestTraceBaseCPLeafVerdict: S1's leaf half. The Filter wrapper peels exactly
// as `relConsiderParallel` peels it, the arm names in the verdict's own
// vocabulary, and a kind the verdict does not enumerate renders "other" — the
// verdict fails closed there, and so does the name.
func TestTraceBaseCPLeafVerdict(t *testing.T) {
	enableDPTrace(t)
	s := traceCtx(t, "a", "b", "c")
	s.trace.baseCP(0b001, &Filter{Child: &SeqScan{}}, true)
	s.trace.baseCP(0b010, &BitmapHeapScan{}, false)
	s.trace.baseCP(0b100, &Sort{}, false)
	if len(s.trace.cpAdmits) != 3 {
		t.Fatalf("cpadmit records = %d, want 3", len(s.trace.cpAdmits))
	}
	for i, want := range []string{"seq", "bitmap", "other"} {
		if got := s.trace.cpAdmits[i].leaf; got != want {
			t.Errorf("leaf %d = %q, want %q", i, got, want)
		}
	}
	out := s.trace.render()
	for _, want := range []string{
		"DPTRACE cpadmit src=base rel={a} cp=1 leaf=seq",
		"DPTRACE cpadmit src=base rel={b} cp=0 leaf=bitmap",
		"DPTRACE cpadmit src=base rel={c} cp=0 leaf=other",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered block missing %q:\n%s", want, out)
		}
	}
}

// TestTraceGatherVerdicts: S4's "generated but lost" vs "never generated"
// separation. The record carries the partial-pathlist length at decision time
// with the gate that fired; whether a Gather won reads off the `cost` line's
// cheapest kind, not here.
func TestTraceGatherVerdicts(t *testing.T) {
	enableDPTrace(t)
	s := traceCtx(t, "a", "b")
	s.trace.gather(0b011, 2, "admitted")
	s.trace.gather(0b011, 1, "no-cp")
	if len(s.trace.gathers) != 2 {
		t.Fatalf("cpgather records = %d, want 2", len(s.trace.gathers))
	}
	out := s.trace.render()
	for _, want := range []string{
		"DPTRACE cpgather rel={a+b} partials=2 verdict=admitted",
		"DPTRACE cpgather rel={a+b} partials=1 verdict=no-cp",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered block missing %q:\n%s", want, out)
		}
	}
}

// TestTraceAdmissionNilSafe: with the gate off the trace is nil and every R54
// call site tolerates it — the search and the post-pass must be untouched in
// production. `traceUpperGate` is a package function rather than a method, so
// it gets its own nil-tolerance pin here: gate off means no output, no panic.
func TestTraceAdmissionNilSafe(t *testing.T) {
	var nilTrace *searchTrace
	nilTrace.admit(0b011, true, true, true, nil, nil)
	nilTrace.baseCP(0b001, nil, true)
	nilTrace.gather(0b011, 0, "no-partials")
	traceUpperGate("agg", "split", "workers=4 divisor=3")
}
