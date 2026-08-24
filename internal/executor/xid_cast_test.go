package executor

import "testing"

// TestEvalCastXidXid8 pins M0134-0087 (xid.sql sizing): evalCast had no
// "xid"/"xid8" case at all, so `'…'::xid`/`'…'::xid8` fell through to the
// bottom default arm and returned the input KindString datum UNCHANGED — no
// hex/octal parsing, no range validation, no 22P02 on garbage input. This
// mirrors the CastExpr-vs-TypedStringLit split the
// pattern_sibling_paths_must_agree memory warns about: `xid '42'`
// (evalTypedStringLit) already worked; `'42'::xid` (evalCast, this test) did
// not share the logic.
func TestEvalCastXidXid8(t *testing.T) {
	cases := []struct {
		name       string
		targetType string
		in         Datum
		wantInt    int64
		wantErr    bool
	}{
		{"xid octal", "xid", NewStringDatum("010"), 8, false},
		{"xid decimal", "xid", NewStringDatum("42"), 42, false},
		{"xid hex", "xid", NewStringDatum("0xffffffff"), int64(uint32(0xffffffff)), false},
		{"xid -1 wraps", "xid", NewStringDatum("-1"), int64(uint32(0xffffffff)), false},
		{"xid empty errors", "xid", NewStringDatum(""), 0, true},
		{"xid garbage errors", "xid", NewStringDatum("asdf"), 0, true},
		{"xid8 octal", "xid8", NewStringDatum("010"), 8, false},
		{"xid8 decimal", "xid8", NewStringDatum("42"), 42, false},
		{"xid8 hex max", "xid8", NewStringDatum("0xffffffffffffffff"), -1, false}, // uint64 max's int64 bit image
		{"xid8 -1 wraps", "xid8", NewStringDatum("-1"), -1, false},
		{"xid8 garbage errors", "xid8", NewStringDatum("asdf"), 0, true},
		// xid8::xid truncates to the low 32 bits (xid8toxid).
		{"xid8-to-xid truncates", "xid", Datum{Kind: KindInt, Int: -1}, int64(uint32(0xffffffff)), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := evalCast(tc.in, tc.targetType, 0, nil)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("evalCast(%q, %q) = %v, want error", tc.in.StringValue(), tc.targetType, got)
				}
				if ee, ok := err.(*ExecError); !ok || ee.Code != "22P02" {
					t.Fatalf("evalCast(%q, %q) error = %v, want 22P02 ExecError", tc.in.StringValue(), tc.targetType, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("evalCast(%q, %q): %v", tc.in.StringValue(), tc.targetType, err)
			}
			if got.Kind != KindInt || got.Int != tc.wantInt {
				t.Fatalf("evalCast(%v, %q) = %+v, want Int=%d", tc.in, tc.targetType, got, tc.wantInt)
			}
		})
	}
}

// TestParseXid8OctalMatchesParseXid pins the sibling-parity fix: parseXid
// already accepted octal ("0NNN") input; parseXid8 did not, even though both
// route through the same base-0 strto[u]l semantics upstream (xidin/xid8in
// via uint32in_subr/uint64in_subr, postgres/src/backend/utils/adt/numutils.c).
func TestParseXid8OctalMatchesParseXid(t *testing.T) {
	n, err := parseXid8("010")
	if err != nil {
		t.Fatalf("parseXid8(\"010\"): %v", err)
	}
	if n != 8 {
		t.Fatalf("parseXid8(\"010\") = %d, want 8 (octal)", n)
	}
}
