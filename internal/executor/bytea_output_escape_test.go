package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestEvalCastByteaToTextHonorsByteaOutput pins evalCast's KindBytes "text"
// arm against the `bytea_output` GUC: `SET bytea_output = 'escape'` must
// change `<bytea>::text` output, hex stays the default. Before this fix the
// arm always called byteaOutHex regardless of the GUC. Boundary bytes and
// escape expectations are captured LIVE from PG 18.3
// (postgres/local_install/bin) — see the leaf package's
// TestByteaOutEscapeBoundaryBytes for the exact capture transcript.
// M0134-0001 S12.
func TestEvalCastByteaToTextHonorsByteaOutput(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		mode string
		want string
	}{
		{"0x00 hex default", []byte{0x00}, "", `\x00`},
		{"0x00 hex explicit", []byte{0x00}, "hex", `\x00`},
		{"0x00 escape", []byte{0x00}, "escape", `\000`},
		{"0x1f escape", []byte{0x1f}, "escape", `\037`},
		{"0x20 escape (space, printable)", []byte{0x20}, "escape", ` `},
		{"0x7e escape (tilde, printable)", []byte{0x7e}, "escape", `~`},
		{"0x7f escape", []byte{0x7f}, "escape", `\177`},
		{"0x80 escape", []byte{0x80}, "escape", `\200`},
		{"0xff escape", []byte{0xff}, "escape", `\377`},
		{"0x5c backslash escape", []byte{0x5c}, "escape", `\\`},
		{"empty escape", []byte{}, "escape", ``},
		{"empty hex", []byte{}, "hex", `\x`},
		{"unrecognised mode falls back to hex", []byte{0xaa}, "bogus", `\xaa`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ctx *Context
			if tc.mode != "" {
				ctx = &Context{GetSetting: func(name string) (string, bool) {
					if name == "bytea_output" {
						return tc.mode, true
					}
					return "", false
				}}
			}
			got, err := evalCast(NewBytesDatum(tc.in), "text", 0, ctx)
			if err != nil {
				t.Fatalf("evalCast(text): %v", err)
			}
			if got.Kind != KindString || got.StringValue() != tc.want {
				t.Errorf("evalCast(bytea %v, mode=%q) = %q (kind %d), want %q",
					tc.in, tc.mode, got.StringValue(), got.Kind, tc.want)
			}
		})
	}
}

// TestEvalCastByteaToTextNilCtxDefaultsHex confirms a nil ctx (no session —
// the same case dateStyleFromCtx/timeZoneFromCtx document for the DateStyle/
// TimeZone GUCs) falls back to hex, matching PG's boot default and every
// pre-existing caller's behavior. M0134-0001 S12.
func TestEvalCastByteaToTextNilCtxDefaultsHex(t *testing.T) {
	got, err := evalCast(NewBytesDatum([]byte{0xaa, 0xbb}), "text", 0, nil)
	if err != nil {
		t.Fatalf("evalCast(text): %v", err)
	}
	if got.StringValue() != `\xaabb` {
		t.Errorf("evalCast(bytea, nil ctx) = %q, want %q", got.StringValue(), `\xaabb`)
	}
}

