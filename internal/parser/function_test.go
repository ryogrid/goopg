package parser

import (
	"reflect"
	"strings"
	"testing"
)

// TestParseCreateFunctionMinimal pins the smallest acceptable form:
// CREATE FUNCTION name() RETURNS rettype LANGUAGE plpgsql AS $$ ... $$.
// Verifies every AST field gets populated and the body is captured
// verbatim.
func TestParseCreateFunctionMinimal(t *testing.T) {
	src := `CREATE FUNCTION add_one() RETURNS int LANGUAGE plpgsql AS $$ BEGIN RETURN 1; END $$`
	stmts, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(stmts) != 1 {
		t.Fatalf("got %d statements, want 1", len(stmts))
	}
	cf, ok := stmts[0].(*CreateFunctionStmt)
	if !ok {
		t.Fatalf("got %T, want *CreateFunctionStmt", stmts[0])
	}
	if cf.OrReplace {
		t.Errorf("OrReplace = true, want false")
	}
	if cf.Name.Name != "add_one" {
		t.Errorf("Name.Name = %q, want add_one", cf.Name.Name)
	}
	if len(cf.Args) != 0 {
		t.Errorf("Args len = %d, want 0", len(cf.Args))
	}
	if cf.ReturnType.Name != "int" {
		t.Errorf("ReturnType.Name = %q, want int", cf.ReturnType.Name)
	}
	if cf.Language != "plpgsql" {
		t.Errorf("Language = %q, want plpgsql", cf.Language)
	}
	if !strings.Contains(cf.Body, "RETURN 1") {
		t.Errorf("Body = %q, missing RETURN 1", cf.Body)
	}
}

// TestParseCreateFunctionOrReplace pins the OR REPLACE flag.
func TestParseCreateFunctionOrReplace(t *testing.T) {
	src := `CREATE OR REPLACE FUNCTION f() RETURNS void LANGUAGE plpgsql AS $$ BEGIN END $$`
	stmts, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cf := stmts[0].(*CreateFunctionStmt)
	if !cf.OrReplace {
		t.Error("OrReplace = false, want true")
	}
}

// TestParseCreateFunctionParallel pins the proparallel marker captured from
// the PARALLEL SAFE/RESTRICTED/UNSAFE clause. The default (no clause) is "u"
// (unsafe), matching PG's CREATE FUNCTION default. DU-002 slice 150.
func TestParseCreateFunctionParallel(t *testing.T) {
	cases := []struct {
		src  string
		want string
	}{
		{`CREATE FUNCTION f() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$`, "u"},
		{`CREATE FUNCTION f() RETURNS int LANGUAGE sql PARALLEL SAFE AS $$ SELECT 1 $$`, "s"},
		{`CREATE FUNCTION f() RETURNS int LANGUAGE sql PARALLEL RESTRICTED AS $$ SELECT 1 $$`, "r"},
		{`CREATE FUNCTION f() RETURNS int LANGUAGE sql PARALLEL UNSAFE AS $$ SELECT 1 $$`, "u"},
	}
	for _, tc := range cases {
		stmts, err := Parse(tc.src)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.src, err)
		}
		cf := stmts[0].(*CreateFunctionStmt)
		if cf.Parallel != tc.want {
			t.Errorf("Parallel = %q, want %q (src=%q)", cf.Parallel, tc.want, tc.src)
		}
	}
}

// TestParseCreateFunctionCostRows pins the procost/prorows numeric values
// captured from the COST/ROWS clauses. Both were previously parsed and then
// discarded, so an explicit COST/ROWS was silently reset to the language /
// SRF default on dump. Empty = no clause given (use the default). DU-002 slice 151.
func TestParseCreateFunctionCostRows(t *testing.T) {
	cases := []struct {
		src      string
		wantCost string
		wantRows string
	}{
		{`CREATE FUNCTION f() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$`, "", ""},
		{`CREATE FUNCTION f() RETURNS int LANGUAGE sql COST 50 AS $$ SELECT 1 $$`, "50", ""},
		{`CREATE FUNCTION f() RETURNS SETOF int LANGUAGE sql ROWS 5 AS $$ SELECT 1 $$`, "", "5"},
		{`CREATE FUNCTION f() RETURNS SETOF int LANGUAGE sql COST 0.5 ROWS 200 AS $$ SELECT 1 $$`, "0.5", "200"},
	}
	for _, tc := range cases {
		stmts, err := Parse(tc.src)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.src, err)
		}
		cf := stmts[0].(*CreateFunctionStmt)
		if cf.Cost != tc.wantCost {
			t.Errorf("Cost = %q, want %q (src=%q)", cf.Cost, tc.wantCost, tc.src)
		}
		if cf.Rows != tc.wantRows {
			t.Errorf("Rows = %q, want %q (src=%q)", cf.Rows, tc.wantRows, tc.src)
		}
	}
}

