package optimizer

// R118 (plan-parity-fix-take2) — measurement-only producer audit for Q96's
// lower Hash Joins. TEMPORARY: the whole file, its PlannerSettings/Explain
// fields, its renderer hooks, its flag registration, and its tests are
// removed before REPORT.md. Nothing here may change a plan, a cost, or an
// estimate; every hook is nil-safe and allocates nothing when disabled.
//
// Design: r118-q96-lower-join-producers/SCOPE.md. The statement-local
// sidecar is created at the outer EXPLAIN wrapper only (planStmtWithSettings'
// ExplainStmt case) and rides inside a PlannerSettings copy into the
// recursive inner call. Nested statements plan through DefaultPlannerSettings
// and never see it. Join constructions record immutable scalar snapshots;
// the pre-return census resolves final occurrences by pointer identity plus
// documented successor edges, freezes a pointer-free report onto the Explain
// node, and the renderers consume only that frozen report.

import (
	"fmt"
	"math"
	"os"
	"sort"
	"sync"
	"sync/atomic"
)

// producerAuditFlag is the default-off diagnostic switch. Read per EXPLAIN
// statement (not cached) so tests can flip it without process restart.
const producerAuditFlag = "GOOPG_Q96_PRODUCER_AUDIT"

func producerAuditEnabled() bool {
	return os.Getenv(producerAuditFlag) == "1"
}

// Construction routes. Only legacy-direct fires in production today: the
// R118 route probe measured the R111 forced forms building exclusively
// through planner.go's legacy join site. path-backed exists so the focused
// tests can exercise the alternate route through the same record path.
const (
	producerRouteLegacyDirect = "legacy-direct"
	producerRoutePathBacked   = "path-backed"
)

// JoinEstimateTrace mirrors estimateJoin's equation branch by branch. Every
// field is a frozen scalar; no expressions, stats pointers, or nodes.
// Jointype is recorded because outerJoinRowFloor branches on it.
type JoinEstimateTrace struct {
	Branch   string
	Jointype string
	L, R     int64
	// SEMI/ANTI branch: MatchFrac is semiJoinMatchFraction, ResidualSel the
	// residual factor; the applied selectivity is min(1, product), flipped
	// for ANTI — exactly as estimateJoin computes it.
	MatchFrac   float64
	ResidualSel float64
	// Hash/merge measured branch.
	SuperkeySel         float64
	SuperkeyFired       bool
	SuperkeyBoundProven bool
	PairSels            []float64
	PairMethods         []string // "key-covered" | "mcv" | "nd" | "default"
	PairNDs             []int64
	RowsBound           float64
	RowsBoundInf        bool
	RowsFloat           float64
	Measured            bool
	// Fallback branch.
	Capped bool
	// Common output.
	FinalRows int64
}

// ProducerColumnLineage is one output position's schema provenance. Only
// fields SchemaColumn actually carries are recorded (Type has no OID;
// nullability is not tracked on the schema) — "where applicable" per scope.
type ProducerColumnLineage struct {
	Pos            int
	Name           string
	TypeName       string
	TypeArgs       []int64
	IsArray        bool
	SourceTableIdx int16
}

// JoinProducerRecord is one instrumented construction's immutable snapshot.
type JoinProducerRecord struct {
	ID           uint64
	Route        string
	Jointype     string
	Algo         string
	Trace        JoinEstimateTrace
	SchemaWriter string
	Columns      []ProducerColumnLineage
	FinalWidth   int
}

// producerAuditSidecar is the statement-local producer registry. It is
// created at most once per top-level EXPLAIN and discarded with the plan;
// concurrent statements hold disjoint sidecars (no shared state).
type producerAuditSidecar struct {
	mu       sync.Mutex
	nextID   uint64
	byPtr    map[*Join]uint64
	succ     map[uint64]uint64 // successor edges: clone/rewrite old ID -> new ID
	records  map[uint64]*JoinProducerRecord
	frozen   *ProducerAuditReport
}

