package optimizer

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// dateDimLikeCatalog seeds the two columns the M0125-0009 reproducer needs.
// Named after TPC-DS date_dim, where the defect was measured.
func dateDimLikeCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "date_dim"}, []catalog.Column{
		{Name: "d_day_name", Type: catalog.Type{Name: "text"}, Ordinal: 0},
		{Name: "d_dom", Type: catalog.Type{Name: "int4"}, Ordinal: 1},
		{Name: "d_year", Type: catalog.Type{Name: "int4"}, Ordinal: 2},
	}); err != nil {
		t.Fatal(err)
	}
	return c
}

func planAggregate(t *testing.T, c catalog.Catalog, sql string) *Aggregate {
	t.Helper()
	plan, err := Plan(parseOne(t, sql), c)
	if err != nil {
		t.Fatalf("Plan(%q): %v", sql, err)
	}
	var found *Aggregate
	var walk func(n Node)
	walk = func(n Node) {
		if n == nil || found != nil {
			return
		}
		switch x := n.(type) {
		case *Aggregate:
			found = x
		case *Project:
			walk(x.Child)
		case *Filter:
			walk(x.Child)
		case *Sort:
			walk(x.Child)
		case *Limit:
			walk(x.Child)
		}
	}
	walk(plan)
	if found == nil {
		t.Fatalf("no Aggregate node in plan for %q (%T)", sql, plan)
	}
	return found
}

// TestSiblingCaseAggregatesGetDistinctSlots is the M0125-0009 reproducer.
//
// parserExprKey's fallback was `fmt.Sprintf("expr:%T", e)` — the Go type name
// with no expression content — so every *parser.CaseExpr hashed to one key and
// aggregateCallKey could not tell two `sum(CASE …)` siblings apart. The second
// was dropped as a duplicate at planner.go's aggByKey check and its output
// column read the FIRST aggregate's slot, so on the SF=1 cluster
//
//	select sum(case when d_day_name='Sunday' then 1 else 0 end),
//	       sum(case when d_day_name='Monday' then 1 else 0 end) from date_dim;
//
// returned goopg `10435|10435` against PG's `10435|10436` — a wrong answer with
// the row count intact. Ten TPC-DS queries carried it (Q2 Q21 Q40 Q43 Q50 Q59
// Q62 Q66 Q97 Q99).
func TestSiblingCaseAggregatesGetDistinctSlots(t *testing.T) {
	c := dateDimLikeCatalog(t)
	agg := planAggregate(t, c, `SELECT `+
		`sum(case when d_day_name='Sunday' then 1 else 0 end), `+
		`sum(case when d_day_name='Monday' then 1 else 0 end), `+
		`sum(case when d_day_name='Tuesday' then 1 else 0 end) `+
		`FROM date_dim`)
	if len(agg.Aggs) != 3 {
		t.Fatalf("got %d aggregate slots, want 3 (one per distinct CASE): %+v", len(agg.Aggs), agg.Aggs)
	}
}

// TestIdenticalCaseAggregatesStillDedup is the other half of the contract: the
// structural key must still COLLAPSE two textually identical aggregates, or the
// fix would have traded a wrong answer for redundant computation (and, for the
// GROUP BY path below, for a spurious 42803).
func TestIdenticalCaseAggregatesStillDedup(t *testing.T) {
	c := dateDimLikeCatalog(t)
	agg := planAggregate(t, c, `SELECT `+
		`sum(case when d_day_name='Sunday' then 1 else 0 end), `+
		`sum(case when d_day_name='Sunday' then 1 else 0 end) `+
		`FROM date_dim`)
	if len(agg.Aggs) != 1 {
		t.Fatalf("got %d aggregate slots, want 1 (both targets are the same aggregate): %+v", len(agg.Aggs), agg.Aggs)
	}
}

// TestGroupByMatchesStructurallyIdenticalCase pins the regression risk the
// structural key introduces. The old type-name key made ANY CaseExpr match ANY
// other, which made `GROUP BY <case>` work by accident. The structural key must
// keep it working on purpose: the SELECT-list copy and the GROUP BY copy of one
// expression differ only in their source offset, which lives in the unexported
// `pos` field the walk skips.
func TestGroupByMatchesStructurallyIdenticalCase(t *testing.T) {
	c := dateDimLikeCatalog(t)
	sql := `SELECT case when d_dom > 15 then 'late' else 'early' end, count(*) ` +
		`FROM date_dim GROUP BY case when d_dom > 15 then 'late' else 'early' end`
	if _, err := Plan(parseOne(t, sql), c); err != nil {
		t.Fatalf("GROUP BY over an identical CASE must plan, got: %v", err)
	}
}