// TestParseCreateFunctionWithArgs pins the named-arg surface and the
// implicit-IN mode. Two args with explicit names + one anonymous
// positional arg.
func TestParseCreateFunctionWithArgs(t *testing.T) {
	src := `CREATE FUNCTION add(a int, b int, text) RETURNS int LANGUAGE plpgsql AS $$ BEGIN END $$`
	stmts, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cf := stmts[0].(*CreateFunctionStmt)
	if len(cf.Args) != 3 {
		t.Fatalf("Args len = %d, want 3", len(cf.Args))
	}
	if cf.Args[0].Name != "a" || cf.Args[0].Type.Name != "int" {
		t.Errorf("Args[0] = %+v, want name=a type=int", cf.Args[0])
	}
	if cf.Args[1].Name != "b" || cf.Args[1].Type.Name != "int" {
		t.Errorf("Args[1] = %+v, want name=b type=int", cf.Args[1])
	}
	if cf.Args[2].Name != "" || cf.Args[2].Type.Name != "text" {
		t.Errorf("Args[2] = %+v, want anonymous type=text", cf.Args[2])
	}
	for i, a := range cf.Args {
		if a.Mode != FuncArgIn {
			t.Errorf("Args[%d].Mode = %d, want FuncArgIn", i, a.Mode)
		}
	}
}

// TestParseCreateFunctionExplicitIN pins that an explicit `IN`
// keyword on an argument is accepted (Stage A treats it as a no-op
// — same as the default).
func TestParseCreateFunctionExplicitIN(t *testing.T) {
	src := `CREATE FUNCTION f(IN x int) RETURNS int LANGUAGE plpgsql AS $$ BEGIN END $$`
	stmts, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cf := stmts[0].(*CreateFunctionStmt)
	if len(cf.Args) != 1 || cf.Args[0].Name != "x" {
		t.Fatalf("Args = %+v, want one arg named x", cf.Args)
	}
}

