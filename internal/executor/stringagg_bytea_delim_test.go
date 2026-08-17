package executor

// M0134-0001 S14 — string_agg(bytea, ...) drops an UNTYPED delimiter literal.
//
// `string_agg(x::bytea, ',')` (no `::bytea` cast on the delimiter) used to
// concatenate every element with NO separator at all, in both bytea_output
// modes: the delimiter evaluated to a KindString datum (a bare `','` literal
// is always typed `unknown`→KindString at eval time, expr.go's
// *optimizer.StringConst case), and accumAgg's bytea branch only accepted a
// delimiter that was already KindBytes.
//
// PG's `parse_coerce.c coerce_type` (UNKNOWNOID Const branch) applies the
// resolved parameter type's typinput — `byteain` — once overload resolution
// locks in `string_agg(bytea,bytea)` (pg_proc oid 3545). goopg has no
// per-overload aggregate-arg dispatch to do this at analyze time, so the fix
// runs the same coercion at accumulation time via `byteaOperand`
// (bytea.go:319), the shared KindString→bytea widening helper already used
// at other bytea operator sites (M0125-0021).

import "testing"

func TestStringAggByteaUntypedDelimiter(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	const sql = `select string_agg(b, ',') from ` +
		`(values ('\x0102'::bytea),('\xff5c'::bytea),('\x41'::bytea)) v(b)`

	cases := []struct {
		mode string
		want string
	}{
		// PG 18.3: \001\002 . 0x2c(,) . \377\\ . 0x2c(,) . A
		{"escape", `\001\002,\377\\,A`},
		// Same bytes, hex-rendered: 01 02 2c ff 5c 2c 41.
		{"hex", `\x01022cff5c2c41`},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			ctx.GetSetting = func(name string) (string, bool) {
				if name == "bytea_output" {
					return tc.mode, true
				}
				return "", false
			}
			d, colType := byteaExprResult(t, ctx, sql)
			if colType != "bytea" {
				t.Fatalf("planner-advertised column type = %q, want %q", colType, "bytea")
			}
			// string_agg(bytea) already returns rendered text (see
			// TestCopyByteaAcceptsStringAggResult), not a KindBytes datum.
			if d.Kind != KindString {
				t.Fatalf("Kind = %d, want KindString (%d)", d.Kind, KindString)
			}
			if d.StringValue() != tc.want {
				t.Errorf("string_agg(bytea, ',') mode=%q = %q, want %q", tc.mode, d.StringValue(), tc.want)
			}
		})
	}
}

// TestStringAggByteaTypedDelimiterUnchanged pins criterion 2: a delimiter
// that already arrives typed as bytea (`','::bytea`) must render byte-
// identically to the untyped case above — the fix must not double-apply or
// otherwise disturb the already-working KindBytes path.
func TestStringAggByteaTypedDelimiterUnchanged(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	ctx.GetSetting = func(name string) (string, bool) {
		if name == "bytea_output" {
			return "escape", true
		}
		return "", false
	}

	const sql = `select string_agg(b, ','::bytea) from ` +
		`(values ('\x0102'::bytea),('\xff5c'::bytea),('\x41'::bytea)) v(b)`
	d, _ := byteaExprResult(t, ctx, sql)
	const want = `\001\002,\377\\,A`
	if d.StringValue() != want {
		t.Errorf("string_agg(bytea, ','::bytea) = %q, want %q", d.StringValue(), want)
	}
}

// TestStringAggByteaHexEscapeDelimiter pins criterion 3: a hex-escape
// delimiter literal `'\x41'` (no cast) must coerce through byteain's hex form
// to the single byte 0x41 — NOT be copied verbatim as the four characters
// `\`, `x`, `4`, `1`. This is the case that proves byteaOperand's byteaIn
// path is actually exercised, not a raw string copy.
func TestStringAggByteaHexEscapeDelimiter(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	ctx.GetSetting = func(name string) (string, bool) {
		if name == "bytea_output" {
			return "hex", true
		}
		return "", false
	}

	const sql = `select string_agg(b, '\x41') from ` +
		`(values ('\x01'::bytea),('\x02'::bytea)) v(b)`
	d, _ := byteaExprResult(t, ctx, sql)
	// 0x01 + 0x41('A') + 0x02, hex-rendered.
	const want = `\x01` + `41` + `02`
	if d.StringValue() != want {
		t.Errorf("string_agg(bytea, '\\x41') = %q, want %q (delimiter should be 1 byte 0x41, "+
			"not the 4 literal chars of the source string)", d.StringValue(), want)
	}
}

// TestStringAggByteaNullOrInvalidDelimiter pins criterion 4: a NULL
// delimiter or a delimiter whose text fails byteain (invalid hex digits)
// must keep today's behaviour — empty separator, no error/panic — matching
// byteaOperand's documented contract of never turning a working query into
// an error.
func TestStringAggByteaNullOrInvalidDelimiter(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	ctx.GetSetting = func(name string) (string, bool) {
		if name == "bytea_output" {
			return "hex", true
		}
		return "", false
	}

	t.Run("null delimiter", func(t *testing.T) {
		const sql = `select string_agg(b, null::bytea) from ` +
			`(values ('\x01'::bytea),('\x02'::bytea)) v(b)`
		d, _ := byteaExprResult(t, ctx, sql)
		const want = `\x0102`
		if d.StringValue() != want {
			t.Errorf("string_agg(bytea, NULL) = %q, want %q", d.StringValue(), want)
		}
	})

	t.Run("invalid bytea input delimiter", func(t *testing.T) {
		// '\xZZ' is not valid hex — byteain rejects it, byteaOperand reports
		// false, and the delimiter must fall back to empty (no panic/error).
		const sql = `select string_agg(b, '\xZZ') from ` +
			`(values ('\x01'::bytea),('\x02'::bytea)) v(b)`
		d, _ := byteaExprResult(t, ctx, sql)
		const want = `\x0102`
		if d.StringValue() != want {
			t.Errorf("string_agg(bytea, invalid delim) = %q, want %q", d.StringValue(), want)
		}
	})
}
