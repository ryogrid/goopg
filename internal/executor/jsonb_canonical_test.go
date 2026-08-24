package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
)

// TestCanonicalizeJSONB pins canonicalizeJSONB against PG 18.3's jsonb_out
// (measured 2026-08-13 on a throwaway `postgres/local_install`). The cases span
// every canonicalisation rule at once so a single regression is caught by a
// byte-identical comparison:
//
//   - key order is length-then-bytewise, NOT Go's lexicographic sort
//     (`{"bb":1,"a":2}` and `{"b":1,"bb":2,"a":3}`);
//   - duplicate keys collapse last-wins (`{"a":1,"a":2}` vs `{"a":2,"a":1}`);
//   - whitespace normalises to `: ` / `, ` and is dropped around the braces;
//   - numbers round-trip numeric_in→numeric_out: `3e1`→`30`, `1e0`→`1`,
//     `1e5`→`100000`, `1.00`/`1.0`/`0.50` keep their scale, `-0.0`→`0.0`;
//   - strings escape \b\f\n\r\t\"\\ and \u00xx control bytes, and pass
//     non-ASCII UTF-8 through verbatim.
func TestCanonicalizeJSONB(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"keys sort", `{"b":1,"a":2}`, `{"a": 2, "b": 1}`},
		{"keys length-then-bytewise", `{"bb":1,"a":2}`, `{"a": 2, "bb": 1}`},
		{"three keys", `{"b":1,"bb":2,"a":3}`, `{"a": 3, "b": 1, "bb": 2}`},
		{"dup last-wins (2 then 1)", `{"a":1,"a":2}`, `{"a": 2}`},
		{"dup last-wins (1 then 2)", `{"a":2,"a":1}`, `{"a": 1}`},
		{"whitespace + numbers", "  {\"a\" :  1,  \"b\": [1, 2.0, 3e1, 1.00]}  ", `{"a": 1, "b": [1, 2.0, 30, 1.00]}`},
		{"number scale kept", `{"n": 1.0}`, `{"n": 1.0}`},
		{"scientific folded", `{"n": 1e0}`, `{"n": 1}`},
		{"number scale 0.50", `{"n": 0.50}`, `{"n": 0.50}`},
		{"string escapes canonical", `{"s": "a\nb\t\"c\"\\d\u0001"}`, `{"s": "a\nb\t\"c\"\\d\u0001"}`},
		{"non-ascii verbatim", `{"s": "héllo"}`, `{"s": "héllo"}`},
		{"array", `[1,true,false,null,"x"]`, `[1, true, false, null, "x"]`},
		{"scalar string", `"just a scalar"`, `"just a scalar"`},
		{"scalar bool", `true`, `true`},
		{"scalar number", `42`, `42`},
		{"empty object", `{}`, `{}`},
		{"empty array", `[]`, `[]`},
		{"nested object", `{"a": {"b": {"c": 1}}}`, `{"a": {"b": {"c": 1}}}`},
		{"scientific large + neg zero", `{"a": 1e5, "b": -0.0}`, `{"a": 100000, "b": 0.0}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := canonicalizeJSONB(tc.in)
			if err != nil {
				t.Fatalf("canonicalizeJSONB(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("canonicalizeJSONB(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestCanonicalizeJSONBRejectsMalformed pins the 22P02 validation the bare
// pass-through was missing: a malformed body, and a body that is more than one
// value, both become invalid-input errors rather than a stored string that
// raises on every later `->`.
func TestCanonicalizeJSONBRejectsMalformed(t *testing.T) {
	for _, in := range []string{"not json", "{} {}", "{", "[1,2", `{"a":}`} {
		if _, err := canonicalizeJSONB(in); err == nil {
			t.Errorf("canonicalizeJSONB(%q) succeeded, want 22P02", in)
		} else if ee, ok := err.(*ExecError); !ok || ee.Code != "22P02" {
			t.Errorf("canonicalizeJSONB(%q) error = %v, want ExecError 22P02", in, err)
		}
	}
}

// TestJSONBCastAndColumnAreTwins pins the two input boundaries — the `::jsonb`
// cast (evalCast) and a jsonb column's coercion (coerceTextLikeDatum) — agree
// byte-for-byte, and that `json` (text) is left verbatim (Hard-won Rule #2).
func TestJSONBCastAndColumnAreTwins(t *testing.T) {
	raw := `{"b":1,"a":2}`
	want := `{"a": 2, "b": 1}`

	cast, err := evalCast(NewStringDatum(raw), "jsonb", 0, nil)
	if err != nil {
		t.Fatalf("evalCast(..., jsonb): %v", err)
	}
	if cast.StringValue() != want {
		t.Errorf("evalCast jsonb = %q, want %q", cast.StringValue(), want)
	}

	col, err := coerceTextLikeDatum(catalog.Type{Name: "jsonb"}, NewStringDatum(raw))
	if err != nil {
		t.Fatalf("coerceTextLikeDatum(jsonb): %v", err)
	}
	if col != want {
		t.Errorf("coerceTextLikeDatum jsonb = %q, want %q", col, want)
	}

	// `json` preserves the input spelling — it is the non-canonicalising twin.
	jsonCast, err := evalCast(NewStringDatum(raw), "json", 0, nil)
	if err != nil {
		t.Fatalf("evalCast(..., json): %v", err)
	}
	if jsonCast.StringValue() != raw {
		t.Errorf("evalCast json = %q, want verbatim %q", jsonCast.StringValue(), raw)
	}
	jsonCol, err := coerceTextLikeDatum(catalog.Type{Name: "json"}, NewStringDatum(raw))
	if err != nil {
		t.Fatalf("coerceTextLikeDatum(json): %v", err)
	}
	if jsonCol != raw {
		t.Errorf("coerceTextLikeDatum json = %q, want verbatim %q", jsonCol, raw)
	}
}

// TestJSONTypedStringLitMatchesCast pins evalTypedStringLit's `json`/`jsonb`
// arms (M0134-0133) as a third twin alongside evalCast's `::json`/`::jsonb`
// and coerceTextLikeDatum's column-coercion arms tested above: `json 'raw'`
// and `jsonb 'raw'` (the SQL-standard typed-literal form used throughout
// json_encoding.sql, e.g. `json '{ "a": 1 }' -> 'a'`) must validate/canonicalise
// identically to the `::` cast (Hard-won Rule #2 — sibling input boundaries).
func TestJSONTypedStringLitMatchesCast(t *testing.T) {
	raw := `{"b":1,"a":2}`
	wantCanon := `{"a": 2, "b": 1}`

	jsonbLit, err := evalTypedStringLit(&optimizer.TypedStringLit{Type: "jsonb", Value: raw}, nil)
	if err != nil {
		t.Fatalf("evalTypedStringLit(jsonb): %v", err)
	}
	if jsonbLit.StringValue() != wantCanon {
		t.Errorf("evalTypedStringLit(jsonb) = %q, want canonical %q", jsonbLit.StringValue(), wantCanon)
	}

	jsonLit, err := evalTypedStringLit(&optimizer.TypedStringLit{Type: "json", Value: raw}, nil)
	if err != nil {
		t.Fatalf("evalTypedStringLit(json): %v", err)
	}
	if jsonLit.StringValue() != raw {
		t.Errorf("evalTypedStringLit(json) = %q, want verbatim %q", jsonLit.StringValue(), raw)
	}

	// Malformed input must raise the same 22P02 both the cast and the typed
	// literal produce (validateJSONText/canonicalizeJSONB are shared).
	if _, err := evalTypedStringLit(&optimizer.TypedStringLit{Type: "json", Value: "not json"}, nil); err == nil {
		t.Error("evalTypedStringLit(json, malformed) succeeded, want 22P02")
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "22P02" {
		t.Errorf("evalTypedStringLit(json, malformed) error = %v, want ExecError 22P02", err)
	}
	if _, err := evalTypedStringLit(&optimizer.TypedStringLit{Type: "jsonb", Value: "not json"}, nil); err == nil {
		t.Error("evalTypedStringLit(jsonb, malformed) succeeded, want 22P02")
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "22P02" {
		t.Errorf("evalTypedStringLit(jsonb, malformed) error = %v, want ExecError 22P02", err)
	}
}