// TestParseCreateFunctionMultiWordArgTypes pins that a multi-word built-in
// type in a function argument is consumed as ONE type, not misread as
// `arg_name type`. Regression for M0119-0006 (74th slice deferral): the
// generic `ident ident` heuristic in parseArgNameAndType read `bit varying`
// as an argument named "bit" of type "varying" (and `double precision` →
// name "double" type "precision"), while `timestamp with time zone` — whose
// continuation is the KwWith keyword — was a syntax error. PostgreSQL's
// grammar (gram.y func_type → Typename) parses the whole spelling as the
// single type. The `name type` form with a real arg name is unaffected.
func TestParseCreateFunctionMultiWordArgTypes(t *testing.T) {
	cases := []struct {
		src      string
		name     string
		typeName string
		args     []int64
	}{
		{`CREATE FUNCTION f(bit varying) RETURNS void LANGUAGE sql AS $$ SELECT 1 $$`, "", "varbit", nil},
		{`CREATE FUNCTION f(character varying) RETURNS void LANGUAGE sql AS $$ SELECT 1 $$`, "", "varchar", nil},
		{`CREATE FUNCTION f(double precision) RETURNS void LANGUAGE sql AS $$ SELECT 1 $$`, "", "float8", nil},
		{`CREATE FUNCTION f(timestamp with time zone) RETURNS void LANGUAGE sql AS $$ SELECT 1 $$`, "", "timestamptz", nil},
		{`CREATE FUNCTION f(time with time zone) RETURNS void LANGUAGE sql AS $$ SELECT 1 $$`, "", "timetz", nil},
		// Interval field qualifier is a multi-word type, not `name type`;
		// the packed INTERVAL_TYPMOD rides in Args (YEAR TO MONTH, full prec).
		{`CREATE FUNCTION f(interval year to month) RETURNS void LANGUAGE sql AS $$ SELECT 1 $$`, "", "interval", []int64{(6 << 16) | 0xFFFF}},
		// The `name type` form still wins when the name is present.
		{`CREATE FUNCTION f(a bit) RETURNS void LANGUAGE sql AS $$ SELECT 1 $$`, "a", "bit", nil},
		{`CREATE FUNCTION f(a double precision) RETURNS void LANGUAGE sql AS $$ SELECT 1 $$`, "a", "float8", nil},
		{`CREATE FUNCTION f(b timestamp with time zone) RETURNS void LANGUAGE sql AS $$ SELECT 1 $$`, "b", "timestamptz", nil},
	}
	for _, tc := range cases {
		stmts, err := Parse(tc.src)
		if err != nil {
			t.Errorf("parse %q: %v", tc.src, err)
			continue
		}
		cf := stmts[0].(*CreateFunctionStmt)
		if len(cf.Args) != 1 {
			t.Errorf("%q: Args len = %d, want 1", tc.src, len(cf.Args))
			continue
		}
		if cf.Args[0].Name != tc.name {
			t.Errorf("%q: arg Name = %q, want %q", tc.src, cf.Args[0].Name, tc.name)
		}
		if cf.Args[0].Type.Name != tc.typeName {
			t.Errorf("%q: type Name = %q, want %q", tc.src, cf.Args[0].Type.Name, tc.typeName)
		}
		if !reflect.DeepEqual(cf.Args[0].Type.Args, tc.args) {
			t.Errorf("%q: type Args = %v, want %v", tc.src, cf.Args[0].Type.Args, tc.args)
		}
	}
}

