package executor

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

var q96LegacyChildTraceTestMu sync.Mutex

type lockedTraceBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

type q96TraceTestNode struct{ schema optimizer.Schema }

func (n *q96TraceTestNode) Pos() int                 { return 0 }
func (n *q96TraceTestNode) Output() optimizer.Schema { return n.schema }

func (b *lockedTraceBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedTraceBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func withQ96Trace(t *testing.T, enabled bool, fn func(*bytes.Buffer)) {
	t.Helper()
	var buf bytes.Buffer
	withQ96TraceWriter(t, enabled, &buf, func() { fn(&buf) })
}

func withQ96TraceWriter(t *testing.T, enabled bool, writer io.Writer, fn func()) {
	t.Helper()
	q96LegacyChildTraceTestMu.Lock()
	defer q96LegacyChildTraceTestMu.Unlock()
	oldEnabled, oldWriter, oldFlags := q96LegacyChildTrace, log.Writer(), log.Flags()
	q96LegacyChildTrace = enabled
	log.SetOutput(writer)
	log.SetFlags(0)
	defer func() {
		q96LegacyChildTrace = oldEnabled
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
	}()
	fn()
}

func TestQ96LegacyChildTraceTextRendererAndCostsOff(t *testing.T) {
	j := &optimizer.Join{Type: optimizer.JoinTypeInner}
	render := func(opts parser.ExplainOptions) (string, []Row) {
		var b strings.Builder
		var rows []Row
		walkPlan(&b, j, 0, &rows, opts)
		return b.String(), rows
	}
	var offText string
	var offRows []Row
	withQ96Trace(t, false, func(buf *bytes.Buffer) {
		offText, offRows = render(parser.ExplainOptions{})
		if buf.Len() != 0 {
			t.Fatalf("trace-off TEXT logged %q", buf.String())
		}
	})
	withQ96Trace(t, true, func(buf *bytes.Buffer) {
		onText, onRows := render(parser.ExplainOptions{})
		if onText != offText || fmt.Sprint(onRows) != fmt.Sprint(offRows) {
			t.Fatalf("trace changed TEXT: on=%q off=%q", onText, offText)
		}
		trace := buf.String()
		if !strings.Contains(trace, "kind=TEXT") || !strings.Contains(trace, "cpu_tuple=") || !strings.Contains(trace, "mapping=one-to-one ledger=emitted") {
			t.Fatalf("TEXT renderer trace = %q", trace)
		}
	})
	withQ96Trace(t, true, func(buf *bytes.Buffer) {
		opts := parser.ExplainOptions{Costs: false}
		opts.Set.Costs = true
		render(opts)
		if got := buf.String(); strings.Contains(got, "Q96CHILD") {
			t.Fatalf("COSTS OFF emitted trace: %q", got)
		}
	})
}

func TestQ96LegacyChildTraceJSONRendererIsAA(t *testing.T) {
	j := &optimizer.Join{Type: optimizer.JoinTypeInner}
	opts := parser.ExplainOptions{Format: parser.ExplainFormatJSON}
	var off map[string]any
	withQ96Trace(t, false, func(buf *bytes.Buffer) {
		off = planToJSON(j, opts)
		if buf.Len() != 0 {
			t.Fatalf("trace-off JSON logged %q", buf.String())
		}
	})
	withQ96Trace(t, true, func(buf *bytes.Buffer) {
		on := planToJSON(j, opts)
		if fmt.Sprint(on) != fmt.Sprint(off) {
			t.Fatalf("trace changed JSON: on=%v off=%v", on, off)
		}
		trace := buf.String()
		if !strings.Contains(trace, "kind=JSON") || !strings.Contains(trace, "cpu_tuple=") || !strings.Contains(trace, "mapping=one-to-one ledger=emitted") {
			t.Fatalf("JSON renderer trace = %q", trace)
		}
	})
}

func TestQ96LegacyChildTraceAnalyzeTextRendererIsAA(t *testing.T) {
	j := &optimizer.Join{Type: optimizer.JoinTypeInner}
	opts := parser.ExplainOptions{Analyze: true}
	render := func() string {
		var b strings.Builder
		var rows []Row
		walkPlanAnalyze(&b, j, 0, &rows, opts, nil, nil, nil, nil, nil, nil, nil, nil)
		return b.String()
	}
	var off string
	withQ96Trace(t, false, func(buf *bytes.Buffer) {
		off = render()
		if buf.Len() != 0 {
			t.Fatalf("trace-off ANALYZE logged %q", buf.String())
		}
	})
	withQ96Trace(t, true, func(buf *bytes.Buffer) {
		if on := render(); on != off {
			t.Fatalf("trace changed ANALYZE text: on=%q off=%q", on, off)
		}
		trace := buf.String()
		if !strings.Contains(trace, "kind=TEXT") || !strings.Contains(trace, "cpu_tuple=") || !strings.Contains(trace, "mapping=one-to-one ledger=emitted") {
			t.Fatalf("ANALYZE TEXT trace = %q", trace)
		}
	})
}

func TestQ96LegacyChildTraceZeroJoinCensus(t *testing.T) {
	withQ96Trace(t, true, func(buf *bytes.Buffer) {
		o := newQ96LegacyChildObserver("TEXT", &optimizer.Values{}, true)
		o.finish()
		line := buf.String()
		if !strings.Contains(line, "Q96CHILD token=r") || !strings.Contains(line, "kind=TEXT mapping=none final_joins=0") {
			t.Fatalf("zero-join census log = %q", line)
		}
	})
}

func TestQ96LegacyChildTraceDeduplicatesUniqueJoinLedger(t *testing.T) {
	withQ96Trace(t, true, func(buf *bytes.Buffer) {
		j := &optimizer.Join{Type: optimizer.JoinTypeInner}
		o := newQ96LegacyChildObserver("TEXT", j, true)
		v := optimizer.LegacyDisplayCostObservation{Node: j}
		o.observe(v)
		o.observe(v)
		o.finish()
		text := buf.String()
		if strings.Count(text, "cpu_tuple=") != 1 || !strings.Contains(text, "mapping=one-to-one ledger=emitted") || !strings.Contains(text, "ledger_callbacks=2") {
			t.Fatalf("deduplicated ledger log = %q", text)
		}
	})
}

func TestQ96LegacyChildTraceReportsMultipleFinalOccurrence(t *testing.T) {
	withQ96Trace(t, true, func(buf *bytes.Buffer) {
		j := &optimizer.Join{Type: optimizer.JoinTypeInner}
		root := &optimizer.Join{Type: optimizer.JoinTypeInner, Left: j, Right: j}
		o := newQ96LegacyChildObserver("TEXT", root, true)
		o.observe(optimizer.LegacyDisplayCostObservation{Node: j})
		o.finish()
		text := buf.String()
		if strings.Contains(text, "cpu_tuple=") || !strings.Contains(text, "mapping=multiple ledger=suppressed") {
			t.Fatalf("multiple-occurrence log = %q", text)
		}
	})
}

func TestQ96LegacyChildTraceReportsUnobservedUniqueJoin(t *testing.T) {
	withQ96Trace(t, true, func(buf *bytes.Buffer) {
		j := &optimizer.Join{Type: optimizer.JoinTypeInner}
		o := newQ96LegacyChildObserver("JSON", j, true)
		o.finish()
		text := buf.String()
		if !strings.Contains(text, "kind=JSON mapping=one-to-one ledger=none") || !strings.Contains(text, "ledger_callbacks=0") {
			t.Fatalf("unobserved-unique log = %q", text)
		}
	})
}

func TestQ96LegacyChildTraceCensusOrderAndNilSources(t *testing.T) {
	withQ96Trace(t, true, func(buf *bytes.Buffer) {
		left := &optimizer.Join{Type: optimizer.JoinTypeInner, Left: &q96TraceTestNode{schema: optimizer.Schema{
			{Name: "three-a", SourceTableIdx: 3},
			{Name: "one", SourceTableIdx: 1},
			{Name: "three-b", SourceTableIdx: 3},
		}}}
		right := &optimizer.Join{Type: optimizer.JoinTypeInner}
		root := &optimizer.Join{Type: optimizer.JoinTypeInner, Left: left, Right: right}
		o := newQ96LegacyChildObserver("TEXT", root, true)
		for _, join := range []*optimizer.Join{right, root, left} {
			o.observe(optimizer.LegacyDisplayCostObservation{Node: join})
		}
		o.finish()
		trace := buf.String()
		if q96SourceSet(nil) != "{}" || q96SourceSet(left.Left) != "{1,3}" || !strings.Contains(trace, "left_sources={1,3} right_sources={}") {
			t.Fatalf("nil source set trace = %q", trace)
		}
		p1 := strings.Index(trace, "mapping=one-to-one ledger=emitted ordinal=1")
		p2 := strings.Index(trace, "mapping=one-to-one ledger=emitted ordinal=2")
		p3 := strings.Index(trace, "mapping=one-to-one ledger=emitted ordinal=3")
		if p1 < 0 || p2 < p1 || p3 < p2 {
			t.Fatalf("census order is not stable: %q", trace)
		}
	})
}

func TestQ96LegacyChildTraceConcurrentRenderersDoNotCrossTalk(t *testing.T) {
	var buf lockedTraceBuffer
	withQ96TraceWriter(t, true, &buf, func() {
		textPlan := &optimizer.Join{Type: optimizer.JoinTypeInner}
		jsonPlan := &optimizer.Join{Type: optimizer.JoinTypeInner}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			var b strings.Builder
			var rows []Row
			walkPlan(&b, textPlan, 0, &rows, parser.ExplainOptions{})
		}()
		go func() {
			defer wg.Done()
			planToJSON(jsonPlan, parser.ExplainOptions{Format: parser.ExplainFormatJSON})
		}()
		wg.Wait()

		kinds := make(map[string]string)
		counts := make(map[string]int)
		for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
			fields := strings.Fields(line)
			var token, kind string
			for _, field := range fields {
				if strings.HasPrefix(field, "token=") {
					token = strings.TrimPrefix(field, "token=")
				}
				if strings.HasPrefix(field, "kind=") {
					kind = strings.TrimPrefix(field, "kind=")
				}
			}
			if token == "" || (kind != "TEXT" && kind != "JSON") {
				t.Fatalf("malformed concurrent trace line %q", line)
			}
			if prior, exists := kinds[token]; exists && prior != kind {
				t.Fatalf("token %s crossed renderer kinds %s/%s: %q", token, prior, kind, line)
			}
			kinds[token] = kind
			counts[token]++
		}
		if len(kinds) != 2 {
			t.Fatalf("renderer token count = %d, trace=%q", len(kinds), buf.String())
		}
		for token, kind := range kinds {
			if counts[token] != 4 {
				t.Fatalf("token %s (%s) records = %d, trace=%q", token, kind, counts[token], buf.String())
			}
		}
	})
}

