package executor

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// M0125-0021 — a bytea literal used to be carried as escaped TEXT.
//
// Every expectation below was captured from the PostgreSQL 18.3 oracle on port
// 65438 before the fix, and the whole matrix was re-run end-to-end through psql
// against a goopg server afterwards and diffed byte-for-byte against the same
// oracle. The pre-fix answers are recorded per subtest because they are the
// point: `length('\xaabb'::bytea)` answered 6 (escape characters, not bytes),
// `encode(…)` answered `''` for EVERY input — a hex dump that silently produced
// the empty string rather than erroring — and a bytea column stored the six
// characters of the escape text.
//
// Both halves of each sibling pair are asserted, per Hard-won Rule #2: the
// `::bytea` cast and the storage encoder share byteaIn; decode() and the cast
// share hexDecodePG; the executor's result Kind and the planner's advertised
// column type are checked together, because a KindBytes datum advertised as
// `text` reaches the wire as raw bytes and prints as garbage.

// byteaExprResult runs a single-row single-column query and returns the datum
// plus the type the planner advertises for it.
func byteaExprResult(t *testing.T, ctx *Context, sql string) (Datum, string) {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("Parse(%q): %v", sql, err)
	}
	plan, err := planner.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		t.Fatalf("Plan(%q): %v", sql, err)
	}
	op, err := Build(plan)
	if err != nil {
		t.Fatalf("Build(%q): %v", sql, err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatalf("Open(%q): %v", sql, err)
	}
	rows, err := drainScan(op)
	_ = op.Close()
	if err != nil {
		t.Fatalf("exec(%q): %v", sql, err)
	}
	if len(rows) != 1 || len(rows[0]) != 1 {
		t.Fatalf("%q: want 1x1 result, got %d rows", sql, len(rows))
	}
	schema := plan.Output()
	colType := ""
	if len(schema) == 1 {
		colType = schema[0].Type.Name
	}
	return rows[0][0], colType
}

// byteaExprErr runs a query expected to fail and returns the ExecError.
func byteaExprErr(t *testing.T, ctx *Context, sql string) *ExecError {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("Parse(%q): %v", sql, err)
	}
	plan, err := planner.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		t.Fatalf("Plan(%q): %v", sql, err)
	}
	op, err := Build(plan)
	if err != nil {
		t.Fatalf("Build(%q): %v", sql, err)
	}
	if err := op.Open(ctx); err != nil {
		if ee, ok := err.(*ExecError); ok {
			return ee
		}
		t.Fatalf("Open(%q): non-ExecError %v", sql, err)
	}
	_, err = drainScan(op)
	_ = op.Close()
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("%q: want ExecError, got %v", sql, err)
	}
	return ee
}

// TestByteaLiteralIsBytesNotEscapeText pins the cast itself: `'\xaabb'::bytea`
// is TWO BYTES. Pre-fix this datum was the six-character KindString `\xaabb`,
// which is why applyAgg's `arg.Kind == KindBytes` branch was unreachable from a
// literal (the discovery that filed this task, via M0125-0019).
func TestByteaLiteralIsBytesNotEscapeText(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct {
		sql      string
		wantHex  string // PG 18.3 oracle, hex of the payload bytes
		wantType string
	}{
		// hex format
		{`select '\xaabb'::bytea`, "aabb", "bytea"},
		{`select cast('\xaabb' as bytea)`, "aabb", "bytea"},
		{`select '\x'::bytea`, "", "bytea"},
		// escape format — an unadorned literal is its own bytes
		{`select 'abc'::bytea`, "616263", "bytea"},
		// text -> bytea also goes through byteain in PG:
		// `select '\xaa'::text::bytea` is the single byte 0xAA, not 4 bytes.
		{`select '\xaa'::text::bytea`, "aa", "bytea"},
		// decode() already produced bytes pre-fix, but was advertised as an
		// untyped column, so the wire layer printed the raw payload.
		{`select decode('aabb','hex')`, "aabb", "bytea"},
		// hex_decode_safe skips whitespace between digits.
		{`select decode('aa bb','hex')`, "aabb", "bytea"},
		{`select decode('YWJj','base64')`, "616263", "bytea"},
		{`select decode('a\061b','escape')`, "613162", "bytea"},
		// byteacat: bytea || bytea is bytea, and the unknown literal on the
		// right is coerced through byteain first.
		{`select '\xaabb'::bytea || '\x00'::bytea`, "aabb00", "bytea"},
		// bytea_substr slices BYTES and stays bytea. Pre-fix this sliced the
		// escape TEXT and returned the character "x".
		{`select substring('\xaabbcc'::bytea from 2 for 1)`, "bb", "bytea"},
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			d, colType := byteaExprResult(t, ctx, tc.sql)
			if d.Kind != KindBytes {
				t.Fatalf("Kind = %d, want KindBytes (%d)", d.Kind, KindBytes)
			}
			if got := hex.EncodeToString(d.BytesValue()); got != tc.wantHex {
				t.Errorf("payload = %s, want %s (PG 18.3)", got, tc.wantHex)
			}
			if colType != tc.wantType {
				t.Errorf("advertised column type = %q, want %q — a KindBytes datum "+
					"typed as anything else reaches the wire as raw bytes", colType, tc.wantType)
			}
		})
	}
}