// TestParseCreateFunctionCharFamilyArgTypes pins the SQL national-character
// aliases (M0119-0006, deferral row 1351 first half): `char varying`,
// `nchar [varying]`, `national character|char [varying]` are all accepted as
// bare types in function args and collapse to the SAME canonical names as
// their plain equivalents (`character varying`→varchar, `character`→bpchar
// spelled as "character"). Verified against PG 18.3: every spelling below
// creates a function whose oid::regprocedure renders byte-identically to the
// canonical spelling (`f_nchar` ≡ `f_character` ≡ `f(character)`).
func TestParseCreateFunctionCharFamilyArgTypes(t *testing.T) {
	cases := []struct {
		src      string
		name     string
		typeName string
		args     []int64
	}{
		// char varying ≡ character varying (varchar).
		{`CREATE FUNCTION f(char varying) RETURNS void LANGUAGE sql AS $$ SELECT 1 $$`, "", "varchar", nil},
		{`CREATE FUNCTION f(char varying(10)) RETURNS void LANGUAGE sql AS $$ SELECT 1 $$`, "", "varchar", []int64{10}},
		// nchar ≡ character (bpchar); nchar varying ≡ character varying.
		// A bare national character defaults to an implicit length of 1,
		// exactly like bare `char`/`character` (gram.y CharacterWithoutLength).
		{`CREATE FUNCTION f(nchar) RETURNS void LANGUAGE sql AS $$ SELECT 1 $$`, "", "character", []int64{1}},
		{`CREATE FUNCTION f(nchar(5)) RETURNS void LANGUAGE sql AS $$ SELECT 1 $$`, "", "character", []int64{5}},
		{`CREATE FUNCTION f(nchar varying) RETURNS void LANGUAGE sql AS $$ SELECT 1 $$`, "", "varchar", nil},
		// national character|char ≡ character; ... varying ≡ character varying.
		{`CREATE FUNCTION f(national character) RETURNS void LANGUAGE sql AS $$ SELECT 1 $$`, "", "character", []int64{1}},
		{`CREATE FUNCTION f(national character varying) RETURNS void LANGUAGE sql AS $$ SELECT 1 $$`, "", "varchar", nil},
		{`CREATE FUNCTION f(national char) RETURNS void LANGUAGE sql AS $$ SELECT 1 $$`, "", "character", []int64{1}},
		{`CREATE FUNCTION f(national char varying) RETURNS void LANGUAGE sql AS $$ SELECT 1 $$`, "", "varchar", nil},
		// The `name type` form still wins when a name precedes the alias.
		{`CREATE FUNCTION f(a nchar) RETURNS void LANGUAGE sql AS $$ SELECT 1 $$`, "a", "character", []int64{1}},
		{`CREATE FUNCTION f(b national character varying) RETURNS void LANGUAGE sql AS $$ SELECT 1 $$`, "b", "varchar", nil},
	}
	for _, tc := range cases {
		stmts, err := Parse(tc.src)
		if err != nil {
			t.Errorf("parse %q: %v", tc.src, err)
			continue
		}
		cf := stmts[0].(*CreateFunctionStmt)
		if len(cf.Args) != 1 {
			t.Errorf("%q: Args len = %d, want 1", tc.src, len(cf.Args))
			continue
		}
		if cf.Args[0].Name != tc.name {
			t.Errorf("%q: arg Name = %q, want %q", tc.src, cf.Args[0].Name, tc.name)
		}
		if cf.Args[0].Type.Name != tc.typeName {
			t.Errorf("%q: type Name = %q, want %q", tc.src, cf.Args[0].Type.Name, tc.typeName)
		}
		if !reflect.DeepEqual(cf.Args[0].Type.Args, tc.args) {
			t.Errorf("%q: type Args = %v, want %v", tc.src, cf.Args[0].Type.Args, tc.args)
		}
	}

	// These spellings can never start an arg name upstream — `f(char int)`,
	// `f(nchar int)`, `f(national int)` are syntax errors in PG 18.3 (the
	// leading word is always the start of a (multi-word) type). goopg must
	// rewind and let parseColumnType consume the leading word so the dangling
	// identifier errors instead of being captured as an arg name.
	errCases := []string{
		`CREATE FUNCTION f(char int) RETURNS void LANGUAGE sql AS $$ SELECT 1 $$`,
		`CREATE FUNCTION f(character int) RETURNS void LANGUAGE sql AS $$ SELECT 1 $$`,
		`CREATE FUNCTION f(nchar int) RETURNS void LANGUAGE sql AS $$ SELECT 1 $$`,
		`CREATE FUNCTION f(national int) RETURNS void LANGUAGE sql AS $$ SELECT 1 $$`,
	}
	for _, src := range errCases {
		if _, err := Parse(src); err == nil {
			t.Errorf("parse %q: expected error, got none", src)
		}
	}
}