func TestQ96LegacyChildTraceConcurrentObserversDoNotCrossTalk(t *testing.T) {
	var buf lockedTraceBuffer
	withQ96TraceWriter(t, true, &buf, func() {
		left := newQ96LegacyChildObserver("TEXT", &optimizer.Join{Type: optimizer.JoinTypeInner}, true)
		right := newQ96LegacyChildObserver("JSON", &optimizer.Join{Type: optimizer.JoinTypeInner}, true)
		var wg sync.WaitGroup
		for _, observer := range []*q96LegacyChildObserver{left, right} {
			wg.Add(1)
			go func(o *q96LegacyChildObserver) {
				defer wg.Done()
				for join := range o.ordinal {
					o.observe(optimizer.LegacyDisplayCostObservation{Node: join})
				}
				o.finish()
			}(observer)
		}
		wg.Wait()
		trace := buf.String()
		for _, observer := range []*q96LegacyChildObserver{left, right} {
			if strings.Count(trace, "token="+observer.token) != 2 {
				t.Fatalf("observer %s trace count = %d, trace=%q", observer.token, strings.Count(trace, "token="+observer.token), trace)
			}
		}
		if !strings.Contains(trace, "token="+left.token+" kind=TEXT") || !strings.Contains(trace, "token="+right.token+" kind=JSON") {
			t.Fatalf("observer kind/token cross-talk: %q", trace)
		}
	})
}

func TestQ96LegacyChildTraceUsesExplainDefaultCosts(t *testing.T) {
	withQ96Trace(t, true, func(_ *bytes.Buffer) {
		defaultOpts := parser.ExplainOptions{}
		if !explainCostsEnabled(defaultOpts) {
			t.Fatal("default EXPLAIN costs must be enabled")
		}
		if got := newQ96LegacyChildObserver("TEXT", &optimizer.Values{}, explainCostsEnabled(defaultOpts)); got == nil {
			t.Fatal("default EXPLAIN costs must create the trace observer")
		}
		offOpts := parser.ExplainOptions{Costs: false}
		offOpts.Set.Costs = true
		if explainCostsEnabled(offOpts) {
			t.Fatal("EXPLAIN (COSTS OFF) must disable costs")
		}
		if got := newQ96LegacyChildObserver("TEXT", &optimizer.Values{}, explainCostsEnabled(offOpts)); got != nil {
			t.Fatal("EXPLAIN (COSTS OFF) must not create the trace observer")
		}
		onOpts := parser.ExplainOptions{Costs: true}
		onOpts.Set.Costs = true
		if !explainCostsEnabled(onOpts) {
			t.Fatal("EXPLAIN (COSTS ON) must enable costs")
		}
	})
}