// freeze runs the final-tree census and stores the frozen report,
// overwriting any previous freeze. Frames nest LIFO during planning, so
// repeated freezes converge on the outermost frame's full tree; the outer
// EXPLAIN wrapper reads the final value. Nil node freezes an empty report
// (never a nil one — callers must not branch on nilness).
func (sc *producerAuditSidecar) freeze(inner Node) {
	if sc == nil {
		return
	}
	rep := freezeProducerAudit(sc, inner)
	// Unmapped non-lateral finals fail the census gate loudly: the report
	// carries the stop instead of silently dropping the occurrence.
	for _, r := range rep.Rows {
		if r.Mapping == "zero-unmapped" {
			rep.Stop = "unmapped-nonlateral-join"
			break
		}
	}
	sc.mu.Lock()
	sc.frozen = rep
	sc.mu.Unlock()
}

// report returns the frozen report (nil if never frozen, e.g. planning
// errored before any frame completed). Nil sidecar returns nil.
func (sc *producerAuditSidecar) report() *ProducerAuditReport {
	if sc == nil {
		return nil
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.frozen
}

func newProducerAuditSidecar() *producerAuditSidecar {
	return &producerAuditSidecar{
		byPtr:   make(map[*Join]uint64),
		succ:    make(map[uint64]uint64),
		records: make(map[uint64]*JoinProducerRecord),
	}
}

// recordJoinConstruction snapshots j's estimateJoin equation and schema
// lineage under a fresh deterministic construction ID. Nil sidecar or nil
// join is a no-op returning 0 (OFF path allocates nothing).
func (sc *producerAuditSidecar) recordJoinConstruction(j *Join, route, schemaWriter string) uint64 {
	if sc == nil || j == nil {
		return 0
	}
	var tr JoinEstimateTrace
	estimateJoinTraced(j, &tr)
	cols := make([]ProducerColumnLineage, 0, len(j.Output()))
	for i, c := range j.Output() {
		cols = append(cols, ProducerColumnLineage{
			Pos:            i,
			Name:           c.Name,
			TypeName:       c.Type.Name,
			TypeArgs:       append([]int64(nil), c.Type.Args...),
			IsArray:        c.Type.IsArray,
			SourceTableIdx: c.SourceTableIdx,
		})
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.nextID++
	id := sc.nextID
	sc.records[id] = &JoinProducerRecord{
		ID:           id,
		Route:        route,
		Jointype:     joinTypeAuditName(j.Type),
		Algo:         joinAlgoAuditName(j.Algo),
		Trace:        tr,
		SchemaWriter: schemaWriter,
		Columns:      cols,
		FinalWidth:   TupleWidth(j.Output()),
	}
	if prev, ok := sc.byPtr[j]; ok {
		// Same pointer recorded twice: keep the first record, link the
		// second as its successor so the census still resolves uniquely.
		sc.succ[prev] = id
		return id
	}
	sc.byPtr[j] = id
	return id
}

// noteSuccessor links a clone/rewrite replacement to its source. Nil-safe;
// unknown sources are ignored (the census reports them as zero-mapping).
// When the replacement was never recorded, it inherits a clone of the
// source record under a fresh ID: the terminal lineage always carries the
// producer data, so resolve-then-read never yields an empty record.
func (sc *producerAuditSidecar) noteSuccessor(old, new *Join) {
	if sc == nil || old == nil || new == nil || old == new {
		return
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	oid, ok := sc.byPtr[old]
	if !ok {
		return
	}
	if nid, ok := sc.byPtr[new]; ok {
		sc.succ[oid] = nid
		return
	}
	sc.nextID++
	nid := sc.nextID
	sc.byPtr[new] = nid
	if src, ok := sc.records[oid]; ok {
		clone := *src
		clone.ID = nid
		sc.records[nid] = &clone
	}
	sc.succ[oid] = nid
}

// resolve follows successor edges to the terminal construction ID.
// ok=false means the pointer was never recorded (zero mapping).
func (sc *producerAuditSidecar) resolve(j *Join) (uint64, bool) {
	if sc == nil || j == nil {
		return 0, false
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	id, ok := sc.byPtr[j]
	if !ok {
		return 0, false
	}
	for {
		nxt, ok := sc.succ[id]
		if !ok {
			return id, true
		}
		id = nxt
	}
}

func joinTypeAuditName(t JoinType) string {
	switch t {
	case JoinTypeInner:
		return "inner"
	case JoinTypeLeft:
		return "left"
	case JoinTypeRight:
		return "right"
	case JoinTypeFull:
		return "full"
	case JoinTypeCross:
		return "cross"
	case JoinTypeSemi:
		return "semi"
	case JoinTypeAnti:
		return "anti"
	default:
		return fmt.Sprintf("unknown(%d)", int(t))
	}
}

func joinAlgoAuditName(a JoinAlgo) string {
	switch a {
	case JoinAlgoNestedLoop:
		return "nestedloop"
	case JoinAlgoHash:
		return "hash"
	case JoinAlgoMerge:
		return "merge"
	default:
		return fmt.Sprintf("unknown(%d)", int(a))
	}
}

// ProducerAuditRow is one frozen final occurrence. No pointers, no mutable
// planner state — safe for renderers and logs.
type ProducerAuditRow struct {
	Ordinal        uint64
	ConstructionID uint64
	// Shared marks a repeated occurrence of one distinct node (DAG sharing
	// or recursive-CTE structure): same lineage, reported per occurrence.
	// A shared occurrence still resolves one-to-one; "multiple" would mean
	// one occurrence claiming two lineages, which pointer identity makes
	// impossible — the gate stays to catch future matcher changes.
	Shared  bool
	Mapping string // "one-to-one" | "zero-unmapped" | "lateral-out-of-population"
	Route          string
	Jointype       string
	Algo           string
	Rows           int64
	Width          int
	Trace          JoinEstimateTrace
	Columns        []ProducerColumnLineage
	EquationOK     bool
	SchemaOK       bool
}

// ProducerAuditReport is the frozen per-statement census attached to the
// Explain plan. Renderers consume ONLY this struct.
type ProducerAuditReport struct {
	Token string
	Stop  string // "" when every selected join resolved; else the stop reason
	Rows  []ProducerAuditRow
}

var producerAuditTokenCounter uint64

func producerAuditToken() string {
	return fmt.Sprintf("pa%d", atomic.AddUint64(&producerAuditTokenCounter, 1))
}

// producerAuditChildren is the census traversal. It mirrors the renderer's
// node-child structure (planChildren in operators_explain.go): the same
// occurrences in the same pre-order, so the frozen ordinals agree with both
// TEXT and JSON renderer-visible Join output by construction, pinned by test.
// Every wrapper that can hide a Join is covered; anything unlisted stops the
// descent (fail-closed: a hidden join maps zero rather than misattributed).
func producerAuditChildren(n Node) []Node {
	switch p := n.(type) {
	case *Project:
		return []Node{p.Child}
	case *Result:
		if p.Child != nil {
			return []Node{p.Child}
		}
		return nil
	case *Filter:
		return []Node{p.Child}
	case *Sort:
		return []Node{p.Child}
	case *Limit:
		return []Node{p.Child}
	case *Gather:
		return []Node{p.Child}
	case *GatherMerge:
		return []Node{p.Child}
	case *Distinct:
		return []Node{p.Child}
	case *DistinctOn:
		return []Node{p.Child}
	case *Aggregate:
		return []Node{p.Child}
	case *WindowAgg:
		return []Node{p.Child}
	case *Join:
		return []Node{p.Left, p.Right}
	case *NestedLoopIndexJoin:
		return []Node{p.Outer, p.Inner}
	case *LockRows:
		return []Node{p.Child}
	case *OrdinalityWrap:
		return []Node{p.Child}
	case *Memoize:
		if p.Child != nil {
			return []Node{p.Child}
		}
		return nil
	case *Insert:
		return []Node{p.Source}
	case *Update:
		out := make([]Node, 0, 1+len(p.FromScans))
		out = append(out, p.Child)
		out = append(out, p.FromScans...)
		return out
	case *Delete:
		out := make([]Node, 0, 1+len(p.UsingScans))
		out = append(out, p.Child)
		out = append(out, p.UsingScans...)
		return out
	case *CTEScan:
		// Recursive self-references would cycle: the visited set in the
		// census (not this function) stops re-descent. Returning the
		// child here matches the renderer's non-recursive arm.
		return []Node{p.Child}
	case *BitmapHeapScan:
		if p.Outer == nil {
			return nil
		}
		return []Node{p.Outer}
	case *BitmapAnd:
		return append([]Node(nil), p.Inputs...)
	case *BitmapOr:
		return append([]Node(nil), p.Inputs...)
	case *SetOp:
		return []Node{p.Left, p.Right}
	case *RecursiveUnion:
		return []Node{p.Anchor, p.Recursive}
	}
	return nil
}

// freezeProducerAudit runs the final-tree census over inner (the EXPLAIN
// subject, not the Explain wrapper) and returns the frozen report. It must
// run after all planning rewrites and before Explain construction.
func freezeProducerAudit(sc *producerAuditSidecar, inner Node) *ProducerAuditReport {
	rep := &ProducerAuditReport{Token: producerAuditToken()}
	if sc == nil {
		rep.Stop = "no-sidecar"
		return rep
	}
	var ord uint64
	// descended guards the recursion against DAG sharing and recursive-CTE
	// cycles. A shared Join still gets one row per occurrence (Shared set,
	// same lineage) — occurrences are reported, descent happens once.
	descended := make(map[Node]bool)
	var walk func(n Node)
	walk = func(n Node) {
		if n == nil {
			return
		}
		if j, ok := n.(*Join); ok {
			ord++
			row := ProducerAuditRow{Ordinal: ord}
			row.Shared = descended[n]
			if j.Lateral {
				// R117 precedent (ledger=none): the decomposed lateral
				// probe bypasses the explicit-join constructor. It is
				// observed (rows/width read off the final node) but
				// out of the audited population — NOT a mapping
				// falsifier, and never a fabricated producer record.
				row.Mapping = "lateral-out-of-population"
				row.Jointype = joinTypeAuditName(j.Type)
				row.Algo = joinAlgoAuditName(j.Algo)
				row.Rows = EstimateRows(j)
				row.Width = TupleWidth(j.Output())
				rep.Rows = append(rep.Rows, row)
			} else {
				id, ok := sc.resolve(j)
				if !ok {
					row.Mapping = "zero-unmapped"
				} else {
					row.Mapping = "one-to-one"
					row.ConstructionID = id
					rec := sc.records[id]
					row.Route = rec.Route
					row.Jointype = rec.Jointype
					row.Algo = rec.Algo
					row.Trace = rec.Trace
					row.Columns = rec.Columns
					// Independent recomputation: the equation must reproduce
					// from the final node or the record is stale.
					var check JoinEstimateTrace
					if estimateJoinTraced(j, &check); check.FinalRows == rec.Trace.FinalRows &&
						check.RowsFloat == rec.Trace.RowsFloat {
						row.EquationOK = true
					}
					row.Rows = check.FinalRows
					row.Width = TupleWidth(j.Output())
					row.SchemaOK = row.Width == rec.FinalWidth && len(j.Output()) == len(rec.Columns)
				}
				rep.Rows = append(rep.Rows, row)
			}
		}
		if descended[n] {
			return
		}
		descended[n] = true
		for _, c := range producerAuditChildren(n) {
			walk(c)
		}
	}
	walk(inner)
	// Deterministic order for byte-identical renders.
	sort.Slice(rep.Rows, func(i, k int) bool { return rep.Rows[i].Ordinal < rep.Rows[k].Ordinal })
	return rep
}

// ProducerAuditTextLines renders the frozen report as EXPLAIN TEXT detail
// lines. Pure function of rep (no planner access). Exported for the
// executor renderer, which consumes ONLY frozen reports.
func ProducerAuditTextLines(rep *ProducerAuditReport) []string {
	if rep == nil {
		return nil
	}
	lines := []string{fmt.Sprintf("ProducerAudit: token=%s stop=%q joins=%d", rep.Token, rep.Stop, len(rep.Rows))}
	for _, r := range rep.Rows {
		lines = append(lines, fmt.Sprintf(
			"ProducerAudit: #%d id=%d map=%s shared=%v route=%s type=%s algo=%s rows=%d width=%d eq=%v schema=%v branch=%s",
			r.Ordinal, r.ConstructionID, r.Mapping, r.Shared, r.Route, r.Jointype, r.Algo,
			r.Rows, r.Width, r.EquationOK, r.SchemaOK, r.Trace.Branch))
	}
	return lines
}

// ProducerAuditJSONRows renders the frozen report as JSON-marshalable rows.
// Exported for the executor renderer (frozen reports only).
func ProducerAuditJSONRows(rep *ProducerAuditReport) []map[string]any {
	if rep == nil {
		return nil
	}
	out := make([]map[string]any, 0, len(rep.Rows)+1)
	out = append(out, map[string]any{"token": rep.Token, "stop": rep.Stop, "joins": len(rep.Rows)})
	for _, r := range rep.Rows {
		out = append(out, map[string]any{
			"ordinal": r.Ordinal, "construction": r.ConstructionID,
			"mapping": r.Mapping, "shared": r.Shared, "route": r.Route,
			"jointype": r.Jointype, "algo": r.Algo,
			"rows": r.Rows, "width": r.Width,
			"equation": r.EquationOK, "schema": r.SchemaOK,
			"branch": r.Trace.Branch,
		})
	}
	return out
}

// reproduceJoinEstimateEquation replays a recorded trace's final rows from
// its own inputs: the unit-testable form of the census EquationOK check.
// It mirrors estimateJoin's arithmetic exactly (including the floor and
// saturation helpers); any divergence between the two is a bug in one of
// them, which is the point of the test.
func reproduceJoinEstimateEquation(tr *JoinEstimateTrace) (int64, bool) {
	applyFloor := func(rows float64) float64 {
		switch tr.Jointype {
		case "left":
			if rows < float64(tr.L) {
				return float64(tr.L)
			}
		case "right":
			if rows < float64(tr.R) {
				return float64(tr.R)
			}
		case "full":
			if rows < float64(tr.L) {
				rows = float64(tr.L)
			}
			if rows < float64(tr.R) {
				rows = float64(tr.R)
			}
		}
		return rows
	}
	switch tr.Branch {
	case "degenerate":
		return 0, true
	case "cross":
		return tr.L * tr.R, true
	case "semi", "anti":
		sel := tr.MatchFrac * tr.ResidualSel
		if sel > 1 {
			sel = 1
		}
		if tr.Branch == "anti" {
			sel = 1 - sel
		}
		return scaleByFloat(tr.L, sel), true
	case "hash-merge-measured":
		sel := tr.SuperkeySel
		for _, s := range tr.PairSels {
			sel *= s
		}
		sel *= tr.ResidualSel
		rows := float64(tr.L) * float64(tr.R) * sel
		if !tr.RowsBoundInf && rows > tr.RowsBound {
			rows = tr.RowsBound
		}
		return saturateRowEst(applyFloor(rows)), true
	case "hash-merge-fallback":
		est := scaleByFloat(tr.L*tr.R, defaultEqSelectivity)
		if est < 1 {
			return 1, true
		}
		mx := tr.L
		capped := false
		if tr.R > mx {
			mx = tr.R
		}
		if est > mx {
			est = mx
			capped = true
		}
		if capped != tr.Capped {
			return 0, false
		}
		return saturateRowEst(applyFloor(float64(est))), true
	}
	return 0, false
}

// nanGuardedFloat keeps non-finite floats out of frozen reports (NaN/Inf
// would poison JSON marshaling and checksum stability).
func nanGuardedFloat(f float64) float64 {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return f
}