// TestByteaOutputPerSession proves the `bytea_output` mode is resolved
// per-call from ctx.GetSetting, not a package-level global — the failure
// mode a global would pass every other test in this file while still
// rendering one session's setting into another's output. M0134-0001 S12.
func TestByteaOutputPerSession(t *testing.T) {
	b := NewBytesDatum([]byte{0x00, 0x5c})
	hexCtx := &Context{GetSetting: func(name string) (string, bool) {
		if name == "bytea_output" {
			return "hex", true
		}
		return "", false
	}}
	escapeCtx := &Context{GetSetting: func(name string) (string, bool) {
		if name == "bytea_output" {
			return "escape", true
		}
		return "", false
	}}

	gotHex, err := evalCast(b, "text", 0, hexCtx)
	if err != nil {
		t.Fatalf("hex session: %v", err)
	}
	gotEscape, err := evalCast(b, "text", 0, escapeCtx)
	if err != nil {
		t.Fatalf("escape session: %v", err)
	}
	// Re-render the hex session AFTER the escape session to catch a global
	// that would have been mutated by the escape call.
	gotHexAgain, err := evalCast(b, "text", 0, hexCtx)
	if err != nil {
		t.Fatalf("hex session (2nd): %v", err)
	}

	if gotHex.StringValue() != `\x005c` {
		t.Errorf("hex session = %q, want %q", gotHex.StringValue(), `\x005c`)
	}
	if gotEscape.StringValue() != `\000\\` {
		t.Errorf("escape session = %q, want %q", gotEscape.StringValue(), `\000\\`)
	}
	if gotHexAgain.StringValue() != gotHex.StringValue() {
		t.Errorf("hex session rendered %q before the escape session and %q after — "+
			"bytea_output leaked across sessions", gotHex.StringValue(), gotHexAgain.StringValue())
	}
}

// TestByteaOutEscapeRoundTripsThroughByteaIn is the escape-mode counterpart
// of the existing hex round-trip coverage: byteain accepts BOTH the hex and
// escape wire forms unconditionally (input is self-describing — the design
// doc's explicit non-scope), so `byteaIn(byteaOutMode(b, "escape")) == b`
// for every byte, not just the printable ones. M0134-0001 S12.
func TestByteaOutEscapeRoundTripsThroughByteaIn(t *testing.T) {
	cases := [][]byte{
		{},
		{0x00},
		{0x1f},
		{0x20},
		{0x7e},
		{0x7f},
		{0x80},
		{0xff},
		{0x5c},
		{0x00, 0x01, 0x1f, 0x20, 0x7e, 0x7f, 0x80, 0xff, 0x5c},
	}
	for _, in := range cases {
		escaped := byteaOutMode(in, "escape")
		got, err := byteaIn(escaped, 0)
		if err != nil {
			t.Fatalf("byteaIn(%q): %v", escaped, err)
		}
		if len(got) != len(in) {
			t.Fatalf("byteaIn(byteaOutMode(%v, escape)) = %v (len %d), want len %d", in, got, len(got), len(in))
		}
		for i := range in {
			if got[i] != in[i] {
				t.Errorf("byteaIn(byteaOutMode(%v, escape))[%d] = %#x, want %#x", in, i, got[i], in[i])
			}
		}
	}
}

// TestCopyTextByteaHonorsByteaOutput pins the COPY TO text renderer
// (datumToCopyText's "bytea" case) against the GUC — previously unhandled,
// so a bytea column fell to the default arm's KindBytes case and wrote the
// RAW payload bytes into the COPY stream instead of an output-function-
// encoded string. BOTH cases below exercise COPY TEXT's own backslash
// escaping on top of byteaout's: byteaout's `\x` prefix, `\\` (a literal
// source backslash), and `\NNN` (an octal escape) all start with a
// backslash, so appendCopyTextEscaped doubles every one of them again —
// captured live from PG 18.3's `COPY ... TO STDOUT` under both
// `bytea_output` values. M0134-0001 S12.
func TestCopyTextByteaHonorsByteaOutput(t *testing.T) {
	tests := []struct {
		name      string
		byteaMode string
		want      string
	}{
		{"hex default", "hex", `\\x00011f207e7f80ff5c`},
		{"escape", "escape", `\\000\\001\\037 ~\\177\\200\\377\\\\`},
	}
	col := catalog.Column{Name: "b", Type: catalog.Type{Name: "bytea"}}
	d := NewBytesDatum([]byte{0x00, 0x01, 0x1f, 0x20, 0x7e, 0x7f, 0x80, 0xff, 0x5c})
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			line, err := EncodeCopyTextRow(nil, Row{d}, []catalog.Column{col}, "ISO", "MDY", "", tc.byteaMode, nil, false)
			if err != nil {
				t.Fatalf("EncodeCopyTextRow: %v", err)
			}
			got := string(line)
			// Trailing newline.
			if len(got) == 0 || got[len(got)-1] != '\n' {
				t.Fatalf("EncodeCopyTextRow output missing trailing newline: %q", got)
			}
			got = got[:len(got)-1]
			if got != tc.want {
				t.Errorf("EncodeCopyTextRow(bytea, byteaMode=%q) = %q, want %q", tc.byteaMode, got, tc.want)
			}
		})
	}
}