// TestByteaLengthCountsBytes pins byteaoctetlen. `length('\xaabb'::bytea)`
// answered 6 pre-fix because the escape text was six characters long.
func TestByteaLengthCountsBytes(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct {
		sql  string
		want int64
	}{
		{`select length('\xaabb'::bytea)`, 2},       // pre-fix: 6
		{`select octet_length('\xaabb'::bytea)`, 2}, // pre-fix: 6
		{`select length('abc'::bytea)`, 3},
		{`select octet_length('abc'::bytea)`, 3},
		{`select length('\x'::bytea)`, 0},
		// text is unaffected — length() still counts characters there.
		{`select length('abc')`, 3},
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

// TestByteaEncodeNoLongerAStub pins binary_encode. Every one of these returned
// the empty string before M0125-0021 — the silent-wrong-answer half of the
// defect, because encode() is exactly how a caller hex-dumps a bytea.
func TestByteaEncodeNoLongerAStub(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct{ sql, want string }{
		{`select encode('\xaabb'::bytea,'hex')`, "aabb"},
		{`select encode('abc'::bytea,'base64')`, "YWJj"},
		{`select encode('abc'::bytea,'escape')`, "abc"},
		// esc_encode escapes NUL, high-bit bytes and the backslash — and
		// NOTHING else, so the 0x0a below passes through as a raw newline.
		// (byteaout's escape mode differs; they are separate upstream
		// functions and encode() must not borrow the other one's rules.)
		{`select encode('\x00610a5c'::bytea,'escape')`, "\\000a\n\\\\"},
		{`select encode('\x'::bytea,'hex')`, ""},
		// Round-trip through the PG decoders.
		{`select encode(decode('YWJj','base64'),'escape')`, "abc"},
		{`select encode(decode('aabb','hex'),'hex')`, "aabb"},
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			d, colType := byteaExprResult(t, ctx, tc.sql)
			if d.Kind != KindString || d.StringValue() != tc.want {
				t.Errorf("= %q (kind %d), want %q (PG 18.3)", d.Format(), d.Kind, tc.want)
			}
			if colType != "text" {
				t.Errorf("advertised column type = %q, want text", colType)
			}
		})
	}
}

// TestByteaBase64WrapsAt76 pins pg_base64_encode's line breaking, which
// base64.StdEncoding does not do. 100 bytes → 136 characters + 1 newline = 137,
// and the decoder has to tolerate the newline its own encoder emitted.
func TestByteaBase64WrapsAt76(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	d, _ := byteaExprResult(t, ctx, `select encode(repeat('a',100)::bytea,'base64')`)
	got := d.StringValue()
	if len(got) != 137 { // PG 18.3: length(...) = 137
		t.Errorf("len = %d, want 137 (PG wraps at 76 characters)", len(got))
	}
	if idx := strings.IndexByte(got, '\n'); idx != 76 {
		t.Errorf("newline at %d, want 76", idx)
	}
	rt, _ := byteaExprResult(t, ctx,
		`select decode(encode(repeat('a',100)::bytea,'base64'),'base64')`)
	if rt.Kind != KindBytes || len(rt.BytesValue()) != 100 {
		t.Errorf("round-trip = %d bytes (kind %d), want 100 — the decoder must "+
			"skip the newlines the encoder emits", len(rt.BytesValue()), rt.Kind)
	}
}