// TestFuncCallTailDistinguishesOrderedSetAggregates covers the same
// content-dropping bug in the one place parserExprKey enumerated explicitly:
// the FuncCall case (and aggregateCallKey) built their key from name+args
// alone, so two aggregates differing only in the in-argument ORDER BY collapsed
// onto one slot. M0097-0032 had already patched FILTER as a single instance;
// funcCallTailKey folds in FILTER, OVER, ORDER BY, WITHIN GROUP and VARIADIC.
func TestFuncCallTailDistinguishesOrderedSetAggregates(t *testing.T) {
	c := dateDimLikeCatalog(t)
	agg := planAggregate(t, c,
		`SELECT string_agg(d_day_name, ',' ORDER BY d_dom), `+
			`string_agg(d_day_name, ',' ORDER BY d_year) FROM date_dim`)
	if len(agg.Aggs) != 2 {
		t.Fatalf("got %d aggregate slots, want 2 (ORDER BY differs): %+v", len(agg.Aggs), agg.Aggs)
	}
}

// ---------------------------------------------------------------------------
// Exhaustiveness gate
// ---------------------------------------------------------------------------

// allExprTypes lists one instance of every parser.Expr implementation.
// TestExprTypeRegistryIsExhaustive scans internal/parser for `exprNode()`
// receivers and fails if this list falls behind, so a newly added expression
// type cannot silently re-open M0125-0009.
var allExprTypes = []parser.Expr{
	&parser.ArrayConstructorExpr{},
	&parser.ArraySubqueryExpr{},
	&parser.ArraySubscriptExpr{},
	&parser.BinaryOp{},
	&parser.BooleanConst{},
	&parser.CaseExpr{},
	&parser.CastExpr{},
	&parser.CollateExpr{},
	&parser.ColumnRef{},
	&parser.DefaultMarker{},
	&parser.ExistsExpr{},
	&parser.ExtractExpr{},
	&parser.FuncCall{},
	&parser.GroupingCall{},
	&parser.InExpr{},
	&parser.IndirectionStar{},
	&parser.IntegerConst{},
	&parser.IntervalLit{},
	&parser.IsBoolExpr{},
	&parser.IsDistinctFromExpr{},
	&parser.IsNullExpr{},
	&parser.LikeEscapePattern{},
	&parser.NullConst{},
	&parser.NumericConst{},
	&parser.ParamRef{},
	&parser.PartitionRangeBoundKeyword{},
	&parser.RowExpr{},
	&parser.ScalarSublinkExpr{},
	&parser.SimilarToPattern{},
	&parser.StarExpr{},
	&parser.StringConst{},
	&parser.SubqueryExpr{},
	&parser.TypedStringLit{},
	&parser.UnaryOp{},
}

// keyInsensitiveFields lists (type, field) pairs that parserExprKey drops ON
// PURPOSE. Every other exported field of every parser.Expr must change the key
// — that is the invariant M0125-0009 violated. Adding an entry here is a
// deliberate, reviewable act; forgetting one is what caused the defect.
var keyInsensitiveFields = map[string]string{
	// M0097-0003: the qualifier is dropped so `lower(c)` in the SELECT list
	// matches `lower(t.c)` in GROUP BY, mirroring PG's transformGroupClause
	// resolving both to the same Var.
	"parser.ColumnRef.Schema": "qualifier deliberately ignored (M0097-0003)",
	"parser.ColumnRef.Table":  "qualifier deliberately ignored (M0097-0003)",
	// M0134-0001 P3b: source offset of the `within` keyword, used only for the
	// error-position caret. Same exclusion as the unexported `pos` field: two
	// aggregate calls differing only in offset must key equal.
	"parser.FuncCall.WithinGroupPos": "source offset, error-position only (M0134-0001 P3b)",
}

func TestExprTypeRegistryIsExhaustive(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "parser", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`func \([A-Za-z_]* ?\*?([A-Za-z0-9_]+)\) exprNode\(\)`)
	declared := map[string]bool{}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			declared[m[1]] = true
		}
	}
	if len(declared) == 0 {
		t.Fatal("found no exprNode() receivers — the scan regexp is broken, not the code")
	}
	registered := map[string]bool{}
	for _, e := range allExprTypes {
		registered[reflect.TypeOf(e).Elem().Name()] = true
	}
	var missing, extra []string
	for name := range declared {
		if !registered[name] {
			missing = append(missing, name)
		}
	}
	for name := range registered {
		if !declared[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Errorf("parser.Expr types absent from allExprTypes: %v\n"+
			"Add them, then make sure TestParserExprKeyUsesEveryField passes for them — "+
			"an expression type whose content is not in the key silently compares EQUAL "+
			"to a different instance of itself (M0125-0009).", missing)
	}
	if len(extra) > 0 {
		t.Errorf("allExprTypes lists types that no longer implement parser.Expr: %v", extra)
	}
}