// TestParseFunctionQuotedTypeArgs pins acceptance of a quoted-identifier
// TYPE in a CREATE FUNCTION / CREATE PROCEDURE argument list (M0119-0006,
// deferral row 1362). PG's func_arg grammar (gram.y:8507-8563) accepts
// `[argmode] [param_name] func_type` where func_type's TypeName may be a
// quoted identifier, so `x "char"` names the CHAROID type (OID 18) —
// distinct from bare `char` (bpchar, OID 1042). The quoted/bare distinction
// is preserved because parseColumnType skips the implicit length-1 typmod
// stamp when the type's first token is TokenQuotedIdent (ddl.go:4666): the
// quoted form carries Args=nil, the bare form Args=[1].
func TestParseFunctionQuotedTypeArgs(t *testing.T) {
	cases := []struct {
		src      string
		name     string
		typeName string
		quoted   bool // quoted type must carry NO implicit typmod (Args nil)
	}{
		// (a) named arg + quoted built-in type `x "char"` — OIDChar, not bpchar.
		{`CREATE FUNCTION g(x "char") RETURNS int LANGUAGE sql AS $$ SELECT 1 $$`, "x", "char", true},
		// (b) named arg + quoted user type.
		{`CREATE FUNCTION g(x "MyType") RETURNS int LANGUAGE sql AS $$ SELECT 1 $$`, "x", "MyType", true},
		// (c) quoted ARGNAME + bare type — the isMultiWordTypeStart rewind is
		//     skipped for quoted names, so "character" is the name, not a
		//     multi-word-type leader.
		{`CREATE FUNCTION g("character" int) RETURNS int LANGUAGE sql AS $$ SELECT 1 $$`, "character", "int", true},
		// Bare named arg still parses (no regress).
		{`CREATE FUNCTION g(x int) RETURNS int LANGUAGE sql AS $$ SELECT 1 $$`, "x", "int", false},
		// Named + multi-word type still parses (no regress).
		{`CREATE FUNCTION g(x double precision) RETURNS int LANGUAGE sql AS $$ SELECT 1 $$`, "x", "float8", false},
		// Bare named char pins the implicit length-1 typmod — contrast with
		// the quoted form above.
		{`CREATE FUNCTION g(x char) RETURNS int LANGUAGE sql AS $$ SELECT 1 $$`, "x", "char", false},
		// Sibling path: CREATE PROCEDURE shares parseArgNameAndType.
		{`CREATE PROCEDURE p(x "char") LANGUAGE sql AS $$ SELECT 1 $$`, "x", "char", true},
		// (d) mode branches still route quoted types: mode-first + name-mode.
		{`CREATE FUNCTION g(IN x "char") RETURNS int LANGUAGE sql AS $$ SELECT 1 $$`, "x", "char", true},
		{`CREATE FUNCTION g(x IN "char") RETURNS int LANGUAGE sql AS $$ SELECT 1 $$`, "x", "char", true},
		// (d) bare mode forms still parse (guard the widening against breaking
		//     the mode branches).
		{`CREATE FUNCTION g(IN x int) RETURNS int LANGUAGE sql AS $$ SELECT 1 $$`, "x", "int", false},
		{`CREATE FUNCTION g(x IN int) RETURNS int LANGUAGE sql AS $$ SELECT 1 $$`, "x", "int", false},
	}
	for _, tc := range cases {
		stmts, err := Parse(tc.src)
		if err != nil {
			t.Errorf("parse %q: %v", tc.src, err)
			continue
		}
		var args []FunctionArg
		switch s := stmts[0].(type) {
		case *CreateFunctionStmt:
			args = s.Args
		case *CreateProcedureStmt:
			args = s.Args
		default:
			t.Errorf("%q: got %T, want CreateFunctionStmt/CreateProcedureStmt", tc.src, stmts[0])
			continue
		}
		if len(args) != 1 {
			t.Errorf("%q: Args len = %d, want 1", tc.src, len(args))
			continue
		}
		if args[0].Name != tc.name {
			t.Errorf("%q: arg Name = %q, want %q", tc.src, args[0].Name, tc.name)
		}
		if args[0].Type.Name != tc.typeName {
			t.Errorf("%q: type Name = %q, want %q", tc.src, args[0].Type.Name, tc.typeName)
		}
		if tc.quoted && len(args[0].Type.Args) != 0 {
			t.Errorf("%q: quoted type must not carry an implicit typmod, got Args=%v", tc.src, args[0].Type.Args)
		}
		if !tc.quoted && args[0].Type.Name == "char" && len(args[0].Type.Args) != 1 {
			t.Errorf("%q: bare char wants the implicit length-1 typmod, got Args=%v", tc.src, args[0].Type.Args)
		}
	}
}

// TestParseCreateFunctionRejectsOutInout guards Stage A's scope: OUT / INOUT
// are not yet supported. VARIADIC is now accepted (M0097-0117).
func TestParseCreateFunctionAcceptsOutInoutVariadic(t *testing.T) {
	// OUT, INOUT, and VARIADIC are all accepted for functions.
	acceptCases := []struct {
		src  string
		mode FuncArgMode
	}{
		{`CREATE FUNCTION f(OUT y int) RETURNS int LANGUAGE plpgsql AS $$ BEGIN END $$`, FuncArgOut},
		{`CREATE FUNCTION f(INOUT y int) RETURNS int LANGUAGE plpgsql AS $$ BEGIN END $$`, FuncArgInout},
		{`CREATE FUNCTION f(VARIADIC y int) RETURNS int LANGUAGE sql AS $$ SELECT $1 $$`, FuncArgVariadic},
		{`CREATE FUNCTION f(a int default 1, out b int) RETURNS int LANGUAGE sql AS $$ SELECT $1 $$`, FuncArgIn},
	}
	for _, tc := range acceptCases {
		t.Run(tc.src[:40], func(t *testing.T) {
			stmts, err := Parse(tc.src)
			if err != nil {
				t.Fatalf("unexpected parse error: %v", err)
			}
			cf := stmts[0].(*CreateFunctionStmt)
			if len(cf.Args) == 0 {
				t.Fatal("no args parsed")
			}
			if cf.Args[0].Mode != tc.mode {
				t.Errorf("Args[0].Mode = %v, want %v", cf.Args[0].Mode, tc.mode)
			}
		})
	}
}

