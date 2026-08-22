package executor

// M0134-0070 — 4 PG builtins that goopg's catalog seeded (pg_proc_names
// generated OIDs 6364/6365/6162/6198) but never dispatched: crc32(bytea),
// crc32c(bytea), bit_count(bytea), unistr(text). All expected values below
// are PG 18.3 oracle output (crc32/crc32c from tmp/regress-diffs/strings.diff,
// unistr cases 1:1 from the brief's reference list, itself sourced from the
// same diff).

import "testing"

func TestCRC32Bytea(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct {
		sql  string
		want int64
	}{
		{`select crc32(''::bytea)`, 0},
		{`select crc32('The quick brown fox jumps over the lazy dog.'::bytea)`, 1368401385},
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			d, _ := byteaExprResult(t, ctx, tc.sql)
			if d.Kind != KindInt || d.Int != tc.want {
				t.Errorf("= %v (kind %d), want %d (PG 18.3)", d.Format(), d.Kind, tc.want)
			}
		})
	}
}

func TestCRC32CBytea(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct {
		sql  string
		want int64
	}{
		{`select crc32c(''::bytea)`, 0},
		{`select crc32c('The quick brown fox jumps over the lazy dog.'::bytea)`, 419469235},
		// repeat('A', N) boundary cases around the 128-byte CRC-32C table
		// stride. These exceed int32 range (4213642571 > 2^31) — the Datum
		// widening must keep them positive, not sign-extend as negative.
		{`select crc32c(repeat('A',127)::bytea)`, 291820082},
		{`select crc32c(repeat('A',128)::bytea)`, 816091258},
		{`select crc32c(repeat('A',129)::bytea)`, 4213642571},
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			d, _ := byteaExprResult(t, ctx, tc.sql)
			if d.Kind != KindInt || d.Int != tc.want {
				t.Errorf("= %v (kind %d), want %d (PG 18.3)", d.Format(), d.Kind, tc.want)
			}
		})
	}
}

func TestBitCountBytea(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	d, _ := byteaExprResult(t, ctx, `select bit_count('\x1234567890'::bytea)`)
	if d.Kind != KindInt || d.Int != 15 {
		t.Errorf("= %v (kind %d), want 15 (PG 18.3)", d.Format(), d.Kind)
	}
}

// TestUnistr pins all 12 reference cases from the M0134-0070 brief — 3
// successful decodes exercising all 4 escape forms (bare \XXXX, \uXXXX,
// \UXXXXXXXX, \+XXXXXX, and \\) plus surrogate-pair reassembly, and 9 error
// cases covering malformed escapes, lone/misordered surrogates, and
// out-of-range codepoints. Message/SQLSTATE text is byte-for-byte from
// tmp/regress-diffs/strings.diff (PG 18.3 oracle).
func TestUnistr(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	okCases := []struct{ sql, want string }{
		{`select unistr('\0064at\+0000610')`, "data0"},
		{`select unistr('dat\U000000610')`, "data0"},
		{`select unistr('a\\b')`, `a\b`},
	}
	for _, tc := range okCases {
		t.Run(tc.sql, func(t *testing.T) {
			d, _ := byteaExprResult(t, ctx, tc.sql)
			if d.Kind != KindString || d.StringValue() != tc.want {
				t.Errorf("= %q (kind %d), want %q (PG 18.3)", d.Format(), d.Kind, tc.want)
			}
		})
	}

	errCases := []struct{ sql, code, msg, hint string }{
		{`select unistr('wrong: \db99')`, "42601", "invalid Unicode surrogate pair", ""},
		{`select unistr('wrong: \db99\0061')`, "42601", "invalid Unicode surrogate pair", ""},
		{`select unistr('wrong: \+00db99\+000061')`, "42601", "invalid Unicode surrogate pair", ""},
		{`select unistr('wrong: \+2FFFFF')`, "22023", "invalid Unicode code point: 2FFFFF", ""},
		{`select unistr('wrong: \udb99\u0061')`, "42601", "invalid Unicode surrogate pair", ""},
		{`select unistr('wrong: \U0000db99\U00000061')`, "42601", "invalid Unicode surrogate pair", ""},
		{`select unistr('wrong: \U002FFFFF')`, "22023", "invalid Unicode code point: 2FFFFF", ""},
		{`select unistr('wrong: \xyz')`, "42601", "invalid Unicode escape",
			`Unicode escapes must be \XXXX, \+XXXXXX, \uXXXX, or \UXXXXXXXX.`},
	}
	for _, tc := range errCases {
		t.Run(tc.sql, func(t *testing.T) {
			ee := byteaExprErr(t, ctx, tc.sql)
			if ee.Code != tc.code || ee.Message != tc.msg {
				t.Errorf("= %s %q, want %s %q (PG 18.3)", ee.Code, ee.Message, tc.code, tc.msg)
			}
			if ee.Hint != tc.hint {
				t.Errorf("hint = %q, want %q", ee.Hint, tc.hint)
			}
		})
	}
}