// TestByteaInvalidInputErrors pins the two DISTINCT upstream error families.
// Every one of these silently succeeded pre-fix: `'\xzz'::bytea` produced the
// four-character string `\xzz`.
func TestByteaInvalidInputErrors(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct{ sql, code, msg string }{
		// encode.c → ERRCODE_INVALID_PARAMETER_VALUE
		{`select '\xzz'::bytea`, "22023", `invalid hexadecimal digit: "z"`},
		{`select '\xaab'::bytea`, "22023", "invalid hexadecimal data: odd number of digits"},
		{`select encode('\xaabb'::bytea,'bogus')`, "22023", `unrecognized encoding: "bogus"`},
		{`select decode('aabb','bogus')`, "22023", `unrecognized encoding: "bogus"`},
		// decode(…,'hex') does NOT accept the `\x` prefix that byteain does —
		// upstream reports the backslash as the offending digit. goopg used to
		// strip the prefix and accept it.
		{`select decode('\xaabb','hex')`, "22023", `invalid hexadecimal digit: "\"`},
		// varlena.c byteain escape pass → ERRCODE_INVALID_TEXT_REPRESENTATION
		{`select decode('a\1b','escape')`, "22P02", "invalid input syntax for type bytea"},
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			ee := byteaExprErr(t, ctx, tc.sql)
			if ee.Code != tc.code || ee.Message != tc.msg {
				t.Errorf("= %s %q, want %s %q (PG 18.3)", ee.Code, ee.Message, tc.code, tc.msg)
			}
		})
	}
}

// TestByteaColumnStoresBytes pins the storage sibling of the cast. Pre-fix
// `INSERT INTO t VALUES ('\xaabb')` stored the SIX characters of the escape
// text, so length(b) answered 6 and the value sorted by its backslash.
func TestByteaColumnStoresBytes(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE bt (id int, b bytea)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, `INSERT INTO bt VALUES (1,'\xaabb'), (2,'abc'), (3,'\x')`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	rows := runQuery(t, ctx, `select id, b, length(b), encode(b,'hex') from bt order by id`)
	want := []struct {
		id      int64
		payload string
		length  int64
	}{
		{1, "aabb", 2}, // pre-fix: 5c7861616262 / 6
		{2, "616263", 3},
		{3, "", 0},
	}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d", len(rows), len(want))
	}
	for i, w := range want {
		if rows[i][0].Int != w.id {
			t.Errorf("row %d: id = %d, want %d", i, rows[i][0].Int, w.id)
		}
		if rows[i][1].Kind != KindBytes {
			t.Errorf("row %d: column Kind = %d, want KindBytes", i, rows[i][1].Kind)
		}
		if got := hex.EncodeToString(rows[i][1].BytesValue()); got != w.payload {
			t.Errorf("row %d: stored bytes = %s, want %s (PG 18.3)", i, got, w.payload)
		}
		if rows[i][2].Int != w.length {
			t.Errorf("row %d: length(b) = %d, want %d", i, rows[i][2].Int, w.length)
		}
		if rows[i][3].StringValue() != w.payload {
			t.Errorf("row %d: encode(b,'hex') = %q, want %q", i, rows[i][3].StringValue(), w.payload)
		}
	}

	// The comparison sibling: an unknown literal on the other side of the
	// operator is coerced through byteain, so a predicate that matched before
	// the storage fix still matches after it. Without this, M0125-0021 would
	// have turned every `b = '\x…'` into a silent zero-row answer.
	if got := runQuery(t, ctx, `select id from bt where b = '\xaabb'`); len(got) != 1 || got[0][0].Int != 1 {
		t.Errorf("b = '\\xaabb' matched %d rows, want row 1", len(got))
	}
	if got := runQuery(t, ctx, `select id from bt where b = 'abc'`); len(got) != 1 || got[0][0].Int != 2 {
		t.Errorf("b = 'abc' matched %d rows, want row 2", len(got))
	}
	// bytea ordering is memcmp over the BYTES: 0x61… < 0xaa…, so 'abc' sorts
	// first. Pre-fix the stored escape text put `\xaabb` (0x5c…) first.
	ordered := runQuery(t, ctx, `select encode(b,'hex') from bt where length(b) > 0 order by b`)
	if len(ordered) != 2 || ordered[0][0].StringValue() != "616263" || ordered[1][0].StringValue() != "aabb" {
		t.Errorf("order by b = %v, want [616263 aabb] (memcmp over bytes)", ordered)
	}
}

// TestByteaCastToTextIsHexEscape pins byteaout: a bytea cast to text is the
// `\x…` escape STRING, not the raw payload. This is what makes
// `<bytea>::text` and a plain `SELECT <bytea>` agree.
func TestByteaCastToTextIsHexEscape(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, tc := range []struct{ sql, want string }{
		{`select '\xaabb'::bytea::text`, `\xaabb`},
		{`select 'abc'::bytea::text`, `\x616263`},
		{`select '\x'::bytea::text`, `\x`},
	} {
		t.Run(tc.sql, func(t *testing.T) {
			d, _ := byteaExprResult(t, ctx, tc.sql)
			if d.Kind != KindString || d.StringValue() != tc.want {
				t.Errorf("= %q (kind %d), want %q (PG 18.3)", d.Format(), d.Kind, tc.want)
			}
		})
	}
}
