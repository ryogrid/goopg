package executor

import (
	"testing"

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
			got, err := evalBinary(c.op, c.left, c.right, 0)
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
	_, err := evalBinary(parser.OpJSONGet, NewStringDatum("not json"), NewStringDatum("a"), 0)
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
	step1, err := evalBinary(parser.OpJSONGet, NewStringDatum(j), NewIntDatum(0), 0)
	if err != nil {
		t.Fatal(err)
	}
	step2, err := evalBinary(parser.OpJSONGet, step1, NewStringDatum("Plan"), 0)
	if err != nil {
		t.Fatal(err)
	}
	step3, err := evalBinary(parser.OpJSONGet, step2, NewStringDatum("Heap Fetches"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if step3.IsNull() || step3.StringValue() != "2" {
		t.Fatalf("got %+v, want 2", step3)
	}
}