// TestParseCreateFunctionLanguageAsAnyOrder pins that LANGUAGE and
// AS can appear in either order — matches upstream's flexible
// clause ordering.
func TestParseCreateFunctionLanguageAsAnyOrder(t *testing.T) {
	srcs := []string{
		`CREATE FUNCTION f() RETURNS int LANGUAGE plpgsql AS $$ x $$`,
		`CREATE FUNCTION f() RETURNS int AS $$ x $$ LANGUAGE plpgsql`,
	}
	for _, src := range srcs {
		t.Run(src, func(t *testing.T) {
			stmts, err := Parse(src)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			cf := stmts[0].(*CreateFunctionStmt)
			if cf.Language != "plpgsql" {
				t.Errorf("Language = %q", cf.Language)
			}
		})
	}
}

// TestParseCreateFunctionTaggedDollarQuote pins the
// `$tag$body$tag$` form — useful when the body itself contains
// `$$`.
func TestParseCreateFunctionTaggedDollarQuote(t *testing.T) {
	src := `CREATE FUNCTION f() RETURNS int LANGUAGE plpgsql AS $body$ SELECT $$inner$$ $body$`
	stmts, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cf := stmts[0].(*CreateFunctionStmt)
	if !strings.Contains(cf.Body, "$$inner$$") {
		t.Errorf("Body = %q, want it to contain $$inner$$", cf.Body)
	}
}

// TestParseCreateFunctionLanguageCTwoItemAS pins upstream's
// interpret_AS_clause: LANGUAGE C is the only language allowed to use
// the two-item `AS 'objfile', 'linksymbol'` form (regress' own
// test_setup.sql defines binary_coercible() this way, AS before
// LANGUAGE) — every other language, including "internal", must reject
// it with "only one AS item needed for language ...".
func TestParseCreateFunctionLanguageCTwoItemAS(t *testing.T) {
	srcs := []string{
		`CREATE FUNCTION f(oid, oid) RETURNS bool AS 'some/path', 'f' LANGUAGE C STRICT STABLE`,
		`CREATE FUNCTION f(oid, oid) RETURNS bool LANGUAGE C AS 'some/path', 'f'`,
	}
	for _, src := range srcs {
		t.Run(src, func(t *testing.T) {
			stmts, err := Parse(src)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			cf := stmts[0].(*CreateFunctionStmt)
			if cf.Language != "c" {
				t.Errorf("Language = %q, want c", cf.Language)
			}
		})
	}
}

// TestParseCreateFunctionNonCTwoItemASRejected guards the negative
// side of the same rule: LANGUAGE sql/plpgsql/internal (or unspecified)
// must reject a two-item AS clause.
func TestParseCreateFunctionNonCTwoItemASRejected(t *testing.T) {
	srcs := []string{
		`CREATE FUNCTION f() RETURNS int AS 'a', 'b' LANGUAGE sql`,
		`CREATE FUNCTION f() RETURNS int LANGUAGE sql AS 'a', 'b'`,
		`CREATE FUNCTION f() RETURNS int AS 'a', 'b' LANGUAGE internal`,
	}
	for _, src := range srcs {
		t.Run(src, func(t *testing.T) {
			_, err := Parse(src)
			if err == nil {
				t.Fatalf("expected parse error for two-item AS on a non-C language")
			}
			if !strings.Contains(err.Error(), "only one AS item needed") {
				t.Errorf("err = %v, want an 'only one AS item needed' diagnostic", err)
			}
		})
	}
}

// TestParseCreateFunctionMissingBody guards the "AS $$body$$
// required" rule: omitting AS surfaces a specific diagnostic.
func TestParseCreateFunctionMissingBody(t *testing.T) {
	src := `CREATE FUNCTION f() RETURNS int LANGUAGE plpgsql`
	_, err := Parse(src)
	if err == nil {
		t.Fatalf("expected parse error for missing AS body")
	}
	if !strings.Contains(err.Error(), "AS $$body$$") {
		t.Errorf("err = %v, want a missing-AS diagnostic", err)
	}
}

