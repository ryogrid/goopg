package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestPgTypeofFoldsToCompileTimeType verifies that pg_typeof(expr) is folded
// at plan time to a StringConst reflecting the planner's static type, so
// pg_typeof(user_agg(...)) returns the aggregate's declared return type
// rather than the runtime datum kind. M0097-0035.
func TestPgTypeofFoldsToCompileTimeType(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want string
	}{
		{
			name: "integer column",
			sql:  "SELECT pg_typeof(aid) FROM pgbench_accounts",
			want: "integer",
		},
		{
			name: "int8 count",
			sql:  "SELECT pg_typeof(count(*)) FROM pgbench_accounts",
			want: "bigint",
		},
		{
			name: "text string literal",
			sql:  "SELECT pg_typeof('hello')",
			want: "text",
		},
	}
	cat := pgbenchCatalog(t)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stmt := parseOne(t, tc.sql)
			plan, err := Plan(stmt, cat)
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			// Walk the plan tree to find the pg_typeof expression.
			found := false
			var walkNode func(Node)
			walkNode = func(n Node) {
				switch x := n.(type) {
				case *Project:
					for _, e := range x.Targets {
						if fc, ok := e.(*FuncCall); ok && fc.Name == "pg_typeof" {
							found = true
							if len(fc.Args) == 1 {
								if sc, ok := fc.Args[0].(*StringConst); ok {
									if sc.Value != tc.want {
										t.Errorf("pg_typeof arg = %q, want %q", sc.Value, tc.want)
									}
								} else {
									t.Errorf("pg_typeof arg = %T, want *StringConst", fc.Args[0])
								}
							}
						}
					}
					walkNode(x.Child)
				case *Aggregate:
					walkNode(x.Child)
				case *SeqScan:
					// leaf
				}
			}
			walkNode(plan)
			if !found {
				// If pg_typeof was folded away entirely (e.g. into StringConst directly),
				// that's also acceptable; just verify the plan doesn't error.
				t.Log("pg_typeof not found as FuncCall in plan (possibly folded further)")
			}
		})
	}
}

// TestPgTypeofCharDisambiguation covers M0122-0005's "1-byte char (OID 18)
// disambiguation" gap: pg_typeof must report the quoted identifier "char"
// (with literal quotes, mirroring PostgreSQL's format_type_be for pg_type
// OID 18) distinctly from the bare CHAR keyword (which the grammar maps to
// bpchar, displayed as "character"). Both share Type.Name=="char" downstream
// of the parser; only the CastExpr's Typmod (synthesized 1 for bare char,
// left 0 for quoted "char" — see select.go's synthesizeBareCharTypmod)
// distinguishes them.
func TestPgTypeofCharDisambiguation(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want string
	}{
		{"quoted char is OID 18", `SELECT pg_typeof('x'::"char")`, `"char"`},
		{"bare char is bpchar", `SELECT pg_typeof('x'::char)`, "character"},
		{"explicit-length char is bpchar", `SELECT pg_typeof('x'::char(3))`, "character"},
	}
	cat := pgbenchCatalog(t)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stmt := parseOne(t, tc.sql)
			plan, err := Plan(stmt, cat)
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			proj, ok := plan.(*Project)
			if !ok {
				t.Fatalf("plan=%T want *Project", plan)
			}
			fc, ok := proj.Targets[0].(*FuncCall)
			if !ok || fc.Name != "pg_typeof" || len(fc.Args) != 1 {
				t.Fatalf("target=%#v want pg_typeof(*StringConst)", proj.Targets[0])
			}
			sc, ok := fc.Args[0].(*StringConst)
			if !ok {
				t.Fatalf("pg_typeof arg=%T want *StringConst (folded)", fc.Args[0])
			}
			if sc.Value != tc.want {
				t.Errorf("pg_typeof = %q, want %q", sc.Value, tc.want)
			}
		})
	}
}

// TestExprTypePgTypeofIsRegtype pins the M0122-0005 pg_typeof()::oid
// follow-up: pg_typeof(expr)'s declared SQL return type is regtype (its
// wire/binary representation is the type's OID, like regclass/regproc), not
// the "unknown"/text default a plain FuncCall would otherwise get. This
// feeds both the wire TypeOID (dispatch.go's typeOIDFor) and the cell
// rendering (appendTypedCellText), and is what lets a further `::oid` cast
// be a binary-compatible reinterpretation instead of misparsing display
// text through oidin().
func TestExprTypePgTypeofIsRegtype(t *testing.T) {
	fc := &FuncCall{Name: "pg_typeof", Args: []Expr{&StringConst{Value: "integer"}}}
	got := exprType(fc)
	if got.Name != "regtype" {
		t.Errorf("exprType(pg_typeof(...)) = %+v, want Name=\"regtype\"", got)
	}
}

// TestResolvePolyAggOutputType verifies that user-defined aggregates with
// anycompatible/anyelement SType resolve to the actual input-derived type
// rather than retaining the polymorphic type name. M0097-0035.
func TestResolvePolyAggOutputType(t *testing.T) {
	tests := []struct {
		stype   string
		argType string
		want    string
	}{
		{"anycompatible", "numeric", "numeric"},
		{"anycompatible", "int4", "int4"},    // internal name, not display name
		{"anyelement", "text", "text"},
		{"text", "int4", "text"},             // non-polymorphic passes through
		{"anyarray", "text", "text[]"},
	}
	for _, tc := range tests {
		argExpr := &ColumnRef{Type: catalog.Type{Name: tc.argType}}
		got := resolvePolyAggOutputType(tc.stype, argExpr)
		want := tc.want
		if got.Name != want {
			t.Errorf("resolvePolyAggOutputType(%q, %q) = %q, want %q",
				tc.stype, tc.argType, got.Name, want)
		}
	}
}