// TestParserExprKeyUsesEveryField asserts, for every registered expression type
// and every exported field of it, that changing the field changes the key.
// This is the generic form of the M0125-0009 defect: `CaseExpr` had NO field in
// its key, so all instances collided; `CastExpr.Typmods` and `FuncCall.OrderBy`
// were narrower instances of the same thing.
func TestParserExprKeyUsesEveryField(t *testing.T) {
	for _, proto := range allExprTypes {
		pt := reflect.TypeOf(proto).Elem()
		zero := reflect.New(pt)
		baseKey := parserExprKey(zero.Interface().(parser.Expr))
		for i := 0; i < pt.NumField(); i++ {
			f := pt.Field(i)
			if f.PkgPath != "" {
				continue // unexported: `pos`, deliberately not in the key
			}
			name := pt.String() + "." + f.Name
			mutated, ok := mutatedFieldValue(f.Type)
			if !ok {
				t.Errorf("%s: test cannot synthesise a value of type %s — extend mutatedFieldValue", name, f.Type)
				continue
			}
			inst := reflect.New(pt)
			inst.Elem().Field(i).Set(mutated)
			gotKey := parserExprKey(inst.Interface().(parser.Expr))
			differs := gotKey != baseKey
			why, exempt := keyInsensitiveFields[name]
			switch {
			case !differs && !exempt:
				t.Errorf("%s does not affect parserExprKey (both %q) — two expressions "+
					"differing only in this field collapse onto one aggregate/GROUP BY slot "+
					"(M0125-0009). Fix the key, or add the field to keyInsensitiveFields "+
					"with a reason.", name, baseKey)
			case differs && exempt:
				t.Errorf("%s now affects the key but is listed in keyInsensitiveFields (%q) — "+
					"drop the stale exemption", name, why)
			}
		}
	}
}

// mutatedFieldValue returns a non-zero value of ft, distinct from ft's zero
// value in a way the key must observe.
func mutatedFieldValue(ft reflect.Type) (reflect.Value, bool) {
	switch ft.Kind() {
	case reflect.Bool:
		return reflect.ValueOf(true).Convert(ft), true
	case reflect.String:
		return reflect.ValueOf("zzmut").Convert(ft), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return reflect.ValueOf(int64(7)).Convert(ft), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return reflect.ValueOf(uint64(7)).Convert(ft), true
	case reflect.Float32, reflect.Float64:
		return reflect.ValueOf(7.5).Convert(ft), true
	case reflect.Interface:
		// Every interface-typed field in the AST is parser.Expr (or a
		// supertype of it), so a ColumnRef is always assignable.
		v := reflect.ValueOf(parser.Expr(&parser.ColumnRef{Column: "zzmut"}))
		if !v.Type().AssignableTo(ft) {
			return reflect.Value{}, false
		}
		return v, true
	case reflect.Pointer:
		// Non-nil vs nil is itself an observable difference.
		return reflect.New(ft.Elem()), true
	case reflect.Slice:
		elem, ok := mutatedFieldValue(ft.Elem())
		if !ok {
			return reflect.Value{}, false
		}
		s := reflect.MakeSlice(ft, 1, 1)
		s.Index(0).Set(elem)
		return s, true
	case reflect.Array:
		a := reflect.New(ft).Elem()
		if ft.Len() == 0 {
			return reflect.Value{}, false
		}
		elem, ok := mutatedFieldValue(ft.Elem())
		if !ok {
			return reflect.Value{}, false
		}
		a.Index(0).Set(elem)
		return a, true
	case reflect.Struct:
		// Mutate the first exported field that can be synthesised, so the
		// struct as a whole differs from its zero value.
		v := reflect.New(ft).Elem()
		for i := 0; i < ft.NumField(); i++ {
			f := ft.Field(i)
			if f.PkgPath != "" {
				continue
			}
			sub, ok := mutatedFieldValue(f.Type)
			if !ok {
				continue
			}
			v.Field(i).Set(sub)
			return v, true
		}
		return reflect.Value{}, false
	}
	return reflect.Value{}, false
}

// TestStructuralKeyIsStableAndContentBearing guards two properties of the walk
// itself: it is deterministic (same input, same key — a map field rendered in
// hash order would break every dedup decision non-reproducibly), and it never
// falls back to a content-free rendering.
func TestStructuralKeyIsStableAndContentBearing(t *testing.T) {
	stmts, err := parser.Parse(
		`SELECT case when d_dom > 15 then 'late' else 'early' end FROM date_dim`)
	if err != nil {
		t.Fatal(err)
	}
	sel := stmts[0].(*parser.SelectStmt)
	e := sel.Targets[0].Expr
	k1 := parserExprKey(e)
	k2 := parserExprKey(e)
	if k1 != k2 {
		t.Fatalf("key not deterministic: %q vs %q", k1, k2)
	}
	// The old fallback was exactly "expr:*parser.CaseExpr" — content-free.
	if !strings.Contains(k1, "late") || !strings.Contains(k1, "early") {
		t.Fatalf("structural key %q does not carry the CASE's literals", k1)
	}
}
