package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestRegexpInstrFamily pins the four M0134-0070 Round E builtins —
// regexp_count/regexp_like/regexp_instr/regexp_substr
// (postgres/src/backend/utils/adt/regexp.c:1138,1329,1198,1904) — against
// the strings.sql regress fixture values (postgres/src/test/regress/
// sql/strings.sql:254-321, expected/strings.out:805-1103), plus the shared
// 'g'-rejection and numeric-parameter-validation error cases.
func TestRegexpInstrFamily(t *testing.T) {
	intC := func(v int64) *optimizer.IntegerConst { return &optimizer.IntegerConst{Value: v} }
	strC := func(v string) *optimizer.StringConst { return &optimizer.StringConst{Value: v} }

	t.Run("happy paths", func(t *testing.T) {
		cases := []struct {
			name string
			fn   string
			args []optimizer.Expr
			want Datum
		}{
			// regexp_count — strings.sql:254-259
			{"count basic", "regexp_count",
				[]optimizer.Expr{strC("123123123123123"), strC("(12)3")},
				Datum{Kind: KindInt, Int: 5}},
			{"count with start=1", "regexp_count",
				[]optimizer.Expr{strC("123123123123"), strC("123"), intC(1)},
				Datum{Kind: KindInt, Int: 4}},
			{"count with start=3", "regexp_count",
				[]optimizer.Expr{strC("123123123123"), strC("123"), intC(3)},
				Datum{Kind: KindInt, Int: 3}},
			{"count with start beyond string", "regexp_count",
				[]optimizer.Expr{strC("123123123123"), strC("123"), intC(33)},
				Datum{Kind: KindInt, Int: 0}},
			{"count case-sensitive no match", "regexp_count",
				[]optimizer.Expr{strC("ABCABCABCABC"), strC("Abc"), intC(1), strC("")},
				Datum{Kind: KindInt, Int: 0}},
			{"count case-insensitive", "regexp_count",
				[]optimizer.Expr{strC("ABCABCABCABC"), strC("Abc"), intC(1), strC("i")},
				Datum{Kind: KindInt, Int: 4}},

			// regexp_like — strings.sql:265-268
			{"like basic true", "regexp_like",
				[]optimizer.Expr{strC("Steven"), strC("^Ste(v|ph)en$")},
				NewBoolDatum(true)},
			{"like n-flag: dot excludes newline", "regexp_like",
				[]optimizer.Expr{strC("a\nd"), strC("a.d"), strC("n")},
				NewBoolDatum(false)},
			{"like s-flag: dot matches newline (PG default)", "regexp_like",
				[]optimizer.Expr{strC("a\nd"), strC("a.d"), strC("s")},
				NewBoolDatum(true)},
			{"like x-flag: insignificant whitespace", "regexp_like",
				[]optimizer.Expr{strC("abc"), strC(" a . c "), strC("x")},
				NewBoolDatum(true)},

			// regexp_instr — strings.sql:272-292
			{"instr basic", "regexp_instr",
				[]optimizer.Expr{strC("abcdefghi"), strC("d.f")},
				Datum{Kind: KindInt, Int: 4}},
			{"instr no match", "regexp_instr",
				[]optimizer.Expr{strC("abcdefghi"), strC("d.q")},
				Datum{Kind: KindInt, Int: 0}},
			{"instr default start/N", "regexp_instr",
				[]optimizer.Expr{strC("abcabcabc"), strC("a.c")},
				Datum{Kind: KindInt, Int: 1}},
			{"instr start=2 idiom-correctness check", "regexp_instr",
				[]optimizer.Expr{strC("abcabcabc"), strC("a.c"), intC(2)},
				Datum{Kind: KindInt, Int: 4}},
			{"instr N=3rd match", "regexp_instr",
				[]optimizer.Expr{strC("abcabcabc"), strC("a.c"), intC(1), intC(3)},
				Datum{Kind: KindInt, Int: 7}},
			{"instr N exceeds match count", "regexp_instr",
				[]optimizer.Expr{strC("abcabcabc"), strC("a.c"), intC(1), intC(4)},
				Datum{Kind: KindInt, Int: 0}},
			{"instr case-insensitive with N", "regexp_instr",
				[]optimizer.Expr{strC("abcabcabc"), strC("A.C"), intC(1), intC(2), intC(0), strC("i")},
				Datum{Kind: KindInt, Int: 4}},
			{"instr nested groups endoption=0 subexpr=0 (whole match)", "regexp_instr",
				[]optimizer.Expr{strC("1234567890"), strC("(123)(4(56)(78))"), intC(1), intC(1), intC(0), strC("i"), intC(0)},
				Datum{Kind: KindInt, Int: 1}},
			{"instr nested groups endoption=0 subexpr=1", "regexp_instr",
				[]optimizer.Expr{strC("1234567890"), strC("(123)(4(56)(78))"), intC(1), intC(1), intC(0), strC("i"), intC(1)},
				Datum{Kind: KindInt, Int: 1}},
			{"instr nested groups endoption=0 subexpr=2", "regexp_instr",
				[]optimizer.Expr{strC("1234567890"), strC("(123)(4(56)(78))"), intC(1), intC(1), intC(0), strC("i"), intC(2)},
				Datum{Kind: KindInt, Int: 4}},
			{"instr nested groups endoption=0 subexpr=3", "regexp_instr",
				[]optimizer.Expr{strC("1234567890"), strC("(123)(4(56)(78))"), intC(1), intC(1), intC(0), strC("i"), intC(3)},
				Datum{Kind: KindInt, Int: 5}},
			{"instr nested groups endoption=0 subexpr=4", "regexp_instr",
				[]optimizer.Expr{strC("1234567890"), strC("(123)(4(56)(78))"), intC(1), intC(1), intC(0), strC("i"), intC(4)},
				Datum{Kind: KindInt, Int: 7}},
			{"instr nested groups endoption=0 subexpr=5 (exceeds npatterns)", "regexp_instr",
				[]optimizer.Expr{strC("1234567890"), strC("(123)(4(56)(78))"), intC(1), intC(1), intC(0), strC("i"), intC(5)},
				Datum{Kind: KindInt, Int: 0}},
			{"instr nested groups endoption=1 subexpr=0 (whole match)", "regexp_instr",
				[]optimizer.Expr{strC("1234567890"), strC("(123)(4(56)(78))"), intC(1), intC(1), intC(1), strC("i"), intC(0)},
				Datum{Kind: KindInt, Int: 9}},
			{"instr nested groups endoption=1 subexpr=1", "regexp_instr",
				[]optimizer.Expr{strC("1234567890"), strC("(123)(4(56)(78))"), intC(1), intC(1), intC(1), strC("i"), intC(1)},
				Datum{Kind: KindInt, Int: 4}},
			{"instr nested groups endoption=1 subexpr=2", "regexp_instr",
				[]optimizer.Expr{strC("1234567890"), strC("(123)(4(56)(78))"), intC(1), intC(1), intC(1), strC("i"), intC(2)},
				Datum{Kind: KindInt, Int: 9}},
			{"instr nested groups endoption=1 subexpr=3", "regexp_instr",
				[]optimizer.Expr{strC("1234567890"), strC("(123)(4(56)(78))"), intC(1), intC(1), intC(1), strC("i"), intC(3)},
				Datum{Kind: KindInt, Int: 7}},
			{"instr nested groups endoption=1 subexpr=4", "regexp_instr",
				[]optimizer.Expr{strC("1234567890"), strC("(123)(4(56)(78))"), intC(1), intC(1), intC(1), strC("i"), intC(4)},
				Datum{Kind: KindInt, Int: 9}},
			{"instr nested groups endoption=1 subexpr=5 (exceeds npatterns)", "regexp_instr",
				[]optimizer.Expr{strC("1234567890"), strC("(123)(4(56)(78))"), intC(1), intC(1), intC(1), strC("i"), intC(5)},
				Datum{Kind: KindInt, Int: 0}},
			{"instr match without subexpression match returns 0", "regexp_instr",
				[]optimizer.Expr{strC("foo"), strC("foo(bar)?"), intC(1), intC(1), intC(0), strC(""), intC(1)},
				Datum{Kind: KindInt, Int: 0}},

			// regexp_substr — strings.sql:302-316
			{"substr basic", "regexp_substr",
				[]optimizer.Expr{strC("abcdefghi"), strC("d.f")},
				NewStringDatum("def")},
			{"substr no match is NULL", "regexp_substr",
				[]optimizer.Expr{strC("abcdefghi"), strC("d.q")},
				NullDatum},
			{"substr default start/N", "regexp_substr",
				[]optimizer.Expr{strC("abcabcabc"), strC("a.c")},
				NewStringDatum("abc")},
			{"substr start=2", "regexp_substr",
				[]optimizer.Expr{strC("abcabcabc"), strC("a.c"), intC(2)},
				NewStringDatum("abc")},
			{"substr N=3rd match", "regexp_substr",
				[]optimizer.Expr{strC("abcabcabc"), strC("a.c"), intC(1), intC(3)},
				NewStringDatum("abc")},
			{"substr N exceeds match count is NULL", "regexp_substr",
				[]optimizer.Expr{strC("abcabcabc"), strC("a.c"), intC(1), intC(4)},
				NullDatum},
			{"substr case-insensitive with N", "regexp_substr",
				[]optimizer.Expr{strC("abcabcabc"), strC("A.C"), intC(1), intC(2), strC("i")},
				NewStringDatum("abc")},
			{"substr nested groups subexpr=0 (whole match)", "regexp_substr",
				[]optimizer.Expr{strC("1234567890"), strC("(123)(4(56)(78))"), intC(1), intC(1), strC("i"), intC(0)},
				NewStringDatum("12345678")},
			{"substr nested groups subexpr=1", "regexp_substr",
				[]optimizer.Expr{strC("1234567890"), strC("(123)(4(56)(78))"), intC(1), intC(1), strC("i"), intC(1)},
				NewStringDatum("123")},
			{"substr nested groups subexpr=2", "regexp_substr",
				[]optimizer.Expr{strC("1234567890"), strC("(123)(4(56)(78))"), intC(1), intC(1), strC("i"), intC(2)},
				NewStringDatum("45678")},
			{"substr nested groups subexpr=3", "regexp_substr",
				[]optimizer.Expr{strC("1234567890"), strC("(123)(4(56)(78))"), intC(1), intC(1), strC("i"), intC(3)},
				NewStringDatum("56")},
			{"substr nested groups subexpr=4", "regexp_substr",
				[]optimizer.Expr{strC("1234567890"), strC("(123)(4(56)(78))"), intC(1), intC(1), strC("i"), intC(4)},
				NewStringDatum("78")},
			{"substr nested groups subexpr=5 (exceeds npatterns) is NULL", "regexp_substr",
				[]optimizer.Expr{strC("1234567890"), strC("(123)(4(56)(78))"), intC(1), intC(1), strC("i"), intC(5)},
				NullDatum},
			{"substr match without subexpression match is NULL", "regexp_substr",
				[]optimizer.Expr{strC("foo"), strC("foo(bar)?"), intC(1), intC(1), strC(""), intC(1)},
				NullDatum},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				fc := &optimizer.FuncCall{Name: c.fn, Args: c.args}
				got, err := evalFuncCall(fc, nil, &Context{})
				if err != nil {
					t.Fatalf("evalFuncCall: %v", err)
				}
				if got.IsNull() != c.want.IsNull() {
					t.Fatalf("got null=%v, want null=%v (got=%+v want=%+v)", got.IsNull(), c.want.IsNull(), got, c.want)
				}
				if got.IsNull() {
					return
				}
				switch c.want.Kind {
				case KindInt:
					if got.Kind != KindInt || got.Int != c.want.Int {
						t.Fatalf("got %+v, want %+v", got, c.want)
					}
				case KindBool:
					if got.Kind != KindBool || got.BoolValue() != c.want.BoolValue() {
						t.Fatalf("got %+v, want %+v", got, c.want)
					}
				default:
					if got.StringValue() != c.want.StringValue() {
						t.Fatalf("got %+v, want %+v", got, c.want)
					}
				}
			})
		}
	})

	t.Run("errors", func(t *testing.T) {
		cases := []struct {
			name     string
			fn       string
			args     []optimizer.Expr
			wantCode string
			wantMsg  string
		}{
			// regexp_count — strings.sql:261-262
			{"count start=0", "regexp_count",
				[]optimizer.Expr{strC("123123123123"), strC("123"), intC(0)},
				"22023", `invalid value for parameter "start": 0`},
			{"count start=-3", "regexp_count",
				[]optimizer.Expr{strC("123123123123"), strC("123"), intC(-3)},
				"22023", `invalid value for parameter "start": -3`},
			{"count rejects 'g'", "regexp_count",
				[]optimizer.Expr{strC("123123123123"), strC("123"), intC(1), strC("g")},
				"22023", `regexp_count() does not support the "global" option`},

			// regexp_like — strings.sql:269
			{"like rejects 'g'", "regexp_like",
				[]optimizer.Expr{strC("abc"), strC("a.c"), strC("g")},
				"22023", `regexp_like() does not support the "global" option`},

			// regexp_instr — strings.sql:294-299
			{"instr start=0", "regexp_instr",
				[]optimizer.Expr{strC("abcabcabc"), strC("a.c"), intC(0), intC(1)},
				"22023", `invalid value for parameter "start": 0`},
			{"instr n=0", "regexp_instr",
				[]optimizer.Expr{strC("abcabcabc"), strC("a.c"), intC(1), intC(0)},
				"22023", `invalid value for parameter "n": 0`},
			{"instr endoption=-1", "regexp_instr",
				[]optimizer.Expr{strC("abcabcabc"), strC("a.c"), intC(1), intC(1), intC(-1)},
				"22023", `invalid value for parameter "endoption": -1`},
			{"instr endoption=2", "regexp_instr",
				[]optimizer.Expr{strC("abcabcabc"), strC("a.c"), intC(1), intC(1), intC(2)},
				"22023", `invalid value for parameter "endoption": 2`},
			{"instr rejects 'g'", "regexp_instr",
				[]optimizer.Expr{strC("abcabcabc"), strC("a.c"), intC(1), intC(1), intC(0), strC("g")},
				"22023", `regexp_instr() does not support the "global" option`},
			{"instr subexpr=-1", "regexp_instr",
				[]optimizer.Expr{strC("abcabcabc"), strC("a.c"), intC(1), intC(1), intC(0), strC(""), intC(-1)},
				"22023", `invalid value for parameter "subexpr": -1`},

			// regexp_substr — strings.sql:318-321
			{"substr start=0", "regexp_substr",
				[]optimizer.Expr{strC("abcabcabc"), strC("a.c"), intC(0), intC(1)},
				"22023", `invalid value for parameter "start": 0`},
			{"substr n=0", "regexp_substr",
				[]optimizer.Expr{strC("abcabcabc"), strC("a.c"), intC(1), intC(0)},
				"22023", `invalid value for parameter "n": 0`},
			{"substr rejects 'g'", "regexp_substr",
				[]optimizer.Expr{strC("abcabcabc"), strC("a.c"), intC(1), intC(1), strC("g")},
				"22023", `regexp_substr() does not support the "global" option`},
			{"substr subexpr=-1", "regexp_substr",
				[]optimizer.Expr{strC("abcabcabc"), strC("a.c"), intC(1), intC(1), strC(""), intC(-1)},
				"22023", `invalid value for parameter "subexpr": -1`},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				fc := &optimizer.FuncCall{Name: c.fn, Args: c.args}
				_, err := evalFuncCall(fc, nil, &Context{})
				if err == nil {
					t.Fatalf("expected error, got none")
				}
				ee, ok := err.(*ExecError)
				if !ok {
					t.Fatalf("err type=%T, want *ExecError", err)
				}
				if ee.Code != c.wantCode {
					t.Errorf("SQLSTATE=%s want %s", ee.Code, c.wantCode)
				}
				if ee.Message != c.wantMsg {
					t.Errorf("message=%q want %q", ee.Message, c.wantMsg)
				}
			})
		}
	})
}