// TestCopyByteaAcceptsStringAggResult is a Round-2 review regression guard:
// string_agg(bytea, bytea) advertises its column as `bytea` (pg_proc OID
// 3545, RetType 17) but returns its accumulated result as a KindString datum
// (already output-function text — see operators_join_agg.go's finishAgg
// "string_agg" case), NOT a KindBytes datum. An earlier version of
// datumToCopyText's new "bytea" case rejected anything that was not
// KindBytes outright, which turned this reachable, previously-working query
// into a hard error:
//
//	CREATE TABLE zs(id int, b bytea);
//	INSERT INTO zs VALUES (1,'\x0102'),(2,'\xff5c'),(3,'\x41');
//	COPY (SELECT string_agg(b,','::bytea) FROM zs) TO STDOUT;
//
// PG 18.3 and goopg@HEAD both answer `\x01022cff5c2c41`. The fix mirrors the
// shape dispatch.go's appendTypedCellText (SELECT wire) already uses: render
// for KindBytes, pass a KindString straight through (it is already rendered
// text), error only on anything else.
func TestCopyByteaAcceptsStringAggResult(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	d, colType := byteaExprResult(t, ctx,
		`select string_agg(b,','::bytea) from (values ('\x0102'::bytea),('\xff5c'::bytea),('\x41'::bytea)) v(b)`)
	if colType != "bytea" {
		t.Fatalf("planner-advertised column type = %q, want %q", colType, "bytea")
	}
	if d.Kind != KindString {
		t.Fatalf("string_agg(bytea) result Kind = %d, want KindString (%d) — "+
			"if this changed, the regression this guard exists for no longer applies "+
			"and the guard should be revisited, not just re-asserted", d.Kind, KindString)
	}

	col := catalog.Column{Name: "b", Type: catalog.Type{Name: colType}}
	const want = `\\x01022cff5c2c41`

	t.Run("text", func(t *testing.T) {
		line, err := EncodeCopyTextRow(nil, Row{d}, []catalog.Column{col}, "ISO", "MDY", "", "hex", nil, false)
		if err != nil {
			t.Fatalf("EncodeCopyTextRow: %v", err)
		}
		got := string(line)
		if len(got) == 0 || got[len(got)-1] != '\n' {
			t.Fatalf("EncodeCopyTextRow output missing trailing newline: %q", got)
		}
		got = got[:len(got)-1]
		if got != want {
			t.Errorf("EncodeCopyTextRow(string_agg(bytea) result) = %q, want %q", got, want)
		}
	})

	t.Run("csv", func(t *testing.T) {
		f := copyToFormatFromOptions([]parser.CopyOption{{Name: "format", Value: "csv"}})
		line, err := EncodeCopyCsvRow(nil, Row{d}, []catalog.Column{col}, f, "ISO", "MDY", "", "hex", nil, false)
		if err != nil {
			t.Fatalf("EncodeCopyCsvRow: %v", err)
		}
		got := string(line)
		if len(got) == 0 || got[len(got)-1] != '\n' {
			t.Fatalf("EncodeCopyCsvRow output missing trailing newline: %q", got)
		}
		got = got[:len(got)-1]
		// CSV format does not backslash-escape; the raw string_agg text (which
		// already contains a literal backslash from the "\x" prefix) passes
		// through unquoted unless it collides with the CSV delimiter/quote/
		// newline set, none of which appear here.
		const wantCsv = `\x01022cff5c2c41`
		if got != wantCsv {
			t.Errorf("EncodeCopyCsvRow(string_agg(bytea) result) = %q, want %q", got, wantCsv)
		}
	})
}