// TestParseDropFunctionMinimal pins the smallest DROP form.
func TestParseDropFunctionMinimal(t *testing.T) {
	src := `DROP FUNCTION f`
	stmts, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	df, ok := stmts[0].(*DropFunctionStmt)
	if !ok {
		t.Fatalf("got %T, want *DropFunctionStmt", stmts[0])
	}
	if df.Name.Name != "f" {
		t.Errorf("Name = %q", df.Name.Name)
	}
	if df.IfExists {
		t.Errorf("IfExists = true, want false")
	}
	if df.Args != nil {
		t.Errorf("Args = %+v, want nil for no parens", df.Args)
	}
}

// TestParseDropFunctionIfExistsAndArgs pins the full surface:
// IF EXISTS + parenthesised arg list + CASCADE.
func TestParseDropFunctionIfExistsAndArgs(t *testing.T) {
	src := `DROP FUNCTION IF EXISTS f(int, text) CASCADE`
	stmts, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	df := stmts[0].(*DropFunctionStmt)
	if !df.IfExists {
		t.Errorf("IfExists = false, want true")
	}
	if len(df.Args) != 2 {
		t.Fatalf("Args len = %d, want 2", len(df.Args))
	}
	if df.Args[0].Type.Name != "int" || df.Args[1].Type.Name != "text" {
		t.Errorf("Args = %+v", df.Args)
	}
	if df.Behavior != DropCascade {
		t.Errorf("Behavior = %d, want DropCascade", df.Behavior)
	}
}

// TestDollarQuoteEmptyTag is a lexer-level regression: `$$` empty
// body must round-trip.
func TestDollarQuoteEmptyTag(t *testing.T) {
	src := `CREATE FUNCTION f() RETURNS void LANGUAGE plpgsql AS $$$$`
	stmts, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cf := stmts[0].(*CreateFunctionStmt)
	if cf.Body != "" {
		t.Errorf("Body = %q, want empty", cf.Body)
	}
}

// TestDollarQuoteUnterminated pins clear diagnostic on a missing
// closing tag.
func TestDollarQuoteUnterminated(t *testing.T) {
	src := `CREATE FUNCTION f() RETURNS void LANGUAGE plpgsql AS $tag$ no closer here`
	_, err := Parse(src)
	if err == nil {
		t.Fatal("expected parse error for unterminated dollar-quoted string")
	}
	if !strings.Contains(err.Error(), "unterminated") {
		t.Errorf("err = %v, want a 'unterminated' diagnostic", err)
	}
}

// TestPositionalParameterStillParses guards the lexer regression:
// the dollar-quote support must not break `$1` / `$N` positional
// parameter parsing.
func TestPositionalParameterStillParses(t *testing.T) {
	src := `SELECT $1`
	stmts, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(stmts) != 1 {
		t.Fatalf("got %d stmts", len(stmts))
	}
}

// TestParseCreateProcedureMinimal pins the basic CREATE PROCEDURE form.
// Stage B (procedure follow-up) of M0015.
func TestParseCreateProcedureMinimal(t *testing.T) {
	src := `CREATE PROCEDURE proc() LANGUAGE plpgsql AS $$ BEGIN END $$`
	stmts, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(stmts) != 1 {
		t.Fatalf("got %d statements, want 1", len(stmts))
	}
	cp, ok := stmts[0].(*CreateProcedureStmt)
	if !ok {
		t.Fatalf("got %T, want *CreateProcedureStmt", stmts[0])
	}
	if cp.OrReplace {
		t.Errorf("OrReplace = true, want false")
	}
	if cp.Name.Name != "proc" {
		t.Errorf("Name.Name = %q, want proc", cp.Name.Name)
	}
	if len(cp.Args) != 0 {
		t.Errorf("Args len = %d, want 0", len(cp.Args))
	}
	if cp.Language != "plpgsql" {
		t.Errorf("Language = %q, want plpgsql", cp.Language)
	}
	if !strings.Contains(cp.Body, "BEGIN END") {
		t.Errorf("Body = %q, missing BEGIN END", cp.Body)
	}
}

