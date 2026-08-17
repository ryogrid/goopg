package nodes

import (
	"reflect"
	"testing"
)

// goldenCases pins the canonical pg_node_tree text against real PostgreSQL 18.3
// output. Each `want` string was captured verbatim from a live PG18 server via
//
//	CREATE TABLE t(a int DEFAULT 42, b int DEFAULT -1, c oid DEFAULT 16384,
//	               d text DEFAULT 'x', e int DEFAULT 40+2,
//	               f text DEFAULT upper('x'), g bigint DEFAULT <int8-max>,
//	               h bool DEFAULT true);
//	SELECT a.attname, ad.adbin FROM pg_attrdef ad
//	  JOIN pg_attribute a ON a.attrelid=ad.adrelid AND a.attnum=ad.adnum
//	  WHERE ad.adrelid='t'::regclass ORDER BY a.attnum;
//
// pg_attrdef.adbin is exactly nodeToString(<default expr>), so byte-equality
// here is the adversarial oracle the M0123-S1 gate requires.
var goldenCases = []struct {
	name string
	node Node
	want string
}{
	{
		// a int DEFAULT 42
		name: "int4_positive",
		node: NewInt4Const(42),
		want: `{CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 42 0 0 0 0 0 0 0 ]}`,
	},
	{
		// b int DEFAULT -1 — negative int4 sign-extends across all 8 datum bytes.
		name: "int4_negative_sign_extend",
		node: NewInt4Const(-1),
		want: `{CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ -1 -1 -1 -1 -1 -1 -1 -1 ]}`,
	},
	{
		// c oid DEFAULT 16384 — the int4 literal is wrapped in a RelabelType to oid.
		name: "relabel_int4_to_oid",
		node: &RelabelType{
			Arg:           NewInt4Const(16384),
			Resulttype:    OidOid,
			Resulttypmod:  -1,
			Resultcollid:  0,
			Relabelformat: 2,
			Location:      -1,
		},
		want: `{RELABELTYPE :arg {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 0 64 0 0 0 0 0 0 ]} :resulttype 26 :resulttypmod -1 :resultcollid 0 :relabelformat 2 :location -1}`,
	},
	{
		// d text DEFAULT 'x' — by-ref varlena, 4-byte header (5<<2=20) + 'x'(120).
		name: "text_varlena",
		node: NewTextConst("x"),
		want: `{CONST :consttype 25 :consttypmod -1 :constcollid 100 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 5 [ 20 0 0 0 120 ]}`,
	},
	{
		// e int DEFAULT 40+2 — int4 + int4 operator (opno 551, opfuncid 177).
		name: "opexpr_int4_add",
		node: &OpExpr{
			Opno:         551,
			Opfuncid:     177,
			Opresulttype: OidInt4,
			Opretset:     false,
			Opcollid:     0,
			Inputcollid:  0,
			Args:         []Node{NewInt4Const(40), NewInt4Const(2)},
			Location:     -1,
		},
		want: `{OPEXPR :opno 551 :opfuncid 177 :opresulttype 23 :opretset false :opcollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 40 0 0 0 0 0 0 0 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 2 0 0 0 0 0 0 0 ]}) :location -1}`,
	},
	{
		// f text DEFAULT upper('x') — funcid 871 (upper), collation 100.
		name: "funcexpr_upper",
		node: &FuncExpr{
			Funcid:         871,
			Funcresulttype: OidText,
			Funcretset:     false,
			Funcvariadic:   false,
			Funcformat:     0,
			Funccollid:     DefaultCollationOid,
			Inputcollid:    DefaultCollationOid,
			Args:           []Node{NewTextConst("x")},
			Location:       -1,
		},
		want: `{FUNCEXPR :funcid 871 :funcresulttype 25 :funcretset false :funcvariadic false :funcformat 0 :funccollid 100 :inputcollid 100 :args ({CONST :consttype 25 :consttypmod -1 :constcollid 100 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 5 [ 20 0 0 0 120 ]}) :location -1}`,
	},
	{
		// g bigint DEFAULT 9223372036854775807 — full 8-byte int8 datum word.
		name: "int8_max",
		node: NewInt8Const(9223372036854775807),
		want: `{CONST :consttype 20 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ -1 -1 -1 -1 -1 -1 -1 127 ]}`,
	},
	{
		// h bool DEFAULT true — constlen 1 but 8 datum bytes are still emitted.
		name: "bool_true",
		node: NewBoolConst(true),
		want: `{CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 1 0 0 0 0 0 0 0 ]}`,
	},
}

// TestOutMatchesRealPGGolden asserts Out produces byte-identical text to the
// real-PG18 pg_attrdef.adbin strings above.
func TestOutMatchesRealPGGolden(t *testing.T) {
	for _, tc := range goldenCases {
		t.Run(tc.name, func(t *testing.T) {
			got := Out(tc.node)
			if got != tc.want {
				t.Fatalf("Out mismatch:\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
}

// TestReadRoundTrip asserts Read(golden) reconstructs an IR deep-equal to the
// hand-built node, closing the round trip text -> IR -> text.
func TestReadRoundTrip(t *testing.T) {
	for _, tc := range goldenCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Read(tc.want)
			if err != nil {
				t.Fatalf("Read error: %v", err)
			}
			if !reflect.DeepEqual(got, tc.node) {
				t.Fatalf("Read IR mismatch:\n got: %#v\nwant: %#v", got, tc.node)
			}
			// And re-serializing the parsed IR must reproduce the golden.
			if reOut := Out(got); reOut != tc.want {
				t.Fatalf("re-Out mismatch:\n got: %s\nwant: %s", reOut, tc.want)
			}
		})
	}
}

// TestReadRejectsUnsupportedTag ensures an unknown node tag is a clean error so
// callers can fall back to SQL text rather than mis-parsing.
func TestReadRejectsUnsupportedTag(t *testing.T) {
	if _, err := Read(`{CASEEXPR :casetype 23}`); err == nil {
		t.Fatal("expected error for unsupported tag, got nil")
	}
}

// TestReadNullConst covers an is-null Const (constvalue "<>").
func TestReadNullConst(t *testing.T) {
	want := `{CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull true :location -1 :constvalue <>}`
	n, err := Read(want)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	c, ok := n.(*Const)
	if !ok || !c.ConstIsNull || c.Datum != nil {
		t.Fatalf("unexpected null Const: %#v", n)
	}
	if got := Out(c); got != want {
		t.Fatalf("null Const round-trip mismatch:\n got: %s\nwant: %s", got, want)
	}
}

// TestOutListBareList covers the top-level List shape used by
// pg_proc.proargdefaults (see initdb/pg_proc_seed_defaults.go). A List is NOT
// wrapped in braces — nodeToString emits "(elem elem)" — and a NIL List emits
// "<>", the same token outNode uses for a nil node.
func TestOutListBareList(t *testing.T) {
	if got := OutList(nil); got != "<>" {
		t.Errorf("OutList(nil) = %q, want %q", got, "<>")
	}
	if got := OutList([]Node{}); got != "<>" {
		t.Errorf("OutList(empty) = %q, want %q", got, "<>")
	}
	one := OutList([]Node{NewInt4Const(5)})
	if want := "(" + Out(NewInt4Const(5)) + ")"; one != want {
		t.Errorf("OutList(1 elem) = %q, want %q", one, want)
	}
	two := OutList([]Node{NewBoolConst(false), NewBoolConst(true)})
	if want := "(" + Out(NewBoolConst(false)) + " " + Out(NewBoolConst(true)) + ")"; two != want {
		t.Errorf("OutList(2 elems) = %q, want %q", two, want)
	}
}
