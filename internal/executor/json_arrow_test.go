package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestJSONArrowOperators pins the json accessor operators -> and ->>
// (M0118-0009, horizons enabler). goopg carries json/jsonb as KindString,
// so both operands are text/int Datums and the result is a text Datum whose
// surface form matches PostgreSQL 18.3.
func TestJSONArrowOperators(t *testing.T) {
	jdat := func(s string) Datum { return NewStringDatum(s) }
	idat := func(i int64) Datum { return NewIntDatum(i) }

	cases := []struct {
		name     string
		op       parser.OpCode
		left     Datum
		right    Datum
		want     string
		wantNull bool
	}{
		// object field by key
		{"get-field", parser.OpJSONGet, jdat(`{"a": 2, "b": "x"}`), jdat("a"), "2", false},
		{"get-field-str", parser.OpJSONGet, jdat(`{"a": 2, "b": "x"}`), jdat("b"), `"x"`, false},
		{"gettext-field", parser.OpJSONGetText, jdat(`{"a": 2, "b": "x"}`), jdat("a"), "2", false},
		{"gettext-field-str", parser.OpJSONGetText, jdat(`{"a": 2, "b": "x"}`), jdat("b"), "x", false},
		{"get-missing-key", parser.OpJSONGet, jdat(`{"a": 2}`), jdat("z"), "", true},
		// array element by index
		{"get-idx0", parser.OpJSONGet, jdat(`[10, 20, 30]`), idat(0), "10", false},
		{"get-idx2", parser.OpJSONGet, jdat(`[10, 20, 30]`), idat(2), "30", false},
		{"get-idx-neg", parser.OpJSONGet, jdat(`[10, 20, 30]`), idat(-1), "30", false},
		{"get-idx-oob", parser.OpJSONGet, jdat(`[10, 20, 30]`), idat(5), "", true},
		{"gettext-idx1", parser.OpJSONGetText, jdat(`["a", "b"]`), idat(1), "b", false},
		// nested object value returned as json by ->
		{"get-nested-obj", parser.OpJSONGet, jdat(`{"p": {"k": 1}}`), jdat("p"), `{"k":1}`, false},
		// type mismatches → NULL
		{"int-key-on-object", parser.OpJSONGet, jdat(`{"a": 1}`), idat(0), "", true},
		{"text-key-on-array", parser.OpJSONGet, jdat(`[1, 2]`), jdat("a"), "", true},
		// JSON null element: -> yields literal "null"; ->> yields SQL NULL
		{"get-json-null", parser.OpJSONGet, jdat(`{"a": null}`), jdat("a"), "null", false},
		{"gettext-json-null", parser.OpJSONGetText, jdat(`{"a": null}`), jdat("a"), "", true},
		// boolean text extraction
		{"gettext-bool", parser.OpJSONGetText, jdat(`{"a": true}`), jdat("a"), "true", false},
		// NULL operand short-circuits to NULL (handled before evalJSONArrow)
		{"null-left", parser.OpJSONGet, NullDatum, jdat("a"), "", true},
		{"null-right", parser.OpJSONGet, jdat(`{"a": 1}`), NullDatum, "", true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := evalBinary(c.op, c.left, c.right, 0, nil)
			if err != nil {
				t.Fatalf("evalBinary: %v", err)
			}
			if c.wantNull {
				if !got.IsNull() {
					t.Fatalf("got %+v, want NULL", got)
				}
				return
			}
			if got.IsNull() {
				t.Fatalf("got NULL, want %q", c.want)
			}
			if got.StringValue() != c.want {
				t.Fatalf("got %q, want %q", got.StringValue(), c.want)
			}
		})
	}
}

// TestJSONArrowInvalidJSON pins the 22P02 error for a non-JSON left operand.
func TestJSONArrowInvalidJSON(t *testing.T) {
	_, err := evalBinary(parser.OpJSONGet, NewStringDatum("not json"), NewStringDatum("a"), 0, nil)
	if err == nil {
		t.Fatal("expected error for invalid json, got nil")
	}
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "22P02" {
		t.Fatalf("got %v, want ExecError 22P02", err)
	}
}