// TestParseCreateProcedureOrReplace pins OR REPLACE flag.
func TestParseCreateProcedureOrReplace(t *testing.T) {
	src := `CREATE OR REPLACE PROCEDURE p() LANGUAGE sql AS $$ SELECT 1 $$`
	stmts, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(stmts) != 1 {
		t.Fatalf("got %d statements, want 1", len(stmts))
	}
	cp := stmts[0].(*CreateProcedureStmt)
	if !cp.OrReplace {
		t.Errorf("OrReplace = false, want true")
	}
}

// TestParseCreateProcedureArgs pins argument parsing.
func TestParseCreateProcedureArgs(t *testing.T) {
	src := `CREATE PROCEDURE p(a int, b text) LANGUAGE plpgsql AS $$ BEGIN END $$`
	stmts, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cp := stmts[0].(*CreateProcedureStmt)
	if len(cp.Args) != 2 {
		t.Fatalf("Args len = %d, want 2", len(cp.Args))
	}
	if cp.Args[0].Name != "a" || cp.Args[0].Type.Name != "int" {
		t.Errorf("first arg = %v", cp.Args[0])
	}
	if cp.Args[1].Name != "b" || cp.Args[1].Type.Name != "text" {
		t.Errorf("second arg = %v", cp.Args[1])
	}
}

// TestParseCreateProcedureNoLanguage leaves Language empty when omitted.
func TestParseCreateProcedureNoLanguage(t *testing.T) {
	src := `CREATE PROCEDURE p() AS $$ SELECT 1 $$`
	stmts, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cp := stmts[0].(*CreateProcedureStmt)
	if cp.Language != "" {
		t.Errorf("Language = %q, want empty when omitted", cp.Language)
	}
}

// TestParseCallStatement pins CALL with and without arguments.
func TestParseCallStatement(t *testing.T) {
	tests := []struct {
		src  string
		want string
		args int
	}{
		{"CALL foo", "foo", 0},
		{"CALL foo()", "foo", 0},
		{"CALL foo(1)", "foo", 1},
		{"CALL foo(1 + 2, 'text')", "foo", 2},
	}
	for _, tt := range tests {
		t.Run(tt.src, func(t *testing.T) {
			stmts, err := Parse(tt.src)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			call := stmts[0].(*CallStmt)
			if call.Name.Name != tt.want {
				t.Errorf("Name.Name = %q, want %s", call.Name.Name, tt.want)
			}
			if len(call.Args) != tt.args {
				t.Errorf("Args len = %d, want %d", len(call.Args), tt.args)
			}
		})
	}
}

// TestParseCallRejectsTrailingSemicolon ensures CALL ends cleanly.
func TestParseCallRejectsTrailingSemicolon(t *testing.T) {
	src := "CALL foo();"
	stmts, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(stmts) != 1 {
		t.Fatalf("got %d statements", len(stmts))
	}
}

// TestParseDropProcedureMinimal pins DROP PROCEDURE with no args.
func TestParseDropProcedureMinimal(t *testing.T) {
	src := `DROP PROCEDURE p`
	stmts, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	dp := stmts[0].(*DropProcedureStmt)
	if dp.IfExists {
		t.Errorf("IfExists = true, want false")
	}
	if dp.Name.Name != "p" {
		t.Errorf("Name.Name = %q, want p", dp.Name.Name)
	}
	if dp.Args != nil {
		t.Errorf("Args = %v, want nil", dp.Args)
	}
}

// TestParseDropProcedureIfExists pins IF EXISTS.
func TestParseDropProcedureIfExists(t *testing.T) {
	src := `DROP PROCEDURE IF EXISTS p`
	stmts, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	dp := stmts[0].(*DropProcedureStmt)
	if !dp.IfExists {
		t.Errorf("IfExists = false, want true")
	}
}

// TestParseDropProcedureArgs pins DROP PROCEDURE with argument types.
func TestParseDropProcedureArgs(t *testing.T) {
	src := `DROP PROCEDURE p(int, text)`
	stmts, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	dp := stmts[0].(*DropProcedureStmt)
	if len(dp.Args) != 2 {
		t.Fatalf("Args len = %d, want 2", len(dp.Args))
	}
}