// TestJSONArrowChained pins the left-associative chain the horizons spec uses:
// j -> 0 -> 'Plan' -> 'Heap Fetches' navigates an array-of-objects to a scalar.
func TestJSONArrowChained(t *testing.T) {
	j := `[{"Plan": {"Heap Fetches": 2}}]`
	step1, err := evalBinary(parser.OpJSONGet, NewStringDatum(j), NewIntDatum(0), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	step2, err := evalBinary(parser.OpJSONGet, step1, NewStringDatum("Plan"), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	step3, err := evalBinary(parser.OpJSONGet, step2, NewStringDatum("Heap Fetches"), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if step3.IsNull() || step3.StringValue() != "2" {
		t.Fatalf("got %+v, want 2", step3)
	}
}

// TestJsonExtractPath pins json_extract_path/json_extract_path_text/
// jsonb_extract_path/jsonb_extract_path_text — the VARIADIC text[] path-array
// siblings of -> / ->> (M0134-0037). Oracle:
// postgres/src/backend/utils/adt/jsonfuncs.c get_path_all/get_jsonb_path_all.
func TestJsonExtractPath(t *testing.T) {
	sconst := func(s string) optimizer.Expr { return &optimizer.StringConst{Value: s} }
	doc := `{"f2":{"f3":1},"f4":{"f5":99,"f6":"stringy"}}`

	cases := []struct {
		name     string
		fn       string
		src      string
		path     []string
		want     string
		wantNull bool
	}{
		{"json-nested-scalar-as-json", "json_extract_path", doc, []string{"f4", "f6"}, `"stringy"`, false},
		{"json-nested-scalar-as-text", "json_extract_path_text", doc, []string{"f4", "f6"}, "stringy", false},
		{"jsonb-nested-scalar-as-json", "jsonb_extract_path", doc, []string{"f4", "f6"}, `"stringy"`, false},
		{"jsonb-nested-scalar-as-text", "jsonb_extract_path_text", doc, []string{"f4", "f6"}, "stringy", false},
		{"json-numeric-path-indexes-array", "json_extract_path", `{"a":[1,2,3]}`, []string{"a", "1"}, "2", false},
		{"jsonb-numeric-path-indexes-array", "jsonb_extract_path", `{"a":[1,2,3]}`, []string{"a", "1"}, "2", false},
		{"json-missing-key-is-null", "json_extract_path", `{"a":1}`, []string{"b"}, "", true},
		{"jsonb-missing-key-is-null", "jsonb_extract_path", `{"a":1}`, []string{"b"}, "", true},
		{"json-oob-array-index-is-null", "json_extract_path", `{"a":[1,2,3]}`, []string{"a", "5"}, "", true},
		{"json-non-numeric-index-against-array-is-null", "json_extract_path", `{"a":[1,2,3]}`, []string{"a", "x"}, "", true},
		{"json-nested-object-as-json-not-unwrapped", "json_extract_path", doc, []string{"f4"}, `{"f5":99,"f6":"stringy"}`, false},
		{"json-nested-object-as-text-not-unwrapped", "json_extract_path_text", doc, []string{"f4"}, `{"f5":99,"f6":"stringy"}`, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			args := []optimizer.Expr{sconst(c.src)}
			for _, p := range c.path {
				args = append(args, sconst(p))
			}
			fc := &optimizer.FuncCall{Name: c.fn, Args: args}
			got, err := evalFuncCall(fc, nil, &Context{})
			if err != nil {
				t.Fatalf("evalFuncCall: %v", err)
			}
			if c.wantNull {
				if !got.IsNull() {
					t.Fatalf("got %+v, want NULL", got)
				}
				return
			}
			if got.IsNull() {
				t.Fatalf("got NULL, want %q", c.want)
			}
			if got.StringValue() != c.want {
				t.Fatalf("got %q, want %q", got.StringValue(), c.want)
			}
		})
	}
}
